"""Pydantic schemas for versioned Weave model artifacts."""

from __future__ import annotations

from datetime import datetime
from enum import Enum
from typing import Any, Literal, Self

from pydantic import Field, model_validator

from hmm_sidecar.classifier_package.weave_data import (
    ServingFeatureComposition,
    WeaveDataKind,
    WeaveDataRef,
    WeaveFeatureContract,
)
from hmm_sidecar.classifier_package.rl_fields import RLActionSpaceContract
from hmm_sidecar.classifier_package.schema_base import (
    FrozenClosedModel,
    NonEmptyString,
    Sha256Hex,
)

MUTABLE_ARTIFACT_PATH_PARTS = frozenset(
    {
        "current.json",
        "latest",
        "latest_bundle.json",
    }
)


WEAVE_MODEL_SCHEMA_VERSION = "weave_model.v0"


class WeaveModelStatus(str, Enum):
    CANDIDATE = "candidate"
    GOLD = "gold"
    PROD = "prod"
    RETIRED = "retired"
    QUARANTINED = "quarantined"


PINNED_MODEL_STATUSES = frozenset(
    {
        WeaveModelStatus.GOLD,
        WeaveModelStatus.PROD,
    }
)


def _has_mutable_artifact_alias(path: str) -> bool:
    return any(
        part in MUTABLE_ARTIFACT_PATH_PARTS
        for part in path.replace("\\", "/").split("/")
    )


class ModelArtifact(FrozenClosedModel):
    bundle_path: NonEmptyString
    bundle_sha256: Sha256Hex | None
    bundle_schema_version: NonEmptyString
    artifact_version: str | None = None
    artifact_paths: tuple[NonEmptyString, ...] = Field(min_length=0)
    required_files: tuple[NonEmptyString, ...] = Field(min_length=0)


class ModelContracts(FrozenClosedModel):
    input_contract: dict[str, Any]
    output_contract: dict[str, Any]
    canonical_turn_schema_version: str | None
    feature_schema_version: str | None
    feature_contract: WeaveFeatureContract | None = None
    # Block-by-block feature composition the model was trained against;
    # serving loaders diff their recomputed composition against this
    # before any inference (mismatch = loud startup error).
    serving_features: ServingFeatureComposition | None = None
    score_schema_version: str | None
    sidecar_api_schema_version: str | None


class ModelEvals(FrozenClosedModel):
    primary_metric: str | None
    metrics: dict[str, Any]
    eval_artifacts: tuple[NonEmptyString, ...] = Field(min_length=0)
    baseline_model_id: str | None
    passed_regression_gate: bool


class ModelPromotion(FrozenClosedModel):
    promoted_at: datetime | None
    promoted_by: str | None
    promotion_reason: str | None
    promotion_checks: dict[str, Any]


class ModelServing(FrozenClosedModel):
    sidecar_model_name: NonEmptyString
    sidecar_bundle_path: NonEmptyString
    score_aliases: dict[str, NonEmptyString]
    expected_input_dim: int | None = Field(ge=1)
    expected_output_fields: tuple[NonEmptyString, ...] = Field(min_length=1)
    docker_include_paths: tuple[NonEmptyString, ...] = Field(min_length=0)


class ModelLineage(FrozenClosedModel):
    training_run_ids: tuple[NonEmptyString, ...] = Field(default_factory=tuple)
    base_model_ids: tuple[NonEmptyString, ...] = Field(default_factory=tuple)
    source_model_ids: tuple[NonEmptyString, ...] = Field(default_factory=tuple)
    source_data_ids: tuple[NonEmptyString, ...] = Field(default_factory=tuple)
    code_version: str | None = None
    # Set when this model replaces an earlier one in a migration campaign.
    supersedes_model_id: str | None = None


class ModelRLFields(RLActionSpaceContract):
    uses_old_q_estimates_as_labels: Literal[False] = False


class WeaveModel(FrozenClosedModel):
    object_type: Literal["WeaveModel"] = "WeaveModel"
    schema_version: NonEmptyString = WEAVE_MODEL_SCHEMA_VERSION
    model_id: NonEmptyString
    model_family: NonEmptyString
    task_family: NonEmptyString
    status: WeaveModelStatus
    input_data: tuple[WeaveDataRef, ...] = Field(min_length=1)
    output_data: tuple[WeaveDataRef, ...] = Field(min_length=1)
    artifact: ModelArtifact
    contracts: ModelContracts
    evals: ModelEvals
    promotion: ModelPromotion
    serving: ModelServing
    rl: ModelRLFields | None = None
    lineage: ModelLineage = Field(default_factory=ModelLineage)
    owner: str | None
    known_limits: tuple[str, ...] = Field(default_factory=tuple)
    metadata: dict[str, Any] = Field(default_factory=dict)

    @model_validator(mode="after")
    def check_rl_contract(self) -> Self:
        has_rl_input = any(
            data_ref.data_kind is WeaveDataKind.RL_TRAJECTORY
            for data_ref in self.input_data
        )
        if has_rl_input and self.rl is None:
            raise ValueError(
                "models trained from rl_trajectory WeaveData require rl fields"
            )
        return self

    @model_validator(mode="after")
    def check_pinned_artifact_contract(self) -> Self:
        if self.status not in PINNED_MODEL_STATUSES:
            return self

        missing_fields: list[str] = []
        if self.artifact.bundle_sha256 is None:
            missing_fields.append("artifact.bundle_sha256")
        if not self.artifact.artifact_paths:
            missing_fields.append("artifact.artifact_paths")
        if not self.artifact.required_files:
            missing_fields.append("artifact.required_files")

        if missing_fields:
            fields = ", ".join(missing_fields)
            raise ValueError(
                f"{self.status.value} WeaveModel requires pinned artifacts: {fields}"
            )

        mutable_paths = [
            path
            for path in (
                self.artifact.bundle_path,
                self.serving.sidecar_bundle_path,
            )
            if _has_mutable_artifact_alias(path)
        ]
        if mutable_paths:
            paths = ", ".join(mutable_paths)
            raise ValueError(
                f"{self.status.value} WeaveModel must not reference mutable "
                f"artifact aliases: {paths}"
            )

        return self

    @model_validator(mode="after")
    def check_prod_promotion_gate(self) -> Self:
        if self.status is not WeaveModelStatus.PROD:
            return self

        missing_promotion_fields = [
            field_name
            for field_name in (
                "promoted_at",
                "promoted_by",
                "promotion_reason",
            )
            if getattr(self.promotion, field_name) in (None, "")
        ]
        if missing_promotion_fields:
            fields = ", ".join(missing_promotion_fields)
            raise ValueError(f"prod WeaveModel is missing promotion fields: {fields}")

        if not self.evals.passed_regression_gate:
            raise ValueError("prod WeaveModel requires passed_regression_gate=True")

        return self
