from __future__ import annotations

import hashlib
import io
import json
import tarfile
from pathlib import Path

import pytest

from hmm_sidecar.classifier_package.complexity_classifier import (
    no_tool_context_feature_layout,
)
from hmm_sidecar.classifier_package.core import canonical_json
from hmm_sidecar.classifier_package.legacy_conversion import (
    CONVERSION_SCHEMA_VERSION,
    ConvertedClassifierPackage,
    convert_legacy_classifier_package,
)
from hmm_sidecar.classifier_package.package_loader import load_classifier_package
from hmm_sidecar.classifier_package.runtime_contract import (
    INTERACTION_STATE_RUNTIME_CONTRACT_VERSION,
    InteractionStateGeometry,
    parse_runtime_contract,
)
from hmm_sidecar.classifier_package.serving_profile import (
    DocumentWindow,
    EmbeddingProvider,
    ServingConfig,
    ToolEvidenceMode,
    serving_profile,
)

CLASSIFIER_BYTES = b"verbatim-classifier-model"
HMM_BYTES = b"verbatim-hmm-bundle"
ROSTER_BYTES = b"legacy-policy-roster"
ARM_SELECTOR_BYTES = b"legacy-arm-selector"
SERVING_INPUT_SHA256 = "9" * 64


def _sha256(value: bytes) -> str:
    return hashlib.sha256(value).hexdigest()


def _raw_runtime_contract() -> dict[str, object]:
    config = ServingConfig(
        provider=EmbeddingProvider.GEMINI,
        tool_evidence=ToolEvidenceMode.USER_AGENT_ONLY,
        document_window=DocumentWindow.PER_MESSAGE,
        lookback_rounds=1,
    )
    profile = serving_profile(config)
    interaction_state = InteractionStateGeometry(
        hmm_chain="interaction_state",
        user_pca_dim=1,
        agent_pca_dim=1,
        element_lookback_rounds=1,
        element_lookback_source=None,
    )
    layout = no_tool_context_feature_layout(
        2,
        embedding_dim=profile.embedding_dim,
        belief_dynamics=True,
        element_lookback=interaction_state.element_lookback(
            embedding_dim=profile.embedding_dim
        ),
    )
    raw_contract: dict[str, object] = {
        "contract_version": INTERACTION_STATE_RUNTIME_CONTRACT_VERSION,
        "input_source": "raw_v5_live_conversation_messages",
        "serving_config": config.model_dump(mode="json"),
        "interaction_state": interaction_state.model_dump(mode="json"),
        "sequence_representation": profile.representation.value,
        "embedding_provider": config.provider.value,
        "embedding_model": profile.embedding_model,
        "embedding_dim": profile.embedding_dim,
        "document_serializer_version": profile.document_serializer_version,
        "document_serializer_sha256": profile.document_serializer_sha256,
        "state_encoder_contract_sha256": "a" * 64,
        "serving_input_sha256": SERVING_INPUT_SHA256,
        "hmm_training_contract_version": "fixture_hmm_training_contract_v1",
        "hmm_training_contract_sha256": "b" * 64,
        "hmm_states": 2,
        "pca_dim": 1,
        "classes": ["fast", "maximum"],
        "classifier_feature_layout_sha256": hashlib.sha256(
            canonical_json(layout).encode()
        ).hexdigest(),
        "classifier_feature_dim": sum(int(block["dim"]) for block in layout),
        "no_canonical_fallback": True,
    }
    parse_runtime_contract(raw_contract)
    return raw_contract


def _training_privacy() -> dict[str, dict[str, str]]:
    return {
        artifact: {
            "schema_version": "ai_training_privacy_attestation_v1",
            "policy": "exclude_ai_training_opt_out_organizations",
            "opt_out_org_ids_sha256": "c" * 64,
        }
        for artifact in ("classifier", "hmm_corpus", "arm_selector")
    }


