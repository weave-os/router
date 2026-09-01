#!/usr/bin/env python3
"""Deterministic native Responses upstream for local Codex routing tests.

The router's Codex subscription branch normally targets chatgpt.com. Set
ROUTER_CODEX_BASE_URL to this server's /v1 URL to exercise that same native
Responses path without spending credits or calling the real backend.
"""
import json
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PORT = 8099


def event(event_type, sequence, **fields):
    payload = {"type": event_type, "sequence_number": sequence, **fields}
    return (f"event: {event_type}\ndata: {json.dumps(payload)}\n\n").encode()


def stream():
    response = {"id": "resp_mock", "model": "gpt-5.6-sol", "status": "in_progress", "output": []}
    yield event("response.created", 0, response=response)
    item = {"id": "fc_mock", "type": "function_call", "call_id": "call_mock", "name": "shell", "arguments": "", "status": "in_progress"}
    yield event("response.output_item.added", 1, output_index=0, item=item)
    yield event("response.function_call_arguments.delta", 2, item_id="fc_mock", output_index=0, delta='{"cmd":"pwd"}')
    yield event("response.function_call_arguments.done", 3, item_id="fc_mock", output_index=0, arguments='{"cmd":"pwd"}')
    item["arguments"] = '{"cmd":"pwd"}'
    item["status"] = "completed"
    yield event("response.output_item.done", 4, output_index=0, item=item)
    response["status"] = "completed"
    response["output"] = [item]
    yield event("response.completed", 5, response=response)


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *_args):
        pass

    def do_POST(self):
        self.rfile.read(int(self.headers.get("content-length", 0)))
        self.send_response(200)
        self.send_header("content-type", "text/event-stream")
        self.end_headers()
        for chunk in stream():
            self.wfile.write(chunk)
        self.wfile.flush()


if __name__ == "__main__":
    print(f"mock native Codex Responses upstream on :{PORT}")
    ThreadingHTTPServer(("0.0.0.0", PORT), Handler).serve_forever()
