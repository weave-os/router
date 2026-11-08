from __future__ import annotations

import asyncio
import hashlib
import time

from fastapi import FastAPI
from fastapi.testclient import TestClient

from hmm_sidecar.classifier_api import (
    ClassifierAPIVersion,
    ClassifierFactsV5,
    ClassifierRequestV5,
    ClassifierVariantID,
    ClassifierWarmingError,
    MappingClassifierReleaseResolver,
    ResolvedClassifierRelease,
    ResolvedClassifierSelection,
    ResolvedClassifierVariant,
    create_classifier_router,
)

RELEASE_ID = "a" * 64
TAXONOMY = ("fast", "maximum")
TAXONOMY_SHA256 = hashlib.sha256(b'["fast","maximum"]').hexdigest()


class FakeClassifierRuntime:
    def __init__(self, facts: ClassifierFactsV5 | None = None) -> None:
        self.facts = facts or ClassifierFactsV5(
            predicted_label="fast",
            class_order=TAXONOMY,
            class_probabilities={"fast": 0.7, "maximum": 0.3},
            classifier_confidence=0.7,
            classifier_margin=0.4,
            hmm_state_id=0,
            hmm_state_path=(0, 0),
            hmm_state_probabilities=(0.8, 0.2),
        )
        self.requests: list[tuple[ClassifierRequestV5, str | None]] = []

    async def classify(
        self, request: ClassifierRequestV5, *, session_id: str | None
    ) -> ClassifierFactsV5:
        self.requests.append((request, session_id))
        return self.facts


def _runtime_app(
    runtime: FakeClassifierRuntime | None = None,
) -> tuple[FastAPI, FakeClassifierRuntime]:
    classifier_runtime = runtime or FakeClassifierRuntime()
    release = ResolvedClassifierRelease(
        release_id=RELEASE_ID,
        api_major=5,
        wire_schema=ClassifierAPIVersion.V5,
        class_order=TAXONOMY,
        taxonomy_sha256=TAXONOMY_SHA256,
        variants={
            ClassifierVariantID.PRIMARY: ResolvedClassifierVariant(
                variant_id=ClassifierVariantID.PRIMARY,
                artifact_id="hmm-primary",
                package_sha256="b" * 64,
                runtime=classifier_runtime,
            )
        },
    )
    resolver = MappingClassifierReleaseResolver({RELEASE_ID: release})
    app = FastAPI()
    app.include_router(create_classifier_router(resolver))
    return app, classifier_runtime


def _request() -> dict[str, object]:
    return {
        "schema_version": "policy_router_v5",
        "classifier_selection_id": RELEASE_ID,
        "classifier_release_id": RELEASE_ID,
        "classifier_variant_id": "primary",
        "request_id": "request-1",
        "session_id": "client-session-opaque-1",
        "harness": "codex",
        "latest_user_text": "Fix the parser",
        "turn_index": 0,
        "conversation_messages": [{"role": "user", "text": "Fix the parser"}],
        "estimated_input_tokens": 14,
    }


def test_capabilities_require_exact_classifier_release() -> None:
    app, _ = _runtime_app()
    with TestClient(app) as client:
        missing = client.get("/v5/capabilities")
        unknown = client.get(
            "/v5/capabilities",
            params={
                "classifier_selection_id": RELEASE_ID,
                "classifier_release_id": "c" * 64,
            },
        )
        ready = client.get(
            "/v5/capabilities",
            params={
                "classifier_selection_id": RELEASE_ID,
                "classifier_release_id": RELEASE_ID,
            },
        )

    assert missing.status_code == 422
    assert unknown.status_code == 404
    assert ready.status_code == 200
    assert ready.json() == {
        "schema_version": "policy_router_v5",
        "api_major": 5,
        "classifier_selection_id": RELEASE_ID,
        "classifier_release_id": RELEASE_ID,
        "wire_schema": "policy_router_v5",
        "class_order": list(TAXONOMY),
        "classifier_taxonomy_sha256": TAXONOMY_SHA256,
        "classifier_variants": ["primary"],
        "supports_preview": True,
    }


