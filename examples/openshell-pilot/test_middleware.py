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
        self.signer = Ed25519PrivateKey.generate()
        self.service = m.Shield(os.environ["PILOT_SHIELD_BIN"], self.signer.public_key(),
                               self.path / "audit.jsonl", "test-model")
        self.environment = patch.dict(os.environ, {"HOME": str(self.path), "CAPTAIN_REDACT": "off"})
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


if __name__ == "__main__":
    unittest.main()
