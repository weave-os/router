"""Tests for the route-decision client.

Focus on the wire contract the router actually serves: header shaping, the
schema-version guard, and status-to-typed-error mapping. Each test asserts a
value the client produced against a value chosen here, so deleting the
corresponding client code fails the test.
"""

from __future__ import annotations

from typing import Any

import httpx
import pytest
import respx

from weave_router_client import (
    ROUTE_SCHEMA_VERSION_V1,
    AsyncRouteClient,
    InvalidRequestError,
    RouteClient,
    RouteOptions,
    RoutingFailedError,
    UnauthorizedError,
    UnexpectedSchemaError,
)

BASE_URL = "https://router-test.invalid"
API_KEY = "rk_test_key"
ROUTE_URL = f"{BASE_URL}/v1/route"
PREVIEW_URL = f"{BASE_URL}/v1/route/preview"

BODY: dict[str, Any] = {
    "model": "claude-sonnet-4-5",
    "max_tokens": 256,
    "messages": [{"role": "user", "content": "add a null check to parseConfig"}],
}

DECISION_PAYLOAD: dict[str, Any] = {
    "schema_version": ROUTE_SCHEMA_VERSION_V1,
    "model": "claude-haiku-4-5",
    "provider": "anthropic",
    "reason": "cheap_and_cheerful",
}


def _client(**kwargs: Any) -> RouteClient:
    return RouteClient(BASE_URL, API_KEY, **kwargs)


def _error_body(kind: str, message: str) -> dict[str, Any]:
    """Build the router's Anthropic-shaped error envelope."""
    return {"type": "error", "error": {"type": kind, "message": message}}


@respx.mock
def test_route_returns_decision_and_sends_bearer_token() -> None:
    captured: dict[str, Any] = {}

    def respond(request: httpx.Request) -> httpx.Response:
        captured["headers"] = dict(request.headers)
        return httpx.Response(200, json=DECISION_PAYLOAD)

    respx.post(ROUTE_URL).mock(side_effect=respond)

    with _client() as client:
        decision = client.route(BODY)

    assert decision.model == "claude-haiku-4-5"
    assert decision.provider == "anthropic"
    assert decision.reason == "cheap_and_cheerful"
    assert captured["headers"]["authorization"] == f"Bearer {API_KEY}"


@respx.mock
def test_route_options_serialize_to_weave_headers() -> None:
    """Every set option must reach the router as the header its middleware reads."""
    captured: dict[str, Any] = {}

    def respond(request: httpx.Request) -> httpx.Response:
        captured["headers"] = dict(request.headers)
        return httpx.Response(200, json=DECISION_PAYLOAD)

    respx.post(ROUTE_URL).mock(side_effect=respond)

    options = RouteOptions(
        strategy="hmm",
        cluster_version="v0.70",
        effort="high",
        embed_only_user_message=True,
        alpha=0.25,
        speed_weight=0.5,
        output_cost_ratio=4.0,
        expected_output_tokens=1024,
        per_model_verbosity="terse",
    )
    with _client() as client:
        client.route(BODY, options=options)

    headers = captured["headers"]
    assert headers["x-weave-router-strategy"] == "hmm"
    assert headers["x-weave-cluster-version"] == "v0.70"
    assert headers["x-weave-effort"] == "high"
    assert headers["x-weave-embed-only-user-message"] == "true"
    assert headers["x-weave-routing-alpha"] == "0.25"
    assert headers["x-weave-routing-speed-weight"] == "0.5"
    assert headers["x-weave-routing-output-cost-ratio"] == "4.0"
    assert headers["x-weave-routing-expected-output-tokens"] == "1024"
    assert headers["x-weave-routing-per-model-verbosity"] == "terse"


@respx.mock
def test_route_omits_unset_options() -> None:
    """An unset option must not be sent at all, so the server default applies."""
    captured: dict[str, Any] = {}

    def respond(request: httpx.Request) -> httpx.Response:
        captured["headers"] = dict(request.headers)
        return httpx.Response(200, json=DECISION_PAYLOAD)

    respx.post(ROUTE_URL).mock(side_effect=respond)

    with _client() as client:
        client.route(BODY, options=RouteOptions(strategy="hmm"))

    headers = captured["headers"]
    assert headers["x-weave-router-strategy"] == "hmm"
    assert "x-weave-cluster-version" not in headers
    assert "x-weave-routing-alpha" not in headers
    assert "x-weave-effort" not in headers


