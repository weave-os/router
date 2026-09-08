from __future__ import annotations

import json
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer

import pytest

from weave_bench.probe import probe_router, response_output_text

RESPONSES_OBJECT = {
    "id": "resp_1",
    "status": "completed",
    "output": [
        {"type": "reasoning", "summary": []},
        {
            "type": "message",
            "role": "assistant",
            "content": [
                {"type": "output_text", "text": "✦ **Weave Router** → "},
                {"type": "output_text", "text": "Beta enabled. Type /beta again to turn it off."},
            ],
        },
    ],
    "usage": {"input_tokens": 3, "output_tokens": 1},
}


def test_output_text_concatenates_message_parts_only() -> None:
    assert (
        response_output_text(RESPONSES_OBJECT) == "✦ **Weave Router** → Beta enabled. Type /beta again to turn it off."
    )
    assert response_output_text({"output": "not-a-list"}) == ""
    assert response_output_text({"error": {"message": "invalid_key"}}) == ""


class _FakeRouter(BaseHTTPRequestHandler):
    beta_enabled = True
    responses_requests: list[tuple[dict[str, str], dict[str, object]]] = []

    def _send(self, status: int, payload: dict[str, object]) -> None:
        body = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self) -> None:  # noqa: N802
        if self.headers.get("Authorization") != "Bearer rk_test":
            return self._send(401, {"error": "invalid_key"})
        if self.path == "/v1/version":
            return self._send(
                200, {"commit": "7523cdb", "display": "#1209 (7523cdb)", "cluster_version": "artifacts/latest"}
            )
        if self.path == "/v1/router/hmm-roster":
            return self._send(200, {"roster_sha256": "ae0e" * 16})
        return self._send(404, {})

    def do_POST(self) -> None:  # noqa: N802
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        type(self).responses_requests.append(({k.lower(): v for k, v in self.headers.items()}, body))
        if not type(self).beta_enabled:
            return self._send(403, {"error": "beta_not_enabled"})
        return self._send(200, RESPONSES_OBJECT)

    def log_message(self, *_: object) -> None:
        return


@pytest.fixture
def fake_router():
    _FakeRouter.responses_requests = []
    _FakeRouter.beta_enabled = True
    server = HTTPServer(("127.0.0.1", 0), _FakeRouter)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    yield f"http://127.0.0.1:{server.server_port}/"
    server.shutdown()


def test_probe_reports_version_roster_and_beta_ack(fake_router: str) -> None:
    probe = probe_router(fake_router, "rk_test")
    assert probe.beta_enabled
    assert probe.roster_sha256 == "ae0e" * 16
    rendered = probe.render()
    assert "router commit: #1209 (7523cdb)" in rendered
    assert "beta lane enabled for this key: yes" in rendered
    ((headers, body),) = _FakeRouter.responses_requests
    assert body == {"model": "gpt-5.6-sol", "input": "/beta", "stream": False}
    assert headers["x-app"] == "codex"
    assert headers["session-id"].startswith("weave-bench-probe-")
    assert headers["authorization"] == "Bearer rk_test"


def test_probe_surfaces_http_errors_as_not_enabled(fake_router: str) -> None:
    _FakeRouter.beta_enabled = False
    probe = probe_router(fake_router, "rk_test")
    assert not probe.beta_enabled
    assert probe.beta_ack.startswith("HTTP 403")
    assert "beta lane enabled for this key: NO" in probe.render()
    wrong_key = probe_router(fake_router, "wrong")
    assert wrong_key.roster_sha256 is None and not wrong_key.beta_enabled
