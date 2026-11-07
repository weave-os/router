"""Immutable, portable classifier release manifests."""

from __future__ import annotations

import hashlib
import json
import re
from enum import StrEnum
from typing import Literal

from pydantic import ConfigDict, Field, model_validator

from hmm_sidecar.classifier_package.core import canonical_json
from hmm_sidecar.classifier_package.manifest import is_classifier_package_manifest
from hmm_sidecar.schemas import FrozenModel


class ClassifierVariantID(StrEnum):
    """Classifier implementations addressable inside one admitted release."""

    PRIMARY = "primary"
    HMM_BETA = "hmm_beta"
    QWEN = "qwen"


class ClassifierVariantKind(StrEnum):
    """Immutable runtime source kinds supported by classifier releases."""

    HMM_PACKAGE = "hmm_package"
    QWEN_BUNDLE = "qwen_bundle"


class ModelArtifactDataKind(StrEnum):
    """Canonical WeaveData kind for published HMM model artifacts."""

    MODEL_ARTIFACT = "model_artifact"


class ClassifierModelArtifactRole(StrEnum):
    """Canonical provenance role assigned to a trained classifier package."""

    TRAINED_POLICY_ARTIFACT = "trained_policy_artifact"


class ArtifactReference(FrozenModel):
    """Content-addressed model/configuration bytes, independent of a deployment URL."""

    uri: str = Field(min_length=1)
    sha256: str = Field(pattern=r"^[0-9a-f]{64}$")
    generation: int = Field(gt=0)


class WeaveDataModelArtifactReference(FrozenModel):
    """Structural, public-repo representation of a canonical WeaveDataRef."""

    data_id: str = Field(min_length=1)
    data_kind: ModelArtifactDataKind
    schema_version: str = Field(min_length=1)
    version: str = Field(min_length=1)
    storage_uri: str = Field(min_length=1)
    content_sha256: str = Field(pattern=r"^[0-9a-f]{64}$")
    row_count: int | None = Field(default=None, ge=0)
    role: ClassifierModelArtifactRole
    required: Literal[True]


class ClassifierTaxonomy(FrozenModel):
    """Ordered classifier labels whose exact bytes identify a taxonomy."""

    class_order: tuple[str, ...] = Field(min_length=1)
    sha256: str = Field(pattern=r"^[0-9a-f]{64}$")

    @model_validator(mode="after")
    def validate_digest(self) -> ClassifierTaxonomy:
        encoded = classifier_taxonomy_bytes(self.class_order)
        if hashlib.sha256(encoded).hexdigest() != self.sha256:
            raise ValueError("taxonomy digest does not match class_order")
        if len(set(self.class_order)) != len(self.class_order):
            raise ValueError("taxonomy class_order must not contain duplicates")
        return self


def classifier_taxonomy_bytes(class_order: tuple[str, ...] | list[str]) -> bytes:
    """Encode taxonomy bytes identically to Go encoding/json's HTML escaping."""
    encoded = json.dumps(list(class_order), separators=(",", ":"), ensure_ascii=False)
    for character, escape in (
        ("<", r"\u003c"),
        (">", r"\u003e"),
        ("&", r"\u0026"),
        ("\u2028", r"\u2028"),
        ("\u2029", r"\u2029"),
    ):
        encoded = encoded.replace(character, escape)
    return encoded.encode("utf-8")


class ClassifierVariantManifest(FrozenModel):
    """Immutable package and semantic configuration for one classifier variant."""

    kind: ClassifierVariantKind
    artifact_id: str = Field(min_length=1)
    artifact_sha256: str = Field(pattern=r"^[0-9a-f]{64}$")
    package_weavedata_ref: WeaveDataModelArtifactReference | None = None
    bundle_sha256: str | None = Field(default=None, pattern=r"^[0-9a-f]{64}$")
    auxiliary_models: dict[str, ArtifactReference]
    semantic_configuration_sha256: str = Field(pattern=r"^[0-9a-f]{64}$")
    embedding_contract_sha256: str | None = Field(
        default=None, pattern=r"^[0-9a-f]{64}$"
    )

    @model_validator(mode="after")
    def validate_runtime_source(self) -> ClassifierVariantManifest:
        if self.kind is ClassifierVariantKind.HMM_PACKAGE:
            if (
                self.package_weavedata_ref is None
                or self.package_weavedata_ref.content_sha256 != self.artifact_sha256
            ):
                raise ValueError(
                    "HMM WeaveDataRef content_sha256 must match artifact_sha256"
                )
            if self.embedding_contract_sha256 is None or self.bundle_sha256 is not None:
                raise ValueError(
                    "HMM package requires an embedding contract digest only"
                )
            if self.auxiliary_models:
                raise ValueError(
                    "HMM packages cannot declare unsupported auxiliary model refs"
                )
        if self.kind is ClassifierVariantKind.QWEN_BUNDLE:
            if (
                self.package_weavedata_ref is not None
                or self.embedding_contract_sha256 is not None
            ):
                raise ValueError(
                    "Qwen bundle must not claim an HMM package or embedding ref"
                )
            if self.bundle_sha256 != self.artifact_sha256:
                raise ValueError("Qwen bundle digest must match artifact_sha256")
            if self.auxiliary_models:
                raise ValueError("Qwen bundles do not use HMM auxiliary model refs")
        return self


