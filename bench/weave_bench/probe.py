"""Verify what a router deployment will serve before spending on a run.

``/v1/version`` and ``/v1/router/hmm-roster`` identify the binary and the
frozen stable roster; a ``/beta`` control turn on a throwaway session proves
the beta lane is enabled for this API key (the ack never leaves the router, so
this costs no upstream tokens). The beta *package* itself is deployment
config (``ROUTER_HMM_BETA_SIDECAR_URL``/``ROUTER_HMM_BETA_ROSTER_PATH``) and
must be recorded from the deployment, not probed.
"""

from __future__ import annotations

import json
import uuid
from dataclasses import dataclass
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen

from weave_bench.agent.beta_turn import BETA_ACK_MARKER, BETA_COMMAND
from weave_bench.codex_config import ROUTER_CLIENT_APP, ROUTER_CLIENT_APP_HEADER

VERSION_PATH = "/v1/version"
HMM_ROSTER_PATH = "/v1/router/hmm-roster"
RESPONSES_PATH = "/v1/responses"
# Codex identifies its session to the router with this header (session_key.go).
CODEX_SESSION_HEADER = "Session-Id"
PROBE_MODEL = "gpt-5.6-sol"
PROBE_TIMEOUT_SECONDS = 30


@dataclass(frozen=True)
class RouterProbe:
    version: dict[str, object]
    roster_sha256: str | None
    beta_ack: str
    beta_enabled: bool

    def render(self) -> str:
        lines = [
            f"router commit: {self.version.get('display') or self.version.get('commit')}",
            f"cluster version: {self.version.get('cluster_version')}",
            f"stable HMM roster sha256: {self.roster_sha256 or 'unavailable'}",
            f"/beta ack: {self.beta_ack.strip() or '(empty)'}",
            f"beta lane enabled for this key: {'yes' if self.beta_enabled else 'NO'}",
        ]
        return "\n".join(lines) + "\n"


def _get_json(url: str, api_key: str) -> dict[str, object] | None:
    request = Request(url, headers={"Authorization": f"Bearer {api_key}"})
    try:
        with urlopen(request, timeout=PROBE_TIMEOUT_SECONDS) as response:
            return json.loads(response.read())
    except (HTTPError, URLError):
        return None


def _beta_turn(base_url: str, api_key: str) -> str:
    """Send ``/beta`` as a Responses API turn; return the concatenated output text."""
    body = json.dumps({"model": PROBE_MODEL, "input": BETA_COMMAND, "stream": False}).encode()
    request = Request(
        f"{base_url}{RESPONSES_PATH}",
        data=body,
        method="POST",
        headers={
            "Authorization": f"Bearer {api_key}",
            "Content-Type": "application/json",
            ROUTER_CLIENT_APP_HEADER: ROUTER_CLIENT_APP,
            CODEX_SESSION_HEADER: f"weave-bench-probe-{uuid.uuid4()}",
        },
    )
    try:
        with urlopen(request, timeout=PROBE_TIMEOUT_SECONDS) as response:
            payload = json.loads(response.read())
    except HTTPError as exc:
        return f"HTTP {exc.code}: {exc.read().decode(errors='replace')[:500]}"
    return response_output_text(payload)


def response_output_text(payload: dict[str, object]) -> str:
    """Concatenated ``output_text`` parts of a non-streaming Responses object."""
    output = payload.get("output")
    if not isinstance(output, list):
        return ""
    texts: list[str] = []
    for item in output:
        content = item.get("content") if isinstance(item, dict) else None
        if not isinstance(content, list):
            continue
        texts += [str(part.get("text", "")) for part in content if isinstance(part, dict)]
    return "".join(texts)


def probe_router(base_url: str, api_key: str) -> RouterProbe:
    base_url = base_url.rstrip("/")
    version = _get_json(f"{base_url}{VERSION_PATH}", api_key) or {}
    roster = _get_json(f"{base_url}{HMM_ROSTER_PATH}", api_key) or {}
    roster_sha = roster.get("roster_sha256")
    ack = _beta_turn(base_url, api_key)
    return RouterProbe(
        version=version,
        roster_sha256=str(roster_sha) if isinstance(roster_sha, str) else None,
        beta_ack=ack,
        beta_enabled=BETA_ACK_MARKER in ack,
    )
