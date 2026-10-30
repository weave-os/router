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

from collections.abc import AsyncIterator
from dataclasses import dataclass
from urllib.parse import quote

import httpx
import uvicorn
from starlette.applications import Starlette
from starlette.requests import Request
from starlette.responses import JSONResponse, Response, StreamingResponse
from starlette.routing import Route

from weave_bench.codex_config import ROUTER_FORCE_MODEL_HEADER, ROUTER_ROLLOUT_ID_HEADER

PIN_PREFIX = "/pin"
UNPINNED = "-"
# Model ids may contain "/" (``z-ai/glm-5.3``), which a path segment cannot hold;
# "~" appears in no catalog id.
SLASH_STAND_IN = "~"
UPSTREAM_TIMEOUT_SECONDS = 90 * 60
HOP_BY_HOP_HEADERS = frozenset(
    {"host", "content-length", "connection", "keep-alive", "transfer-encoding", "te", "trailer", "upgrade"}
)
# httpx decodes the body it relays, so the encoding header must not survive.
DROPPED_RESPONSE_HEADERS = HOP_BY_HOP_HEADERS | {"content-encoding"}


def model_token(force_model: str | None) -> str:
    if not force_model:
        return UNPINNED
    if SLASH_STAND_IN in force_model:
        raise ValueError(f"model id {force_model!r} contains {SLASH_STAND_IN!r}")
    return force_model.replace("/", SLASH_STAND_IN)


def pin_base_url(proxy_url: str, *, rollout_id: str, force_model: str | None) -> str:
    return f"{proxy_url.rstrip('/')}{PIN_PREFIX}/{quote(rollout_id, safe='')}/{model_token(force_model)}"


@dataclass(frozen=True)
class PinnedRequest:
    rollout_id: str
    force_model: str | None
    upstream_path: str


def parse_pin_path(path: str) -> PinnedRequest | None:
    """``/pin/<rollout>/<token>/<upstream path>``, or None for any other path."""
    if not path.startswith(PIN_PREFIX + "/"):
        return None
    parts = path[len(PIN_PREFIX) + 1 :].split("/", 2)
    if len(parts) < 3 or not parts[0] or not parts[1]:
        return None
    rollout_id, token, upstream_path = parts
    force_model = None if token == UNPINNED else token.replace(SLASH_STAND_IN, "/")
    return PinnedRequest(rollout_id, force_model, "/" + upstream_path)


def upstream_headers(headers: dict[str, str], pinned: PinnedRequest) -> dict[str, str]:
    forwarded = {
        name: value
        for name, value in headers.items()
        if name.lower() not in HOP_BY_HOP_HEADERS
        and name.lower() not in (ROUTER_ROLLOUT_ID_HEADER, ROUTER_FORCE_MODEL_HEADER)
    }
    forwarded[ROUTER_ROLLOUT_ID_HEADER] = pinned.rollout_id
    if pinned.force_model is not None:
        forwarded[ROUTER_FORCE_MODEL_HEADER] = pinned.force_model
    return forwarded


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

    methods = ["GET", "POST", "PUT", "PATCH", "DELETE"]
    return Starlette(routes=[Route(PIN_PREFIX + "/{rest:path}", relay, methods=methods)])


def serve(router_base_url: str, host: str, port: int) -> None:
    client = httpx.AsyncClient(base_url=router_base_url.rstrip("/"), timeout=UPSTREAM_TIMEOUT_SECONDS)
    uvicorn.run(build_app(client), host=host, port=port, log_level="warning")
