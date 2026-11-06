from __future__ import annotations

import hashlib

import pytest
from pydantic import ValidationError

from hmm_sidecar.classifier_release import (
    ArtifactReference,
    ClassifierReleaseManifestV1,
    ClassifierVariantKind,
    ClassifierVariantID,
    classifier_release_id,
    classifier_taxonomy_bytes,
)


def _release_payload() -> dict[str, object]:
    attestation = ArtifactReference(
        uri="gs://classifier-releases/build-attestation.json",
        sha256="c" * 64,
        generation=1,
    ).model_dump(mode="json")
    taxonomy_bytes = b'["fast","maximum"]'
    return {
        "schema_version": "router_classifier_release_v1",
        "api_major": 5,
        "wire_schema": "policy_router_v5",
        "taxonomy": {
            "class_order": ["fast", "maximum"],
            "sha256": hashlib.sha256(taxonomy_bytes).hexdigest(),
        },
        "variants": {
            "primary": {
                "kind": "hmm_package",
                "artifact_id": "hmm-primary",
                "artifact_sha256": "b" * 64,
                "package_weavedata_ref": {
                    "data_id": "dataset-123",
                    "data_kind": "model_artifact",
                    "schema_version": "hmm_router_classifier_package_v1",
                    "version": "v1",
                    "storage_uri": "gs://model-artifacts/package.tgz",
                    "content_sha256": "b" * 64,
                    "row_count": 1,
                    "role": "trained_policy_artifact",
                    "required": True,
                },
                "auxiliary_models": {},
                "semantic_configuration_sha256": "d" * 64,
                "embedding_contract_sha256": "e" * 64,
            }
        },
        "runtime_image_digest": "sha256:" + "d" * 64,
        "provenance": {
            "router_revision": "1" * 40,
            "weave_revision": "2" * 40,
            "build_attestation": attestation,
        },
    }


def test_release_manifest_validates_taxonomy_and_requires_primary_variant() -> None:
    manifest = ClassifierReleaseManifestV1.model_validate(_release_payload())
    assert manifest.api_major == 5
    assert ClassifierVariantID.PRIMARY in manifest.variants

    no_primary = _release_payload()
    no_primary["variants"] = {
        "hmm_beta": {**no_primary["variants"]["primary"], "artifact_id": "hmm-beta"}
    }
    with pytest.raises(ValidationError, match="requires a primary variant"):
        ClassifierReleaseManifestV1.model_validate(no_primary)


def test_release_id_is_the_sha256_of_exact_manifest_bytes_without_self_id() -> None:
    release_bytes = b'{"schema_version":"router_classifier_release_v1"}'
    assert (
        classifier_release_id(release_bytes)
        == hashlib.sha256(release_bytes).hexdigest()
    )
    assert "release_id" not in ClassifierReleaseManifestV1.model_fields


def test_release_manifest_rejects_taxonomy_tampering_and_unknown_fields() -> None:
    wrong_taxonomy = _release_payload()
    wrong_taxonomy["taxonomy"]["sha256"] = "a" * 64
    with pytest.raises(ValidationError, match="taxonomy digest"):
        ClassifierReleaseManifestV1.model_validate(wrong_taxonomy)

    unknown_field = _release_payload()
    unknown_field["mutable_default"] = True
    with pytest.raises(ValidationError, match="Extra inputs are not permitted"):
        ClassifierReleaseManifestV1.model_validate(unknown_field)


def test_taxonomy_bytes_match_go_encoding_json_html_escaping() -> None:
    assert classifier_taxonomy_bytes(["<&>\u2028\u2029"]) == (
        b'["\\u003c\\u0026\\u003e\\u2028\\u2029"]'
    )
    assert (
        hashlib.sha256(classifier_taxonomy_bytes(["<&>\u2028\u2029"])).hexdigest()
        == "bf1f62894c7640c81ac0fdaa084c86a1e39d5886f11df541f8617cca246b5058"
    )


def test_qwen_bundle_has_no_fabricated_package_or_embedding_reference() -> None:
    manifest = _release_payload()
    manifest["variants"]["qwen"] = {
        "kind": ClassifierVariantKind.QWEN_BUNDLE.value,
        "artifact_id": "qwen-classifier",
        "artifact_sha256": "e" * 64,
        "bundle_sha256": "e" * 64,
        "auxiliary_models": {},
        "semantic_configuration_sha256": "f" * 64,
    }
    parsed = ClassifierReleaseManifestV1.model_validate(manifest)
    qwen_variant = parsed.variants[ClassifierVariantID.QWEN]
    assert qwen_variant.package_weavedata_ref is None
    assert qwen_variant.embedding_contract_sha256 is None

    fake_embedding = {**manifest, "variants": {**manifest["variants"]}}
    fake_embedding["variants"]["qwen"] = {
        **manifest["variants"]["qwen"],
        "embedding_contract_sha256": "b" * 64,
    }
    with pytest.raises(
        ValidationError, match="must not claim an HMM package or embedding"
    ):
        ClassifierReleaseManifestV1.model_validate(fake_embedding)


def test_hmm_package_rejects_unconsumed_auxiliary_model_refs() -> None:
    manifest = _release_payload()
    manifest["variants"]["primary"]["auxiliary_models"] = {
        "classifier": {
            "uri": "gs://model-artifacts/auxiliary.json",
            "sha256": "f" * 64,
            "generation": 1,
        }
    }
    with pytest.raises(ValidationError, match="unsupported auxiliary model refs"):
        ClassifierReleaseManifestV1.model_validate(manifest)
