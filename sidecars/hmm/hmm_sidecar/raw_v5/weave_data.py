"""Pydantic schemas for versioned Weave data artifacts."""

from __future__ import annotations

from datetime import datetime
from enum import Enum
from typing import Any, Literal, Self

from pydantic import Field, model_validator

from hmm_sidecar.raw_v5.schema_base import FrozenOpenModel, NonEmptyString, Sha256Hex


class WeaveDataKind(str, Enum):
    SUPERVISED_EXAMPLES = "supervised_examples"
    RL_TRAJECTORY = "rl_trajectory"
    MODEL_ARTIFACT = "model_artifact"
    CONVERSATION_TURNS = "conversation_turns"
    ROUTER_EXAMPLES = "router_examples"
    FEEDBACK_LABELS = "feedback_labels"
    AGENT_FLOW_LABELS = "agent_flow_labels"
    EMBEDDINGS = "embeddings"
    MODEL_OUTPUTS = "model_outputs"
    RUN_RESULTS = "run_results"
    MODEL_ROSTER = "model_roster"
    RUN_ARTIFACT = "run_artifact"


class WeaveDataRef(FrozenOpenModel):
    data_id: NonEmptyString | None
    data_kind: WeaveDataKind
    schema_version: NonEmptyString
    version: NonEmptyString | None = None
    storage_uri: NonEmptyString | None = None
    content_sha256: Sha256Hex | None = None
    row_count: int | None = Field(default=None, ge=0)
    role: NonEmptyString
    required: bool

    @model_validator(mode="after")
    def check_snapshot_pin(self) -> Self:
        if self.required and self.data_id is None:
            raise ValueError("required WeaveDataRef requires data_id")

        if self.data_id is None:
            return self

        missing_fields = [
            field_name
            for field_name in ("version", "storage_uri", "content_sha256")
            if getattr(self, field_name) in (None, "")
        ]
        if missing_fields:
            fields = ", ".join(missing_fields)
            raise ValueError(f"WeaveDataRef with data_id requires: {fields}")

        return self


class PayloadContract(FrozenOpenModel):
    required_fields: tuple[NonEmptyString, ...] = Field(default_factory=tuple)


class FeatureBlock(FrozenOpenModel):
    """One named block in a composed feature vector layout."""

    block: NonEmptyString
    dim: int = Field(ge=0)
    names: tuple[str, ...] = Field(default_factory=tuple)


class ServingFeatureComposition(FrozenOpenModel):
    """Registry-side train/serve feature contract for a composed model.

    Captures exactly the composition facts whose absence has caused silent
    train/serve skew: the embedding space, the state-definition text
    contract, the categorical encoding mode, upstream artifact pins, and
    the block-by-block feature layout. A serving loader should recompute
    its own composition and diff it against this block before running any
    inference — mismatches must be a loud startup error, never a zeroed
    feature block.
    """

    embedding_model: NonEmptyString | None = None
    embedding_dim: int | None = Field(default=None, ge=1)
    state_definition: NonEmptyString | None = None
    categorical_mode: NonEmptyString | None = None
    feature_dim: int | None = Field(default=None, ge=1)
    feature_layout: tuple[FeatureBlock, ...] = Field(default_factory=tuple)
    upstream_bundle_version: str | None = None
    upstream_bundle_sha256: Sha256Hex | None = None

    @model_validator(mode="after")
    def check_layout_dims(self) -> Self:
        if self.feature_layout and self.feature_dim is not None:
            layout_dim = sum(block.dim for block in self.feature_layout)
            if layout_dim != self.feature_dim:
                raise ValueError(
                    "feature_layout dims sum to "
                    f"{layout_dim} but feature_dim is {self.feature_dim}"
                )
        return self


class WeaveFeatureContract(FrozenOpenModel):
    episode_schema_version: NonEmptyString
    state_feature_schema_version: NonEmptyString
    cache_feature_schema_version: NonEmptyString
    action_feature_schema_version: NonEmptyString
    cost_table_version: NonEmptyString
    cache_accounting_version: NonEmptyString
    provider_usage_schema_version: NonEmptyString

    def asdict(self) -> dict[str, str]:
        return {
            key: str(value)
            for key, value in self.model_dump().items()
            if value is not None
        }

    def summary(self) -> str:
        return (
            f"episode={self.episode_schema_version} "
            f"state={self.state_feature_schema_version} "
            f"cache={self.cache_feature_schema_version} "
            f"action={self.action_feature_schema_version} "
            f"cost={self.cost_table_version}"
        )


