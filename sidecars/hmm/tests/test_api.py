from __future__ import annotations

import logging

from fastapi.testclient import TestClient

from hmm_sidecar.api import app, configure_logging
from hmm_sidecar.policy import RouteTimings
from hmm_sidecar.schemas import RoutePreviewResult, RouteResult


class RejectingPolicy:
    async def route_with_timings(self, payload: dict[str, object]) -> None:
        del payload
        raise ValueError("private artifact path: /secret/model.npz")


class PreviewingPolicy:
    async def preview(self, payload: dict[str, object]) -> object:
        assert payload["execution_mode"] == "preview"
        return RoutePreviewResult(
            route_id="route",
            policy_artifact_id="artifact",
            policy_artifact_sha256="a" * 64,
            roster_sha256="b" * 64,
            hmm_state_id=1,
            hmm_state_path=(0, 1),
            hmm_state_probabilities=(0.3, 0.7),
            class_order=("fast", "maximum"),
            class_probabilities={"fast": 0.6, "maximum": 0.4},
            ranked_fallback=(
                {
                    "group": "fast",
                    "probability": 0.6,
                    "roster_arms": ("provider/a",),
                    "eligible_arms": ("provider/a",),
                },
                {
                    "group": "maximum",
                    "probability": 0.4,
                    "roster_arms": ("provider/b",),
                    "eligible_arms": (),
                },
            ),
            selected_group="fast",
            eligible_roster_ids=("provider/a",),
        )


class RosterPolicy:
    clusters = {
        "maximum": {"arms": ["provider/opus", "provider/fable"]},
        "fast": {"arms": ["provider/haiku"]},
    }
    roster_version = "c" * 64

    def roster_ids(self) -> list[str]:
        return ["openai/gpt-5.6-sol", "anthropic/claude-opus-4.8"]


def test_liveness_does_not_depend_on_model_readiness() -> None:
    with TestClient(app) as client:
        response = client.get("/livez")

    assert response.status_code == 200
    assert response.json()["status"] == "ok"


def test_capabilities_are_frozen_and_do_not_request_content_callbacks() -> None:
    with TestClient(app) as client:
        response = client.get("/capabilities")

    assert response.status_code == 200
    payload = response.json()
    assert payload["schema_version"] == "policy_router_v3"
    assert payload["reports_outcomes"] is False
    assert payload["reports_feedback"] is False
    assert payload["supports_shadow"] is True
    assert payload["reports_ranked_fallback"] is True
    assert payload["learning"]["state"] == "frozen_policy"


def test_roster_returns_ordered_cluster_arms() -> None:
    with TestClient(app) as client:
        app.state.policy = RosterPolicy()
        response = client.get("/roster")

    assert response.status_code == 200
    payload = response.json()
    assert payload["clusters"]["maximum"] == ["provider/opus", "provider/fable"]
    assert payload["clusters"]["fast"] == ["provider/haiku"]
    assert payload["roster_version"] == "c" * 64


def test_roster_fails_closed_without_a_policy() -> None:
    with TestClient(app) as client:
        response = client.get("/roster")

    assert response.status_code == 503


def test_disabled_callbacks_are_contract_compatible_noops() -> None:
    with TestClient(app) as client:
        outcome = client.post("/outcome", json={"response_text": "not retained"})
        feedback = client.post("/feedback", json={"feedback": "not retained"})

    assert outcome.status_code == 204
    assert feedback.status_code == 204
    assert outcome.content == b""
    assert feedback.content == b""


def test_readiness_fails_closed_without_an_artifact() -> None:
    with TestClient(app) as client:
        response = client.get("/readyz")

    assert response.status_code == 503
    assert response.json()["ready"] is False


def test_route_rejections_do_not_expose_internal_exception_text() -> None:
    with TestClient(app) as client:
        app.state.policy = RejectingPolicy()
        response = client.post("/route", json={"schema_version": "policy_router_v3"})

    assert response.status_code == 422
    assert response.json() == {"error": "route request rejected"}
    assert "/secret/model.npz" not in response.text


def test_preview_requires_explicit_mode_and_returns_all_selected_arms() -> None:
    with TestClient(app) as client:
        app.state.policy = PreviewingPolicy()
        rejected = client.post("/preview", json={"schema_version": "policy_router_v3"})
        response = client.post(
            "/preview",
            json={
                "schema_version": "policy_router_v3",
                "execution_mode": "preview",
            },
        )

    assert rejected.status_code == 400
    assert response.status_code == 200
    assert response.json()["eligible_roster_ids"] == ["provider/a"]


def test_roster_returns_arm_union() -> None:
    with TestClient(app) as client:
        app.state.policy = RosterPolicy()
        response = client.get("/roster")

    assert response.status_code == 200
    payload = response.json()
    assert payload["schema_version"] == "policy_router_v3"
    assert payload["roster_ids"] == [
        "openai/gpt-5.6-sol",
        "anthropic/claude-opus-4.8",
    ]


def test_roster_fails_closed_without_policy() -> None:
    with TestClient(app) as client:
        app.state.policy = None
        response = client.get("/roster")

    assert response.status_code == 503


