"""Pin-proxy URL paths: ``/pin/<rollout id>/<model token>/<upstream path>``.

Dependency-free, so planning trials needs none of the proxy's server packages.
"""

from __future__ import annotations

from dataclasses import dataclass
from urllib.parse import quote

from weave_bench.codex_config import ROUTER_FORCE_MODEL_HEADER, ROUTER_ROLLOUT_ID_HEADER

PIN_PREFIX = "/pin"
UNPINNED = "-"
# Model ids may contain "/" (``z-ai/glm-5.3``), which a path segment cannot hold;
# "~" appears in no catalog id.
SLASH_STAND_IN = "~"
HOP_BY_HOP_HEADERS = frozenset(
    {"host", "content-length", "connection", "keep-alive", "transfer-encoding", "te", "trailer", "upgrade"}
)


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