class SupervisedPayloadContract(PayloadContract):
    input_fields: tuple[NonEmptyString, ...] = Field(min_length=1)
    label_fields: tuple[NonEmptyString, ...] = Field(min_length=1)
    split_fields: tuple[NonEmptyString, ...] = Field(default_factory=tuple)


class FeedbackLabelsPayloadContract(PayloadContract):
    required_fields: tuple[NonEmptyString, ...] = (
        "record_id",
        "created_at",
        "feedback",
        "reward",
        "state",
        "action",
    )
    label_fields: tuple[NonEmptyString, ...] = Field(
        default=("feedback.label",),
        min_length=1,
    )
    reward_field: NonEmptyString = "reward"
    collection_policy_id: NonEmptyString | None = None
    reward_version: NonEmptyString | None = None
    prompt_format_version: NonEmptyString | None = None


class RLTrajectoryPayloadContract(PayloadContract):
    required_fields: tuple[NonEmptyString, ...] = (
        "episode_id",
        "turns",
        "state",
        "action",
        "reward",
        "propensity",
    )
    trajectory_fields: tuple[NonEmptyString, ...] = Field(
        default=(
            "episode_id",
            "turns",
            "state",
            "action",
            "reward",
            "propensity",
        ),
        min_length=1,
    )
    behavior_policy_ids: tuple[NonEmptyString, ...] = Field(min_length=1)
    collection_policy_id: NonEmptyString
    reward_version: NonEmptyString
    prompt_format_version: NonEmptyString
    model_roster_version: NonEmptyString
    cost_table_version: NonEmptyString
    feature_contract: WeaveFeatureContract | None = None
    propensity_required: Literal[True] = True
    rewards_are_ground_truth: bool = True
    old_q_estimates_are_ground_truth: Literal[False] = False

    @model_validator(mode="after")
    def check_required_trajectory_fields(self) -> Self:
        missing_fields = [
            field_name
            for field_name in self.trajectory_fields
            if field_name not in self.required_fields
        ]
        if missing_fields:
            fields = ", ".join(missing_fields)
            raise ValueError(
                "required_fields must include every trajectory field: " + fields
            )
        return self


class RunResultsPayloadContract(PayloadContract):
    run_id: NonEmptyString
    run_kind: Literal["training", "eval"]
    run_method: NonEmptyString | None = None
    metric_fields: tuple[NonEmptyString, ...] = Field(default_factory=tuple)
    plot_fields: tuple[NonEmptyString, ...] = Field(default_factory=tuple)
    table_fields: tuple[NonEmptyString, ...] = Field(default_factory=tuple)
    artifact_fields: tuple[NonEmptyString, ...] = Field(default_factory=tuple)
    goal_fields: tuple[NonEmptyString, ...] = Field(default_factory=tuple)
    summary_fields: tuple[NonEmptyString, ...] = Field(default_factory=tuple)
    primary_metric: str | None = None
    passed_regression_gate_field: str | None = None


PayloadContractType = (
    RLTrajectoryPayloadContract
    | FeedbackLabelsPayloadContract
    | RunResultsPayloadContract
    | SupervisedPayloadContract
    | PayloadContract
)


DataQualityCheckStatus = Literal["pass", "warn", "fail"]


class DataQualityCheck(FrozenOpenModel):
    """One named build-time quality check with its measured value."""

    name: NonEmptyString
    status: DataQualityCheckStatus
    value: float | None = None
    threshold: float | None = None
    detail: str | None = None