@respx.mock
def test_embed_only_user_message_false_is_sent_not_dropped() -> None:
    """False is a meaningful value here, distinct from unset."""
    captured: dict[str, Any] = {}

    def respond(request: httpx.Request) -> httpx.Response:
        captured["headers"] = dict(request.headers)
        return httpx.Response(200, json=DECISION_PAYLOAD)

    respx.post(ROUTE_URL).mock(side_effect=respond)

    with _client() as client:
        client.route(BODY, options=RouteOptions(embed_only_user_message=False))

    assert captured["headers"]["x-weave-embed-only-user-message"] == "false"


@respx.mock
def test_route_rejects_unknown_schema_version() -> None:
    respx.post(ROUTE_URL).mock(
        return_value=httpx.Response(
            200, json={**DECISION_PAYLOAD, "schema_version": "router_route_v99"}
        )
    )

    with _client() as client:
        with pytest.raises(UnexpectedSchemaError) as excinfo:
            client.route(BODY)

    assert "router_route_v99" in str(excinfo.value)


@respx.mock
def test_route_accepts_unknown_schema_version_when_check_disabled() -> None:
    """The escape hatch lets a caller read a newer server without a client bump."""
    respx.post(ROUTE_URL).mock(
        return_value=httpx.Response(
            200, json={**DECISION_PAYLOAD, "schema_version": "router_route_v99"}
        )
    )

    with _client(check_schema_version=False) as client:
        decision = client.route(BODY)

    assert decision.model == "claude-haiku-4-5"
    assert decision.schema_version == "router_route_v99"


@respx.mock
@pytest.mark.parametrize(
    ("status", "expected"),
    [
        (400, InvalidRequestError),
        (401, UnauthorizedError),
        (413, InvalidRequestError),
        (502, RoutingFailedError),
        (503, RoutingFailedError),
        (500, RoutingFailedError),
    ],
)
def test_status_maps_to_typed_error(status: int, expected: type[Exception]) -> None:
    respx.post(ROUTE_URL).mock(
        return_value=httpx.Response(
            status, json=_error_body("api_error", "something went wrong")
        )
    )

    with _client() as client:
        with pytest.raises(expected):
            client.route(BODY)


@respx.mock
def test_error_message_comes_from_anthropic_envelope() -> None:
    respx.post(ROUTE_URL).mock(
        return_value=httpx.Response(
            400,
            json=_error_body(
                "invalid_request_error", "Invalid routing knobs supplied."
            ),
        )
    )

    with _client() as client:
        with pytest.raises(InvalidRequestError) as excinfo:
            client.route(BODY)

    assert "Invalid routing knobs supplied." in str(excinfo.value)


@respx.mock
def test_non_json_response_raises_routing_failed() -> None:
    """Staging occasionally serves HTML error bodies through the load balancer."""
    respx.post(ROUTE_URL).mock(
        return_value=httpx.Response(
            200,
            text="<html>502 Bad Gateway</html>",
            headers={"content-type": "text/html"},
        )
    )

    with _client() as client:
        with pytest.raises(RoutingFailedError):
            client.route(BODY)


@respx.mock
def test_preview_returns_policy_trace() -> None:
    respx.post(PREVIEW_URL).mock(
        return_value=httpx.Response(
            200,
            json={
                "schema_version": "policy_router_v1",
                "strategy": "hmm",
                "policy_artifact_id": "hmm-prod",
                "policy_artifact_sha256": "sha256:artifact",
                "hmm_state_id": 3,
                "class_probabilities": {"coding": 0.8, "qa": 0.2},
                "selected_group": "coding",
                "eligible_roster_ids": ["anthropic/claude-opus-4-8", "openai/gpt-5.5"],
                "ranked_fallback": [
                    {
                        "group": "coding",
                        "probability": 0.8,
                        "roster_arms": ["anthropic/claude-opus-4-8"],
                        "eligible_arms": ["anthropic/claude-opus-4-8"],
                    }
                ],
                "resolver_exclusions": [
                    {
                        "catalog_id": "gemini-3.1-pro-preview",
                        "reason": "excluded_by_org",
                    }
                ],
            },
        )
    )

    with _client() as client:
        preview = client.preview(BODY, options=RouteOptions(strategy="hmm"))

    assert preview.schema_version == "policy_router_v1"
    assert preview.hmm_state_id == 3
    assert preview.class_probabilities == {"coding": 0.8, "qa": 0.2}
    assert preview.selected_group == "coding"
    assert preview.eligible_roster_ids == [
        "anthropic/claude-opus-4-8",
        "openai/gpt-5.5",
    ]
    assert preview.ranked_fallback[0].group == "coding"
    assert preview.resolver_exclusions[0].reason == "excluded_by_org"


