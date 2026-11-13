#!/usr/bin/env python3
# /// script
# requires-python = ">=3.11"
# dependencies = ["pyte==0.8.2"]
# ///
"""Offline installed-client UI check using authored Router transport fixtures."""

import argparse
import base64
import codecs
from enum import StrEnum
import fcntl
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import pty
import select
import shutil
import signal
import sqlite3
import struct
import tempfile
import termios
import threading
import time

import pyte


class Client(StrEnum):
    CODEX = "codex"
    CLAUDE = "claude"


WAITING_MESSAGE = "Waiting on upstream provider.."
ANSWER = "Authored fixture reply."
INSTALL = Path(__file__).resolve().parents[2]


class FixtureRouter(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def log_error(self, *_):
        pass

    def do_POST(self):
        payload = json.loads(
            self.rfile.read(int(self.headers.get("Content-Length", "0"))) or b"{}"
        )
        if self.path.endswith("/count_tokens"):
            body = b'{"input_tokens":3}'
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        framed = self.headers.get("X-Weave-Provider-Wait") == "1"
        self.server.observed.append({"path": self.path, "framed": framed})
        if self.server.expected_router_header:
            assert (
                self.headers.get("X-Weave-Router-Key")
                == self.server.expected_router_header
            )
        self.send_response(200)
        self.send_header(
            "Content-Type",
            (
                "application/vnd.weave.provider-wait+ndjson"
                if framed
                else "text/event-stream"
            ),
        )
        self.end_headers()

        def frame(kind, **fields):
            self.wfile.write((json.dumps({"type": kind, **fields}) + "\n").encode())
            self.wfile.flush()

        def emit(kind, **fields):
            event = {"type": kind, **fields}
            body = f"event: {kind}\ndata: {json.dumps(event)}\n\n".encode()
            if framed:
                frame("body", body=base64.b64encode(body).decode())
            else:
                self.wfile.write(body)
                self.wfile.flush()

        try:
            if framed:
                frame("waiting", waiting=True)
            time.sleep(4)
            if framed:
                frame("waiting", waiting=False)
                frame(
                    "response",
                    status=200,
                    headers={"Content-Type": ["text/event-stream"]},
                )
            model = payload.get("model", "fixture-model")
            if self.path.endswith("/responses"):
                response = {
                    "id": "resp_fixture",
                    "object": "response",
                    "created_at": int(time.time()),
                    "status": "in_progress",
                    "model": model,
                    "output": [],
                }
                emit("response.created", response=response)
                message = {
                    "id": "msg_fixture",
                    "type": "message",
                    "role": "assistant",
                    "status": "in_progress",
                    "content": [],
                }
                emit("response.output_item.added", output_index=0, item=message)
                part = {"type": "output_text", "text": "", "annotations": []}
                emit(
                    "response.content_part.added",
                    item_id=message["id"],
                    output_index=0,
                    content_index=0,
                    part=part,
                )
                emit(
                    "response.output_text.delta",
                    item_id=message["id"],
                    output_index=0,
                    content_index=0,
                    delta=ANSWER,
                )
                emit(
                    "response.output_text.done",
                    item_id=message["id"],
                    output_index=0,
                    content_index=0,
                    text=ANSWER,
                )
                part["text"] = ANSWER
                emit(
                    "response.content_part.done",
                    item_id=message["id"],
                    output_index=0,
                    content_index=0,
                    part=part,
                )
                message.update(status="completed", content=[part])
                emit("response.output_item.done", output_index=0, item=message)
                response.update(
                    status="completed",
                    output=[message],
                    usage={"input_tokens": 3, "output_tokens": 3, "total_tokens": 6},
                )
                emit("response.completed", response=response)
            else:
                emit(
                    "message_start",
                    message={
                        "id": "msg_fixture",
                        "type": "message",
                        "role": "assistant",
                        "model": model,
                        "content": [],
                        "stop_reason": None,
                        "stop_sequence": None,
                        "usage": {"input_tokens": 3, "output_tokens": 0},
                    },
                )
                emit(
                    "content_block_start",
                    index=0,
                    content_block={"type": "text", "text": ""},
                )
                emit(
                    "content_block_delta",
                    index=0,
                    delta={"type": "text_delta", "text": ANSWER},
                )
                emit("content_block_stop", index=0)
                emit(
                    "message_delta",
                    delta={"stop_reason": "end_turn", "stop_sequence": None},
                    usage={"output_tokens": 3},
                )
                emit("message_stop")
            if framed:
                frame("done")
        except (BrokenPipeError, ConnectionResetError):
            pass


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--client", type=Client, choices=list(Client), required=True)
    parser.add_argument("--interrupt", action="store_true")
    parser.add_argument(
        "--profile",
        action="store_true",
        help="Exercise a Codex profile with its own Router endpoint",
    )
    args = parser.parse_args()
    evidence = Path(tempfile.mkdtemp(prefix=f"weave-wait-{args.client}-ui-")).resolve()
    client_home = evidence / "client-home"
    client_home.mkdir()
    router = ThreadingHTTPServer(("127.0.0.1", 0), FixtureRouter)
    router.daemon_threads = True
    router.observed = []
    router.expected_router_header = (
        "fixture-profile-router-key"
        if args.client == Client.CODEX and args.profile
        else None
    )
    base_url = f"http://127.0.0.1:{router.server_port}"
    environment = dict(os.environ, TERM="xterm-256color")
    for key in (
        "OPENAI_API_KEY",
        "OPENAI_BASE_URL",
        "CODEX_API_KEY",
        "WEAVE_API_KEY",
        "ANTHROPIC_AUTH_TOKEN",
        "CLAUDE_CODE_OAUTH_TOKEN",
    ):
        environment.pop(key, None)
    if args.client == Client.CODEX:
        environment["CODEX_HOME"] = str(client_home)
        (client_home / "config.toml").write_text(
            f"""model = "gpt-6.1-sol"
model_provider = "weave"
approval_policy = "never"
sandbox_mode = "read-only"
check_for_update_on_startup = false
suppress_unstable_features_warning = true
cli_auth_credentials_store = "file"
web_search = "disabled"
[model_providers.weave]
name = "Authored fixture Router"
base_url = "{base_url}/v1"
wire_api = "responses"
requires_openai_auth = false
supports_websockets = false
[features]
hooks = true
apps = false
plugins = false
memories = false
[analytics]
enabled = false
[feedback]
enabled = false
"""
        )
        client_args = ["--no-alt-screen", "Read the authored fixture."]
        if args.profile:
            config = client_home / "config.toml"
            config.write_text(
                config.read_text().replace(f"{base_url}/v1", "http://127.0.0.1:9/v1")
            )
            (client_home / "fixture.config.toml").write_text(
                f'[model_providers.weave]\nbase_url = "{base_url}/v1"\n[model_providers.weave.http_headers]\n"X-Weave-Router-Key" = "fixture-profile-router-key"\n'
            )
            client_args = ["--profile", "fixture", *client_args]
    else:
        environment.update(
            CLAUDE_CONFIG_DIR=str(client_home),
            ANTHROPIC_BASE_URL=base_url,
            ANTHROPIC_API_KEY="fixture-key",
            CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC="1",
        )
        (client_home / ".claude.json").write_text(
            json.dumps(
                {
                    "hasCompletedOnboarding": True,
                    "lastOnboardingVersion": "2.1.292",
                    "theme": "dark",
                    "customApiKeyResponses": {
                        "approved": ["fixture-key"],
                        "rejected": [],
                    },
                    "projects": {str(evidence): {"hasTrustDialogAccepted": True}},
                }
            )
        )
        (client_home / "settings.json").write_text(
            json.dumps(
                {
                    "env": {
                        "ANTHROPIC_BASE_URL": base_url,
                        "ANTHROPIC_API_KEY": "fixture-key",
                    }
                }
            )
        )
        client_args = ["--model", "claude-sonnet-4-6", "Read the authored fixture."]
    child, descriptor = pty.fork()
    if child == 0:
        os.chdir(evidence)
        os.execve(
            shutil.which("node"),
            ["node", str(INSTALL / "npm" / "bin.js"), args.client, *client_args],
            environment,
        )
    threading.Thread(target=router.serve_forever, daemon=True).start()
    fcntl.ioctl(descriptor, termios.TIOCSWINSZ, struct.pack("HHHH", 45, 160, 0, 0))
    screen = pyte.Screen(160, 45)
    stream = pyte.Stream(screen)
    decoder = codecs.getincrementaldecoder("utf-8")("replace")
    raw = bytearray()
    waiting_seen = False
    waiting_frames = set()
    answer_seen = False
    settled_at = None
    final_screen = ""
    accepted = set()
    try:
        deadline = time.monotonic() + 60
        while time.monotonic() < deadline:
            if select.select([descriptor], [], [], 0.1)[0]:
                try:
                    chunk = os.read(descriptor, 65536)
                except OSError:
                    break
                if not chunk:
                    break
                raw.extend(chunk)
                stream.feed(decoder.decode(chunk))
                if b"\x1b[6n" in chunk:
                    os.write(descriptor, b"\x1b[1;1R")
            visible = "\n".join(line.rstrip() for line in screen.display)
            (evidence / "live-screen.txt").write_text(visible)
            for phrase in ("Yes, I trust this folder", "Yes, I trust the files"):
                if phrase.lower() in visible.lower() and phrase not in accepted:
                    accepted.add(phrase)
                    os.write(descriptor, b"\x1b[B\r")
            if (
                "Do you want to use this API key?" in visible
                and "fixture-api-key" not in accepted
            ):
                accepted.add("fixture-api-key")
                os.write(descriptor, b"\x1b[A\r")
            if WAITING_MESSAGE in visible:
                waiting_frames.update(
                    line[:5] for line in screen.display if WAITING_MESSAGE in line
                )
                if not waiting_seen:
                    waiting_seen = True
                    (evidence / "waiting-screen.txt").write_text(visible)
                    if args.interrupt:
                        os.write(descriptor, b"\x1b")
                        settled_at = time.monotonic()
            if ANSWER in visible:
                answer_seen = True
                if settled_at is None:
                    settled_at = time.monotonic()
            if settled_at and time.monotonic() - settled_at > 2:
                final_screen = visible
                break
        (evidence / "tui.ansi").write_bytes(raw)
        (evidence / "final-screen.txt").write_text(final_screen or visible)
        saved = []
        for history_path in client_home.rglob("thread_history_*.sqlite"):
            with sqlite3.connect(f"file:{history_path}?mode=ro", uri=True) as history:
                saved.extend(
                    row[0]
                    for row in history.execute("SELECT item_json FROM thread_items")
                )
        saved.extend(history.read_text() for history in client_home.rglob("*.jsonl"))
        summary = {
            "client": args.client,
            "interrupted": args.interrupt,
            "waiting_visible": waiting_seen,
            "waiting_animated": len(waiting_frames) > 1,
            "waiting_absent_afterward": bool(final_screen)
            and WAITING_MESSAGE not in final_screen,
            "answer_visible": answer_seen,
            "provider_wait_transport_used": any(
                call["framed"] for call in router.observed
            ),
            "waiting_absent_from_history": bool(saved)
            and all(WAITING_MESSAGE not in entry for entry in saved),
            "evidence_directory": str(evidence),
        }
        (evidence / "verification.json").write_text(
            json.dumps(summary, indent=2) + "\n"
        )
        print(json.dumps(summary, indent=2), flush=True)
        assert summary[
            "provider_wait_transport_used"
        ], "Client did not use the real Router bridge transport"
        assert summary["waiting_visible"], "Provider wait did not render"
        assert summary[
            "waiting_absent_afterward"
        ], "Provider wait remained after the request"
        assert summary[
            "waiting_absent_from_history"
        ], "Provider wait leaked into saved history or no history was written"
        if not args.interrupt:
            assert summary["answer_visible"], "Authored response did not arrive"
            assert summary["waiting_animated"], "Waiting indicator did not animate"
    finally:
        os.write(descriptor, b"\x03\x03")
        time.sleep(0.2)
        try:
            os.kill(child, signal.SIGTERM)
        except ProcessLookupError:
            pass
        deadline = time.monotonic() + 6
        while os.waitpid(child, os.WNOHANG) == (0, 0):
            if time.monotonic() > deadline:
                os.kill(child, signal.SIGKILL)
                break
            time.sleep(0.05)
        os.close(descriptor)
        router.shutdown()
        router.server_close()


if __name__ == "__main__":
    main()