class DataQualityReport(FrozenOpenModel):
    """Build-time data-quality summary attached to a WeaveData version.

    Computed by ml_infra.data.quality.compute_quality_report() when the
    dataset snapshot is built; each WeaveData version carries its own
    report. The full per-feature statistics payload (too large to inline)
    lives at feature_stats_uri and doubles as the drift baseline for
    models trained on this snapshot. Optional on WeaveData only so
    previously stored registry objects keep loading — the registry publish
    path requires it for quality-gated data kinds.
    """

    computed_at: datetime
    row_count: int = Field(ge=0)
    checks: tuple[DataQualityCheck, ...] = Field(default_factory=tuple)
    label_balance: dict[str, float] | None = None
    null_rates: dict[str, float] | None = None
    degenerate_vector_count: int | None = Field(default=None, ge=0)
    feature_stats_uri: str | None = None

    def failed_checks(self) -> tuple[str, ...]:
        return tuple(check.name for check in self.checks if check.status == "fail")

    def warned_checks(self) -> tuple[str, ...]:
        return tuple(check.name for check in self.checks if check.status == "warn")


class DataProducer(FrozenOpenModel):
    """One model + prompt that produced or transformed rows in this dataset.

    LLM-derived datasets (canonicalized docs, judged labels, classified
    turns, compacted traces) and embedding corpora are only reproducible —
    and only safely mixable — when the producing model and prompt version
    are part of the data contract. Merge helpers should refuse to combine
    datasets whose producers differ for the same role. Common roles:
    "canonicalizer", "judge", "classifier", "embedder", "compactor",
    "generator".
    """

    role: NonEmptyString
    model_id: NonEmptyString
    prompt_sha256: Sha256Hex | None = None
    params_sha256: Sha256Hex | None = None
    version: str | None = None


class Lineage(FrozenOpenModel):
    source_data_ids: tuple[NonEmptyString, ...] = Field(default_factory=tuple)
    source_paths: tuple[NonEmptyString, ...] = Field(default_factory=tuple)
    producer: str | None = None
    code_version: str | None = None
    # Models/prompts that generated or transformed the payload rows.
    producers: tuple[DataProducer, ...] = Field(default_factory=tuple)
    # Set when this dataset replaces an earlier version in a migration
    # (e.g. re-embedding a corpus under a new embedding model).
    supersedes_data_id: str | None = None


KIND_TO_CONTRACT: dict[WeaveDataKind, type[PayloadContract]] = {
    WeaveDataKind.RL_TRAJECTORY: RLTrajectoryPayloadContract,
    WeaveDataKind.FEEDBACK_LABELS: FeedbackLabelsPayloadContract,
    WeaveDataKind.RUN_RESULTS: RunResultsPayloadContract,
    WeaveDataKind.SUPERVISED_EXAMPLES: SupervisedPayloadContract,
}


class WeaveData(FrozenOpenModel):
    object_type: Literal["WeaveData"] = "WeaveData"
    data_id: NonEmptyString
    data_kind: WeaveDataKind
    schema_version: NonEmptyString
    version: NonEmptyString
    storage_uri: NonEmptyString
    content_sha256: Sha256Hex
    row_count: int | None = Field(default=None, ge=0)
    payload_contract: PayloadContractType
    lineage: Lineage = Field(default_factory=Lineage)
    quality: DataQualityReport | None = None
    metadata: dict[str, Any] = Field(default_factory=dict)

    @model_validator(mode="before")
    @classmethod
    def coerce_payload_contract(cls, data: Any) -> Any:
        if not isinstance(data, dict):
            return data

        contract = data.get("payload_contract")
        if not isinstance(contract, dict):
            return data

        kind = data.get("data_kind")
        kind_value = kind.value if isinstance(kind, WeaveDataKind) else kind
        data = dict(data)

        try:
            kind_enum = WeaveDataKind(kind_value)
        except ValueError:
            kind_enum = None
        contract_cls = KIND_TO_CONTRACT.get(kind_enum, PayloadContract)
        data["payload_contract"] = contract_cls.model_validate(contract)

        return data

    @model_validator(mode="after")
    def check_payload_contract_matches_kind(self) -> Self:
        contract_cls = KIND_TO_CONTRACT.get(self.data_kind)
        if contract_cls is not None and not isinstance(
            self.payload_contract,
            contract_cls,
        ):
            raise ValueError(
                f"{self.data_kind.value} WeaveData requires {contract_cls.__name__}"
            )

        return self