def test_preview_rejects_explicit_non_hmm_strategy_without_a_round_trip() -> None:
    """An explicitly non-HMM strategy is a client-side error; the server 400s."""
    with _client() as client:
        with pytest.raises(InvalidRequestError) as excinfo:
            client.preview(BODY, options=RouteOptions(strategy="cluster"))

    assert "HMM strategy" in str(excinfo.value)


@respx.mock
def test_preview_allows_unset_strategy_so_server_default_decides() -> None:
    """With no strategy set the deployment default may be HMM; only the server knows."""
    route = respx.post(PREVIEW_URL).mock(
        return_value=httpx.Response(
            200, json={"schema_version": "policy_router_v1", "strategy": "hmm"}
        )
    )

    with _client() as client:
        preview = client.preview(BODY)

    assert route.called
    assert preview.strategy == "hmm"


@respx.mock
def test_preview_does_not_enforce_route_schema_version() -> None:
    """Preview versions on the policy contract, not router_route_v1."""
    respx.post(PREVIEW_URL).mock(
        return_value=httpx.Response(
            200, json={"schema_version": "policy_router_v2", "strategy": "hmm"}
        )
    )

    with _client() as client:
        preview = client.preview(BODY)

    assert preview.schema_version == "policy_router_v2"


def test_missing_api_key_is_rejected_at_construction() -> None:
    with pytest.raises(ValueError):
        RouteClient(BASE_URL, "")


def test_missing_base_url_is_rejected_at_construction() -> None:
    with pytest.raises(ValueError):
        RouteClient("", API_KEY)


@respx.mock
def test_trailing_slash_in_base_url_does_not_double_the_path() -> None:
    route = respx.post(ROUTE_URL).mock(
        return_value=httpx.Response(200, json=DECISION_PAYLOAD)
    )

    with RouteClient(BASE_URL + "/", API_KEY) as client:
        client.route(BODY)

    assert route.called


@respx.mock
def test_out_of_range_knob_is_rejected_before_the_request() -> None:
    """Bounds mirror the server's; catching them here saves a guaranteed 400."""
    route = respx.post(ROUTE_URL).mock(
        return_value=httpx.Response(200, json=DECISION_PAYLOAD)
    )

    with pytest.raises(ValueError):
        RouteOptions(alpha=1.5)

    assert not route.called


@respx.mock
async def test_async_client_returns_decision() -> None:
    respx.post(ROUTE_URL).mock(return_value=httpx.Response(200, json=DECISION_PAYLOAD))

    async with AsyncRouteClient(BASE_URL, API_KEY) as client:
        decision = await client.route(BODY)

    assert decision.model == "claude-haiku-4-5"
    assert decision.provider == "anthropic"


@respx.mock
async def test_async_client_maps_errors() -> None:
    respx.post(ROUTE_URL).mock(
        return_value=httpx.Response(
            401, json=_error_body("authentication_error", "bad key")
        )
    )

    async with AsyncRouteClient(BASE_URL, API_KEY) as client:
        with pytest.raises(UnauthorizedError):
            await client.route(BODY)


@respx.mock
async def test_async_preview_returns_trace() -> None:
    respx.post(PREVIEW_URL).mock(
        return_value=httpx.Response(
            200,
            json={
                "schema_version": "policy_router_v1",
                "strategy": "hmm",
                "hmm_state_id": 7,
            },
        )
    )

    async with AsyncRouteClient(BASE_URL, API_KEY) as client:
        preview = await client.preview(BODY, options=RouteOptions(strategy="hmm"))

    assert preview.hmm_state_id == 7


@respx.mock
def test_injected_client_is_not_closed_by_the_wrapper() -> None:
    """A caller-supplied transport is the caller's to manage."""
    respx.post(ROUTE_URL).mock(return_value=httpx.Response(200, json=DECISION_PAYLOAD))

    transport = httpx.Client()
    with RouteClient(BASE_URL, API_KEY, client=transport) as client:
        client.route(BODY)

    assert not transport.is_closed
    transport.close()
