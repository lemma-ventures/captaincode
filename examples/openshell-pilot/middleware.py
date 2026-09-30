import argparse
import concurrent.futures
import hashlib
import json
import os
import subprocess
import threading
import time
from pathlib import Path

import extension_pb2 as extension
import grpc
import jwt
import supervisor_middleware_pb2 as pb
import supervisor_middleware_pb2_grpc as rpc
from response import encode, restore_response

LIMIT = 4 * 1024 * 1024
AUDIENCE = "urn:openshell:extension:middleware:captain-shield"
CAPABILITY = "openshell.supervisor-middleware.contract"
CANARY = "nvapi-" + "captainpilot0123456789" * 2


class Shield(rpc.SupervisorMiddlewareServicer):
    def __init__(self, binary, key, audit, model):
        self.binary = str(binary)
        self.key = key
        self.audit = audit
        self.model = model
        self.lock = threading.Lock()
        self.pending = {}

    def authenticate(self, context, sandbox=None):
        try:
            token = dict(context.invocation_metadata())["authorization"]
            if not token.startswith("Bearer "):
                raise ValueError()
            token = token[7:]
            header = jwt.get_unverified_header(token)
            if header.get("typ") != "openshell-ext+jwt" or header.get("alg") != "EdDSA":
                raise ValueError()
            claims = jwt.decode(token, self.key, algorithms=["EdDSA"], audience=AUDIENCE,
                                issuer="openshell-gateway:captain-pilot",
                                options={"require": ["exp", "iss", "aud", "caller_kind"]})
            if sandbox is None:
                supervisor = (claims["caller_kind"] == "supervisor"
                              and isinstance(claims.get("sandbox_id"), str) and bool(claims["sandbox_id"]))
                if claims["caller_kind"] != "gateway" and not supervisor:
                    raise ValueError()
            elif claims["caller_kind"] != "supervisor" or claims.get("sandbox_id") != sandbox:
                raise ValueError()
        except (KeyError, ValueError, jwt.PyJWTError):
            context.abort(grpc.StatusCode.UNAUTHENTICATED, "invalid extension identity")

    def Describe(self, request, context):
        self.authenticate(context)
        if (request.gateway.protocol_version.major != 1
                or set(request.gateway.required_capabilities) - {CAPABILITY}
                or CAPABILITY not in request.gateway.supported_capabilities):
            context.abort(grpc.StatusCode.FAILED_PRECONDITION, "unsupported middleware protocol")
        return pb.MiddlewareManifest(
            name="captain-shield", service_version="pilot-1", expected_audience=AUDIENCE,
            extension=extension.PeerMetadata(
                protocol_version=extension.ProtocolVersion(major=1, minor=0),
                implementation_name="captain-shield", implementation_version="pilot-1",
                supported_capabilities=[CAPABILITY], required_capabilities=[CAPABILITY]),
            bindings=[pb.MiddlewareBinding(operation=1, phase=1, max_payload_bytes=LIMIT),
                      pb.MiddlewareBinding(operation=3, phase=2, max_payload_bytes=LIMIT)])

    def ValidateConfig(self, request, context):
        self.authenticate(context)
        return pb.ValidateConfigResponse(valid=not bool(request.config.fields),
                                         reason="" if not request.config.fields else "configuration is fixed for this pilot")

    def EvaluateHttpRequest(self, request, context):
        self.authenticate(context, request.context.sandbox_id)
        target = request.target
        if (request.phase != 1 or target.scheme != "https"
                or target.host != "integrate.api.nvidia.com" or target.port != 443
                or target.method != "POST" or target.path != "/v1/chat/completions"
                or target.query or len(request.body) > LIMIT):
            return pb.HttpRequestResult(decision=2, reason_code="outside_pilot_scope")
        try:
            headers = {h.name.lower(): h.value for h in request.headers}
            if "content-encoding" in headers:
                raise ValueError()
            if not headers.get("content-type", "").lower().startswith("application/json"):
                raise ValueError()
            original = json.loads(request.body)
            if original.get("model") != self.model:
                raise ValueError()
            stream_response = original.get("stream", False)
            if type(stream_response) is not bool or not request.context.request_id:
                raise ValueError()
            upstream = dict(original, stream=False)
            upstream.pop("stream_options", None)
            with self.lock:
                now = time.monotonic()
                self.pending = {key: value for key, value in self.pending.items() if now - value[0] < 600}
                key = (request.context.sandbox_id, request.context.request_id)
                if len(self.pending) >= 128 or key in self.pending:
                    raise ValueError()
                result = subprocess.run([self.binary], input=encode(upstream), capture_output=True,
                                        timeout=3, check=True)
                masked = json.loads(result.stdout)
                body = json.dumps(masked["body"], ensure_ascii=False, separators=(",", ":")).encode()
                if len(body) > LIMIT or CANARY.encode() in body:
                    raise ValueError()
                event = {"at": time.time(), "sandbox_id": request.context.sandbox_id,
                         "request_id": request.context.request_id, "secrets": masked["secrets"],
                         "identities": masked["identities"], "body_sha256": hashlib.sha256(body).hexdigest(),
                         "canary_masked": CANARY.encode() in request.body,
                         "tool_canary_masked": any(m.get("role") == "tool" and CANARY in json.dumps(m)
                                                  for m in original.get("messages", []))}
                with self.audit.open("a") as stream:
                    stream.write(json.dumps(event) + "\n")
                    stream.flush()
                    os.fsync(stream.fileno())
                self.pending[key] = (now, stream_response)
            return pb.HttpRequestResult(decision=1, has_body=True, body=body)
        except (ValueError, TypeError, AttributeError, KeyError, OSError, subprocess.SubprocessError):
            return pb.HttpRequestResult(decision=2, reason_code="shield_failed")

    def restore(self, value):
        data = encode(value)
        if len(data) > LIMIT:
            raise ValueError("restoration input exceeds limit")
        result = subprocess.run([self.binary, "restore-identity"], input=data, capture_output=True,
                                timeout=3, check=True)
        restored = json.loads(result.stdout)
        return restored["body"], restored["identities"]

    def Evaluate(self, requests, context):
        self.authenticate(context)
        preflight = None
        complete = False
        trailers = False
        for request in requests:
            event = request.WhichOneof("event")
            if event == "session_end":
                return
            if event == "preflight" and preflight is None:
                preflight = request.preflight
                self.authenticate(context, preflight.context.sandbox_id)
                target = preflight.target
                types = [h.value.split(";", 1)[0].strip().lower() for h in preflight.headers
                         if h.name.lower() == "content-type"]
                target_allowed = (target.scheme == "https" and target.host == "integrate.api.nvidia.com"
                                  and target.port == 443 and target.method == "POST"
                                  and target.path == "/v1/chat/completions" and not target.query)
                type_allowed = len(types) == 1 and types[0] == "application/json"
                inspectable = 2 in preflight.permitted_body_modes and preflight.max_payload_bytes > 0
                with self.lock:
                    pending = self.pending.pop((preflight.context.sandbox_id, preflight.context.request_id), None)
                paired = pending is not None and time.monotonic() - pending[0] < 600
                try:
                    audit = {"at": time.time(), "sandbox_id": preflight.context.sandbox_id,
                             "request_id": preflight.context.request_id, "phase": "response_preflight",
                             "status": preflight.status_code, "target_allowed": bool(target_allowed),
                             "paired": paired,
                             "type_allowed": type_allowed, "whole_body_allowed": inspectable,
                             "encoded": any(h.name.lower() == "content-encoding" for h in preflight.headers),
                             "no_transform": any(h.name.lower() == "cache-control" and "no-transform" in h.value.lower()
                                                 for h in preflight.headers)}
                    with self.lock, self.audit.open("a") as stream:
                        stream.write(json.dumps(audit) + "\n")
                        stream.flush()
                        os.fsync(stream.fileno())
                except OSError:
                    inspectable = False
                if not (target_allowed and type_allowed and inspectable and paired):
                    yield pb.HttpResponseEventResult(preflight_result=pb.HttpResponsePreflightResult(
                        block_delivery=pb.HttpResponseBlockDelivery(), reason_code="shield_response_scope"))
                    return
                limit = min(LIMIT, preflight.max_payload_bytes)
                stream_response = pending[1] and 200 <= preflight.status_code < 300
                mutations = [pb.HeaderMutation(write=pb.WriteHeader(name="content-type", value="text/event-stream", on_existing=2))] if stream_response else []
                yield pb.HttpResponseEventResult(preflight_result=pb.HttpResponsePreflightResult(
                    inspect=pb.HttpResponsePreflightInspect(body_mode=2, header_mutations=mutations)))
            elif event == "body" and preflight is not None and not complete:
                unit = request.body
                try:
                    if unit.sequence != 1 or not unit.end_of_stream or unit.WhichOneof("payload") != "data":
                        raise ValueError("invalid whole response unit")
                    with self.lock:
                        body, identities = restore_response(unit.data, self.restore, limit, stream_response)
                        audit = {"at": time.time(), "sandbox_id": preflight.context.sandbox_id,
                                 "request_id": preflight.context.request_id, "phase": "response",
                                 "identities": identities, "bytes": len(body), "mode": "whole_body"}
                        with self.audit.open("a") as stream:
                            stream.write(json.dumps(audit) + "\n")
                            stream.flush()
                            os.fsync(stream.fileno())
                    complete = True
                    yield pb.HttpResponseEventResult(body_result=pb.HttpResponseBodyResult(
                        sequence=unit.sequence, transform=pb.HttpResponseBodyTransform(data=body)))
                except (ValueError, TypeError, AttributeError, KeyError, OSError,
                        RecursionError, subprocess.SubprocessError):
                    yield pb.HttpResponseEventResult(body_result=pb.HttpResponseBodyResult(
                        sequence=unit.sequence, block_delivery=pb.HttpResponseBlockDelivery(),
                        reason_code="shield_response_failed"))
                    return
            elif event == "trailers" and complete and not trailers:
                trailers = True
                yield pb.HttpResponseEventResult(trailers_result=pb.HttpResponseTrailersResult())
            else:
                context.abort(grpc.StatusCode.INVALID_ARGUMENT, "invalid response sequence")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--state", type=Path, required=True)
    parser.add_argument("--port", type=int, required=True)
    parser.add_argument("--model", required=True)
    args = parser.parse_args()
    certs = args.state / "certs"
    server = grpc.server(concurrent.futures.ThreadPoolExecutor(max_workers=2),
                         options=[("grpc.max_receive_message_length", LIMIT + 131072)])
    service = Shield(args.state / "shield", (certs / "jwt/public.pem").read_text(),
                     args.state / "shield-audit.jsonl", args.model)
    rpc.add_SupervisorMiddlewareServicer_to_server(service, server)
    rpc.add_HttpResponsePreReturnServicer_to_server(service, server)
    credentials = grpc.ssl_server_credentials([((certs / "server/tls.key").read_bytes(),
                                                (certs / "server/tls.crt").read_bytes())])
    if not server.add_secure_port(f"0.0.0.0:{args.port}", credentials):
        raise RuntimeError("middleware listener unavailable")
    server.start()
    server.wait_for_termination()


if __name__ == "__main__":
    main()
