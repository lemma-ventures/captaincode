#!/usr/bin/env python3
import argparse
import faulthandler
import json
import os
import shutil
import signal
import subprocess
import tempfile
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path


def response_events(content, stop_reason):
    events = [
        (
            "message_start",
            {
                "type": "message_start",
                "message": {
                    "id": "msg_bb4",
                    "type": "message",
                    "role": "assistant",
                    "model": "fixture",
                    "content": [],
                    "stop_reason": None,
                    "stop_sequence": None,
                    "usage": {"input_tokens": 1, "output_tokens": 1},
                },
            },
        )
    ]
    for index, block in enumerate(content):
        start = {
            "type": "content_block_start",
            "index": index,
            "content_block": dict(block),
        }
        if block["type"] == "tool_use":
            start["content_block"]["input"] = {}
        events.append(("content_block_start", start))
        if block["type"] == "tool_use":
            delta = {
                "type": "input_json_delta",
                "partial_json": json.dumps(block["input"]),
            }
        else:
            delta = {"type": "text_delta", "text": block["text"]}
        events.append(
            (
                "content_block_delta",
                {"type": "content_block_delta", "index": index, "delta": delta},
            )
        )
        events.append(
            ("content_block_stop", {"type": "content_block_stop", "index": index})
        )
    events.extend(
        [
            (
                "message_delta",
                {
                    "type": "message_delta",
                    "delta": {"stop_reason": stop_reason, "stop_sequence": None},
                    "usage": {"output_tokens": 1},
                },
            ),
            ("message_stop", {"type": "message_stop"}),
        ]
    )
    return events


class FixtureServer(ThreadingHTTPServer):
    daemon_threads = True
    block_on_close = False

    def server_bind(self):
        self.socket.bind(self.server_address)
        self.server_address = self.socket.getsockname()
        self.server_name = "127.0.0.1"
        self.server_port = self.server_address[1]


