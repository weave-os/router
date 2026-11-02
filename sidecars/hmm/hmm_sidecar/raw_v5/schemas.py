"""Versioned schemas for the HMM delegator router."""

from __future__ import annotations

from enum import Enum
from pathlib import Path
from typing import Any, Literal

from pydantic import BaseModel, ConfigDict, Field, model_validator

from hmm_sidecar.raw_v5.complexity_labels import (
    BALANCED_LABEL,
    COMPLEXITY_CLASSES,
    FAST_LABEL,
    HIGH_LABEL,
    LEGACY_BALANCED_LABEL,
    LEGACY_EXPLORE_LABEL,
    LEGACY_FAST_LABEL,
    LEGACY_FOUR_CLASS_COMPLEXITY_CLASSES,
    LEGACY_FIVE_CLASS_COMPLEXITY_CLASSES,
    LOW_LABEL,
    MAXIMUM_LABEL,
    MEDIUM_LABEL,
    RouterMode,
)
from hmm_sidecar.raw_v5.roster_schema import RosterSchemaVersion

FEEDBACK_DATASET_SCHEMA_VERSION = "hmm_router_feedback_dataset_v5"
PREVIOUS_FEEDBACK_DATASET_SCHEMA_VERSION = "hmm_router_feedback_dataset_v4"
LEGACY_FEEDBACK_DATASET_SCHEMA_VERSION = "hmm_router_feedback_dataset_v3"
CLUSTER_ROSTER_SCHEMA_VERSION = RosterSchemaVersion.AA
# Deterministic per-harness roster over the FIVE-class taxonomy prod's live
# classifier still emits (fast/explore/balanced/high/maximum). Same ranking
# semantics as v5; only the cluster set differs, so prod can serve deterministic
# AA-ranked arms without first retraining a four-class classifier.
FIVE_CLUSTER_ROSTER_SCHEMA_VERSION = RosterSchemaVersion.AA_FIVE_CLUSTER
# v6 carries per-arm WMI scores so Go can apply within-cluster hysteresis at
# decision time without re-computing them from the raw snapshot.
CLUSTER_ROSTER_SCHEMA_VERSION_WMI = RosterSchemaVersion.WMI
FIVE_CLUSTER_ROSTER_SCHEMA_VERSION_WMI = RosterSchemaVersion.WMI_FIVE_CLUSTER
CLUSTER_ROSTER_SCHEMA_VERSION_INDICES = RosterSchemaVersion.INDICES
FIVE_CLUSTER_ROSTER_SCHEMA_VERSION_INDICES = RosterSchemaVersion.INDICES_FIVE_CLUSTER
PREVIOUS_CLUSTER_ROSTER_SCHEMA_VERSION = RosterSchemaVersion.PREVIOUS
LEGACY_CLUSTER_ROSTER_SCHEMA_VERSION = RosterSchemaVersion.LEGACY
OLDEST_CLUSTER_ROSTER_SCHEMA_VERSION = RosterSchemaVersion.OLDEST


class FrozenModel(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True, protected_namespaces=())


class CognitiveAction(str, Enum):
    ANSWER = "Answer"
    PLAN = "Plan"
    RETRIEVE = "Retrieve"
    TRANSFORM = "Transform"
    DELEGATE = "Delegate"
    VERIFY = "Verify"
    ASK = "Ask"
    SYNTHESIZE = "Synthesize"
    LEARN = "Learn"
    MONITOR = "Monitor"


COGNITIVE_ACTIONS: tuple[CognitiveAction, ...] = tuple(CognitiveAction)


class ComplexityCluster(str, Enum):
    """Supported classifier labels across current and legacy artifacts.

    New artifacts use the low/medium/high/maximum vocabulary. Literal legacy
    values remain runtime compatibility values for immutable artifacts; they
    are never emitted by current training.
    """

    LOW = LOW_LABEL
    MEDIUM = MEDIUM_LABEL
    # Deprecated source aliases used throughout the current code. Their values
    # are current; LEGACY_FAST/LEGACY_BALANCED represent old artifact strings.
    FAST = FAST_LABEL
    BALANCED = BALANCED_LABEL
    HIGH = HIGH_LABEL
    MAXIMUM = MAXIMUM_LABEL
    LEGACY_FAST = LEGACY_FAST_LABEL
    EXPLORE = LEGACY_EXPLORE_LABEL
    LEGACY_BALANCED = LEGACY_BALANCED_LABEL


