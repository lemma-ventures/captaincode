import hashlib
import json
import os
import tempfile
import time
import unittest
from pathlib import Path
from unittest.mock import patch

import jwt
import middleware as m
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey


class Rejected(Exception):
    pass


class Context:
    def __init__(self, value):
        self.value = value

    def invocation_metadata(self):
        return [("authorization", "Bearer " + self.value)]

    def abort(self, code, message):
        raise Rejected(code)


class ShieldTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.path = Path(self.directory.name)
        self.next_request = 0
        self.signer = Ed25519PrivateKey.generate()
        self.service = m.Shield(os.environ["PILOT_SHIELD_BIN"], self.signer.public_key(),
                               self.path / "audit.jsonl", "test-model")
        self.environment = patch.dict(os.environ, {"HOME": str(self.path), "CAPTAIN_REDACT": "off",
                                                  "CAPTAIN_REDACT_IDENTITY": "Pilot Person 7d84"})
        self.environment.start()
        self.addCleanup(self.environment.stop)

    def context(self, **overrides):
        claims = {"iss": "openshell-gateway:captain-pilot", "aud": m.AUDIENCE,
                  "exp": int(time.time()) + 60, "caller_kind": "supervisor", "sandbox_id": "s-1"}
        claims.update(overrides)
        return Context(jwt.encode(claims, self.signer, algorithm="EdDSA", headers={"typ": "openshell-ext+jwt"}))

    def request(self):
        return m.pb.HttpRequestEvaluation(
            phase=1, context=m.pb.RequestContext(sandbox_id="s-1", request_id="r-1"),
            target=m.pb.HttpRequestTarget(scheme="https", host="integrate.api.nvidia.com", port=443,
                                          method="POST", path="/v1/chat/completions"),
            headers=[m.pb.HttpHeader(name="content-type", value="application/json")],
            body=json.dumps({"model": "test-model", "messages": [{"role": "tool", "content": m.CANARY}]}).encode())

    def test_real_shield_masks_decoded_tool_json_even_when_disabled_in_environment(self):
        request = self.request()
        request.body = request.body.replace(b"nvapi", b"\\u006evapi")
        result = self.service.EvaluateHttpRequest(request, self.context())
        self.assertEqual(result.decision, 1)
        self.assertNotIn(m.CANARY, result.body.decode())
        self.assertIn("[[secret:nvidia:", result.body.decode())
        event = json.loads(self.service.audit.read_text())
        self.assertTrue(event["tool_canary_masked"])
        self.assertNotIn(m.CANARY, self.service.audit.read_text())

    def test_wrong_identity_expiry_audience_and_signature_are_refused(self):
        cases = [{"sandbox_id": "s-2"}, {"caller_kind": "gateway"}, {"exp": 1},
                 {"aud": "other"}, {"iss": "openshell-gateway:other"}]
        for overrides in cases:
            with self.subTest(overrides=overrides), self.assertRaises(Rejected):
                self.service.EvaluateHttpRequest(self.request(), self.context(**overrides))
        self.service.key = Ed25519PrivateKey.generate().public_key()
        with self.assertRaises(Rejected):
            self.service.EvaluateHttpRequest(self.request(), self.context())

    def test_closed_scope(self):
        for field, value in [("scheme", "http"), ("host", "example.com"), ("path", "/v1/models"),
                             ("method", "GET"), ("port", 8443), ("query", "x=y")]:
            with self.subTest(field=field):
                request = self.request()
                setattr(request.target, field, value)
                self.assertEqual(self.service.EvaluateHttpRequest(request, self.context()).decision, 2)

    def test_malformed_compressed_oversized_and_wrong_model_are_refused(self):
        for body in [b"{", b"[]", b"{}", b"x" * (m.LIMIT + 1), b'{"model":"other"}']:
            with self.subTest(size=len(body)):
                request = self.request()
                request.body = body
                self.assertEqual(self.service.EvaluateHttpRequest(request, self.context()).decision, 2)
        request = self.request()
        request.headers.append(m.pb.HttpHeader(name="content-encoding", value="gzip"))
        self.assertEqual(self.service.EvaluateHttpRequest(request, self.context()).decision, 2)

    def test_redactor_or_audit_failure_does_not_allow_traffic(self):
        with patch("middleware.subprocess.run", side_effect=OSError("unavailable")):
            self.assertEqual(self.service.EvaluateHttpRequest(self.request(), self.context()).decision, 2)
        self.service.audit = self.path
        self.assertEqual(self.service.EvaluateHttpRequest(self.request(), self.context()).decision, 2)

    def test_lane_scope_masking_and_pinning_are_enforced_together(self):
        from profiles import profile
        binary = self.service.binary
        for name, other in [("cerebras", "SambaNova"), ("deepinfra", "Cerebras")]:
            lane = profile(name)
            with self.subTest(lane=name):
                self.service = m.Shield(binary, self.signer.public_key(), self.path / "audit.jsonl", lane["model"], name)
                request = self.request()
                self.assertEqual(self.service.EvaluateHttpRequest(request, self.context()).decision, 2)
                request.target.host = "openrouter.ai"
                request.target.path = "/api/v1/chat/completions"
                request.body = json.dumps({"model": self.service.model, "stream": True,
                                          "provider": {"allow_fallbacks": True, "zdr": False},
                                          "messages": [{"role": "tool", "content": m.CANARY}]}).encode()
                result = self.service.EvaluateHttpRequest(request, self.context())
                self.assertEqual(result.decision, 1)
                body = json.loads(result.body)
                self.assertEqual(body["provider"]["only"], [lane["route"]])
                self.assertFalse(body["provider"]["allow_fallbacks"])
                self.assertTrue(body["provider"]["zdr"])
                self.assertEqual(body["temperature"], 0)
                self.assertNotIn(m.CANARY, result.body.decode())
                self.assertEqual(result.header_mutations[0].write.name, "accept-encoding")
                self.assertEqual(result.header_mutations[0].write.value, "identity")
                self.assertEqual(result.header_mutations[0].write.on_existing, 2)
                head = m.pb.HttpResponseEvent(preflight=m.pb.HttpResponsePreflight(
                    context=request.context, target=request.target, status_code=200,
                    max_payload_bytes=m.LIMIT, permitted_body_modes=[2],
                    headers=[m.pb.HttpHeader(name="content-type", value="application/json")]))
                wrong_provider = self.response_body(json.dumps({"provider": other, "model": self.service.model}).encode())
                result = list(self.service.Evaluate(iter([head, wrong_provider]), self.context()))
                self.assertTrue(result[1].body_result.HasField("block_delivery"))
                self.assertEqual(json.loads(self.service.audit.read_text().splitlines()[-1])["stage"], "provider")
                request.context.request_id = "confirmed"
                self.assertEqual(self.service.EvaluateHttpRequest(request, self.context()).decision, 1)
                head.preflight.context.request_id = "confirmed"
                valid = self.response_body(json.dumps({"provider": lane["served_by"], "model": self.service.model,
                    "choices": [{"index": 0, "message": {"role": "assistant", "content": "done"},
                                 "finish_reason": "stop"}]}).encode())
                results = list(self.service.Evaluate(iter([head, valid]), self.context()))
                self.assertTrue(results[1].body_result.HasField("transform"))
                last = json.loads(self.service.audit.read_text().splitlines()[-1])
                self.assertEqual(last["provider"], lane["served_by"])
                self.assertEqual(len(last["upstream_body_sha256"]), 64)

    def test_protocol_negotiation(self):
        request = m.pb.MiddlewareDescribeRequest(gateway=m.extension.PeerMetadata(
            protocol_version=m.extension.ProtocolVersion(major=1),
            supported_capabilities=[m.CAPABILITY], required_capabilities=[m.CAPABILITY]))
        result = self.service.Describe(request, self.context(caller_kind="gateway"))
        self.assertEqual(result.expected_audience, m.AUDIENCE)
        self.assertEqual(self.service.Describe(request, self.context()).expected_audience, m.AUDIENCE)
        for values in [{"caller_kind": "worker"}, {"sandbox_id": ""}, {"sandbox_id": None}]:
            with self.subTest(values=values), self.assertRaises(Rejected):
                self.service.Describe(request, self.context(**values))
        request.gateway.required_capabilities.append("unknown")
        with self.assertRaises(Rejected):
            self.service.Describe(request, self.context(caller_kind="gateway"))

    def response_head(self, **overrides):
        request = self.request()
        self.next_request += 1
        request.context.request_id = f"response-{self.next_request}"
        value = json.loads(request.body)
        value["stream"] = True
        value["stream_options"] = {"include_usage": True}
        request.body = json.dumps(value).encode()
        result = self.service.EvaluateHttpRequest(request, self.context())
        self.assertEqual(result.decision, 1)
        self.assertFalse(json.loads(result.body)["stream"])
        self.assertNotIn("stream_options", json.loads(result.body))
        values = {"context": request.context, "target": request.target,
                  "status_code": 200, "max_payload_bytes": m.LIMIT, "permitted_body_modes": [1, 2, 3],
                  "headers": [m.pb.HttpHeader(name="content-type", value="application/json")]}
        values.update(overrides)
        return m.pb.HttpResponseEvent(preflight=m.pb.HttpResponsePreflight(**values))

    def response_body(self, data=None, **overrides):
        if data is None:
            data = json.dumps({"id": "chat-1", "choices": [{"index": 0, "message": {
                "role": "assistant", "content": "identity-1 [[secret:nvidia:123456]]"},
                "finish_reason": "stop"}]}).encode()
        return m.pb.HttpResponseEvent(body=m.pb.HttpResponseBodyUnit(
            **dict({"sequence": 1, "data": data, "end_of_stream": True}, **overrides)))

    def test_response_restores_identity_with_real_shield_and_keeps_secrets_masked(self):
        events = [self.response_head(), self.response_body(),
                  m.pb.HttpResponseEvent(trailers=m.pb.HttpResponseTrailers()),
                  m.pb.HttpResponseEvent(session_end=m.pb.MiddlewareSessionEnd())]
        results = list(self.service.Evaluate(iter(events), self.context()))
        self.assertEqual(results[0].preflight_result.inspect.body_mode, 2)
        self.assertEqual(results[0].preflight_result.inspect.header_mutations[0].write.value, "text/event-stream")
        body = results[1].body_result.transform.data
        self.assertIn(b"Pilot Person 7d84", body)
        self.assertIn(b"[[secret:nvidia:123456]]", body)
        self.assertEqual(results[2].WhichOneof("result"), "trailers_result")
        audit = self.service.audit.read_text()
        self.assertEqual(json.loads(audit.splitlines()[-1])["identities"], 1)
        self.assertNotIn("Pilot Person", audit)
        self.assertNotIn("[[secret:", audit)

    def test_response_authentication_is_bound_to_sandbox(self):
        for overrides in [{"sandbox_id": "s-2"}, {"caller_kind": "gateway"}, {"exp": 1}]:
            with self.subTest(overrides=overrides), self.assertRaises(Rejected):
                list(self.service.Evaluate(iter([self.response_head()]), self.context(**overrides)))

    def test_response_refuses_uninspectable_or_wrong_scope_heads(self):
        for overrides in [{"permitted_body_modes": [1, 3]}, {"max_payload_bytes": 0},
                          {"headers": []}, {"headers": [m.pb.HttpHeader(name="content-type", value="text/html")]},
                          {"target": m.pb.HttpRequestTarget(scheme="https", host="example.com")}]:
            with self.subTest(overrides=overrides):
                results = list(self.service.Evaluate(iter([self.response_head(**overrides)]), self.context()))
                self.assertEqual(results[0].preflight_result.WhichOneof("action"), "block_delivery")

    def test_response_malformed_sequences_and_restoration_failures_are_closed(self):
        for body in [self.response_body(sequence=2), self.response_body(end_of_stream=False),
                     self.response_body(b"invalid"), self.response_body(b"x" * (m.LIMIT + 1))]:
            with self.subTest(body_size=len(body.body.data)):
                results = list(self.service.Evaluate(iter([self.response_head(), body]), self.context()))
                self.assertEqual(results[1].body_result.WhichOneof("action"), "block_delivery")
        head = self.response_head()
        with patch("middleware.subprocess.run", side_effect=OSError("unavailable")):
            results = list(self.service.Evaluate(iter([head, self.response_body()]), self.context()))
            self.assertEqual(results[1].body_result.WhichOneof("action"), "block_delivery")
        head = self.response_head()
        self.service.audit = self.path
        results = list(self.service.Evaluate(iter([head, self.response_body()]), self.context()))
        self.assertEqual(results[0].preflight_result.WhichOneof("action"), "block_delivery")
        with self.assertRaises(Rejected):
            list(self.service.Evaluate(iter([self.response_body()]), self.context()))

    def test_blocked_response_records_stage_and_error_class_without_content(self):
        arguments = {"choices": [{"index": 0, "message": {"role": "assistant", "content": "identity-1", "tool_calls": [
            {"type": "function", "id": "call-1", "function": {"name": "bash", "arguments": "{"}}]},
            "finish_reason": "tool_calls"}]}
        cases = [(self.response_body(sequence=2), "unit", "ValueError"),
                 (self.response_body(b"invalid"), "restore", "JSONDecodeError"),
                 (self.response_body(json.dumps(arguments).encode()), "restore", "JSONDecodeError")]
        for body, stage, error in cases:
            with self.subTest(stage=stage, error=error):
                head = self.response_head()
                results = list(self.service.Evaluate(iter([head, body]), self.context()))
                self.assertEqual(results[1].body_result.WhichOneof("action"), "block_delivery")
                row = json.loads(self.service.audit.read_text().splitlines()[-1])
                self.assertEqual(set(row), {"at", "sandbox_id", "request_id", "phase", "status", "stage",
                                            "error", "upstream_body_sha256", "bytes"})
                self.assertEqual((row["phase"], row["status"], row["stage"], row["error"]),
                                 ("response_blocked", 200, stage, error))
                self.assertEqual(row["request_id"], head.preflight.context.request_id)
                self.assertEqual(row["upstream_body_sha256"], hashlib.sha256(body.body.data).hexdigest())
                self.assertEqual(row["bytes"], len(body.body.data))
        head = self.response_head()
        with patch("middleware.subprocess.run", side_effect=OSError("unavailable")):
            results = list(self.service.Evaluate(iter([head, self.response_body()]), self.context()))
        self.assertEqual(results[1].body_result.WhichOneof("action"), "block_delivery")
        row = json.loads(self.service.audit.read_text().splitlines()[-1])
        self.assertEqual((row["stage"], row["error"]), ("restore", "OSError"))
        audit = self.service.audit.read_text()
        for content in ["identity-1", "[[secret:", "Pilot Person", "unavailable", "invalid", "Expecting"]:
            self.assertNotIn(content, audit)

    def test_blocked_response_is_refused_even_when_audit_cannot_be_written(self):
        head = self.response_head()

        def events():
            yield head
            self.service.audit = self.path
            yield self.response_body(b"invalid")

        results = list(self.service.Evaluate(events(), self.context()))
        self.assertEqual(results[0].preflight_result.WhichOneof("action"), "inspect")
        self.assertEqual(results[1].body_result.WhichOneof("action"), "block_delivery")

    def test_server_accepts_supervisor_keepalive_pings(self):
        options = dict(m.OPTIONS)
        self.assertLessEqual(options["grpc.http2.min_ping_interval_without_data_ms"], 10000)
        self.assertEqual(options["grpc.keepalive_permit_without_calls"], 1)

    def test_missing_expired_or_replayed_request_state_refuses_delivery(self):
        for mode in ("missing", "expired", "replayed"):
            head = self.response_head()
            key = (head.preflight.context.sandbox_id, head.preflight.context.request_id)
            if mode == "missing":
                self.service.pending.clear()
            elif mode == "expired":
                self.service.pending[key] = (time.monotonic() - 601, True)
            else:
                list(self.service.Evaluate(iter([head, self.response_body()]), self.context()))
            results = list(self.service.Evaluate(iter([head]), self.context()))
            self.assertEqual(results[0].preflight_result.WhichOneof("action"), "block_delivery")

    def test_error_status_keeps_json_and_request_capacity_is_bounded(self):
        head = self.response_head(status_code=429)
        results = list(self.service.Evaluate(iter([head, self.response_body(b'{"error":{"message":"limit"}}')]), self.context()))
        self.assertEqual(list(results[0].preflight_result.inspect.header_mutations), [])
        self.assertEqual(json.loads(results[1].body_result.transform.data), {"error": {"message": "limit"}})
        self.service.pending = {("s-1", str(i)): (time.monotonic(), True) for i in range(128)}
        self.assertEqual(self.service.EvaluateHttpRequest(self.request(), self.context()).decision, 2)

    def test_tool_secret_round_trip_is_scoped_to_masked_request(self):
        request = self.request()
        value = json.loads(request.body)
        value["tools"] = [{"type": "function", "function": {"name": "bash"}}]
        request.body = json.dumps(value).encode()
        result = self.service.EvaluateHttpRequest(request, self.context())
        self.assertEqual(result.decision, 1)
        handle = json.loads(result.body)["messages"][0]["content"]
        value = {"choices": [{"index": 0, "message": {"role": "assistant", "content": handle,
                 "reasoning_content": handle, "tool_calls": [{"type": "function", "id": "call-1",
                 "function": {"name": "bash", "arguments": json.dumps({"command": handle})}}]},
                 "finish_reason": "tool_calls"}]}

        def deliver(request, context):
            head = m.pb.HttpResponseEvent(preflight=m.pb.HttpResponsePreflight(
                context=request.context, target=request.target, status_code=200,
                max_payload_bytes=m.LIMIT, permitted_body_modes=[2],
                headers=[m.pb.HttpHeader(name="content-type", value="application/json")]))
            return list(self.service.Evaluate(iter([head, self.response_body(json.dumps(value).encode())]), context))

        results = deliver(request, self.context())
        message = json.loads(results[1].body_result.transform.data)["choices"][0]["message"]
        self.assertEqual(message["content"], handle)
        self.assertEqual(message["reasoning_content"], handle)
        self.assertEqual(json.loads(message["tool_calls"][0]["function"]["arguments"]), {"command": m.CANARY})
        self.assertEqual(json.loads(self.service.audit.read_text().splitlines()[-1])["tool_secrets_restored"], 1)
        self.assertNotIn(m.CANARY, self.service.audit.read_text())
        self.assertNotIn(handle, self.service.audit.read_text())

        returned = self.request()
        returned.context.request_id = "returned-tool-arguments"
        conversation = json.loads(returned.body)
        conversation["messages"] = [message]
        returned.body = json.dumps(conversation).encode()
        masked_again = self.service.EvaluateHttpRequest(returned, self.context())
        self.assertEqual(masked_again.decision, 1)
        self.assertNotIn(m.CANARY.encode(), masked_again.body)
        self.assertIn(handle.encode(), masked_again.body)

        for sandbox in ["s-1", "s-2"]:
            request.context.request_id = "forged-" + sandbox
            request.context.sandbox_id = sandbox
            forged = json.loads(request.body)
            forged["messages"][0]["content"] = handle
            request.body = json.dumps(forged).encode()
            context = self.context(sandbox_id=sandbox)
            self.assertEqual(self.service.EvaluateHttpRequest(request, context).decision, 1)
            results = deliver(request, context)
            self.assertEqual(results[1].body_result.WhichOneof("action"), "block_delivery")

    def test_provider_error_cannot_trigger_tool_secret_restoration(self):
        head = self.response_head(status_code=429)
        key = (head.preflight.context.sandbox_id, head.preflight.context.request_id)
        handle = next(iter(self.service.pending[key][2]))
        value = {"choices": [{"message": {"function_call": {
            "name": "bash", "arguments": json.dumps({"command": handle})}}}]}
        results = list(self.service.Evaluate(iter([head, self.response_body(json.dumps(value).encode())]), self.context()))
        self.assertIn(handle.encode(), results[1].body_result.transform.data)
        self.assertNotIn(m.CANARY.encode(), results[1].body_result.transform.data)


if __name__ == "__main__":
    unittest.main()
