"""Sync + async HTTP clients for the router's route-decision endpoints."""

from __future__ import annotations

from typing import Any, Final

import httpx
from pydantic import BaseModel, ConfigDict, Field

# ROUTE_SCHEMA_VERSION_V1 mirrors anthropic.RouteSchemaVersionV1 in the router.
# The client accepts an unknown value only when the caller opts out of the check,
# so a server-side contract bump surfaces here instead of silently misparsing.
ROUTE_SCHEMA_VERSION_V1: Final = "router_route_v1"

_ROUTE_PATH: Final = "/v1/route"
_PREVIEW_PATH: Final = "/v1/route/preview"

# A routing decision is a single cheap call against an in-region service. If it
# has not answered in this long, the strategy is unhealthy and waiting longer
# does not help.
DEFAULT_TIMEOUT_S: Final = 10.0

_HEADER_STRATEGY: Final = "x-weave-router-strategy"
_HEADER_CLUSTER_VERSION: Final = "x-weave-cluster-version"
_HEADER_EFFORT: Final = "x-weave-effort"
_HEADER_EMBED_ONLY_USER_MESSAGE: Final = "x-weave-embed-only-user-message"
_HEADER_ALPHA: Final = "x-weave-routing-alpha"
_HEADER_SPEED_WEIGHT: Final = "x-weave-routing-speed-weight"
_HEADER_OUTPUT_COST_RATIO: Final = "x-weave-routing-output-cost-ratio"
_HEADER_EXPECTED_OUTPUT_TOKENS: Final = "x-weave-routing-expected-output-tokens"
_HEADER_PER_MODEL_VERBOSITY: Final = "x-weave-routing-per-model-verbosity"

# Strategies that support /v1/route/preview. Mirrors router.IsHMMStrategy; the
# preview response is HMM-shaped (hmm_state_id, class_probabilities).
_PREVIEW_STRATEGIES: Final = frozenset({"hmm", "hmm_embedding"})


class RouteClientError(Exception):
    """Base class for every route-client failure."""


class UnauthorizedError(RouteClientError):
    """The rk_ bearer token was missing, malformed, or revoked (HTTP 401)."""


class InvalidRequestError(RouteClientError):
    """The router rejected the request (HTTP 400/413).

    Covers a non-JSON-object body, invalid routing knobs, a preview asked for on
    a non-HMM strategy, and an over-limit body.
    """


class RoutingFailedError(RouteClientError):
    """Routing itself failed (HTTP 502, or any other unexpected status).

    The strategy errored or its policy sidecar is unreachable. There is no
    silent fallback to a default model by design, so this is a real failure —
    do not blanket-retry it.
    """


class UnexpectedSchemaError(RouteClientError):
    """The response carried an unrecognized ``schema_version``."""


class RouteOptions(BaseModel):
    """Optional per-request routing overrides.

    Each field maps to one ``x-weave-*`` request header the router's middleware
    already reads. Leave a field ``None`` to inherit the deployment default.

    Some overrides additionally require the installation to be authorized for
    policy header overrides; an unauthorized value is ignored by the server
    rather than rejected, so the response reflects the deployment default.
    """

    model_config = ConfigDict(frozen=True)

    strategy: str | None = None
    cluster_version: str | None = None
    effort: str | None = None
    embed_only_user_message: bool | None = None
    alpha: float | None = Field(default=None, ge=0.0, le=1.0)
    speed_weight: float | None = Field(default=None, ge=0.0, le=1.0)
    output_cost_ratio: float | None = Field(default=None, ge=0.0, le=10.0)
    expected_output_tokens: int | None = Field(default=None, ge=0, le=100_000)
    per_model_verbosity: str | None = None

    def to_headers(self) -> dict[str, str]:
        """Serialize the set options to their ``x-weave-*`` header form."""
        headers: dict[str, str] = {}
        if self.strategy is not None:
            headers[_HEADER_STRATEGY] = self.strategy
        if self.cluster_version is not None:
            headers[_HEADER_CLUSTER_VERSION] = self.cluster_version
        if self.effort is not None:
            headers[_HEADER_EFFORT] = self.effort
        if self.embed_only_user_message is not None:
            headers[_HEADER_EMBED_ONLY_USER_MESSAGE] = str(
                self.embed_only_user_message
            ).lower()
        if self.alpha is not None:
            headers[_HEADER_ALPHA] = repr(self.alpha)
        if self.speed_weight is not None:
            headers[_HEADER_SPEED_WEIGHT] = repr(self.speed_weight)
        if self.output_cost_ratio is not None:
            headers[_HEADER_OUTPUT_COST_RATIO] = repr(self.output_cost_ratio)
        if self.expected_output_tokens is not None:
            headers[_HEADER_EXPECTED_OUTPUT_TOKENS] = str(self.expected_output_tokens)
        if self.per_model_verbosity is not None:
            headers[_HEADER_PER_MODEL_VERBOSITY] = self.per_model_verbosity
        return headers