CURRENT_COMPLEXITY_CLUSTERS: tuple[ComplexityCluster, ...] = tuple(
    ComplexityCluster(label) for label in COMPLEXITY_CLASSES
)
PREVIOUS_COMPLEXITY_CLUSTERS: tuple[ComplexityCluster, ...] = tuple(
    ComplexityCluster(label) for label in LEGACY_FOUR_CLASS_COMPLEXITY_CLASSES
)
LEGACY_COMPLEXITY_CLUSTERS: tuple[ComplexityCluster, ...] = tuple(
    ComplexityCluster(label) for label in LEGACY_FIVE_CLASS_COMPLEXITY_CLASSES
)


DEFAULT_ROSTER_CONFIG_PATH = (
    Path(__file__).resolve().parent / "rosters" / "roster_v7.json"
)
# Last generated five-class roster, kept as a fixture for the loaders that still
# accept the legacy taxonomy. Nothing generates or serves it.
FIVE_CLUSTER_ROSTER_CONFIG_PATH = (
    Path(__file__).resolve().parent / "tests" / "fixtures" / "roster_v7_5c.json"
)
PREVIOUS_ROSTER_CONFIG_PATH = (
    Path(__file__).resolve().parent / "rosters" / "roster_v4.json"
)


class ArmIndices(FrozenModel):
    wii_v1: float = Field(ge=0.0, le=100.0)
    wpi_v1: float = Field(ge=0.0, le=100.0)


class ClusterSpec(FrozenModel):
    complexity_label: str
    arms: tuple[str, ...] = Field(min_length=1)
    arms_by_harness: dict[str, tuple[str, ...]] = Field(default_factory=dict)
    cost_ref_usd: float = Field(gt=0.0)
    latency_ref_ms: float = Field(gt=0.0)
    # Per-arm WMI scores keyed by arm ID, present in v6+ rosters.  The value is
    # the WMI composite used for ranking within this cluster (WMI primary, AA
    # Agentic Index fallback shifted below the WMI floor).  Go reads this for
    # within-cluster hysteresis at decision time.
    arm_scores: dict[str, float] = Field(default_factory=dict)
    arm_indices: dict[str, ArmIndices] = Field(default_factory=dict)
    manual_pins_by_harness: dict[str, tuple[str, ...]] = Field(default_factory=dict)
    preferred_vendors_by_harness: dict[str, tuple[str, ...]] = Field(
        default_factory=dict
    )


class ClusterRosterConfig(FrozenModel):
    """Pinned arms using the exact taxonomy declared by the source roster."""

    # Stamped from the loaded file, so this never disagrees with
    # source_schema_version. Every supported roster generation is listed:
    # a v2 rollback artifact must report v2, not the current default.
    schema_version: Literal[
        "hmm_router_cluster_roster_v6_6c",
        "hmm_router_cluster_roster_v6",
        "hmm_router_cluster_roster_v7",
        "hmm_router_cluster_roster_v7_5c",
        "hmm_router_cluster_roster_v5",
        "hmm_router_cluster_roster_v5_5c",
        "hmm_router_cluster_roster_v4",
        "hmm_router_cluster_roster_v3",
        "hmm_router_cluster_roster_v2",
    ] = CLUSTER_ROSTER_SCHEMA_VERSION_INDICES
    source_path: str
    source_sha256: str
    source_schema_version: str = CLUSTER_ROSTER_SCHEMA_VERSION_INDICES
    legacy_explore_merged: bool = False
    clusters: dict[ComplexityCluster, ClusterSpec]

    @model_validator(mode="after")
    def check_cluster_coverage(self) -> "ClusterRosterConfig":
        raise NotImplementedError("not on the classifier serving path")

    def spec_for(self, cluster: ComplexityCluster) -> ClusterSpec:
        raise NotImplementedError("not on the classifier serving path")

    def arms_for(
        self,
        cluster: ComplexityCluster,
        harness: str | None = None,
    ) -> tuple[str, ...]:
        raise NotImplementedError("not on the classifier serving path")

    def action_union(self) -> tuple[str, ...]:
        raise NotImplementedError("not on the classifier serving path")

    def cluster_for_label(self, label: str) -> ComplexityCluster:
        raise NotImplementedError("not on the classifier serving path")


def _cluster_spec_from_payload(
    raw_spec: object,
    *,
    key: str,
    expected_label: str,
) -> ClusterSpec:
    raise NotImplementedError("not on the classifier serving path")


def load_cluster_roster(
    path: Path | str = DEFAULT_ROSTER_CONFIG_PATH,
) -> ClusterRosterConfig:
    raise NotImplementedError("not on the classifier serving path")


