from __future__ import annotations

import asyncio
import hashlib
import logging
from pathlib import Path
from types import SimpleNamespace

import numpy as np
import pytest
from fastapi.testclient import TestClient

import hmm_sidecar.api as api_module
from hmm_sidecar.api import app, configure_logging
from hmm_sidecar.artifacts import ArtifactRegistry, FrozenArtifacts
from hmm_sidecar.schemas import EmbeddingContract
from hmm_sidecar.policy import RouteTimings
from hmm_sidecar.schemas import ClassificationResult


def _classification(sha: str = "a" * 64) -> ClassificationResult:
    return ClassificationResult(
        schema_version="policy_router_v4",
        route_id="route",
        classifier_artifact_id="artifact",
        classifier_artifact_sha256=sha,
        predicted_label="fast",
        class_order=("fast", "maximum"),
        class_probabilities={"fast": 0.6, "maximum": 0.4},
        classifier_confidence=0.6,
        classifier_margin=0.2,
        hmm_state_id=1,
        hmm_state_path=(0, 1),
        hmm_state_probabilities=(0.3, 0.7),
    )


def _timings() -> RouteTimings:
    return RouteTimings(
        sequence_ms=1.0,
        embed_ms=1.0,
        hmm_ms=1.0,
        classifier_ms=1.0,
        total_ms=4.0,
        turns=1,
        embed_requested=1,
        embed_cached=1,
        embed_fetched=0,
    )


class RejectingPolicy:
    async def classify_with_timings(self, payload: dict[str, object]) -> None:
        del payload
        raise ValueError("private artifact path: /secret/model.npz")


class ClassifyingPolicy:
    def __init__(self, sha: str = "a" * 64) -> None:
        self.sha = sha
        self.classes = ("fast", "maximum")
        self.classifier = SimpleNamespace(classes=self.classes)
        self.payloads: list[dict[str, object]] = []

    async def classify_with_timings(self, payload: dict[str, object]):
        self.payloads.append(payload)
        return _classification(self.sha), _timings()


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
    assert payload["schema_version"] == "policy_router_v4"
    assert payload["reports_outcomes"] is False
    assert payload["reports_feedback"] is False
    assert payload["supports_shadow"] is True
    assert payload["supported_schema_versions"] == ["policy_router_v4"]
    assert "reports_ranked_fallback" not in payload
    assert payload["learning"]["state"] == "frozen_policy"


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


def test_readiness_reports_the_classifier_identity_the_router_pins(monkeypatch) -> None:
    monkeypatch.setenv("CLASSIFIER_IMAGE_DIGEST", "sha256:" + "c" * 64)
    with TestClient(app) as client:
        app.state.policy = ClassifyingPolicy()
        app.state.artifacts = SimpleNamespace(
            model_id="artifact", package_sha256="a" * 64
        )
        response = client.get("/readyz")

    assert response.status_code == 200
    payload = response.json()
    assert payload["schema_version"] == "policy_router_v4"
    assert payload["classifier_artifact_id"] == "artifact"
    assert payload["classifier_artifact_sha256"] == "a" * 64
    assert payload["classifier_image_digest"] == "sha256:" + "c" * 64
    assert payload["classifier_class_order"] == ["fast", "maximum"]
    # sha256 of Go's json.Marshal([]string{"fast","maximum"}).
    assert (
        payload["classifier_taxonomy_sha256"]
        == hashlib.sha256(b'["fast","maximum"]').hexdigest()
    )


def test_readiness_fails_closed_without_an_image_digest(monkeypatch) -> None:
    monkeypatch.delenv("CLASSIFIER_IMAGE_DIGEST", raising=False)
    with TestClient(app) as client:
        app.state.policy = ClassifyingPolicy()
        app.state.artifacts = SimpleNamespace(
            model_id="artifact", package_sha256="a" * 64
        )
        response = client.get("/readyz")

    assert response.status_code == 503
    assert response.json()["classifier_image_digest"] is None
    assert "CLASSIFIER_IMAGE_DIGEST" in response.json()["error"]


def test_route_rejections_do_not_expose_internal_exception_text() -> None:
    with TestClient(app) as client:
        app.state.policy = RejectingPolicy()
        response = client.post("/route", json={"schema_version": "policy_router_v4"})

    assert response.status_code == 422
    assert response.json() == {"error": "route request rejected"}
    assert "/secret/model.npz" not in response.text