def _source_package(
    *,
    classifier_bytes: bytes = CLASSIFIER_BYTES,
    declared_classifier_sha256: str | None = None,
    include_arm_selector: bool = True,
    extra_members: dict[str, bytes] | None = None,
    runtime_contract: dict[str, object] | None = None,
    training_privacy: dict[str, dict[str, str]] | None = None,
) -> bytes:
    members = {
        "classifier/model.bin": classifier_bytes,
        "hmm_bundle/bundle.json": HMM_BYTES,
        "roster.json": ROSTER_BYTES,
    }
    arm_selector_files = {}
    if include_arm_selector:
        members["arm_selector/model.bin"] = ARM_SELECTOR_BYTES
        arm_selector_files = {"model.bin": _sha256(ARM_SELECTOR_BYTES)}
    if extra_members:
        members.update(extra_members)
    runtime = runtime_contract or _raw_runtime_contract()
    manifest: dict[str, object] = {
        "schema_version": "hmm_router_sidecar_package_v8",
        "package_id": "prod-legacy-package",
        "classifier_dir": "/private/build/classifier",
        "hmm_bundle_dir": "/private/build/hmm_bundle",
        "classifier_files": {
            "model.bin": declared_classifier_sha256 or _sha256(classifier_bytes)
        },
        "classifier_contract": {"classes": ["fast", "maximum"]},
        "hmm_bundle_files": {"bundle.json": _sha256(HMM_BYTES)},
        "required_files": [
            "manifest.json",
            "classifier/",
            "hmm_bundle/",
            "roster.json",
        ],
        "training_privacy": training_privacy or _training_privacy(),
        "runtime_contract": {
            "route_endpoint": "/route",
            "outcome_endpoint": "/outcome",
            "sidecar_api_schema_version": "policy_router_v4",
            "input_mode": "raw_v5_configured_serving_v1",
            "raw_v5_runtime_contract": runtime,
            "raw_v5_serving_input": {"schema_version": "fixture_serving_input_v1"},
            "raw_v5_serving_input_sha256": SERVING_INPUT_SHA256,
            "policy_defaults": {"cold_start_cluster": "fast"},
        },
        "roster_path": "/private/training/roster.json",
        "roster_sha256": _sha256(ROSTER_BYTES),
    }
    if include_arm_selector:
        manifest.update(
            {
                "arm_selector_dir": "/private/build/arm_selector",
                "arm_selector_files": arm_selector_files,
                "arm_selector_contract": {"training_only": True},
            }
        )
    members["manifest.json"] = json.dumps(
        manifest, ensure_ascii=False, sort_keys=True, separators=(",", ":")
    ).encode()
    output = io.BytesIO()
    with tarfile.open(fileobj=output, mode="w:gz") as archive:
        for member_name, payload in sorted(members.items()):
            info = tarfile.TarInfo(member_name)
            info.size = len(payload)
            archive.addfile(info, io.BytesIO(payload))
    return output.getvalue()


def _manifest_bytes(archive_bytes: bytes) -> bytes:
    with tarfile.open(fileobj=io.BytesIO(archive_bytes), mode="r:*") as archive:
        manifest = archive.extractfile("manifest.json")
        assert manifest is not None
        return manifest.read()


def _converted(source: bytes) -> ConvertedClassifierPackage:
    return convert_legacy_classifier_package(source, _sha256(source))


def test_conversion_preserves_classifier_hmm_contract_and_privacy_but_strips_policy(
    tmp_path: Path,
) -> None:
    source = _source_package()
    converted = _converted(source)

    loaded_root, manifest = load_classifier_package(
        converted.archive_bytes, converted.archive_sha256, tmp_path
    )

    assert converted.source_archive_sha256 == _sha256(source)
    assert converted.source_manifest_sha256 == _sha256(_manifest_bytes(source))
    assert manifest["schema_version"] == "hmm_router_classifier_package_v1"
    assert manifest["required_files"] == ["manifest.json", "classifier/", "hmm_bundle/"]
    assert manifest["runtime_contract"]["selection_owner"] == "go"
    assert "policy_defaults" not in manifest["runtime_contract"]
    assert (
        manifest["runtime_contract"]["raw_v5_runtime_contract"]
        == _raw_runtime_contract()
    )
    assert set(manifest["training_privacy"]) == {"classifier", "hmm_corpus"}
    assert (
        manifest["conversion_provenance"]["schema_version"]
        == CONVERSION_SCHEMA_VERSION.value
    )
    assert manifest["conversion_provenance"]["source_archive_sha256"] == _sha256(source)
    assert (loaded_root / "classifier/model.bin").read_bytes() == CLASSIFIER_BYTES
    assert (loaded_root / "hmm_bundle/bundle.json").read_bytes() == HMM_BYTES
    with tarfile.open(
        fileobj=io.BytesIO(converted.archive_bytes), mode="r:*"
    ) as archive:
        names = set(archive.getnames())
    assert names == {
        "manifest.json",
        "classifier/model.bin",
        "hmm_bundle/bundle.json",
    }


def test_conversion_is_byte_deterministic_and_rejects_wrong_source_digest() -> None:
    source = _source_package()
    first = _converted(source)
    second = _converted(source)

    assert first.archive_bytes == second.archive_bytes
    assert first.archive_sha256 == second.archive_sha256
    with pytest.raises(ValueError, match="does not match expected_sha256"):
        convert_legacy_classifier_package(source, "0" * 64)


def test_conversion_rejects_tampered_weights_and_unrecognized_members() -> None:
    tampered_weights = _source_package(
        classifier_bytes=b"tampered-classifier",
        declared_classifier_sha256=_sha256(CLASSIFIER_BYTES),
    )
    with pytest.raises(ValueError, match="classifier/model.bin"):
        _converted(tampered_weights)

    with pytest.raises(ValueError, match="unexpected=.*training_rows"):
        _converted(_source_package(extra_members={"training_rows.json": b"private"}))


def test_conversion_rejects_missing_privacy_evidence_and_unknown_runtime_fields() -> (
    None
):
    bad_privacy = _training_privacy()
    del bad_privacy["hmm_corpus"]
    with pytest.raises(ValueError, match="hmm_corpus privacy attestation"):
        _converted(_source_package(training_privacy=bad_privacy))

    bad_runtime = _raw_runtime_contract()
    bad_runtime["unmodeled_semantic"] = True
    with pytest.raises(ValueError, match="unsupported fields: unmodeled_semantic"):
        _converted(_source_package(runtime_contract=bad_runtime))
