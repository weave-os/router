from __future__ import annotations

import hashlib
import json
from pathlib import Path
from types import SimpleNamespace

from fastapi import FastAPI
from fastapi.testclient import TestClient

from hmm_sidecar.classifier_api import create_classifier_router
from hmm_sidecar.classifier_release import ClassifierVariantID
from hmm_sidecar.local_classifier_resolver import (
    LocalClassifierReleaseResolver,
    resolve_local_classifier_resolver,
)


def test_existing_self_hosted_app_mounts_versioned_classifier_routes() -> None:
    from hmm_sidecar.api import app

    paths = {route.path for route in app.routes}
    assert {"/v5/classify", "/v5/preview", "/v5/capabilities"} <= paths


class FakePackageRuntime:
    classes = ("fast", "maximum")

    async def classify(self, payload, *, session_id=None):
        return (
            SimpleNamespace(
                label="fast",
                probabilities={"fast": 0.7, "maximum": 0.3},
                confidence=0.7,
                margin=0.4,
                state_id=0,
                state_path=(0,),
                state_probabilities=(1.0,),
            ),
            None,
        )


def _release_payload(package_sha256: str) -> dict[str, object]:
    attestation = {
        "uri": "file:///tmp/build-attestation.json",
        "sha256": "c" * 64,
        "generation": 1,
    }
    return {
        "schema_version": "router_classifier_release_v1",
        "api_major": 5,
        "wire_schema": "policy_router_v5",
        "taxonomy": {
            "class_order": ["fast", "maximum"],
            "sha256": hashlib.sha256(b'["fast","maximum"]').hexdigest(),
        },
        "variants": {
            "primary": {
                "kind": "hmm_package",
                "artifact_id": "hmm-primary",
                "artifact_sha256": package_sha256,
                "package_weavedata_ref": {
                    "data_id": "local-dataset",
                    "data_kind": "model_artifact",
                    "schema_version": "hmm_router_classifier_package_v1",
                    "version": "v1",
                    "storage_uri": "file:///tmp/package.tar.gz",
                    "content_sha256": package_sha256,
                    "row_count": 1,
                    "role": "trained_policy_artifact",
                    "required": True,
                },
                "auxiliary_models": {},
                "semantic_configuration_sha256": "d" * 64,
                "embedding_contract_sha256": "e" * 64,
            }
        },
        "runtime_image_digest": "sha256:" + "f" * 64,
        "provenance": {
            "router_revision": "1" * 40,
            "weave_revision": "2" * 40,
            "build_attestation": attestation,
        },
    }


def test_local_files_mount_v5_without_managed_gcp_resolver(
    tmp_path: Path, monkeypatch
) -> None:
    for name in (
        "GOOGLE_APPLICATION_CREDENTIALS",
        "GOOGLE_CLOUD_PROJECT",
        "HMM_VERTEX_PROJECT",
    ):
        monkeypatch.delenv(name, raising=False)
    package_sha256 = "b" * 64
    release_path = tmp_path / "release.json"
    release_path.write_text(
        json.dumps(_release_payload(package_sha256), separators=(",", ":"))
    )
    release_id = hashlib.sha256(release_path.read_bytes()).hexdigest()
    selection_path = tmp_path / "selection.json"
    selection_path.write_text(
        json.dumps(
            {
                "schema_version": "router_classifier_selection_v1",
                "classifier_release_id": release_id,
                "enabled_variants": ["primary"],
            },
            separators=(",", ":"),
        )
    )
    package_artifact = SimpleNamespace(
        package_manifest={"local": True}, model_id="hmm-primary"
    )
    artifacts = SimpleNamespace(
        by_sha256={package_sha256: package_artifact},
        get=lambda digest: package_artifact if digest == package_sha256 else None,
    )
    policies = {package_sha256: SimpleNamespace(runtime=FakePackageRuntime())}
    monkeypatch.setattr(
        "hmm_sidecar.local_classifier_resolver.embedded_semantic_configuration_sha256",
        lambda _: "d" * 64,
    )
    monkeypatch.setattr(
        "hmm_sidecar.local_classifier_resolver.embedded_embedding_contract_sha256",
        lambda _: "e" * 64,
    )
    monkeypatch.setenv("HMM_CLASSIFIER_RELEASE_MANIFEST_PATH", str(release_path))
    monkeypatch.setenv("HMM_CLASSIFIER_SELECTION_PATH", str(selection_path))
    resolver = resolve_local_classifier_resolver(artifacts, policies)
    assert resolver is not None
    assert resolver.release_id == release_id
    app = FastAPI()
    app.include_router(create_classifier_router(resolver))

    with TestClient(app) as client:
        capabilities = client.get(
            "/v5/capabilities",
            params={
                "classifier_selection_id": resolver.selection_id,
                "classifier_release_id": resolver.release_id,
            },
        )
        assert capabilities.status_code == 200
        assert capabilities.json()["classifier_release_id"] == resolver.release_id
        classified = client.post(
            "/v5/classify",
            json={
                "schema_version": "policy_router_v5",
                "classifier_selection_id": resolver.selection_id,
                "classifier_release_id": resolver.release_id,
                "classifier_variant_id": ClassifierVariantID.PRIMARY.value,
                "request_id": "request-local",
                "session_id": "opaque-session",
                "harness": "codex",
                "latest_user_text": "fix this",
            },
        )
        assert classified.status_code == 200
        assert classified.json()["predicted_label"] == "fast"
        assert classified.json()["classifier_selection_id"] == resolver.selection_id


def test_local_selection_fails_closed_when_it_pins_other_release(
    tmp_path: Path,
) -> None:
    package_sha256 = "b" * 64
    release_path = tmp_path / "release.json"
    release_path.write_text(json.dumps(_release_payload(package_sha256)))
    selection_path = tmp_path / "selection.json"
    selection_path.write_text(
        json.dumps(
            {
                "schema_version": "router_classifier_selection_v1",
                "classifier_release_id": "0" * 64,
                "enabled_variants": ["primary"],
            }
        )
    )
    package_artifact = SimpleNamespace(
        package_manifest={"local": True}, model_id="hmm-primary"
    )
    artifacts = SimpleNamespace(
        by_sha256={package_sha256: package_artifact},
        get=lambda digest: package_artifact if digest == package_sha256 else None,
    )
    policies = {package_sha256: SimpleNamespace(runtime=FakePackageRuntime())}

    import pytest

    with pytest.raises(ValueError, match="does not pin the exact release bytes"):
        LocalClassifierReleaseResolver(
            release_path, selection_path, artifacts, policies
        )