def test_preview_requires_explicit_mode_and_returns_classification_facts() -> None:
    with TestClient(app) as client:
        app.state.policy = ClassifyingPolicy()
        rejected = client.post("/preview", json={"schema_version": "policy_router_v4"})
        response = client.post(
            "/preview",
            json={
                "schema_version": "policy_router_v4",
                "execution_mode": "preview",
            },
        )

    assert rejected.status_code == 400
    assert response.status_code == 200
    assert response.json()["hmm_state_probabilities"] == [0.3, 0.7]


class TimedPolicy:
    """Returns a fixed classification plus fixed timings, so the log line is assertable."""

    def __init__(self) -> None:
        self.calls = 0

    async def classify_with_timings(self, payload: dict[str, object]):
        del payload
        self.calls += 1
        return (
            _classification(),
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
                "/route", json={"schema_version": "policy_router_v4"}
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


def test_route_response_is_classification_facts_only() -> None:
    """The Go router decodes v4 bodies with unknown fields disallowed."""
    with TestClient(app) as client:
        app.state.policy = TimedPolicy()
        response = client.post("/route", json={"schema_version": "policy_router_v4"})

    payload = response.json()
    assert set(payload) == {
        "schema_version",
        "route_id",
        "classifier_artifact_id",
        "classifier_artifact_sha256",
        "predicted_label",
        "class_order",
        "class_probabilities",
        "classifier_confidence",
        "classifier_margin",
        "hmm_state_id",
        "hmm_state_path",
        "hmm_state_probabilities",
    }
    assert payload["schema_version"] == "policy_router_v4"


@pytest.mark.parametrize("field", ["candidates", "preferred_models", "quality_bias"])
def test_route_rejects_selection_authority_fields(field: str) -> None:
    policy = ClassifyingPolicy()
    with TestClient(app) as client:
        app.state.policy = policy
        response = client.post(
            "/route", json={"schema_version": "policy_router_v4", field: []}
        )

    assert response.status_code == 400
    assert response.json()["fields"] == [field]
    assert policy.payloads == []


def test_route_rejects_the_superseded_selection_schema() -> None:
    with TestClient(app) as client:
        app.state.policy = TimedPolicy()
        response = client.post("/route", json={"schema_version": "policy_router_v3"})

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
                "/route", json={"schema_version": "policy_router_v4"}
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


def test_route_serves_the_requested_registry_artifact() -> None:
    default = ClassifyingPolicy("d" * 64)
    pinned = ClassifyingPolicy("e" * 64)
    with TestClient(app) as client:
        app.state.policy = default
        app.state.policies = {default.sha: default, pinned.sha: pinned}
        unpinned = client.post("/route", json={"schema_version": "policy_router_v4"})
        selected = client.post(
            "/route",
            json={"schema_version": "policy_router_v4", "artifact_sha256": pinned.sha},
        )

    assert unpinned.status_code == 200
    assert unpinned.json()["classifier_artifact_sha256"] == default.sha
    assert selected.status_code == 200
    assert selected.json()["classifier_artifact_sha256"] == pinned.sha


def test_unknown_artifact_sha256_is_a_404_never_the_default() -> None:
    default = ClassifyingPolicy("d" * 64)
    with TestClient(app) as client:
        app.state.policy = default
        app.state.policies = {default.sha: default}
        route = client.post(
            "/route",
            json={"schema_version": "policy_router_v4", "artifact_sha256": "f" * 64},
        )
        preview = client.post(
            "/preview",
            json={
                "schema_version": "policy_router_v4",
                "execution_mode": "preview",
                "artifact_sha256": "f" * 64,
            },
        )
        bad_type = client.post(
            "/route", json={"schema_version": "policy_router_v4", "artifact_sha256": 7}
        )

    assert route.status_code == 404
    assert route.json()["artifact_sha256"] == "f" * 64
    assert preview.status_code == 404
    assert bad_type.status_code == 400


def _artifacts(
    sha: str, contract: EmbeddingContract, probe: np.ndarray
) -> FrozenArtifacts:
    return FrozenArtifacts(
        root=Path("/nonexistent") / sha,
        manifest=SimpleNamespace(embedding_contract=contract),  # type: ignore[arg-type]
        package_sha256=sha,
        probe_vector=probe,
    )


def _contract(**overrides: object) -> EmbeddingContract:
    fields: dict[str, object] = {
        "model": "gemini-embedding-001",
        "dimensions": 3,
        "task_type": None,
        "probe_text": "probe",
        "probe_vector_file": "probe.npy",
        "minimum_cosine_similarity": 0.99,
    }
    fields.update(overrides)
    return EmbeddingContract(**fields)  # type: ignore[arg-type]


def test_boot_probes_once_per_distinct_embedding_contract(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    shared = _contract()
    other = _contract(model="text-embedding-3-large", dimensions=3)
    probe = np.array([1.0, 0.0, 0.0])
    default = _artifacts("d" * 64, shared, probe)
    registry = ArtifactRegistry(
        default=default,
        by_sha256={
            default.package_sha256: default,
            "e" * 64: _artifacts("e" * 64, shared, probe.copy()),
            "f" * 64: _artifacts("f" * 64, other, probe),
        },
    )
    built: list[EmbeddingContract] = []
    probed: list[EmbeddingContract] = []

    def fake_build(contract: EmbeddingContract) -> object:
        built.append(contract)
        return object()

    async def fake_verify(
        embedder: object, contract: EmbeddingContract, reference: np.ndarray
    ) -> float:
        del embedder, reference
        probed.append(contract)
        return 0.5 if contract is shared else 0.25

    class FakePolicy:
        def __init__(self, artifacts: FrozenArtifacts, embedder: object) -> None:
            self.artifacts = artifacts
            self.embedder = embedder

    monkeypatch.setattr(api_module, "build_embedder", fake_build)
    monkeypatch.setattr(api_module, "verify_embedding_contract", fake_verify)
    monkeypatch.setattr(api_module, "FrozenPolicy", FakePolicy)

    policies, similarity = asyncio.run(api_module.load_policies(registry))

    assert set(policies) == set(
        registry.by_sha256
    ), "every registry entry still gets a policy"
    assert (
        len(probed) == 2
    ), "one probe per distinct embedding contract, not per package"
    assert probed == built == [shared, other]
    assert policies["d" * 64].embedder is policies["e" * 64].embedder
    assert policies["f" * 64].embedder is not policies["d" * 64].embedder
    assert similarity == 0.5


def _package_artifacts(sha: str) -> FrozenArtifacts:
    return FrozenArtifacts(
        Path("/nonexistent") / sha, None, sha, None, package_manifest={}
    )


def test_boot_fails_when_a_package_cannot_embed(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    default = _package_artifacts("a" * 64)
    registry = ArtifactRegistry(
        default=default, by_sha256={default.package_sha256: default}
    )
    probed: list[str] = []

    class FakePackagePolicy:
        def __init__(self, artifacts: FrozenArtifacts) -> None:
            self.artifacts = artifacts

        async def verify_embedding_config(self) -> None:
            probed.append(self.artifacts.package_sha256)
            raise api_module.EmbeddingError("embedding probe failed")

    monkeypatch.setattr(api_module, "PackagePolicy", FakePackagePolicy)

    with pytest.raises(api_module.EmbeddingError, match="embedding probe failed"):
        asyncio.run(api_module.load_policies(registry))
    assert probed == [default.package_sha256]


def test_readiness_fails_closed_when_the_embedding_probe_fails(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    default = _package_artifacts("b" * 64)
    registry = ArtifactRegistry(
        default=default, by_sha256={default.package_sha256: default}
    )

    class FakePackagePolicy:
        def __init__(self, artifacts: FrozenArtifacts) -> None:
            self.artifacts = artifacts

        async def verify_embedding_config(self) -> None:
            raise api_module.EmbeddingError(
                "Vertex embeddings need HMM_VERTEX_PROJECT or GOOGLE_CLOUD_PROJECT"
            )

    monkeypatch.setattr(api_module, "resolve_artifact_registry", lambda: registry)
    monkeypatch.setattr(api_module, "PackagePolicy", FakePackagePolicy)

    with TestClient(app) as client:
        ready = client.get("/readyz")

    assert ready.status_code == 503
    assert ready.json()["ready"] is False
    assert "HMM_VERTEX_PROJECT" in ready.json()["error"]
