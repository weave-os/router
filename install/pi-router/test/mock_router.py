#!/usr/bin/env python3
"""Mock Weave Router for the bundled pi and OpenCode endpoint tests.

Speaks enough Anthropic Messages for pi and OpenAI Responses for OpenCode,
with no real model spend and no network beyond localhost:

  GET  /health, /validate    -> 200            (the install.sh --pi probes)
  POST /v1/messages          -> Anthropic Messages response (SSE or JSON)
  POST /v1/responses         -> OpenAI Responses response
  POST /v1/route/handoff     -> preparation bypass (no model escalation)

It also *is* the model. To exercise the `dispatch` tool it returns a tool_use
block for `dispatch` when the latest user turn contains DISPATCH_MARKER and no
tool_result is present yet; once the tool_result comes back it answers with
plain text so the agent loop terminates. Subagent calls (X-App: pi-subagent)
always get plain text. Every response carries an `x-router-model` header so the
extension's routed-model path fires.

Every request is appended as one JSON object per line to MOCK_LOG so the e2e
script can assert the header / knob / metadata.user_id shape. The router key is
never logged in full -- only presence + last 4 chars.

The mock also implements the router's synthetic /force-model acknowledgement
and a small test-only alias map, allowing an interactive Pi smoke test without
provider spend. Command responses intentionally omit x-router-* headers just as
the real router does.

Env:
  MOCK_PORT          listen port                       (default 8899)
  MOCK_LOG           request log path (JSONL)          (default ./requests.jsonl)
  MOCK_MAIN_MODEL    x-router-model for main requests  (default claude-opus-4-8)
  MOCK_SUBAGENT_MODEL x-router-model for subagents     (default claude-haiku-4-5)
  DISPATCH_MARKER    main-loop prompt trigger          (default __DISPATCH__)
"""

import json
import os
import sys
import threading
from uuid import UUID
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

from responses_fixture import Scenario, responses_fixture

PORT = int(os.environ.get("MOCK_PORT", "8899"))
LOG_PATH = os.environ.get("MOCK_LOG", os.path.join(os.getcwd(), "requests.jsonl"))
MAIN_MODEL = os.environ.get("MOCK_MAIN_MODEL", "claude-opus-4-8")
SUBAGENT_MODEL = os.environ.get("MOCK_SUBAGENT_MODEL", "claude-haiku-4-5")
DISPATCH_MARKER = os.environ.get("DISPATCH_MARKER", "__DISPATCH__")

# The clients intentionally use different SDK base-URL conventions. Reject
# anything except their two exact paths so a doubled or missing /v1 fails loudly.
MESSAGES_PATH = "/v1/messages"
RESPONSES_PATH = "/v1/responses"
HANDOFF_PATH = "/v1/route/handoff"
CLASSIFIER_THREAD_PATH = "/v1/router/threads"
CLASSIFIER_DENIED_KEY = "rk_e2e_classifier_denied"

KNOB_HEADERS = (
    "x-weave-routing-alpha",
    "x-weave-routing-speed-weight",
    "x-weave-routing-output-cost-ratio",
    "x-weave-routing-expected-output-tokens",
)
DISPATCH_TASKS = [
    {"prompt": "Reply with exactly: SUBAGENT_ONE_OK"},
    {"prompt": "Reply with exactly: SUBAGENT_TWO_OK"},
]

_log_lock = threading.Lock()
_pin_lock = threading.Lock()
_forced_models: dict[str, str] = {}
_responses_requests: dict[str, int] = {}

FORCE_MODEL_ALIASES = {
    "haiku": "claude-haiku-4-5",
    "opus": "claude-opus-4-8",
    "sonnet": "claude-sonnet-4-6",
}


def log_request(record: dict) -> None:
    line = json.dumps(record, separators=(",", ":"))
    with _log_lock:
        with open(LOG_PATH, "a", encoding="utf-8") as fh:
            fh.write(line + "\n")
    print(
        f"[mock] {record['method']} {record['path']} "
        f"app={record.get('app')} user_id={record.get('user_id')} "
        f"served={record.get('served', '-')}",
        file=sys.stderr,
        flush=True,
    )


def latest_user_text(messages: list) -> str:
    for msg in reversed(messages):
        if not isinstance(msg, dict) or msg.get("role") != "user":
            continue
        content = msg.get("content")
        if isinstance(content, str):
            return content
        if isinstance(content, list):
            parts = [
                b.get("text", "")
                for b in content
                if isinstance(b, dict) and b.get("type") in {"text", "input_text"}
            ]
            return " ".join(parts)
        return ""
    return ""


