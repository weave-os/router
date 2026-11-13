"""Pure request/response rewriting for the OpenRouter tap (no I/O, unit-testable).

Codex cannot add body fields or read ``response.model`` back, so the tap does
both: it carries the Auto Router's request-only settings and records what
OpenRouter actually served, keyed by the Codex thread that Harbor's trial owns.
"""

from __future__ import annotations

import json
from dataclasses import asdict, dataclass
from enum import StrEnum

from weave_bench.arms import OPENROUTER_MODEL_PREFIX, CostTier
from weave_bench.codex_config import ROUTER_ROLLOUT_ID_HEADER, TAP_COST_TIER_HEADER


class TapHeader(StrEnum):
    """Client headers read off each request. Codex 0.149+ sends the thread id as
    ``Session-Id`` (``Thread-Id`` carries the same value on the main thread); it
    is Harbor's ``trajectory.session_id`` and the analytics join key on the
    router arm. ``prompt_cache_key`` in the body is the fallback."""

    SESSION_ID = "session-id"
    THREAD_ID = "thread-id"
    ROLLOUT_ID = ROUTER_ROLLOUT_ID_HEADER
    COST_TIER = TAP_COST_TIER_HEADER


class OpenRouterAutoModel(StrEnum):
    """Metarouter slugs; the Auto Router plugin id is the suffix plus ``-router``."""

    AUTO = "openrouter/auto"
    AUTO_BETA = "openrouter/auto-beta"

    @property
    def plugin_id(self) -> str:
        return f"{self.removeprefix(OPENROUTER_MODEL_PREFIX)}-router"


class CacheControlType(StrEnum):
    """Request-level ``cache_control.type`` — the only form OpenRouter's Responses
    API accepts (per-part markers are rejected with 400). Anthropic picks get
    prompt caching on Codex's append-only conversations; OpenAI/Google picks
    cache automatically and ignore it."""

    EPHEMERAL = "ephemeral"


PROMPT_CACHE_KEY_FIELD = "prompt_cache_key"
SESSION_ID_FIELD = "session_id"
PLUGINS_FIELD = "plugins"
CACHE_CONTROL_FIELD = "cache_control"
USAGE_FIELD = "usage"
MODEL_FIELD = "model"

# Case-insensitive; Starlette lower-cases header names already.
DROPPED_REQUEST_HEADERS = frozenset(
    {"host", "content-length", "connection", "transfer-encoding", "accept-encoding", TapHeader.COST_TIER}
)
DROPPED_RESPONSE_HEADERS = frozenset({"content-length", "connection", "transfer-encoding", "content-encoding"})

_SSE_DATA_PREFIX = b"data:"


def codex_session_id(headers: dict[str, str], body: dict[str, object]) -> str:
    candidates = (
        headers.get(TapHeader.SESSION_ID, ""),
        headers.get(TapHeader.THREAD_ID, ""),
        str(body.get(PROMPT_CACHE_KEY_FIELD) or ""),
    )
    return next((value for value in candidates if value), "")


def restore_openrouter_model(requested_model: str) -> str:
    """Harbor passes Codex the part after the provider slash, so ``auto-beta``
    arrives bare and OpenRouter needs ``openrouter/auto-beta``."""
    if requested_model.startswith(OPENROUTER_MODEL_PREFIX):
        return requested_model
    return f"{OPENROUTER_MODEL_PREFIX}{requested_model}"


@dataclass(frozen=True)
class RewrittenRequest:
    body: dict[str, object]
    upstream_model: str
    session_id: str
    plugin_id: str | None


def rewrite_request(body: dict[str, object], headers: dict[str, str], cost_tier: CostTier | None) -> RewrittenRequest:
    upstream_model = restore_openrouter_model(str(body.get(MODEL_FIELD, "")))
    session_id = codex_session_id(headers, body)
    rewritten: dict[str, object] = {**body, MODEL_FIELD: upstream_model, USAGE_FIELD: {"include": True}}
    plugin_id = None
    if cost_tier is not None:
        plugin_id = OpenRouterAutoModel(upstream_model).plugin_id
        rewritten[PLUGINS_FIELD] = [{"id": plugin_id, "cost_tier": cost_tier.value}]
        rewritten[CACHE_CONTROL_FIELD] = {"type": CacheControlType.EPHEMERAL.value}
        if session_id:
            rewritten[SESSION_ID_FIELD] = session_id
    return RewrittenRequest(rewritten, upstream_model, session_id, plugin_id)


def forwardable_request_headers(headers: dict[str, str]) -> dict[str, str]:
    return {name: value for name, value in headers.items() if name.lower() not in DROPPED_REQUEST_HEADERS}


def forwardable_response_headers(headers: dict[str, str]) -> dict[str, str]:
    return {name: value for name, value in headers.items() if name.lower() not in DROPPED_RESPONSE_HEADERS}


@dataclass(frozen=True)
class TapRecord:
    """One upstream response. ``cost_usd`` is OpenRouter's own charge (``usage.cost``,
    present because the tap sets ``usage.include``); tokens follow the Responses
    API usage shape."""

    response_id: str
    rollout_id: str
    session_id: str
    requested_model: str
    upstream_model: str
    served_model: str | None
    plugin_id: str | None
    cost_tier: str | None
    http_status: int
    response_status: str | None
    input_tokens: int | None
    cached_input_tokens: int | None
    output_tokens: int | None
    reasoning_tokens: int | None
    cost_usd: float | None
    requested_at: float
    duration_seconds: float

    def to_json(self) -> str:
        return json.dumps(asdict(self), sort_keys=True)


def usage_fields(
    usage: dict[str, object] | None,
) -> tuple[int | None, int | None, int | None, int | None, float | None]:
    if usage is None:
        return None, None, None, None, None
    input_details = usage.get("input_tokens_details")
    output_details = usage.get("output_tokens_details")
    cached = input_details.get("cached_tokens") if isinstance(input_details, dict) else None
    reasoning = output_details.get("reasoning_tokens") if isinstance(output_details, dict) else None
    cost = usage.get("cost")
    return (
        _optional_int(usage.get("input_tokens")),
        _optional_int(cached),
        _optional_int(usage.get("output_tokens")),
        _optional_int(reasoning),
        float(cost) if isinstance(cost, (int, float)) else None,
    )


def _optional_int(value: object) -> int | None:
    return int(value) if isinstance(value, (int, float)) else None


def terminal_response_body(response_bytes: bytes) -> dict[str, object] | None:
    """The final Responses-API ``response`` object: the last ``data:`` event that
    carries one when streaming, or the body itself otherwise."""
    terminal: dict[str, object] | None = None
    for line in response_bytes.split(b"\n"):
        if not line.startswith(_SSE_DATA_PREFIX):
            continue
        try:
            event = json.loads(line[len(_SSE_DATA_PREFIX) :])
        except ValueError:
            continue
        if isinstance(event, dict) and isinstance(event.get("response"), dict):
            terminal = event["response"]
    if terminal is not None:
        return terminal
    try:
        parsed = json.loads(response_bytes)
    except ValueError:
        return None
    return parsed if isinstance(parsed, dict) else None
