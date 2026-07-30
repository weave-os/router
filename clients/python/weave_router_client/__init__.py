"""Typed client for the Weave Router route-decision API.

Wraps ``POST /v1/route`` (decision outcome) and ``POST /v1/route/preview``
(full HMM policy trace) — the router's side-effect-free "which model would you
pick?" endpoints. See ``docs/ROUTE_DECISION_API.md`` in the router repo for the
wire contract.

Both calls take an Anthropic Messages request body: the same JSON you would
POST to ``/v1/messages``.

Decisions are per-request by construction, so this client never caches and
never retries: a 502 means the routing strategy is down, not that the request
was transiently unlucky.
"""

from weave_router_client.client import (
    ROUTE_SCHEMA_VERSION_V1,
    AsyncRouteClient,
    InvalidRequestError,
    PreviewCandidate,
    PreviewDiagnostic,
    PreviewGroup,
    RouteClient,
    RouteClientError,
    RouteDecision,
    RouteOptions,
    RoutePreview,
    RoutingFailedError,
    UnauthorizedError,
    UnexpectedSchemaError,
)

__all__ = [
    "ROUTE_SCHEMA_VERSION_V1",
    "AsyncRouteClient",
    "InvalidRequestError",
    "PreviewCandidate",
    "PreviewDiagnostic",
    "PreviewGroup",
    "RouteClient",
    "RouteClientError",
    "RouteDecision",
    "RouteOptions",
    "RoutePreview",
    "RoutingFailedError",
    "UnauthorizedError",
    "UnexpectedSchemaError",
]
