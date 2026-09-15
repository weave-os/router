#!/usr/bin/env python3
"""Drive a real Pi compaction and reserved continuation against a local mock.

All transcript content is authored here. No installed user settings or provider
credentials are used; the child and session live in a temporary directory.
"""

import json
import os
from pathlib import Path
import subprocess
import tempfile
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


SOURCE = Path(__file__).resolve().parents[1] / "src" / "index.ts"
MODEL = "claude-sonnet-4-6"
UPGRADED_MODEL = "claude-opus-4-7"
SESSION_ID = "9e09b90c-04b9-44f3-ae13-8cfe2073bfb2"
TIMESTAMP = "2026-09-15T00:00:00.000Z"
FINAL_REQUEST = "Finish parser validation and report the remaining checks."
SUMMARY = "The parser implementation is complete. Preserve the final request and verify remaining checks."


def write_session(path, cwd):
    entries = [{"type": "session", "version": 3, "id": SESSION_ID, "timestamp": TIMESTAMP, "cwd": str(cwd)}]
    parent = None
    for index in range(12):
        role = "user" if index % 2 == 0 else "assistant"
        message = {
            "role": role,
            "content": [{"type": "text", "text": f"Archived parser step {index}. " + "Authored parser design and test evidence. " * 1400}],
            "timestamp": 1,
        }
        if role == "assistant":
            message.update({"api": "anthropic-messages", "provider": "weave", "model": MODEL, "stopReason": "stop", "usage": {
                "input": 60000, "output": 100, "cacheRead": 0, "cacheWrite": 0, "totalTokens": 60100,
                "cost": {"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0, "total": 0},
            }})
        entry_id = f"seed{index:04d}"
        entries.append({"type": "message", "id": entry_id, "parentId": parent, "timestamp": TIMESTAMP, "message": message})
        parent = entry_id
    path.write_text("".join(json.dumps(entry) + "\n" for entry in entries))


def main():
    requests = []
    preparations = []

    class Handler(BaseHTTPRequestHandler):
        def log_message(self, *_args):
            pass

        def do_POST(self):
            payload = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
            if self.path == "/v1/route/handoff":
                preparations.append(payload)
                elevated = len(preparations) > 1
                response = {"token": "high-ticket" if elevated else "low-ticket", "model": UPGRADED_MODEL if elevated else MODEL,
                            "provider": "anthropic", "complexity": "high" if elevated else "low"}
                if elevated:
                    response["summary_token"] = "summary-ticket"
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.end_headers()
                self.wfile.write(json.dumps(response).encode())
                return
            if self.path != "/v1/messages":
                self.send_error(404)
                return
            requests.append(payload)
            ticket = payload.get("weave_handoff")
            if ticket == "low-ticket":
                block = {"type": "tool_use", "id": "handoff_probe", "name": "bash", "input": {}}
                delta = {"type": "input_json_delta", "partial_json": json.dumps({"command": "echo handoff_probe"})}
                stop = "tool_use"
            else:
                block = {"type": "text", "text": ""}
                delta = {"type": "text_delta", "text": SUMMARY if ticket == "summary-ticket" else "HANDOFF_COMPLETE"}
                stop = "end_turn"
            model = UPGRADED_MODEL if ticket == "high-ticket" else MODEL
            usage = {"input_tokens": 60000 if ticket == "low-ticket" else 2000, "output_tokens": 100}
            message = {"id": "msg_handoff", "type": "message", "role": "assistant", "model": model, "content": [], "stop_reason": None, "usage": usage}
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.send_header("x-router-model", model)
            self.send_header("x-router-context-window", "200000")
            self.end_headers()
            for event, fields in [
                ("message_start", {"message": message}),
                ("content_block_start", {"index": 0, "content_block": block}),
                ("content_block_delta", {"index": 0, "delta": delta}),
                ("content_block_stop", {"index": 0}),
                ("message_delta", {"delta": {"stop_reason": stop}, "usage": usage}),
                ("message_stop", {}),
            ]:
                self.wfile.write(f"event: {event}\ndata: {json.dumps({'type': event, **fields})}\n\n".encode())
            self.wfile.flush()

    with tempfile.TemporaryDirectory(prefix="pi-handoff-e2e-") as temp:
        work = Path(temp)
        config = work / "agent"
        config.mkdir()
        (config / "settings.json").write_text(json.dumps({"defaultProvider": "weave", "defaultModel": MODEL, "packages": [], "compaction": {"enabled": True}}))
        session = work / "session.jsonl"
        write_session(session, work)
        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        threading.Thread(target=server.serve_forever, daemon=True).start()
        env = {**os.environ, "PI_CODING_AGENT_DIR": str(config), "WEAVE_ROUTER_KEY": "offline-handoff-key",
               "WEAVE_ROUTER_URL": f"http://127.0.0.1:{server.server_port}",
               "WEAVE_PI_NO_LSP": "1", "WEAVE_NO_SAFETY": "1", "WEAVE_USER_EMAIL": "offline@example.invalid", "WEAVE_USER_NAME": "Offline test"}
        env.pop("WEAVE_PI_ESCALATION_COMPACTION", None)
        try:
            process = subprocess.run(["pi", "-e", str(SOURCE), "--offline", "--session", str(session), "--model", f"weave/{MODEL}",
                                      "--mode", "json", "-p", FINAL_REQUEST], cwd=work, env=env, text=True, capture_output=True, timeout=90)
        finally:
            server.shutdown()
            server.server_close()
        assert process.returncode == 0, process.stderr[-4000:]
        assert [request.get("weave_handoff") for request in requests] == ["low-ticket", "summary-ticket", "high-ticket"], (
            [request.get("weave_handoff") for request in requests], process.stderr[-3000:], process.stdout[-3000:])
        assert len(preparations) == 2, "Compacted context must not be reclassified"
        assert requests[1]["messages"][:-1] == preparations[1]["messages"], "Summary must preserve the old model's message prefix"
        for field in ("system", "tools", "thinking"):
            assert requests[1].get(field) == preparations[1].get(field), f"Summary changed the cached {field} prefix"
        assert "HANDOFF_COMPLETE" in process.stdout, process.stdout[-3000:]
        continuation = json.dumps(requests[-1]["messages"])
        assert FINAL_REQUEST in continuation, "The active user request must survive compaction"
        assert SUMMARY in continuation, "The new model must receive the durable summary"
        assert len(continuation) < len(json.dumps(preparations[-1]["messages"])) / 2, "History was not compacted"
        persisted = [json.loads(line) for line in session.read_text().splitlines()]
        assert any(entry["type"] == "compaction" for entry in persisted), "Pi must persist a compaction boundary"
        assert any(entry.get("id") == "seed0000" for entry in persisted), "Raw transcript must remain available"
        print("PASS: real Pi aborts before dispatch, summarizes once, persists compaction, and resumes the reserved model with reduced context")


if __name__ == "__main__":
    main()
