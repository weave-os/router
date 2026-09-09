"""OpenRouter Responses-API tap: a local ASGI pass-through recorder.

Run with ``weave-bench tap`` (uvicorn) and point the ``openrouter*`` arms'
Codex provider at it; Harbor's Docker sandboxes reach the host through the
bridge gateway (``openrouter_tap.public_url`` in ``bench.toml``). The client's
``Authorization`` is forwarded untouched — the tap never holds the OpenRouter
key — and one ``TapRecord`` per response is appended to a JSONL file that the
report joins to trials on the Codex thread id.

For ``--env modal`` sandboxes the tap must be reachable from Modal; deploy the
same app behind ``modal.asgi_app()`` (see README).
"""

from __future__ import annotations

import argparse
import json
import time
from collections.abc import AsyncIterator
from pathlib import Path

import httpx
import uvicorn
from starlette.applications import Starlette
from starlette.requests import Request
from starlette.responses import JSONResponse, Response, StreamingResponse
from starlette.routing import Route

from weave_bench.arms import CostTier
from weave_bench.tap.rewrite import (
    TapHeader,
    TapRecord,
    forwardable_request_headers,
    forwardable_response_headers,
    rewrite_request,
    terminal_response_body,
    usage_fields,
)

OPENROUTER_BASE_URL = "https://openrouter.ai/api/v1"
RESPONSES_PATH = "/v1/responses"
UPSTREAM_TIMEOUT_SECONDS = 20 * 60


class TapRecorder:
    def __init__(self, records_path: Path) -> None:
        self._records_path = records_path
        records_path.parent.mkdir(parents=True, exist_ok=True)

    def append(self, record: TapRecord) -> None:
        with self._records_path.open("a") as records:
            records.write(record.to_json() + "\n")


def openrouter_client(base_url: str = OPENROUTER_BASE_URL) -> httpx.AsyncClient:
    return httpx.AsyncClient(base_url=base_url, timeout=UPSTREAM_TIMEOUT_SECONDS)


def build_app(recorder: TapRecorder, upstream: httpx.AsyncClient) -> Starlette:
    async def responses(request: Request) -> Response:
        raw_body = await request.body()
        try:
            body = json.loads(raw_body)
        except ValueError:
            return JSONResponse({"error": "request body is not JSON"}, status_code=400)
        headers = dict(request.headers)
        cost_tier_header = headers.get(TapHeader.COST_TIER)
        cost_tier = CostTier(cost_tier_header) if cost_tier_header else None
        rewritten = rewrite_request(body, headers, cost_tier)
        requested_at = time.time()
        upstream_response = await upstream.send(
            upstream.build_request(
                "POST",
                "/responses",
                content=json.dumps(rewritten.body).encode(),
                headers=forwardable_request_headers(headers),
            ),
            stream=True,
        )

        def record(response_bytes: bytes) -> None:
            terminal = terminal_response_body(response_bytes)
            usage = terminal.get("usage") if terminal is not None else None
            input_tokens, cached, output_tokens, reasoning, cost = usage_fields(
                usage if isinstance(usage, dict) else None
            )
            served = terminal.get("model") if terminal is not None else None
            status = terminal.get("status") if terminal is not None else None
            response_id = terminal.get("id") if terminal is not None else None
            recorder.append(
                TapRecord(
                    response_id=str(response_id or f"unknown-{time.time_ns()}"),
                    rollout_id=headers.get(TapHeader.ROLLOUT_ID, ""),
                    session_id=rewritten.session_id,
                    requested_model=str(body.get("model", "")),
                    upstream_model=rewritten.upstream_model,
                    served_model=served if isinstance(served, str) else None,
                    plugin_id=rewritten.plugin_id,
                    cost_tier=cost_tier.value if cost_tier else None,
                    http_status=upstream_response.status_code,
                    response_status=status if isinstance(status, str) else None,
                    input_tokens=input_tokens,
                    cached_input_tokens=cached,
                    output_tokens=output_tokens,
                    reasoning_tokens=reasoning,
                    cost_usd=cost,
                    requested_at=requested_at,
                    duration_seconds=time.time() - requested_at,
                )
            )

        response_headers = forwardable_response_headers(dict(upstream_response.headers))

        async def relay() -> AsyncIterator[bytes]:
            response_bytes = bytearray()
            try:
                async for chunk in upstream_response.aiter_bytes():
                    response_bytes += chunk
                    yield chunk
            finally:
                await upstream_response.aclose()
                record(bytes(response_bytes))

        return StreamingResponse(relay(), status_code=upstream_response.status_code, headers=response_headers)

    return Starlette(routes=[Route(RESPONSES_PATH, responses, methods=["POST"])])


def serve(records_path: Path, host: str, port: int) -> None:
    uvicorn.run(build_app(TapRecorder(records_path), openrouter_client()), host=host, port=port, log_level="warning")


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(prog="python -m weave_bench.tap.app")
    parser.add_argument("--records", type=Path, required=True)
    parser.add_argument("--host", required=True)
    parser.add_argument("--port", type=int, required=True)
    args = parser.parse_args(argv)
    serve(args.records, args.host, args.port)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