class TimedPolicy:
    """Returns a fixed decision plus fixed timings, so the log line is assertable."""

    def __init__(self) -> None:
        self.calls = 0

    async def route_with_timings(self, payload: dict[str, object]):
        del payload
        self.calls += 1
        return (
            RouteResult(
                route_id="route",
                score=0.9,
                candidate_scores={"provider/a": 0.9},
                reason="test",
                state_label="state_1",
                policy_group="fast",
                policy_label="fast",
                policy_route_key="hmm:1:fast",
                confidence=0.9,
                margin=0.4,
                propensity=1.0,
                policy_artifact_id="artifact",
                policy_artifact_sha256="a" * 64,
                roster_version="b" * 64,
                ranked_fallback=(
                    {
                        "group": "fast",
                        "probability": 0.9,
                        "roster_arms": ("provider/a",),
                        "eligible_arms": ("provider/a",),
                    },
                ),
                debug={"frozen_policy": True},
            ),
            RouteTimings(
                sequence_ms=1.25,
                embed_ms=604.5,
                hmm_ms=7.5,
                classifier_ms=2.5,
                total_ms=616.0,
                turns=12,
                embed_requested=12,
                embed_cached=10,
                embed_fetched=2,
            ),
        )


def test_route_logs_one_stage_timing_line(caplog) -> None:
    """The whole point of the change: the per-stage split reaches the logs.

    Asserting the individual stage keys, not just that something was logged —
    a line that collapsed back to a single total would be the regression.
    """
    policy = TimedPolicy()
    with caplog.at_level(logging.INFO, logger="hmm_sidecar.api"):
        with TestClient(app) as client:
            app.state.policy = policy
            response = client.post(
                "/route", json={"schema_version": "policy_router_v3"}
            )

    assert response.status_code == 200
    assert policy.calls == 1

    lines = [
        r.getMessage() for r in caplog.records if "hmm route timing" in r.getMessage()
    ]
    assert len(lines) == 1, lines
    line = lines[0]
    for field in (
        "total_ms=616.0",
        "sequence_ms=1.2",
        "embed_ms=604.5",
        "hmm_ms=7.5",
        "classifier_ms=2.5",
        "turns=12",
        "embed_requested=12",
        "embed_cached=10",
        "embed_fetched=2",
    ):
        assert field in line, f"{field!r} missing from {line!r}"


def test_route_response_names_no_arm() -> None:
    """v3 is classifier-only: a router reading a served arm out of the body is
    reading a null, not a stale pick."""
    with TestClient(app) as client:
        app.state.policy = TimedPolicy()
        response = client.post("/route", json={"schema_version": "policy_router_v3"})

    payload = response.json()
    assert payload["schema_version"] == "policy_router_v3"
    assert payload["selected_roster_id"] is None
    assert payload["selected_provider"] is None
    assert payload["model"] is None
    assert payload["ranked_fallback"][0]["eligible_arms"] == ["provider/a"]


def test_route_rejects_the_superseded_selection_schema() -> None:
    with TestClient(app) as client:
        app.state.policy = TimedPolicy()
        response = client.post("/route", json={"schema_version": "policy_router_v1"})

    assert response.status_code == 400
    assert response.json() == {"error": "unsupported policy schema"}


def test_route_timing_log_carries_no_request_content() -> None:
    """Durations and counts only — the line must stay safe to ship to logs."""
    timings = RouteTimings(
        sequence_ms=1.0,
        embed_ms=2.0,
        hmm_ms=3.0,
        classifier_ms=4.0,
        total_ms=10.0,
        turns=2,
        embed_requested=2,
        embed_cached=1,
        embed_fetched=1,
    )

    rendered = timings.log_fields()

    assert set(rendered.split()) == {
        "total_ms=10.0",
        "sequence_ms=1.0",
        "embed_ms=2.0",
        "hmm_ms=3.0",
        "classifier_ms=4.0",
        "turns=2",
        "embed_requested=2",
        "embed_cached=1",
        "embed_fetched=1",
    }


def test_route_failures_do_not_log_a_timing_line(caplog) -> None:
    """A rejected route has no meaningful split; logging one would poison the
    percentiles computed off these lines."""
    with caplog.at_level(logging.INFO, logger="hmm_sidecar.api"):
        with TestClient(app) as client:
            app.state.policy = RejectingPolicy()
            response = client.post(
                "/route", json={"schema_version": "policy_router_v3"}
            )

    assert response.status_code == 422
    assert not [r for r in caplog.records if "hmm route timing" in r.getMessage()]


def test_package_logger_is_raised_so_info_records_are_not_dropped() -> None:
    """Regression: the timing line was emitted but never reached stdout.

    uvicorn configures only its own loggers and the root logger has no handler,
    so hmm_sidecar INFO records fell through to logging.lastResort (WARNING+)
    and vanished. A route timing that nothing can read is not instrumentation.
    """
    package_log = logging.getLogger("hmm_sidecar")
    original_level = package_log.level
    try:
        package_log.setLevel(logging.NOTSET)
        configure_logging()
        assert package_log.isEnabledFor(logging.INFO)
    finally:
        package_log.setLevel(original_level)


def test_configure_logging_does_not_duplicate_an_existing_setup() -> None:
    """When a host already owns the root logger, records must propagate into it
    rather than getting a second handler here (which would double every line)."""
    package_log = logging.getLogger("hmm_sidecar")
    root = logging.getLogger()
    original_level = package_log.level
    original_handlers = list(package_log.handlers)
    probe = logging.StreamHandler()
    package_log.handlers = []
    root.addHandler(probe)
    try:
        configure_logging()
        assert package_log.handlers == []
        assert package_log.propagate is True
    finally:
        root.removeHandler(probe)
        package_log.handlers = original_handlers
        package_log.setLevel(original_level)
