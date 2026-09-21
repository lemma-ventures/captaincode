#!/usr/bin/env python3
"""A System One-shaped endpoint in front of laya-mlx, on loopback.

Captain's decision leg speaks one shape (pkg/captaincode/systemone.go):

    POST /v1/systemone  {state, model, questions{<name>: {type, instructions, criteria}}}
      -> {model, answers{<name>: {type, choice|score|noul, probabilities, confidence}}, usage}
    GET  /v1/models     -> {models: [{name, description, release_date}]}

laya-mlx already answers in that shape - its Agent.system_one returns
{model, answers, usage} with the same fields, because both descend from the
same typed-decision format - so this file is a socket and two guards, not a
translation layer.

The guards are the reason it exists rather than being a curl one-liner.

Truncation. laya's build_sequence CUTS a state that overruns max_len and
answers anyway. A 512-token checkpoint handed captain's action-gate state
would return a confident reading of the first two thirds of a command, which
is worse than returning nothing. So an oversized state is refused here with
400 max_tokens_exceeded, the same status the vendor answers with, and captain
sends that call to the backend that can hold it.

Loopback. This endpoint answers anything that reaches it and captain sends it
redacted task heads, so it binds 127.0.0.1 and refuses anything else without
--allow-remote said out loud.

    pip install laya-mlx
    python3 sidecars/laya/serve.py --port 8181
    CAPTAIN_SYSTEMONE_OPEN_URL=http://127.0.0.1:8181 captain jev --open

Apple silicon, macOS 14+, Python 3.11+. Requires laya-mlx (Apache-2.0,
github.com/mizorewww/laya-mlx), which is not vendored here and is not a
dependency of Captain Code: without it this file does not run and nothing
else changes.
"""

import argparse
import json
import sys
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

MAX_BODY = 4 << 20

# One lock around inference: MLX evaluates on a shared device and the server
# is threaded so a slow reader cannot wedge the queue.
LOCK = threading.Lock()
AGENT = None
CHECKPOINT = ""


def serialize_state(state):
    """laya's own state rendering, with a local fallback if it moves."""
    try:
        from laya_mlx.common import serialize_state as upstream

        return upstream(state)
    except Exception:
        return state if isinstance(state, str) else json.dumps(state, ensure_ascii=False)


def state_room(questions):
    """Tokens left for the state, the tightest question deciding.

    Exact where laya's internals are reachable, conservative where they are
    not: an estimate that refuses a call laya would have answered costs one
    round trip, and an estimate that lets a truncated one through costs a
    wrong answer nobody can see is wrong.
    """
    cfg = AGENT.cfg
    max_len, head = cfg.get("max_len", 512), cfg.get("head_max_len", 192)
    try:
        from laya_mlx.common import build_prefix

        room = max_len
        for qdef in questions.values():
            prefix, _ = build_prefix(AGENT.tok, type(AGENT)._to_internal(qdef), head)
            room = min(room, max_len - len(prefix) - 1)
        return room
    except Exception:
        # [CLS] head [SEP] options [SEP] is bounded by head_max_len plus the
        # specials; 12 covers them with room to spare.
        return max_len - head - 12