class RouteDecision(FrozenModel):
    """Classifier + contextual arm-selector decision using the canonical taxonomy."""

    schema_version: Literal["hmm_router_route_decision_v4"] = (
        "hmm_router_route_decision_v4"
    )
    cluster: ComplexityCluster
    complexity_label: str
    target_model: str
    arm_index: int = Field(ge=0)
    propensity: float = Field(gt=0.0, le=1.0)
    classifier_probs: dict[str, float] = Field(default_factory=dict)
    classifier_confidence: float | None = Field(default=None, ge=0.0, le=1.0)
    policy_snapshot_id: str | None = None
    preserve_tools: bool = False
    fallback_used: bool = False
    reason: str
    # Session-scoped router mode (`/router-mode`) that was in force, and the
    # class the classifier actually predicted before any mode clamp. These are
    # purely additive diagnostics: `router_mode` is None and `mode_clamped` is
    # False for every decision made without a mode, so unclamped decisions
    # serialize exactly as before.
    #
    # `mode_clamped` is deliberately NOT folded into `fallback_used`: a clamp is
    # a deliberate product control, whereas fallback_used means the predicted
    # cluster had no eligible arm. Offline consumers (bandit refit, complexity
    # dataset builds) must exclude clamped rows for a different reason than
    # fallback rows -- a clamped row is counterfactual w.r.t. the label.
    router_mode: RouterMode | None = None
    predicted_complexity_label: str | None = None
    mode_clamped: bool = False
    # Human-facing route status produced by the live canonicalizer from the
    # HMM state readout plus XGBoost/TreeSHAP factors for this decision.
    state_narration: str | None = None
    routing_explanation: str | None = None
    # TreeSHAP decomposition of the chosen class: named factors with signed
    # contributions (rides into route artifacts, the log, and the marker).
    attribution: dict[str, Any] | None = None

    @model_validator(mode="after")
    def check_label(self) -> "RouteDecision":
        raise NotImplementedError("not on the classifier serving path")

    def validate_against_roster(
        self,
        roster: ClusterRosterConfig,
        harness: str | None = None,
    ) -> "RouteDecision":
        raise NotImplementedError("not on the classifier serving path")

    @property
    def confidence(self) -> float | None:
        raise NotImplementedError("not on the classifier serving path")


class RouterHarness(str, Enum):
    CLAUDE_CODE = "claude_code"
    CODEX = "codex"
    PI = "pi"
    CURSOR = "cursor"
    OPENCODE = "opencode"
    API = "api"
    UNKNOWN = "unknown"


class ModelRoster(FrozenModel):
    low: str = "deepseek/deepseek-v4.1-flash"
    medium: str = "moonshotai/kimi-k2.7-code"
    high: str = "anthropic/claude-sonnet-5"
    maximum: str = "anthropic/claude-fable-5"

    @property
    def fast(self) -> str:
        raise NotImplementedError("not on the classifier serving path")

    @property
    def balanced(self) -> str:
        raise NotImplementedError("not on the classifier serving path")

    def model_for_cluster(self, cluster: ComplexityCluster) -> str:
        raise NotImplementedError("not on the classifier serving path")


DEFAULT_MODEL_ROSTER = ModelRoster()
DEFAULT_DELEGATOR_MODEL = "minimax/minimax-m3"

MODEL_ID_ALIASES: dict[str, str] = {
    "trinity": "arcee-ai/trinity-large-thinking",
    "trinity-large-thinking": "arcee-ai/trinity-large-thinking",
    "minimax": DEFAULT_DELEGATOR_MODEL,
    "minimax-m3": DEFAULT_DELEGATOR_MODEL,
    "kimi": DEFAULT_MODEL_ROSTER.medium,
    "kimi-k2.7": DEFAULT_MODEL_ROSTER.medium,
    "kimi-k2.7-code": DEFAULT_MODEL_ROSTER.medium,
    "moonshotai/kimi-k2.7": DEFAULT_MODEL_ROSTER.medium,
    "deepseek-v4-flash": "deepseek/deepseek-v4-flash",
    "deepseek-v4.1-flash": DEFAULT_MODEL_ROSTER.low,
    "sonnet-5": DEFAULT_MODEL_ROSTER.high,
    "claude-sonnet-5": DEFAULT_MODEL_ROSTER.high,
    "fable-5": DEFAULT_MODEL_ROSTER.maximum,
    "claude-fable-5": DEFAULT_MODEL_ROSTER.maximum,
    "gpt-5.5": "openai/gpt-5.5",
}


def canonical_model_id(model: str) -> str:
    raise NotImplementedError("not on the classifier serving path")