class Fixture:
    def __init__(self, target):
        self.target = target
        self.requests = []
        fixture = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *_):
                return

            def do_POST(self):
                length = int(self.headers.get("content-length", "0"))
                if length > 1_048_576:
                    self.send_error(413)
                    return
                body = json.loads(self.rfile.read(length) or b"{}")
                fixture.requests.append({"path": self.path, "body": body})
                route = self.path.split("?", 1)[0]
                if route.endswith("/count_tokens"):
                    self.answer({"input_tokens": 1})
                    return
                if route != "/v1/messages":
                    self.send_error(404)
                    return
                messages = body.get("messages", [])
                has_result = any(
                    isinstance(message.get("content"), list)
                    and any(
                        block.get("type") == "tool_result"
                        for block in message["content"]
                        if isinstance(block, dict)
                    )
                    for message in messages
                )
                if has_result:
                    content = [{"type": "text", "text": "BB4_CLAUDE_DONE"}]
                    stop_reason = "end_turn"
                else:
                    content = [
                        {
                            "type": "tool_use",
                            "id": "toolu_bb4",
                            "name": "Write",
                            "input": {
                                "file_path": str(fixture.target),
                                "content": "dangerous-example\n",
                            },
                        }
                    ]
                    stop_reason = "tool_use"
                message = {
                    "id": "msg_bb4",
                    "type": "message",
                    "role": "assistant",
                    "model": body.get("model", "fixture"),
                    "content": content,
                    "stop_reason": stop_reason,
                    "stop_sequence": None,
                    "usage": {"input_tokens": 1, "output_tokens": 1},
                }
                if not body.get("stream"):
                    self.answer(message)
                    return
                payload = "".join(
                    f"event: {name}\ndata: {json.dumps(data)}\n\n"
                    for name, data in response_events(content, stop_reason)
                ).encode()
                self.send_response(200)
                self.send_header("content-type", "text/event-stream")
                self.send_header("content-length", str(len(payload)))
                self.end_headers()
                self.wfile.write(payload)

            def answer(self, value):
                payload = json.dumps(value).encode()
                self.send_response(200)
                self.send_header("content-type", "application/json")
                self.send_header("content-length", str(len(payload)))
                self.end_headers()
                self.wfile.write(payload)

        self.server = FixtureServer(("127.0.0.1", 0), Handler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()

    def close(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=5)


def main():
    faulthandler.dump_traceback_later(100, exit=True)
    parser = argparse.ArgumentParser(
        description="Run one real Claude Code tool call through the Blackbox audit hook"
    )
    parser.add_argument("--captain", required=True, type=Path)
    parser.add_argument("--claude", required=True, type=Path)
    args = parser.parse_args()
    captain = args.captain.resolve(strict=True)
    claude = args.claude.resolve(strict=True)
    scratch = Path(tempfile.mkdtemp(prefix="captain-claude-blackbox-")).resolve()
    fixture = None
    child = None
    try:
        home = scratch / "home"
        project = scratch / "project"
        temporary = scratch / "tmp"
        for directory in (home / ".claude", project, temporary):
            directory.mkdir(parents=True)
        target = project / "config" / "private" / "leak.txt"
        snapshot_path = (
            Path(__file__).resolve().parents[2]
            / "pkg"
            / "captaincode"
            / "testdata"
            / "blackbox"
            / "snapshot.json"
        )
        snapshot = json.loads(snapshot_path.read_text())
        snapshot["paths"].append(
            {
                "id": "path-live",
                "path": f"{project}/config/private/**",
                "kind": "protected_path",
                "severity": "critical",
            }
        )
        snapshot_file = scratch / "snapshot.json"
        snapshot_file.write_text(json.dumps(snapshot))
        security_log = scratch / "security.jsonl"
        user_marker = scratch / "user-hook-ran"
        user_hook = scratch / "user-hook.sh"
        user_hook.write_text(
            f"#!/bin/sh\n/bin/cat >/dev/null\n/usr/bin/touch '{user_marker}'\n"
        )
        user_hook.chmod(0o700)
        settings = {
            "hooks": {
                "PreToolUse": [
                    {
                        "matcher": "Write",
                        "hooks": [
                            {
                                "type": "command",
                                "command": f"{captain} redact --hook",
                                "timeout": 10,
                            }
                        ],
                    },
                    {
                        "matcher": "Write",
                        "hooks": [
                            {
                                "type": "command",
                                "command": f"/bin/sh {user_hook}",
                                "timeout": 10,
                            }
                        ],
                    },
                ]
            }
        }
        (home / ".claude" / "settings.json").write_text(json.dumps(settings))
        fixture = Fixture(target)
        profile = scratch / "loopback.sb"
        profile.write_text(
            "\n".join(
                [
                    "(version 1)",
                    "(allow default)",
                    "(deny network-outbound)",
                    "(deny file-write*)",
                    f'(allow file-write* (subpath "{scratch}"))',
                    '(allow file-write* (literal "/dev/null"))',
                    '(allow file-write* (literal "/dev/urandom"))',
                    f'(allow network-outbound (remote ip "localhost:{fixture.server.server_port}"))',
                ]
            )
        )
        env = {
            "HOME": str(home),
            "PATH": "/usr/bin:/bin:/usr/sbin:/sbin",
            "TMPDIR": str(temporary),
            "XDG_CONFIG_HOME": str(scratch / "config"),
            "XDG_CACHE_HOME": str(scratch / "cache"),
            "XDG_DATA_HOME": str(scratch / "data"),
            "XDG_STATE_HOME": str(scratch / "state"),
            "ANTHROPIC_BASE_URL": f"http://127.0.0.1:{fixture.server.server_port}",
            "ANTHROPIC_AUTH_TOKEN": "fixture-token",
            "ANTHROPIC_API_KEY": "fixture-token",
            "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
            "DISABLE_AUTOUPDATER": "1",
            "DISABLE_TELEMETRY": "1",
            "DISABLE_ERROR_REPORTING": "1",
            "CAPTAIN_BLACKBOX": "audit",
            "CAPTAIN_BLACKBOX_SNAPSHOT": str(snapshot_file),
            "CAPTAIN_BLACKBOX_STATE": str(security_log),
        }

        command = [
            "/bin/sh",
            "-c",
            'ulimit -t 90; ulimit -f 65536; ulimit -n 256; exec "$@"',
            "fixture-limits",
            "/usr/bin/sandbox-exec",
            "-f",
            str(profile),
            str(claude),
            "-p",
            "Use Write once for the requested file, then stop.",
            "--model",
            "claude-sonnet-4-5",
            "--output-format",
            "json",
            "--allowedTools",
            "Write",
            "--permission-mode",
            "acceptEdits",
            "--strict-mcp-config",
            "--mcp-config",
            '{"mcpServers":{}}',
            "--max-turns",
            "4",
        ]
        started = time.monotonic()
        child = subprocess.Popen(
            command,
            cwd=project,
            env=env,
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            start_new_session=True,
        )
        while child.poll() is None:
            if time.monotonic() - started > 90:
                os.killpg(child.pid, signal.SIGKILL)
                raise AssertionError("Claude Code exceeded the 90-second wall limit")
            usage = subprocess.run(
                ["/bin/ps", "-axo", "pgid=,rss="],
                capture_output=True,
                text=True,
                check=True,
            ).stdout
            rss = sum(
                int(line.split()[1])
                for line in usage.splitlines()
                if line.split() and line.split()[0] == str(child.pid)
            )
            if rss > 1_048_576:
                os.killpg(child.pid, signal.SIGKILL)
                raise AssertionError(
                    "Claude Code exceeded the 1 GiB process-group memory limit"
                )
            time.sleep(0.1)
        stdout, stderr = child.communicate(timeout=5)
        elapsed = time.monotonic() - started
        assert child.returncode == 0, (
            f"Claude Code failed ({child.returncode}): {stdout[-8000:]}\n{stderr[-8000:]}\nfixture routes={[request['path'] for request in fixture.requests]}"
        )
        assert target.read_text() == "dangerous-example\n", (
            "the restored Write input did not execute"
        )
        assert user_marker.exists(), "the unrelated user hook did not survive"
        rows = [
            json.loads(line) for line in security_log.read_text().splitlines() if line
        ]
        assert len(rows) == 1, (
            f"expected one Claude pre-execution record, got {len(rows)}"
        )
        row = rows[0]
        assert row["host"] == "claude"
        assert row["phase"] == "pre"
        assert "BB4_CLAUDE_DONE" in stdout
        assert row["tool"] == "write"
        assert row["disposition"] == "would_block"
        assert row["snapshot_id"] == "fixture"
        assert row["finding_count"] >= 1
        assert row["workspace_id"] and row["session_id"]
        serialized = json.dumps(row)
        assert str(target) not in serialized and "dangerous-example" not in serialized
        assert any(
            block.get("type") == "tool_result"
            for request in fixture.requests
            for message in request["body"].get("messages", [])
            if isinstance(message.get("content"), list)
            for block in message["content"]
            if isinstance(block, dict)
        ), "Claude Code did not send the tool result back to the fixture model"
        print(
            "PASS: real Claude Code executed one Write through the composed Captain hook"
        )
        print("PASS: the unrelated user hook ran and the private record is path-free")
        print(
            f"PASS: audit disposition={row['disposition']}; findings={row['finding_count']}; elapsed={elapsed:.3f}s"
        )
    finally:
        if child is not None and child.poll() is None:
            os.killpg(child.pid, signal.SIGKILL)
            child.wait(timeout=5)
        if fixture is not None:
            fixture.close()
        shutil.rmtree(scratch)


if __name__ == "__main__":
    main()