class RouteDecision(BaseModel):
    """The decision the proxy would act on for a given request body."""

    model_config = ConfigDict(frozen=True)

    schema_version: str = ""
    model: str
    provider: str
    # reason is a short decision tag for logs. Not a stable enum — do not
    # branch on it.
    reason: str = ""


class PreviewGroup(BaseModel):
    """One classifier group in serving fallback order."""

    model_config = ConfigDict(frozen=True)

    group: str = ""
    probability: float = 0.0
    roster_arms: list[str] = Field(default_factory=list)
    eligible_arms: list[str] = Field(default_factory=list)


class PreviewDiagnostic(BaseModel):
    """Why one candidate was excluded from the eligible set."""

    model_config = ConfigDict(frozen=True)

    catalog_id: str = ""
    roster_id: str = ""
    reason: str = ""


class PreviewCandidate(BaseModel):
    """One catalog-backed model that was offered to the policy.

    Mirrors ``policy.Candidate``. Extra fields are preserved rather than
    dropped, so a server-side additive field is still readable via
    ``model_extra`` without a client release.
    """

    model_config = ConfigDict(frozen=True, extra="allow")

    arm_id: str = ""
    roster_id: str = ""
    catalog_id: str = ""
    provider: str = ""
    upstream_id: str = ""
    endpoint: str = ""
    input_usd_per_1m: float = 0.0
    output_usd_per_1m: float = 0.0
    estimated_cost_usd: float = 0.0
    effective_estimated_cost_usd: float = 0.0


class RoutePreview(BaseModel):
    """A side-effect-free policy evaluation with its full decision trace.

    Mirrors ``policy.PreviewResult``. Its ``schema_version`` carries the
    policy-sidecar contract version (e.g. ``policy_router_v1``), which versions
    independently of :data:`ROUTE_SCHEMA_VERSION_V1`.
    """

    model_config = ConfigDict(frozen=True, extra="allow")

    schema_version: str = ""
    route_id: str = ""
    strategy: str = ""
    policy_artifact_id: str = ""
    policy_artifact_sha256: str = ""
    roster_sha256: str = ""
    hmm_state_id: int = 0
    hmm_state_path: list[int] = Field(default_factory=list)
    hmm_state_probabilities: list[float] = Field(default_factory=list)
    class_order: list[str] = Field(default_factory=list)
    class_probabilities: dict[str, float] = Field(default_factory=dict)
    ranked_fallback: list[PreviewGroup] = Field(default_factory=list)
    selected_group: str = ""
    eligible_roster_ids: list[str] = Field(default_factory=list)
    resolver_candidates: list[PreviewCandidate] = Field(default_factory=list)
    resolver_exclusions: list[PreviewDiagnostic] = Field(default_factory=list)


def _require_api_key(api_key: str) -> None:
    if not api_key:
        raise ValueError("api_key is required; issue one with `wv mr seed-key`")


def _require_base_url(base_url: str) -> str:
    if not base_url:
        raise ValueError("base_url is required (e.g. https://router.workweave.ai)")
    return base_url.rstrip("/")


def _request_headers(api_key: str, options: RouteOptions | None) -> dict[str, str]:
    headers = {
        "authorization": f"Bearer {api_key}",
        "content-type": "application/json",
    }
    if options is not None:
        headers.update(options.to_headers())
    return headers


def _guard_preview_strategy(options: RouteOptions | None) -> None:
    """Fail fast when a caller explicitly asks to preview a non-HMM strategy.

    The server returns 400 for this. Catching an explicitly-set unsupported
    strategy here turns a round-trip into an immediate, clearer error. A None
    strategy is left alone: the deployment default may well be HMM, and only
    the server knows.
    """
    if options is None or options.strategy is None:
        return
    if options.strategy not in _PREVIEW_STRATEGIES:
        raise InvalidRequestError(
            f"route preview requires an HMM strategy; got {options.strategy!r} "
            f"(supported: {', '.join(sorted(_PREVIEW_STRATEGIES))})"
        )


def _raise_for_status(response: httpx.Response) -> None:
    if response.status_code == httpx.codes.OK:
        return
    detail = _error_detail(response)
    if response.status_code == httpx.codes.UNAUTHORIZED:
        raise UnauthorizedError(detail)
    if response.status_code in (
        httpx.codes.BAD_REQUEST,
        httpx.codes.REQUEST_ENTITY_TOO_LARGE,
    ):
        raise InvalidRequestError(detail)
    raise RoutingFailedError(f"router returned {response.status_code}: {detail}")


def _error_detail(response: httpx.Response) -> str:
    """Pull the message out of the router's Anthropic-shaped error envelope."""
    try:
        payload = response.json()
    except ValueError:
        return response.text.strip() or f"HTTP {response.status_code}"
    if isinstance(payload, dict):
        error = payload.get("error")
        if isinstance(error, dict):
            message = error.get("message")
            if isinstance(message, str) and message:
                return message
    return response.text.strip() or f"HTTP {response.status_code}"