def test_classify_echoes_pinned_release_and_classifier_facts_only() -> None:
    app, runtime = _runtime_app()
    with TestClient(app) as client:
        response = client.post("/v5/classify", json=_request())

    assert response.status_code == 200
    assert response.json() == {
        "schema_version": "policy_router_v5",
        "request_id": "request-1",
        "classifier_selection_id": RELEASE_ID,
        "classifier_release_id": RELEASE_ID,
        "classifier_variant_id": "primary",
        "classifier_artifact_id": "hmm-primary",
        "classifier_artifact_sha256": "b" * 64,
        "classifier_taxonomy_sha256": TAXONOMY_SHA256,
        "predicted_label": "fast",
        "class_order": list(TAXONOMY),
        "class_probabilities": {"fast": 0.7, "maximum": 0.3},
        "classifier_confidence": 0.7,
        "classifier_margin": 0.4,
        "hmm_state_id": 0,
        "hmm_state_path": [0, 0],
        "hmm_state_probabilities": [0.8, 0.2],
    }
    assert runtime.requests[0][0].classifier_release_id == RELEASE_ID
    assert runtime.requests[0][1] == "client-session-opaque-1"
    assert "selected_provider" not in response.json()
    assert "model" not in response.json()
    assert "timings" not in response.json()


def test_preview_uses_same_classifier_facts_contract() -> None:
    app, runtime = _runtime_app()
    with TestClient(app) as client:
        response = client.post("/v5/preview", json=_request())

    assert response.status_code == 200
    assert response.json()["classifier_release_id"] == RELEASE_ID
    assert len(runtime.requests) == 1
    assert runtime.requests[0][1] is None, "previews never touch serving session state"


def test_unknown_release_or_variant_never_uses_primary_default() -> None:
    app, runtime = _runtime_app()
    unknown_release = _request()
    unknown_release["classifier_release_id"] = "c" * 64
    unknown_variant = _request()
    unknown_variant["classifier_variant_id"] = "qwen"
    with TestClient(app) as client:
        missing_release = client.post("/v5/classify", json=unknown_release)
        missing_variant = client.post("/v5/classify", json=unknown_variant)

    assert missing_release.status_code == 404
    assert missing_variant.status_code == 404
    assert runtime.requests == []


def test_unknown_selection_never_uses_another_target_binding() -> None:
    app, runtime = _runtime_app()
    payload = {**_request(), "classifier_selection_id": "c" * 64}
    with TestClient(app) as client:
        response = client.post("/v5/classify", json=payload)

    assert response.status_code == 404
    assert runtime.requests == []


def test_selection_allowlist_can_differ_for_internal_and_stable() -> None:
    primary_runtime = FakeClassifierRuntime()
    qwen_runtime = FakeClassifierRuntime()
    release = ResolvedClassifierRelease(
        release_id=RELEASE_ID,
        api_major=5,
        wire_schema=ClassifierAPIVersion.V5,
        class_order=TAXONOMY,
        taxonomy_sha256=TAXONOMY_SHA256,
        variants={
            ClassifierVariantID.PRIMARY: ResolvedClassifierVariant(
                variant_id=ClassifierVariantID.PRIMARY,
                artifact_id="hmm-primary",
                package_sha256="b" * 64,
                runtime=primary_runtime,
            ),
            ClassifierVariantID.QWEN: ResolvedClassifierVariant(
                variant_id=ClassifierVariantID.QWEN,
                artifact_id="qwen-alternative",
                package_sha256="c" * 64,
                runtime=qwen_runtime,
            ),
        },
    )
    stable_selection_id = "d" * 64
    internal_selection_id = "e" * 64
    resolver = MappingClassifierReleaseResolver(
        {RELEASE_ID: release},
        {
            stable_selection_id: ResolvedClassifierSelection(
                selection_id=stable_selection_id,
                release_id=RELEASE_ID,
                enabled_variants=frozenset({ClassifierVariantID.PRIMARY}),
            ),
            internal_selection_id: ResolvedClassifierSelection(
                selection_id=internal_selection_id,
                release_id=RELEASE_ID,
                enabled_variants=frozenset(
                    {ClassifierVariantID.PRIMARY, ClassifierVariantID.QWEN}
                ),
            ),
        },
    )
    app = FastAPI()
    app.include_router(create_classifier_router(resolver))
    stable_request = {
        **_request(),
        "classifier_selection_id": stable_selection_id,
        "classifier_variant_id": "qwen",
    }
    internal_request = {
        **_request(),
        "classifier_selection_id": internal_selection_id,
        "classifier_variant_id": "qwen",
    }

    with TestClient(app) as client:
        stable_caps = client.get(
            "/v5/capabilities",
            params={
                "classifier_selection_id": stable_selection_id,
                "classifier_release_id": RELEASE_ID,
            },
        )
        internal_caps = client.get(
            "/v5/capabilities",
            params={
                "classifier_selection_id": internal_selection_id,
                "classifier_release_id": RELEASE_ID,
            },
        )
        stable = client.post("/v5/classify", json=stable_request)
        assert qwen_runtime.requests == []
        internal = client.post("/v5/classify", json=internal_request)

    assert stable_caps.status_code == 200
    assert stable_caps.json()["classifier_variants"] == ["primary"]
    assert internal_caps.status_code == 200
    assert internal_caps.json()["classifier_variants"] == ["primary", "qwen"]
    assert stable.status_code == 403
    assert internal.status_code == 200
    assert internal.json()["classifier_variant_id"] == "qwen"
    assert len(qwen_runtime.requests) == 1