def state_tokens(state):
    return len(AGENT.tok(serialize_state(state), add_special_tokens=False)["input_ids"])


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    server_version = "captain-laya-sidecar/1"

    def log_message(self, fmt, *args):  # one line per call, on our own terms
        pass

    def _send(self, code, payload):
        body = json.dumps(payload).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _error(self, code, kind, message):
        self._send(code, {"error": {"type": kind, "message": message}})

    def do_GET(self):
        if self.path.rstrip("/") != "/v1/models":
            return self._error(404, "not_found", self.path)
        cfg = AGENT.cfg
        self._send(
            200,
            {
                "models": [
                    {
                        "name": CHECKPOINT,
                        "description": "laya-mlx typed decisions, %d-token context, local"
                        % cfg.get("max_len", 512),
                        "release_date": "",
                    }
                ]
            },
        )

    def do_POST(self):
        if self.path.rstrip("/") != "/v1/systemone":
            return self._error(404, "not_found", self.path)
        length = int(self.headers.get("Content-Length") or 0)
        if length <= 0 or length > MAX_BODY:
            return self._error(400, "invalid_request", "body must be 1..%d bytes" % MAX_BODY)
        try:
            req = json.loads(self.rfile.read(length))
        except json.JSONDecodeError as e:
            return self._error(400, "invalid_request", "body is not JSON: %s" % e)
        state, questions = req.get("state", ""), req.get("questions")
        if not isinstance(questions, dict) or not questions:
            return self._error(422, "invalid_request", "questions must be a non-empty object")

        t0 = time.monotonic()
        try:
            with LOCK:
                room, used = state_room(questions), state_tokens(state)
                if used > room:
                    # The refusal this sidecar exists for. Same status the
                    # vendor answers with, so captain reads it the same way.
                    return self._error(
                        400,
                        "max_tokens_exceeded",
                        "state is %d tokens and this checkpoint leaves %d for it after the "
                        "question; laya would truncate and answer anyway, so it is refused "
                        "here - send this call to a backend that holds it" % (used, room),
                    )
                out = AGENT.system_one(state, questions)
        except ValueError as e:
            return self._error(422, "invalid_question", str(e))
        except Exception as e:  # a wedged device, a bad dtype, anything else
            return self._error(500, "internal_error", "%s: %s" % (type(e).__name__, e))
        out["model"] = CHECKPOINT  # what actually served, not what was asked for
        ms = (time.monotonic() - t0) * 1000
        print(
            "systemone %d question(s) · state %d/%d tokens · %.1fms"
            % (len(questions), used, room, ms),
            file=sys.stderr,
            flush=True,
        )
        self._send(200, out)


def main():
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--model", default="aac6fef/laya-mlx", help="checkpoint id or local path")
    ap.add_argument("--host", default="127.0.0.1")
    ap.add_argument("--port", type=int, default=8181)
    ap.add_argument("--dtype", default="float16", choices=["float16", "float32", "bfloat16"])
    ap.add_argument("--batch-size", type=int, default=16)
    ap.add_argument(
        "--allow-remote",
        action="store_true",
        help="bind somewhere other than loopback: this endpoint has no authentication",
    )
    args = ap.parse_args()
    if args.host not in ("127.0.0.1", "::1", "localhost") and not args.allow_remote:
        sys.exit(
            "refusing to bind %s: this endpoint answers anything that reaches it and has no "
            "authentication. Pass --allow-remote if you meant it." % args.host
        )

    try:
        import laya_mlx
    except ImportError:
        sys.exit("laya-mlx is not installed: pip install laya-mlx (Apple silicon, macOS 14+)")

    global AGENT, CHECKPOINT
    t0 = time.monotonic()
    AGENT = laya_mlx.load(args.model, dtype=args.dtype, batch_size=args.batch_size)
    CHECKPOINT = args.model
    # Warm it before the socket opens. The first call compiles and pages in
    # weights; captain gives a local backend two seconds, and a cold first
    # answer would spend them and teach the shadow that this backend times out.
    AGENT.system_one("warming the sidecar", {"warm": {"type": "noul", "instructions": "Is this a warm-up?"}})
    print(
        "laya sidecar: %s loaded in %.1fs · holds %d tokens · http://%s:%d"
        % (CHECKPOINT, time.monotonic() - t0, AGENT.cfg.get("max_len", 512), args.host, args.port),
        file=sys.stderr,
        flush=True,
    )
    print(
        "set CAPTAIN_SYSTEMONE_OPEN_URL=http://%s:%d and run `captain jev conform --open`"
        % (args.host, args.port),
        file=sys.stderr,
        flush=True,
    )
    ThreadingHTTPServer((args.host, args.port), Handler).serve_forever()


if __name__ == "__main__":
    main()
