"""Pin proxy: stamps the router's pin and rollout headers onto any harness.

A harness that only takes a base URL (SWE-Together's Claude Code and Codex
agents) cannot send ``x-weave-force-model``. Point its base URL at

    http://<proxy>/pin/<rollout id>/<model token>

and every request is forwarded to the router with ``x-weave-rollout-id: <rollout
id>`` and, unless the token is ``UNPINNED``, ``x-weave-force-model: <model>``.
The path carries both, so one proxy serves any number of concurrent trials and
holds no state. Credentials pass through untouched; the proxy authenticates
nobody, so bind it to an interface only the sandboxes reach.

Run with ``weave-bench efficiency pin-proxy``.
"""

from __future__ import annotations

import argparse
from collections.abc import AsyncIterator

import httpx
import uvicorn
from starlette.applications import Starlette
from starlette.requests import Request
from starlette.responses import JSONResponse, Response, StreamingResponse
from starlette.routing import Route

from weave_bench.efficiency.pin_path import HOP_BY_HOP_HEADERS, PIN_PREFIX, parse_pin_path, upstream_headers

UPSTREAM_TIMEOUT_SECONDS = 90 * 60
# httpx decodes the body it relays, so the encoding header must not survive.
DROPPED_RESPONSE_HEADERS = HOP_BY_HOP_HEADERS | {"content-encoding"}


def build_app(upstream: httpx.AsyncClient) -> Starlette:
    async def relay(request: Request) -> Response:
        pinned = parse_pin_path(request.url.path)
        if pinned is None:
            return JSONResponse({"error": f"expected {PIN_PREFIX}/<rollout id>/<model>/<path>"}, status_code=404)
        upstream_response = await upstream.send(
            upstream.build_request(
                request.method,
                pinned.upstream_path,
                params=request.url.query or None,
                content=await request.body(),
                headers=upstream_headers(dict(request.headers), pinned),
            ),
            stream=True,
        )

        async def body() -> AsyncIterator[bytes]:
            try:
                async for chunk in upstream_response.aiter_bytes():
                    yield chunk
            finally:
                await upstream_response.aclose()

        response_headers = {
            name: value
            for name, value in upstream_response.headers.items()
            if name.lower() not in DROPPED_RESPONSE_HEADERS
        }
        return StreamingResponse(body(), status_code=upstream_response.status_code, headers=response_headers)

    return Starlette(
        routes=[Route(PIN_PREFIX + "/{rest:path}", relay, methods=["GET", "POST", "PUT", "PATCH", "DELETE"])]
    )


def serve(router_base_url: str, host: str, port: int) -> None:
    client = httpx.AsyncClient(base_url=router_base_url.rstrip("/"), timeout=UPSTREAM_TIMEOUT_SECONDS)
    uvicorn.run(build_app(client), host=host, port=port, log_level="warning")


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(prog="python -m weave_bench.efficiency.pin_proxy")
    parser.add_argument("--router", required=True)
    parser.add_argument("--host", required=True)
    parser.add_argument("--port", type=int, required=True)
    args = parser.parse_args(argv)
    serve(args.router, args.host, args.port)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