class ClassifierReleaseProvenance(FrozenModel):
    """Public source identity and build evidence for a classifier release."""

    router_revision: str = Field(pattern=r"^[0-9a-f]{40}$")
    weave_revision: str = Field(pattern=r"^[0-9a-f]{40}$")
    build_attestation: ArtifactReference


class ClassifierReleaseManifestV1(FrozenModel):
    """Classifier-only immutable release; release ID is SHA-256 of stored JSON bytes."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    schema_version: Literal["router_classifier_release_v1"]
    api_major: Literal[5]
    wire_schema: Literal["policy_router_v5"]
    taxonomy: ClassifierTaxonomy
    variants: dict[ClassifierVariantID, ClassifierVariantManifest]
    runtime_image_digest: str = Field(pattern=r"^sha256:[0-9a-f]{64}$")
    provenance: ClassifierReleaseProvenance

    @model_validator(mode="after")
    def validate_variants(self) -> ClassifierReleaseManifestV1:
        if ClassifierVariantID.PRIMARY not in self.variants:
            raise ValueError("classifier release requires a primary variant")
        if not self.variants:
            raise ValueError("classifier release requires at least one variant")
        for variant_id, variant in self.variants.items():
            expected_kind = (
                ClassifierVariantKind.QWEN_BUNDLE
                if variant_id is ClassifierVariantID.QWEN
                else ClassifierVariantKind.HMM_PACKAGE
            )
            if variant.kind is not expected_kind:
                raise ValueError(
                    f"classifier variant {variant_id.value!r} has the wrong kind"
                )
        return self


def classifier_release_id(manifest_bytes: bytes) -> str:
    """Return the release identity without embedding it recursively in the manifest."""
    return hashlib.sha256(manifest_bytes).hexdigest()


def classifier_package_artifact_id(archive_sha256: str) -> str:
    """Return the existing Weave model ID for a published HMM package archive."""
    if not re.fullmatch(r"[0-9a-f]{64}", archive_sha256):
        raise ValueError("archive_sha256 must be a lowercase SHA-256")
    return f"hmm_router_sidecar_package_{archive_sha256[:16]}"


def qwen_semantic_configuration_bytes(artifact_id: str, bundle_sha256: str) -> bytes:
    """Return semantic Qwen identity bytes without physical endpoint/revision fields."""
    if not artifact_id or not re.fullmatch(r"[0-9a-f]{64}", bundle_sha256):
        raise ValueError("Qwen semantic configuration requires exact artifact identity")
    return canonical_json(
        {
            "schema_version": "router_qwen_classifier_semantic_configuration_v1",
            "artifact_id": artifact_id,
            "bundle_sha256": bundle_sha256,
            "wire_schema": "policy_router_v5",
        }
    ).encode("utf-8")


def semantic_configuration_sha256(configuration_bytes: bytes) -> str:
    """Hash exact canonical semantic-configuration bytes for a release manifest."""
    return hashlib.sha256(configuration_bytes).hexdigest()


def embedded_semantic_configuration_bytes(package_manifest: dict[str, object]) -> bytes:
    """Return canonical bytes for the serving semantics embedded in an HMM package."""
    raw_runtime = _raw_runtime_contract(package_manifest)
    return canonical_json(raw_runtime).encode("utf-8")


def embedded_embedding_contract_bytes(package_manifest: dict[str, object]) -> bytes:
    """Return the embedding/preprocessing subset that defines HMM feature geometry."""
    raw_runtime = _raw_runtime_contract(package_manifest)
    keys = (
        "contract_version",
        "serving_config",
        "embedding_model",
        "embedding_dim",
        "sequence_representation",
        "document_serializer_version",
        "document_serializer_sha256",
    )
    if any(key not in raw_runtime for key in keys):
        raise ValueError("classifier package lacks its embedding contract identity")
    return canonical_json(
        {
            "schema_version": "router_classifier_embedding_contract_v1",
            **{key: raw_runtime[key] for key in keys},
        }
    ).encode("utf-8")


def embedded_semantic_configuration_sha256(package_manifest: dict[str, object]) -> str:
    """Return the release identity hash for a package's semantic configuration."""
    return semantic_configuration_sha256(
        embedded_semantic_configuration_bytes(package_manifest)
    )


def embedded_embedding_contract_sha256(package_manifest: dict[str, object]) -> str:
    """Return the release identity hash for a package's embedding contract."""
    return semantic_configuration_sha256(
        embedded_embedding_contract_bytes(package_manifest)
    )


def _raw_runtime_contract(package_manifest: dict[str, object]) -> dict[str, object]:
    if not is_classifier_package_manifest(package_manifest):
        raise ValueError("package manifest is not a configured HMM classifier")
    runtime_envelope = package_manifest.get("runtime_contract")
    if not isinstance(runtime_envelope, dict):
        raise ValueError("classifier package runtime contract is missing")
    raw_runtime = runtime_envelope.get("raw_v5_runtime_contract")
    if not isinstance(raw_runtime, dict):
        raise ValueError("classifier package raw-V5 runtime contract is missing")
    return raw_runtime


def is_classifier_release_id(value: str) -> bool:
    """Report whether value is a lowercase SHA-256 release reference."""
    return re.fullmatch(r"[0-9a-f]{64}", value) is not None