def has_tool_result(messages: list) -> bool:
    for msg in messages:
        if not isinstance(msg, dict):
            continue
        content = msg.get("content")
        if isinstance(content, list):
            for block in content:
                if isinstance(block, dict) and block.get("type") == "tool_result":
                    return True
    return False


def force_model_command(text: str) -> tuple[str, str] | None:
    command = text.strip().split()
    if not command:
        return None
    if command[0].lower() in ("/unforce-model", "/ufm") and len(command) == 1:
        return ("clear", "")
    if command[0].lower() not in ("/force-model", "/fm") or len(command) < 2:
        return None
    requested = command[1]
    return ("force", FORCE_MODEL_ALIASES.get(requested.lower(), requested))


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *_args) -> None:  # silence the default access log
        return

    # ---- helpers -------------------------------------------------------

    def _send_json(
        self, code: int, obj: dict, extra_headers: dict | None = None
    ) -> None:
        payload = json.dumps(obj).encode("utf-8")
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        for key, val in (extra_headers or {}).items():
            self.send_header(key, val)
        self.end_headers()
        try:
            self.wfile.write(payload)
        except BrokenPipeError:
            pass

    def _send_sse(
        self, events: list, routed_model: str, route_headers: bool = True
    ) -> None:
        body = "".join(
            f"event: {ev}\ndata: {json.dumps(data)}\n\n" for ev, data in events
        ).encode("utf-8")
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Content-Length", str(len(body)))
        if route_headers:
            self.send_header("x-router-model", routed_model)
            self.send_header("x-router-provider", "mock")
            self.send_header("x-router-decision", "mock-e2e")
        self.end_headers()
        try:
            self.wfile.write(body)
        except BrokenPipeError:
            pass

    # ---- response builders --------------------------------------------

    @staticmethod
    def _message_obj(block: dict, stop_reason: str, routed_model: str) -> dict:
        return {
            "id": "msg_mock",
            "type": "message",
            "role": "assistant",
            "model": routed_model,
            "content": [block],
            "stop_reason": stop_reason,
            "stop_sequence": None,
            "usage": {"input_tokens": 12, "output_tokens": 8},
        }

    @staticmethod
    def _sse_events(block: dict, stop_reason: str, routed_model: str) -> list:
        start_msg = {
            "id": "msg_mock",
            "type": "message",
            "role": "assistant",
            "model": routed_model,
            "content": [],
            "stop_reason": None,
            "stop_sequence": None,
            "usage": {"input_tokens": 12, "output_tokens": 1},
        }
        events = [("message_start", {"type": "message_start", "message": start_msg})]
        if block["type"] == "text":
            events += [
                (
                    "content_block_start",
                    {
                        "type": "content_block_start",
                        "index": 0,
                        "content_block": {"type": "text", "text": ""},
                    },
                ),
                (
                    "content_block_delta",
                    {
                        "type": "content_block_delta",
                        "index": 0,
                        "delta": {"type": "text_delta", "text": block["text"]},
                    },
                ),
            ]
        else:  # tool_use
            events += [
                (
                    "content_block_start",
                    {
                        "type": "content_block_start",
                        "index": 0,
                        "content_block": {
                            "type": "tool_use",
                            "id": block["id"],
                            "name": block["name"],
                            "input": {},
                        },
                    },
                ),
                (
                    "content_block_delta",
                    {
                        "type": "content_block_delta",
                        "index": 0,
                        "delta": {
                            "type": "input_json_delta",
                            "partial_json": json.dumps(block["input"]),
                        },
                    },
                ),
            ]
        events += [
            ("content_block_stop", {"type": "content_block_stop", "index": 0}),
            (
                "message_delta",
                {
                    "type": "message_delta",
                    "delta": {"stop_reason": stop_reason, "stop_sequence": None},
                    "usage": {"output_tokens": 8},
                },
            ),
            ("message_stop", {"type": "message_stop"}),
        ]
        return events

    # ---- handlers ------------------------------------------------------

    def do_GET(self) -> None:  # noqa: N802 (stdlib naming)
        path = self.path.split("?")[0]

        log_request({"method": "GET", "path": path, "app": self.headers.get("x-app")})
        self._send_json(200, {"status": "ok"})

    def do_POST(self) -> None:  # noqa: N802 (stdlib naming)
        length = int(self.headers.get("content-length") or 0)
        raw = self.rfile.read(length) if length else b""  # always drain the body
        path = self.path.split("?")[0]

        if path == CLASSIFIER_THREAD_PATH:
            try:
                new_chat_id = str(UUID(json.loads(raw)["new_chat_id"]))
            except (ValueError, KeyError, TypeError):
                self._send_json(400, {"error": "invalid_new_chat_id"})
                return
            denied = self.headers.get("x-weave-router-key") == CLASSIFIER_DENIED_KEY
            log_request({"method": "POST", "path": path, "new_chat_id": new_chat_id, "classifier_denied": denied})
            self._send_json(503 if denied else 200, {"thread_token": f"fixture-{new_chat_id}"})
            return

        if path == HANDOFF_PATH:
            log_request(
                {"method": "POST", "path": path, "app": self.headers.get("x-app")}
            )
            self._send_json(200, {"bypass": True})
            return

        if path == RESPONSES_PATH:
            if self.headers.get("x-weave-classifier-thread") == "weave-classifier-unavailable":
                log_request({"method": "POST", "path": path, "rejected": True,
                             "classifier_thread_unavailable": True})
                self._send_json(400, {"error": {"type": "invalid_request_error",
                                                "message": "classifier_client_unavailable"}})
                return
            try:
                body = json.loads(raw or b"{}")
            except json.JSONDecodeError:
                log_request({"method": "POST", "path": path, "rejected": True})
                self._send_json(400, {"error": {"message": "invalid JSON"}})
                return
            scenario_name = self.headers.get("x-conformance-scenario") or Scenario.TEXT
            try:
                scenario = Scenario(scenario_name)
            except ValueError:
                log_request({"method": "POST", "path": path, "rejected": True})
                self._send_json(400, {"error": {"message": "unknown conformance scenario"}})
                return
            agent = self.headers.get("x-conformance-agent", "")
            weave_agent = self.headers.get("x-weave-opencode-agent")
            inputs = body.get("input") or []
            tool_outputs = [item for item in inputs if isinstance(item, dict)
                            and item.get("type") == "function_call_output"]
            key = self.headers.get("x-weave-router-key") or ""
            log_request({
                "method": "POST", "path": path, "rejected": False,
                "app": self.headers.get("x-app"), "model": body.get("model"),
                "stream": bool(body.get("stream")), "served": scenario.value,
                "agent": agent, "weave_agent": weave_agent, "session_id": self.headers.get("session-id"),
                "codex_agent_id": self.headers.get("x-codex-agent-id"),
                "codex_parent_agent_id": self.headers.get("x-codex-parent-agent-id"),
                "codex_header_names": [key for key in self.headers if "codex" in key.lower() or "session" in key.lower()],
                "codex_parent_thread_id": self.headers.get("x-codex-parent-thread-id"),
                "codex_window_id": self.headers.get("x-codex-window-id"),
                "classifier_thread": self.headers.get("x-weave-classifier-thread"),
                "tool_names": [tool.get("name") for tool in body.get("tools", []) if isinstance(tool, dict)],
                "key_present": bool(key), "key_suffix": key[-4:],
                "input": inputs, "tool_outputs": tool_outputs,
            })
            session_id = self.headers.get("session-id", "")
            with _log_lock:
                request_count = _responses_requests.get(session_id, 0) + 1
                _responses_requests[session_id] = request_count
            if self.headers.get("x-conformance-scenario") and request_count > 12:
                self._send_json(400, {"error": {"message": "Conformance request budget exceeded"}})
                return
            if scenario == Scenario.ERROR:
                self._send_json(400, {"error": {"type": "invalid_request_error", "message": "MOCK_REJECTED"}})
                return
            fixture_scenario = scenario
            if scenario == Scenario.CODEX_CHILD and "CODEX_CHILD_PROBE" not in latest_user_text(inputs):
                fixture_scenario = Scenario.TEXT
            if scenario == Scenario.COMPACTION and request_count > 1:
                fixture_scenario = Scenario.TEXT
            child_agent_id = ""
            for tool_output in tool_outputs:
                if tool_output.get("call_id") == "call_codex_child":
                    try:
                        child_agent_id = json.loads(tool_output.get("output", "")).get("agent_id", "")
                    except (json.JSONDecodeError, AttributeError):
                        pass
            response, events = responses_fixture(fixture_scenario, agent, bool(tool_outputs),
                                                 child_agent_id, any(output.get("call_id") == "call_codex_wait" for output in tool_outputs))
            if body.get("stream"):
                self._send_sse(events, MAIN_MODEL)
            else:
                self._send_json(200, response, {"x-router-model": MAIN_MODEL})
            return

        if path != MESSAGES_PATH:
            log_request(
                {
                    "method": "POST",
                    "path": path,
                    "app": self.headers.get("x-app"),
                    "rejected": True,
                }
            )
            self._send_json(
                404,
                {
                    "type": "error",
                    "error": {
                        "type": "not_found_error",
                        "message": f"no route for POST {path}",
                    },
                },
            )
            return

        try:
            body = json.loads(raw or b"{}")
        except json.JSONDecodeError:
            body = {}

        messages = body.get("messages") or []
        app = self.headers.get("x-app") or "pi"
        is_subagent = app == "pi-subagent"
        key = self.headers.get("x-weave-router-key") or ""
        metadata = body.get("metadata") or {}
        user_text = latest_user_text(messages)
        tool_result_present = has_tool_result(messages)
        stream = bool(body.get("stream"))
        user_id = metadata.get("user_id") or ""
        force_command = force_model_command(user_text) if not is_subagent else None

        want_dispatch = (
            DISPATCH_MARKER in user_text and not is_subagent and not tool_result_present
        )
        want_claude_child = "CLAUDE_CHILD_PROBE" in user_text and not tool_result_present
        with _pin_lock:
            forced_model = _forced_models.get(user_id)
        routed_model = SUBAGENT_MODEL if is_subagent else forced_model or MAIN_MODEL

        route_headers = True
        if force_command:
            action, model = force_command
            route_headers = False
            routed_model = "weave-router"
            if action == "clear":
                with _pin_lock:
                    _forced_models.pop(user_id, None)
                reply = "✦ **Weave Router** → force-model cleared · resuming automatic model selection"
                served = "unforce_model"
            else:
                with _pin_lock:
                    _forced_models[user_id] = model
                reply = f"✦ **Weave Router** → force-model applied: {model} (mock) · Use /unforce-model to clear"
                served = "force_model"
            block = {"type": "text", "text": reply}
            stop_reason = "end_turn"
        elif want_claude_child:
            block = {
                "type": "tool_use", "id": "toolu_mock_claude_child", "name": "Agent",
                "input": {"description": "Mock child", "prompt": "Return only CHILD_OK.", "subagent_type": "general-purpose"},
            }
            stop_reason = "tool_use"
            served = "claude_child"
        elif want_dispatch:
            block = {
                "type": "tool_use",
                "id": "toolu_mock_dispatch",
                "name": "dispatch",
                "input": {"tasks": DISPATCH_TASKS},
            }
            stop_reason = "tool_use"
            served = "tool_use"
        else:
            if is_subagent:
                reply = "SUBAGENT_OK"
            elif tool_result_present:
                reply = "DISPATCH_COMPLETE_OK"
            else:
                reply = "MAIN_LOOP_OK"
            block = {"type": "text", "text": reply}
            stop_reason = "end_turn"
            served = "text"

        log_request(
            {
                "method": "POST",
                "path": path,
                "rejected": False,
                "app": app,
                "claude_session_id": self.headers.get("x-claude-code-session-id"),
                "claude_agent_id": self.headers.get("x-claude-code-agent-id"),
                "claude_parent_agent_id": self.headers.get("x-claude-code-parent-agent-id"),
                "claude_header_names": [header for header in self.headers if "claude" in header.lower() or "session" in header.lower()],
                "tool_names": [tool.get("name") for tool in body.get("tools", []) if isinstance(tool, dict)],
                "model": body.get("model"),
                "stream": stream,
                "user_id": user_id,
                "key_present": bool(key),
                "key_suffix": key[-4:] if key else "",
                "email": self.headers.get("x-weave-user-email"),
                "name": self.headers.get("x-weave-user-name"),
                "knobs": {h: self.headers.get(h) for h in KNOB_HEADERS},
                "marker_opt": self.headers.get("x-weave-routing-marker"),
                "has_tool_result": tool_result_present,
                "served": served,
                "forced_model": forced_model,
                "classifier_thread": self.headers.get("x-weave-classifier-thread") or body.get("weave_classifier_thread"),
            }
        )

        if stream:
            self._send_sse(
                self._sse_events(block, stop_reason, routed_model),
                routed_model,
                route_headers=route_headers,
            )
        else:
            extra_headers = None
            if route_headers:
                extra_headers = {
                    "x-router-model": routed_model,
                    "x-router-provider": "mock",
                    "x-router-decision": "mock-e2e",
                }
            self._send_json(
                200,
                self._message_obj(block, stop_reason, routed_model),
                extra_headers=extra_headers,
            )


def main() -> None:
    open(LOG_PATH, "w", encoding="utf-8").close()  # truncate per run
    server = ThreadingHTTPServer(("127.0.0.1", PORT), Handler)
    print(
        f"[mock] listening on http://127.0.0.1:{PORT}  log={LOG_PATH}",
        file=sys.stderr,
        flush=True,
    )
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass


if __name__ == "__main__":
    main()