def test_wrong_api_major_or_unknown_selection_fields_fail_before_inference() -> None:
    app, runtime = _runtime_app()
    wrong_schema = _request()
    wrong_schema["schema_version"] = "policy_router_v4"
    with_selection_authority = _request()
    with_selection_authority["candidate_models"] = ["model-a"]
    with TestClient(app) as client:
        unsupported = client.post("/v5/classify", json=wrong_schema)
        forbidden = client.post("/v5/classify", json=with_selection_authority)

    assert unsupported.status_code == 422
    assert forbidden.status_code == 422
    assert runtime.requests == []


def _single_variant_release(
    release_id: str, runtime: FakeClassifierRuntime, **overrides: object
) -> ResolvedClassifierRelease:
    fields: dict[str, object] = {
        "release_id": release_id,
        "api_major": 5,
        "wire_schema": ClassifierAPIVersion.V5,
        "class_order": TAXONOMY,
        "taxonomy_sha256": TAXONOMY_SHA256,
        "variants": {
            ClassifierVariantID.PRIMARY: ResolvedClassifierVariant(
                variant_id=ClassifierVariantID.PRIMARY,
                artifact_id="hmm-primary",
                package_sha256="b" * 64,
                runtime=runtime,
            )
        },
    }
    return ResolvedClassifierRelease(**{**fields, **overrides})


def test_resolved_release_with_another_api_major_fails_before_inference() -> None:
    runtime = FakeClassifierRuntime()
    resolver = MappingClassifierReleaseResolver(
        {RELEASE_ID: _single_variant_release(RELEASE_ID, runtime, api_major=4)}
    )
    app = FastAPI()
    app.include_router(create_classifier_router(resolver))
    with TestClient(app) as client:
        response = client.post("/v5/classify", json=_request())

    assert response.status_code == 406
    assert runtime.requests == []


def test_selection_pinned_to_another_release_fails_before_inference() -> None:
    requested_runtime = FakeClassifierRuntime()
    other_runtime = FakeClassifierRuntime()
    other_release_id = "f" * 64
    resolver = MappingClassifierReleaseResolver(
        {
            RELEASE_ID: _single_variant_release(RELEASE_ID, requested_runtime),
            other_release_id: _single_variant_release(other_release_id, other_runtime),
        },
        {
            RELEASE_ID: ResolvedClassifierSelection(
                selection_id=RELEASE_ID,
                release_id=other_release_id,
                enabled_variants=frozenset({ClassifierVariantID.PRIMARY}),
            )
        },
    )
    app = FastAPI()
    app.include_router(create_classifier_router(resolver))
    with TestClient(app) as client:
        response = client.post("/v5/classify", json=_request())

    assert response.status_code == 409
    assert requested_runtime.requests == []
    assert other_runtime.requests == []


def test_session_identity_is_runtime_only_and_never_echoed(caplog) -> None:
    app, runtime = _runtime_app()
    payload = _request()
    payload["session_id"] = "client-session-private"
    with TestClient(app) as client:
        response = client.post("/v5/classify", json=payload)

    assert response.status_code == 200
    assert runtime.requests[0][1] == "client-session-private"
    assert "client-session-private" not in response.text
    assert all(
        "client-session-private" not in record.getMessage() for record in caplog.records
    )


