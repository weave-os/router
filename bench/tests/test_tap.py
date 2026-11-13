from __future__ import annotations

import json
from pathlib import Path

import httpx
import pytest
from starlette.applications import Starlette
from starlette.requests import Request
from starlette.responses import JSONResponse, Response, StreamingResponse
from starlette.routing import Route
from starlette.testclient import TestClient

from weave_bench.arms import CostTier
from weave_bench.tap.app import TapRecorder, build_app
from weave_bench.tap.rewrite import (
    codex_session_id,
    forwardable_request_headers,
    rewrite_request,
    terminal_response_body,
    usage_fields,
)

TERMINAL = {
    "id": "resp_or_1",
    "status": "completed",
    "model": "anthropic/claude-sonnet-4.5",
    "usage": {
        "input_tokens": 120,
        "input_tokens_details": {"cached_tokens": 100},
        "output_tokens": 30,
        "output_tokens_details": {"reasoning_tokens": 12},
        "cost": 0.0042,
    },
}
SSE_BODY = (
    b'data: {"type":"response.created","response":{"id":"resp_or_1","status":"in_progress"}}\n\n'
    b'data: {"type":"response.output_text.delta","delta":"hi"}\n\n'
    b"data: not-json\n\n"
    b"data: " + json.dumps({"type": "response.completed", "response": TERMINAL}).encode() + b"\n\n"
    b"data: [DONE]\n\n"
)


def test_session_id_prefers_header_then_thread_then_prompt_cache_key() -> None:
    assert codex_session_id({"session-id": "s", "thread-id": "t"}, {"prompt_cache_key": "p"}) == "s"
    assert codex_session_id({"thread-id": "t"}, {"prompt_cache_key": "p"}) == "t"
    assert codex_session_id({}, {"prompt_cache_key": "p"}) == "p"
    assert codex_session_id({}, {}) == ""


def test_auto_beta_rewrite_adds_plugin_cache_control_session_and_usage() -> None:
    body = {"model": "auto-beta", "input": "hi", "stream": True}
    rewritten = rewrite_request(body, {"session-id": "thread-1"}, CostTier.XHIGH)
    assert rewritten.body == {
        "model": "openrouter/auto-beta",
        "input": "hi",
        "stream": True,
        "usage": {"include": True},
        "plugins": [{"id": "auto-beta-router", "cost_tier": "xhigh"}],
        "cache_control": {"type": "ephemeral"},
        "session_id": "thread-1",
    }
    assert rewritten.plugin_id == "auto-beta-router"
    assert body == {"model": "auto-beta", "input": "hi", "stream": True}


def test_plain_auto_rewrite_only_restores_prefix_and_usage() -> None:
    rewritten = rewrite_request({"model": "openrouter/auto", "input": "x"}, {}, None)
    assert rewritten.body == {"model": "openrouter/auto", "input": "x", "usage": {"include": True}}
    assert rewritten.plugin_id is None and rewritten.session_id == ""


def test_hop_by_hop_and_routing_headers_are_not_forwarded() -> None:
    forwarded = forwardable_request_headers(
        {"host": "tap", "content-length": "3", "Authorization": "Bearer k", "x-weave-openrouter-cost-tier": "max"}
    )
    assert forwarded == {"Authorization": "Bearer k"}


def test_terminal_body_from_sse_or_json() -> None:
    assert terminal_response_body(SSE_BODY) == TERMINAL
    assert terminal_response_body(json.dumps(TERMINAL).encode()) == TERMINAL
    assert terminal_response_body(b"<html>bad gateway</html>") is None
    assert usage_fields(TERMINAL["usage"]) == (120, 100, 30, 12, 0.0042)
    assert usage_fields(None) == (None, None, None, None, None)


@pytest.fixture
def fake_openrouter() -> tuple[Starlette, list[dict[str, object]], list[dict[str, str]]]:
    bodies: list[dict[str, object]] = []
    headers: list[dict[str, str]] = []

    async def responses(request: Request) -> Response:
        bodies.append(json.loads(await request.body()))
        headers.append(dict(request.headers))
        if request.headers.get("authorization") != "Bearer or_key":
            return JSONResponse({"error": {"message": "no auth"}}, status_code=401)

        async def stream():
            yield SSE_BODY[: len(SSE_BODY) // 2]
            yield SSE_BODY[len(SSE_BODY) // 2 :]

        return StreamingResponse(stream(), media_type="text/event-stream", headers={"x-upstream": "1"})

    return Starlette(routes=[Route("/responses", responses, methods=["POST"])]), bodies, headers


def test_tap_forwards_rewritten_request_relays_stream_and_records_usage(tmp_path: Path, fake_openrouter) -> None:
    upstream_app, bodies, upstream_headers = fake_openrouter
    upstream = httpx.AsyncClient(transport=httpx.ASGITransport(app=upstream_app), base_url="http://openrouter.test")
    records = tmp_path / "tap.jsonl"
    client = TestClient(build_app(TapRecorder(records), upstream))

    response = client.post(
        "/v1/responses",
        json={"model": "auto-beta", "input": "hi", "stream": True},
        headers={
            "Authorization": "Bearer or_key",
            "Session-Id": "thread-9",
            "x-weave-rollout-id": "run-7",
            "x-weave-openrouter-cost-tier": "max",
        },
    )
    assert response.status_code == 200
    assert response.content == SSE_BODY
    assert response.headers["x-upstream"] == "1"
    (body,) = bodies
    assert body["model"] == "openrouter/auto-beta"
    assert body["plugins"] == [{"id": "auto-beta-router", "cost_tier": "max"}]
    assert body["cache_control"] == {"type": "ephemeral"}
    assert body["session_id"] == "thread-9"
    assert upstream_headers[0]["authorization"] == "Bearer or_key"
    assert "x-weave-openrouter-cost-tier" not in upstream_headers[0]

    (record,) = [json.loads(line) for line in records.read_text().splitlines()]
    assert record["response_id"] == "resp_or_1"
    assert record["session_id"] == "thread-9" and record["rollout_id"] == "run-7"
    assert record["requested_model"] == "auto-beta" and record["upstream_model"] == "openrouter/auto-beta"
    assert record["served_model"] == "anthropic/claude-sonnet-4.5"
    assert record["plugin_id"] == "auto-beta-router" and record["cost_tier"] == "max"
    assert (record["input_tokens"], record["cached_input_tokens"], record["output_tokens"]) == (120, 100, 30)
    assert record["cost_usd"] == pytest.approx(0.0042)
    assert record["http_status"] == 200 and record["response_status"] == "completed"


def test_tap_records_upstream_failures_and_rejects_non_json(tmp_path: Path, fake_openrouter) -> None:
    upstream_app, _, _ = fake_openrouter
    upstream = httpx.AsyncClient(transport=httpx.ASGITransport(app=upstream_app), base_url="http://openrouter.test")
    records = tmp_path / "tap.jsonl"
    client = TestClient(build_app(TapRecorder(records), upstream))
    denied = client.post("/v1/responses", json={"model": "auto"}, headers={"Authorization": "Bearer wrong"})
    assert denied.status_code == 401
    (record,) = [json.loads(line) for line in records.read_text().splitlines()]
    assert record["http_status"] == 401 and record["served_model"] is None and record["cost_usd"] is None
    assert client.post("/v1/responses", content=b"{not json").status_code == 400
