"""Local-file resolver for self-hosted classifier v5 releases and selections."""

from __future__ import annotations

import hashlib
from pathlib import Path
from typing import Literal

from pydantic import ConfigDict, Field, ValidationError

from hmm_sidecar.artifacts import ArtifactRegistry
from hmm_sidecar.classifier_api import (
    ClassifierAPIVersion,
    ClassifierFactsV5,
    ClassifierRequestV5,
    ClassifierVariantID,
    ResolvedClassifierRelease,
    ResolvedClassifierSelection,
    ResolvedClassifierVariant,
    UnknownClassifierRelease,
)
from hmm_sidecar.classifier_package.serving import PackageClassifier
from hmm_sidecar.classifier_release import (
    ClassifierReleaseManifestV1,
    embedded_embedding_contract_sha256,
    embedded_semantic_configuration_sha256,
)
from hmm_sidecar.package_policy import PackagePolicy
from hmm_sidecar.schemas import FrozenModel

MAX_LOCAL_MANIFEST_BYTES = 1 << 20


class LocalClassifierSelectionV1(FrozenModel):
    """Exact local allowlist file; its stored bytes define selection identity."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    schema_version: Literal["router_classifier_selection_v1"]
    classifier_release_id: str = Field(pattern=r"^[0-9a-f]{64}$")
    enabled_variants: tuple[ClassifierVariantID, ...] = Field(min_length=1)


class LocalPackageClassifierRuntime:
    """Adapt the self-hosted package runtime to the classifier-only contract."""

    def __init__(self, runtime: PackageClassifier) -> None:
        self._runtime = runtime

    async def classify(
        self, request: ClassifierRequestV5, *, session_id: str | None
    ) -> ClassifierFactsV5:
        readout, _ = await self._runtime.classify(
            request.model_dump(
                mode="json",
                exclude={
                    "schema_version",
                    "classifier_selection_id",
                    "classifier_release_id",
                    "classifier_variant_id",
                    "request_id",
                    "session_id",
                },
            )
            | {"route_id": request.request_id},
            session_id=session_id,
        )
        return ClassifierFactsV5(
            predicted_label=readout.label,
            class_order=tuple(self._runtime.classes),
            class_probabilities=readout.probabilities,
            classifier_confidence=readout.confidence,
            classifier_margin=readout.margin,
            hmm_state_id=readout.state_id,
            hmm_state_path=readout.state_path,
            hmm_state_probabilities=readout.state_probabilities,
        )


class LocalClassifierReleaseResolver:
    """Resolve exact manifest/selection files against already-loaded local packages."""

    def __init__(
        self,
        release_manifest_path: Path,
        selection_path: Path,
        artifacts: ArtifactRegistry,
        policies: dict[str, PackagePolicy],
    ) -> None:
        self.release_manifest_path = release_manifest_path
        self.selection_path = selection_path
        self.artifacts = artifacts
        self.policies = policies
        self._release, self._selection = self._load()
        self.release_id = self._release.release_id
        self.selection_id = self._selection.selection_id

    def _read_exact(self, path: Path) -> bytes:
        raw = path.read_bytes()
        if not raw or len(raw) > MAX_LOCAL_MANIFEST_BYTES:
            raise ValueError(f"local classifier manifest is empty or too large: {path}")
        return raw

    def _load(self) -> tuple[ResolvedClassifierRelease, ResolvedClassifierSelection]:
        release_bytes = self._read_exact(self.release_manifest_path)
        selection_bytes = self._read_exact(self.selection_path)
        try:
            release_manifest = ClassifierReleaseManifestV1.model_validate_json(
                release_bytes
            )
            selection_manifest = LocalClassifierSelectionV1.model_validate_json(
                selection_bytes
            )
        except ValidationError as error:
            raise ValueError(
                "local classifier release or selection manifest is invalid"
            ) from error
        release_id = hashlib.sha256(release_bytes).hexdigest()
        selection_id = hashlib.sha256(selection_bytes).hexdigest()
        if selection_manifest.classifier_release_id != release_id:
            raise ValueError(
                "local classifier selection does not pin the exact release bytes"
            )
        if len(set(selection_manifest.enabled_variants)) != len(
            selection_manifest.enabled_variants
        ):
            raise ValueError("local classifier selection variants must be unique")
        if (
            tuple(
                sorted(
                    selection_manifest.enabled_variants,
                    key=lambda variant: variant.value,
                )
            )
            != selection_manifest.enabled_variants
        ):
            raise ValueError("local classifier selection variants must be sorted")
        if ClassifierVariantID.PRIMARY not in selection_manifest.enabled_variants:
            raise ValueError("local classifier selection must enable primary")

        variants: dict[ClassifierVariantID, ResolvedClassifierVariant] = {}
        for variant_id in selection_manifest.enabled_variants:
            variant_manifest = release_manifest.variants.get(variant_id)
            if variant_manifest is None:
                raise ValueError(
                    "local classifier selection enables a missing release variant"
                )
            if variant_manifest.kind.value != "hmm_package":
                raise ValueError(
                    f"self-hosted resolver does not support {variant_manifest.kind.value}"
                )
            package_sha = variant_manifest.artifact_sha256
            package_artifact = self.artifacts.get(package_sha)
            if package_artifact is None or package_artifact.package_manifest is None:
                raise ValueError(
                    f"local package {package_sha} is not in HMM_PACKAGE_REGISTRY"
                )
            if package_artifact.model_id != variant_manifest.artifact_id:
                raise ValueError(
                    "local package artifact ID does not match release manifest"
                )
            if (
                embedded_semantic_configuration_sha256(
                    package_artifact.package_manifest
                )
                != variant_manifest.semantic_configuration_sha256
            ):
                raise ValueError(
                    "local package semantic configuration does not match release"
                )
            if (
                embedded_embedding_contract_sha256(package_artifact.package_manifest)
                != variant_manifest.embedding_contract_sha256
            ):
                raise ValueError(
                    "local package embedding contract does not match release"
                )
            package_policy = self.policies.get(package_sha)
            if package_policy is None:
                raise ValueError(f"local package policy {package_sha} is not loaded")
            if package_policy.runtime.classes != release_manifest.taxonomy.class_order:
                raise ValueError(
                    "local package taxonomy does not match release taxonomy"
                )
            variants[variant_id] = ResolvedClassifierVariant(
                variant_id=variant_id,
                artifact_id=variant_manifest.artifact_id,
                package_sha256=package_sha,
                runtime=LocalPackageClassifierRuntime(package_policy.runtime),
            )
        release = ResolvedClassifierRelease(
            release_id=release_id,
            api_major=release_manifest.api_major,
            wire_schema=ClassifierAPIVersion(release_manifest.wire_schema),
            class_order=release_manifest.taxonomy.class_order,
            taxonomy_sha256=release_manifest.taxonomy.sha256,
            variants=variants,
        )
        selection = ResolvedClassifierSelection(
            selection_id=selection_id,
            release_id=release_id,
            enabled_variants=frozenset(selection_manifest.enabled_variants),
        )
        return release, selection

    async def resolve(self, release_id: str) -> ResolvedClassifierRelease:
        if release_id != self.release_id:
            raise UnknownClassifierRelease(release_id)
        return self._release

    async def resolve_selection(self, selection_id: str) -> ResolvedClassifierSelection:
        if selection_id != self.selection_id:
            raise UnknownClassifierRelease(selection_id)
        return self._selection


def resolve_local_classifier_resolver(
    artifacts: ArtifactRegistry, policies: dict[str, PackagePolicy]
) -> LocalClassifierReleaseResolver | None:
    """Load local v5 configuration when both manifest paths are configured."""
    import os

    release_path = os.environ.get("HMM_CLASSIFIER_RELEASE_MANIFEST_PATH", "").strip()
    selection_path = os.environ.get("HMM_CLASSIFIER_SELECTION_PATH", "").strip()
    if not release_path and not selection_path:
        return None
    if not release_path or not selection_path:
        raise ValueError(
            "configure both HMM_CLASSIFIER_RELEASE_MANIFEST_PATH and HMM_CLASSIFIER_SELECTION_PATH"
        )
    return LocalClassifierReleaseResolver(
        Path(release_path), Path(selection_path), artifacts, policies
    )