class HMMStateSummary(FrozenModel):
    state_id: int | None = None
    state_label: str | None = None
    state_description: str | None = None
    posterior: float | None = Field(default=None, ge=0.0, le=1.0)
    posterior_margin: float | None = Field(default=None, ge=0.0, le=1.0)
    state_path: tuple[int, ...] = Field(default_factory=tuple)
    transition_key: str | None = None
    transition_name: str | None = None
    transition_description: str | None = None


class ClassifierReadouts(FrozenModel):
    session: dict[str, float] = Field(default_factory=dict)
    latest_turn: dict[str, float] = Field(default_factory=dict)

    def score(self, key: str, *, default: float = 0.0) -> float:
        raise NotImplementedError("not on the classifier serving path")


class ContextSignals(FrozenModel):
    harness: RouterHarness = RouterHarness.UNKNOWN
    input_token_estimate: int | None = Field(default=None, ge=0)
    context_window: int | None = Field(default=None, ge=1)
    has_tools: bool = False
    requires_tool_use: bool = False
    has_retrieval_context: bool = False
    high_stakes: bool = False
    user_requested_fast: bool = False
    user_requested_strong: bool = False
    uncertainty: float | None = Field(default=None, ge=0.0, le=1.0)

    @property
    def context_pressure(self) -> float:
        raise NotImplementedError("not on the classifier serving path")


class CognitiveStatePacket(FrozenModel):
    schema_version: Literal["hmm_router_cognitive_state_packet_v1"] = (
        "hmm_router_cognitive_state_packet_v1"
    )
    request_id: str | None = None
    action: CognitiveAction
    action_confidence: float | None = Field(default=None, ge=0.0, le=1.0)
    latest_user_request: str
    recent_user_turns: tuple[str, ...] = Field(default_factory=tuple)
    source_canonical_prompt: str | None = None
    hmm: HMMStateSummary = Field(default_factory=HMMStateSummary)
    readouts: ClassifierReadouts = Field(default_factory=ClassifierReadouts)
    context: ContextSignals = Field(default_factory=ContextSignals)
    constraints: tuple[str, ...] = Field(default_factory=tuple)
    available_tools: tuple[str, ...] = Field(default_factory=tuple)
    metadata: dict[str, Any] = Field(default_factory=dict)


class CompactMessage(FrozenModel):
    role: Literal["system", "user", "assistant", "tool"]
    content: str
    name: str | None = None
    tool_call_id: str | None = None


class DelegatorDecision(FrozenModel):
    schema_version: Literal["hmm_router_delegator_decision_v4"] = (
        "hmm_router_delegator_decision_v4"
    )
    cluster: ComplexityCluster
    target_model: str
    compact_messages: tuple[CompactMessage, ...] = Field(default_factory=tuple)
    preserved_facts: tuple[str, ...] = Field(default_factory=tuple)
    dropped_context_summary: str | None = None
    confidence: float = Field(ge=0.0, le=1.0)
    preserve_tools: bool = False
    fallback_used: bool = False
    reason: str

    @model_validator(mode="after")
    def check_target_model(self) -> "DelegatorDecision":
        raise NotImplementedError("not on the classifier serving path")


class DelegatorCallAudit(FrozenModel):
    model: str
    request_sha256: str
    prompt_token_estimate: int = Field(ge=0)
    response_sha256: str | None = None
    completion_token_estimate: int | None = Field(default=None, ge=0)
    reported_prompt_tokens: int | None = Field(default=None, ge=0)
    reported_completion_tokens: int | None = Field(default=None, ge=0)
    reported_total_tokens: int | None = Field(default=None, ge=0)
    reported_cost_usd: float | None = Field(default=None, ge=0.0)
    raw_usage: dict[str, Any] = Field(default_factory=dict)


class PromptAudit(FrozenModel):
    original_sha256: str
    compacted_sha256: str
    baseline_model: str | None = None
    downstream_model: str | None = None
    original_bytes: int = Field(ge=0)
    compacted_bytes: int = Field(ge=0)
    original_token_estimate: int | None = Field(default=None, ge=0)
    compacted_token_estimate: int | None = Field(default=None, ge=0)
    route_input_token_estimate: int | None = Field(default=None, ge=0)
    route_output_token_estimate_excluding_downstream: int | None = Field(
        default=None,
        ge=0,
    )
    delegator: DelegatorCallAudit | None = None
    downstream_priced_by: str = "unchanged_weave_router_catalog"
    cost_accounting_note: str = (
        "The unchanged Weave Router prices the selected downstream request after "
        "HMM compaction. Add the delegator component here when comparing against "
        "the original baseline model prompt."
    )

    @property
    def byte_savings_ratio(self) -> float:
        raise NotImplementedError("not on the classifier serving path")