def test_session_identity_is_preserved_across_sequential_turns() -> None:
    app, runtime = _runtime_app()
    first_turn = _request()
    second_turn = _request()
    second_turn["request_id"] = "request-2"
    second_turn["latest_user_text"] = "Now handle the edge case"
    different_conversation = _request()
    different_conversation["request_id"] = "request-3"
    different_conversation["session_id"] = "client-session-opaque-2"

    with TestClient(app) as client:
        first = client.post("/v5/classify", json=first_turn)
        second = client.post("/v5/classify", json=second_turn)
        other = client.post("/v5/classify", json=different_conversation)

    assert first.status_code == 200
    assert second.status_code == 200
    assert other.status_code == 200
    assert [session_id for _, session_id in runtime.requests] == [
        "client-session-opaque-1",
        "client-session-opaque-1",
        "client-session-opaque-2",
    ]


def test_classifier_taxonomy_mismatch_fails_closed() -> None:
    facts = ClassifierFactsV5(
        predicted_label="other",
        class_order=("other",),
        class_probabilities={"other": 1.0},
        classifier_confidence=1.0,
        classifier_margin=1.0,
        hmm_state_id=0,
        hmm_state_path=(0,),
        hmm_state_probabilities=(1.0,),
    )
    app, _ = _runtime_app(FakeClassifierRuntime(facts))
    with TestClient(app) as client:
        response = client.post("/v5/classify", json=_request())

    assert response.status_code == 409
    assert response.json()["detail"] == "classifier taxonomy does not match release"


def test_classifier_without_hmm_state_does_not_receive_synthetic_state_facts() -> None:
    facts = ClassifierFactsV5(
        predicted_label="fast",
        class_order=TAXONOMY,
        class_probabilities={"fast": 0.7, "maximum": 0.3},
        classifier_confidence=0.7,
        classifier_margin=0.4,
    )
    app, _ = _runtime_app(FakeClassifierRuntime(facts))
    with TestClient(app) as client:
        response = client.post("/v5/classify", json=_request())

    assert response.status_code == 200
    assert response.json()["hmm_state_id"] is None
    assert response.json()["hmm_state_path"] is None
    assert response.json()["hmm_state_probabilities"] is None


class WarmingClassifierRuntime(FakeClassifierRuntime):
    async def classify(
        self, request: ClassifierRequestV5, *, session_id: str | None
    ) -> ClassifierFactsV5:
        del request, session_id
        raise ClassifierWarmingError


def test_classifier_warming_error_preserves_retry_signal_without_diagnostics() -> None:
    app, _ = _runtime_app(WarmingClassifierRuntime())
    with TestClient(app) as client:
        response = client.post("/v5/classify", json=_request())

    assert response.status_code == 503
    assert response.headers["retry-after"] == "2"
    assert response.json() == {"error": "classifier_warming"}


class SlowClassifierRuntime(FakeClassifierRuntime):
    async def classify(
        self, request: ClassifierRequestV5, *, session_id: str | None
    ) -> ClassifierFactsV5:
        await asyncio.sleep(5)
        return await super().classify(request, session_id=session_id)


def test_router_classify_budget_bounds_inference() -> None:
    app, _ = _runtime_app(SlowClassifierRuntime())
    with TestClient(app) as client:
        started = time.monotonic()
        response = client.post(
            "/v5/classify",
            json=_request(),
            headers={"X-Weave-Classifier-Timeout-Ms": "50"},
        )
        elapsed = time.monotonic() - started

    assert response.status_code == 503
    assert response.json() == {"error": "classifier_unavailable"}
    assert elapsed < 2


def test_malformed_router_classify_budget_fails_before_inference() -> None:
    app, runtime = _runtime_app()
    with TestClient(app) as client:
        for budget in ("0", "-5", "soon", "1e3", "600001"):
            response = client.post(
                "/v5/preview",
                json=_request(),
                headers={"X-Weave-Classifier-Timeout-Ms": budget},
            )
            assert response.status_code == 422, budget
    assert runtime.requests == []