def _decode_json(response: httpx.Response) -> dict[str, Any]:
    try:
        payload = response.json()
    except ValueError as err:
        raise RoutingFailedError(f"router returned a non-JSON response: {err}") from err
    if not isinstance(payload, dict):
        raise RoutingFailedError("router returned a non-object JSON response")
    return payload


def _parse_decision(payload: dict[str, Any], check_schema: bool) -> RouteDecision:
    decision = RouteDecision.model_validate(payload)
    if check_schema and decision.schema_version != ROUTE_SCHEMA_VERSION_V1:
        raise UnexpectedSchemaError(
            f"expected schema_version {ROUTE_SCHEMA_VERSION_V1!r}, "
            f"got {decision.schema_version!r}; upgrade this client"
        )
    return decision


class RouteClient:
    """Blocking client for the router's route-decision endpoints.

    ``base_url`` is the router root (e.g. ``https://router.workweave.ai``);
    ``api_key`` is an ``rk_`` router key.

    Reuses one :class:`httpx.Client`, so prefer a single long-lived instance.
    Use it as a context manager, or call :meth:`close`, to release the pool.
    """

    def __init__(
        self,
        base_url: str,
        api_key: str,
        *,
        timeout_s: float = DEFAULT_TIMEOUT_S,
        check_schema_version: bool = True,
        client: httpx.Client | None = None,
    ) -> None:
        """Build a client; pass ``client`` to supply your own transport."""
        _require_api_key(api_key)
        self._base_url = _require_base_url(base_url)
        self._api_key = api_key
        self._check_schema_version = check_schema_version
        self._owns_client = client is None
        self._client = client or httpx.Client(timeout=httpx.Timeout(timeout_s))

    def __enter__(self) -> RouteClient:
        """Return self so the client can be used as a context manager."""
        return self

    def __exit__(self, *exc_info: object) -> None:
        """Close the underlying transport if this client created it."""
        self.close()

    def close(self) -> None:
        """Release the underlying connection pool, if this client owns it."""
        if self._owns_client:
            self._client.close()

    def route(
        self,
        body: dict[str, Any],
        *,
        options: RouteOptions | None = None,
    ) -> RouteDecision:
        """Return the decision the proxy would act on for ``body``.

        ``body`` is an Anthropic Messages request payload.
        """
        response = self._client.post(
            self._base_url + _ROUTE_PATH,
            headers=_request_headers(self._api_key, options),
            json=body,
        )
        _raise_for_status(response)
        return _parse_decision(_decode_json(response), self._check_schema_version)

    def preview(
        self,
        body: dict[str, Any],
        *,
        options: RouteOptions | None = None,
    ) -> RoutePreview:
        """Return the full policy trace for ``body`` without serving it.

        Requires an HMM strategy; see :class:`RoutePreview`.
        """
        _guard_preview_strategy(options)
        response = self._client.post(
            self._base_url + _PREVIEW_PATH,
            headers=_request_headers(self._api_key, options),
            json=body,
        )
        _raise_for_status(response)
        return RoutePreview.model_validate(_decode_json(response))


class AsyncRouteClient:
    """Async counterpart to :class:`RouteClient`.

    Same contract and errors; awaits instead of blocking. Prefer this in eval
    harnesses that fan many decisions out concurrently.
    """

    def __init__(
        self,
        base_url: str,
        api_key: str,
        *,
        timeout_s: float = DEFAULT_TIMEOUT_S,
        check_schema_version: bool = True,
        client: httpx.AsyncClient | None = None,
    ) -> None:
        """Build a client; pass ``client`` to supply your own transport."""
        _require_api_key(api_key)
        self._base_url = _require_base_url(base_url)
        self._api_key = api_key
        self._check_schema_version = check_schema_version
        self._owns_client = client is None
        self._client = client or httpx.AsyncClient(timeout=httpx.Timeout(timeout_s))

    async def __aenter__(self) -> AsyncRouteClient:
        """Return self so the client can be used as an async context manager."""
        return self

    async def __aexit__(self, *exc_info: object) -> None:
        """Close the underlying transport if this client created it."""
        await self.aclose()

    async def aclose(self) -> None:
        """Release the underlying connection pool, if this client owns it."""
        if self._owns_client:
            await self._client.aclose()

    async def route(
        self,
        body: dict[str, Any],
        *,
        options: RouteOptions | None = None,
    ) -> RouteDecision:
        """Return the decision the proxy would act on for ``body``."""
        response = await self._client.post(
            self._base_url + _ROUTE_PATH,
            headers=_request_headers(self._api_key, options),
            json=body,
        )
        _raise_for_status(response)
        return _parse_decision(_decode_json(response), self._check_schema_version)

    async def preview(
        self,
        body: dict[str, Any],
        *,
        options: RouteOptions | None = None,
    ) -> RoutePreview:
        """Return the full policy trace for ``body`` without serving it."""
        _guard_preview_strategy(options)
        response = await self._client.post(
            self._base_url + _PREVIEW_PATH,
            headers=_request_headers(self._api_key, options),
            json=body,
        )
        _raise_for_status(response)
        return RoutePreview.model_validate(_decode_json(response))
