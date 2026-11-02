"""LLM output validation schemas."""

from __future__ import annotations

from typing import Annotated, Any, Literal

from pydantic import (
    AliasChoices,
    BaseModel,
    BeforeValidator,
    ConfigDict,
    Field,
    StrictBool,
    field_validator,
    model_validator,
)

SESSION_SKILL_APPLICABILITY_SCHEMA_VERSION = (
    "agent_flow_session_skill_applicability_v1"
)
HMM_ROUTER_SESSION_CONTEXT_SCHEMA_VERSION = (
    "hmm_router_stage2_session_context_v1"
)
HMM_ROUTER_TURN_CLASSIFICATION_SCHEMA_VERSION = (
    "hmm_router_stage2_turn_classification_v4"
)
HMM_ROUTER_RAW_TURN_CLASSIFICATION_SCHEMA_VERSION = (
    "hmm_router_stage2_turn_classification_v5"
)
HMM_ROUTER_PRE_DUAL_VIEW_TURN_CLASSIFICATION_SCHEMA_VERSION = (
    "hmm_router_stage2_turn_classification_v2"
)
HMM_ROUTER_LEGACY_TURN_CLASSIFICATION_SCHEMA_VERSION = (
    "hmm_router_stage2_turn_classification_v1"
)
CANONICAL_SESSION_SCHEMA_VERSION = "stage2_canonical_session_v2"
CANONICAL_ROUND_SCHEMA_VERSION = "stage2_canonical_round_v2"
AGENT_FLOW_ROUND_VIEW_SCHEMA_VERSION = "agent_flow_round_view_v2"
HMM_ROUTER_ROUND_VIEW_SCHEMA_VERSION = "hmm_router_round_view_v1"
SESSION_SKILL_APPLICABILITY_FIELDS = (
    "terminal_completion",
    "overall_planning",
    "overall_verification",
    "overall_task_handling",
    "overall_agent_parallelization",
    "overall_code_quality",
    "overall_cognitive_surrender",
    "overall_collaboration",
)

EvidenceTurnIndex = Annotated[int, Field(strict=True, ge=0)]


def _best_effort_evidence_turn_indices(value: object) -> tuple[int, ...]:
    raise NotImplementedError("not on the classifier serving path")


EvidenceTurnIndices = Annotated[
    tuple[EvidenceTurnIndex, ...],
    BeforeValidator(_best_effort_evidence_turn_indices),
]


def _best_effort_evidence_refs(value: object) -> tuple[str, ...]:
    raise NotImplementedError("not on the classifier serving path")


EvidenceRefs = Annotated[
    tuple[str, ...],
    BeforeValidator(_best_effort_evidence_refs),
]


def _supply_missing_best_effort_field(
    value: object,
    *,
    field: str,
) -> object:
    raise NotImplementedError("not on the classifier serving path")


TurnType = Literal[
    "providing_info",
    "planning",
    "learning",
    "user_mistake",
    "tool_call",
    "confirmation",
    "refinement",
]
Modality = Literal[
    "text", "image", "file_ref", "code_snippet", "url", "slash_command"
]
ComplexityClassification = Literal[
    "low",
    "medium",
    "high",
    "maximum",
]
HMMRouterCognitiveRole = Literal[
    "planning",
    "information_gathering",
    "information_synthesis",
    "execution",
]
HMM_ROUTER_COGNITIVE_ROLES: tuple[HMMRouterCognitiveRole, ...] = (
    "planning",
    "information_gathering",
    "information_synthesis",
    "execution",
)
COMPLEXITY_CLASSIFICATION_LABELS = frozenset(
    ("low", "medium", "high", "maximum")
)
_COMPLEXITY_CLASSIFICATION_RANK: dict[ComplexityClassification, int] = {
    "low": 0,
    "medium": 1,
    "high": 2,
    "maximum": 3,
}
LEGACY_COMPLEXITY_CLASSIFICATION_ALIASES: dict[str, ComplexityClassification] = {
    "fast": "low",
    "explore": "low",
    "balanced": "medium",
    "Simple Tool Call Request": "low",
    "SPAWN_EXPLORE": "low",
    "Simple Followup": "medium",
    "Complex Followup": "high",
    "Complex Design": "maximum",
}


def canonical_complexity_classification(value: object) -> object:
    raise NotImplementedError("not on the classifier serving path")


class SessionSummary(BaseModel, frozen=True):
    """Summarized session goal and interaction flow."""

    session_goal: str = Field(min_length=1)
    interaction_summary: str = Field(min_length=1)
    terminal_completion: int = Field(ge=1, le=10)
    terminal_completion_reasoning: str = Field(min_length=1)
    overall_planning: int = Field(ge=1, le=10)
    overall_verification: int = Field(
        ge=1,
        le=10,
        validation_alias=AliasChoices(
            "overall_verification",
            "overall_validation",
            "engineer_validated",
        ),
    )
    overall_task_handling: int = Field(ge=1, le=10)
    overall_agent_parallelization: int = Field(ge=1, le=10)
    overall_code_quality: int = Field(ge=1, le=10)
    overall_cognitive_surrender: int = Field(ge=1, le=10)
    overall_collaboration: int = Field(ge=1, le=10)


class HMMRouterSessionSummary(BaseModel):
    """Score-free session context used only for HMM turn labeling."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    schema_version: Literal["hmm_router_stage2_session_context_v1"]
    session_goal: str = Field(min_length=1)
    interaction_summary: str = Field(min_length=1)


class SessionSkillApplicabilityEntry(BaseModel):
    """Whether one session skill had a meaningful opportunity or need."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    applicable: StrictBool
    reasoning: str = Field(min_length=1)
    evidence_turn_indices: EvidenceTurnIndices

    @model_validator(mode="before")
    @classmethod
    def _supply_missing_evidence_turn_indices(cls, value: object) -> object:
        raise NotImplementedError("not on the classifier serving path")

    @field_validator("reasoning")
    @classmethod
    def _reasoning_must_not_be_blank(cls, value: str) -> str:
        raise NotImplementedError("not on the classifier serving path")


class SessionSkillApplicability(BaseModel):
    """Strict applicability judgements for the fixed session-skill roster."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    terminal_completion: SessionSkillApplicabilityEntry
    overall_planning: SessionSkillApplicabilityEntry
    overall_verification: SessionSkillApplicabilityEntry
    overall_task_handling: SessionSkillApplicabilityEntry
    overall_agent_parallelization: SessionSkillApplicabilityEntry
    overall_code_quality: SessionSkillApplicabilityEntry
    overall_cognitive_surrender: SessionSkillApplicabilityEntry
    overall_collaboration: SessionSkillApplicabilityEntry


class SessionChunkSummary(BaseModel, frozen=True):
    """Partial summary for one oversized transcript chunk."""

    chunk_goal: str = Field(min_length=1)
    chunk_summary: str = Field(min_length=1)
    completion_evidence: str = Field(min_length=1)
    planning_evidence: str = Field(min_length=1)
    verification_evidence: str = Field(
        min_length=1,
        validation_alias=AliasChoices("verification_evidence", "validation_evidence"),
    )
    engineer_verification_evidence: str | None = Field(
        default=None,
        min_length=1,
        validation_alias=AliasChoices(
            "engineer_verification_evidence",
            "engineer_validation_evidence",
        ),
    )
    task_handling_evidence: str = Field(min_length=1)
    agent_parallelization_evidence: str = Field(min_length=1)
    code_quality_evidence: str = Field(min_length=1)
    cognitive_surrender_evidence: str = Field(min_length=1)
    collaboration_evidence: str = Field(min_length=1)


class HMMRouterSessionChunkSummary(BaseModel):
    """Score-free partial context for one oversized session chunk."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    chunk_goal: str = Field(min_length=1)
    chunk_summary: str = Field(min_length=1)


class EngineerJudgement(BaseModel, frozen=True):
    """Quality scores for an engineer's interaction turn."""

    type: TurnType
    eval_score: int = Field(ge=1, le=10)
    importance: int = Field(ge=1, le=10)
    ambiguity: int = Field(ge=1, le=10)
    context_completeness: int = Field(ge=1, le=10)
    turn_difficulty: int | None = Field(default=None, ge=1, le=10)
    turn_difficulty_reasoning: str | None = Field(default=None, min_length=1)
    modality: Modality
    scope_clarity: int = Field(ge=1, le=10)
    verification: int = Field(ge=1, le=10)
    frustration: int = Field(ge=1, le=10)
    cognitive_surrender: int = Field(ge=1, le=10)
    collaboration: int = Field(ge=1, le=10)


class AgentJudgement(BaseModel, frozen=True):
    """Quality scores for an agent's observable actions."""

    eval_score: int = Field(ge=1, le=10)
    response_quality: int = Field(ge=1, le=10)
    task_completion: int = Field(ge=1, le=10)
    addressed_request: int = Field(ge=1, le=10)
    clarification_sought: bool
    scope_adherence: int = Field(ge=1, le=10)


class AgentToolSummary(BaseModel, frozen=True):
    """Canonical summary of observable agent tool behavior."""

    objective: str = Field(min_length=1)
    actions_taken: tuple[str, ...] = Field(default_factory=tuple)
    evidence_used: tuple[str, ...] = Field(default_factory=tuple)
    changes_made: tuple[str, ...] = Field(default_factory=tuple)
    verification: tuple[str, ...] = Field(
        default_factory=tuple,
        validation_alias=AliasChoices("verification", "validation"),
    )
    errors_or_blockers: tuple[str, ...] = Field(default_factory=tuple)
    omitted_detail: str = Field(min_length=1)


class CanonicalTurn(BaseModel, frozen=True):
    """Canonical turn text source for sequence embeddings."""

    user_request: str = Field(min_length=1)
    user_intent: str = Field(min_length=1)
    constraints_guidelines: tuple[str, ...] = Field(default_factory=tuple)
    context_supplied: tuple[str, ...] = Field(default_factory=tuple)
    clarifications_corrections: tuple[str, ...] = Field(default_factory=tuple)
    agent_tool_summary: AgentToolSummary
    observed_outcome: str = Field(min_length=1)
    open_state_after_turn: str = Field(min_length=1)


CanonicalEvidenceKind = Literal[
    "user_turn",
    "agent_tool_trace",
    "agent_response",
    "source_context",
]


class CanonicalSourceEvidence(BaseModel, frozen=True):
    """Content-free binding from a canonical fact to complete source evidence."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    evidence_id: str = Field(min_length=1)
    kind: CanonicalEvidenceKind
    content_sha256: str = Field(pattern=r"^[0-9a-f]{64}$")
    content_chars: int = Field(ge=0)


class CanonicalFact(BaseModel, frozen=True):
    """One score-free derived fact with auditable source references."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    text: str = Field(min_length=1)
    evidence_refs: EvidenceRefs = Field(json_schema_extra={"minItems": 1})

    @model_validator(mode="before")
    @classmethod
    def _supply_missing_evidence_refs(cls, value: object) -> object:
        raise NotImplementedError("not on the classifier serving path")


def _best_effort_canonical_facts(value: object) -> tuple[CanonicalFact, ...]:
    raise NotImplementedError("not on the classifier serving path")


CanonicalEvidenceFacts = Annotated[
    tuple[CanonicalFact, ...],
    BeforeValidator(_best_effort_canonical_facts),
]


CanonicalActor = Literal[
    "engineer",
    "agent",
    "subagent",
    "tool",
    "external",
    "unknown",
]


AgentFlowEvidenceBasis = Literal[
    "direct_engineer_statement",
    "direct_agent_statement",
    "tool_trace_observation",
    "external_observation",
    "cross_source_corroboration",
    "derived_from_cited_sources",
]
AgentFlowMateriality = Literal["routine", "material", "high_impact", "unknown"]
AgentFlowLifecycleStatus = Literal[
    "requested",
    "proposed",
    "planned",
    "active",
    "blocked",
    "paused",
    "completed",
    "failed",
    "deferred",
    "cancelled",
    "superseded",
    "unknown",
]


class AgentFlowSemanticRecord(BaseModel, frozen=True):
    """Common provenance and identity for one typed Agent Flow semantic record."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    record_id: str = Field(min_length=1)
    subject_key: str = Field(
        min_length=1,
        description=(
            "Stable concise key for the same requirement, artifact, workstream, "
            "claim, or decision when it recurs in later round views."
        ),
    )
    sequence_index: int | None = Field(
        ge=0,
        description=(
            "Best visible zero-based order within the round; null when source "
            "telemetry does not establish relative order."
        ),
    )
    evidence_basis: AgentFlowEvidenceBasis
    evidence_refs: EvidenceRefs = Field(json_schema_extra={"minItems": 1})

    @model_validator(mode="before")
    @classmethod
    def _supply_missing_evidence_refs(cls, value: object) -> object:
        raise NotImplementedError("not on the classifier serving path")


class AgentFlowRequirementRecord(AgentFlowSemanticRecord):
    """One requested outcome, boundary, constraint, or success condition."""

    requirement_type: Literal[
        "desired_outcome",
        "scope_inclusion",
        "scope_exclusion",
        "constraint",
        "output_requirement",
        "acceptance_condition",
        "intended_use",
        "requested_action",
        "human_only_context",
        "other",
    ]
    description: str = Field(min_length=1)
    source_actor: CanonicalActor
    change_type: Literal[
        "introduced",
        "reaffirmed",
        "refined",
        "added",
        "replaced",
        "narrowed",
        "cancelled",
    ]
    status: Literal[
        "active",
        "satisfied",
        "partially_satisfied",
        "unsatisfied",
        "superseded",
        "cancelled",
        "unknown",
    ]
    materiality: AgentFlowMateriality


class AgentFlowPlanRecord(AgentFlowSemanticRecord):
    """One prospective plan step or evidence-driven revision."""

    planner: CanonicalActor
    objective: str = Field(min_length=1)
    step: str = Field(min_length=1)
    status: Literal[
        "proposed",
        "planned",
        "active",
        "completed",
        "revised",
        "abandoned",
        "unknown",
    ]
    priority: Literal["highest", "high", "normal", "low", "unspecified"]
    depends_on_record_ids: tuple[str, ...]
    success_condition_record_ids: tuple[str, ...]
    blocker_record_ids: tuple[str, ...]
    revision_of_record_id: str | None = Field(min_length=1)
    revision_trigger: str = Field(min_length=1)


class AgentFlowWorkItemRecord(AgentFlowSemanticRecord):
    """One tracked work item, commitment, blocker, or outstanding follow-up."""

    item_type: Literal[
        "action",
        "workstream",
        "commitment",
        "follow_up",
        "blocker",
        "remaining_work",
        "interrupted_work",
        "other",
    ]
    description: str = Field(min_length=1)
    owner: CanonicalActor
    status: AgentFlowLifecycleStatus
    depends_on_record_ids: tuple[str, ...]
    blocked_by_record_ids: tuple[str, ...]
    expected_output: str = Field(min_length=1)
    disposition: Literal[
        "none_visible",
        "resolved",
        "worked_around",
        "deferred",
        "escalated",
        "reprioritized",
        "removed_from_scope",
        "unknown",
    ]


class AgentFlowStateTransitionRecord(AgentFlowSemanticRecord):
    """One observable lifecycle or scope transition for tracked work."""

    subject_record_id: str | None = Field(min_length=1)
    subject: str = Field(min_length=1)
    transition_type: Literal[
        "started",
        "progressed",
        "blocked",
        "unblocked",
        "paused",
        "resumed",
        "completed",
        "failed",
        "deferred",
        "cancelled",
        "superseded",
        "reprioritized",
        "scope_changed",
    ]
    from_status: AgentFlowLifecycleStatus
    to_status: AgentFlowLifecycleStatus
    trigger: str = Field(min_length=1)
    actor: CanonicalActor


class AgentFlowParallelWorkstreamRecord(AgentFlowSemanticRecord):
    """One concurrency-like workstream with enough detail to classify its kind."""

    worker_kind: Literal[
        "llm_agent",
        "llm_subagent",
        "llm_agent_worker",
        "non_agent_tool",
        "code_runtime",
        "passive_process",
        "human",
        "unknown",
    ]
    dispatcher: CanonicalActor
    owner: CanonicalActor
    scope: str = Field(min_length=1)
    expected_output: str = Field(min_length=1)
    context_provided: str = Field(min_length=1)
    status: AgentFlowLifecycleStatus
    depends_on_record_ids: tuple[str, ...]
    concurrent_with_record_ids: tuple[str, ...]
    overlap_status: Literal[
        "observed_overlap",
        "sequential",
        "claimed_overlap",
        "unknown",
    ]
    result: str = Field(min_length=1)
    collection_status: Literal[
        "not_started",
        "pending",
        "collected",
        "partially_collected",
        "lost",
        "not_applicable",
        "unknown",
    ]
    integration_status: Literal[
        "not_started",
        "pending",
        "integrated",
        "partially_integrated",
        "rejected",
        "not_applicable",
        "unknown",
    ]
    failure_disposition: str = Field(min_length=1)
    capacity_constraint: str = Field(min_length=1)
    observed_gain: str = Field(min_length=1)


class AgentFlowInteractionRecord(AgentFlowSemanticRecord):
    """One question, feedback loop, correction, disagreement, or handoff."""

    interaction_type: Literal[
        "question",
        "answer",
        "feedback",
        "correction",
        "handoff",
        "information_share",
        "disagreement",
        "resolution",
        "position_change",
        "goal_alignment",
        "other",
    ]
    initiator: CanonicalActor
    recipient: CanonicalActor
    subject: str = Field(min_length=1)
    content: str = Field(min_length=1)
    status: Literal[
        "offered",
        "received",
        "answered",
        "incorporated",
        "rejected",
        "resolved",
        "unresolved",
        "superseded",
        "unknown",
    ]
    responds_to_record_id: str | None = Field(min_length=1)
    required_output: str = Field(min_length=1)
    effect_on_work: str = Field(min_length=1)


class AgentFlowDecisionRecord(AgentFlowSemanticRecord):
    """One proposal, decision, authorization, acceptance, or reliance event."""

    decision_type: Literal[
        "scope",
        "priority",
        "tradeoff",
        "approval",
        "authorization",
        "autonomy_boundary",
        "acceptance",
        "rejection",
        "reliance",
        "escalation",
        "other",
    ]
    decision_maker: CanonicalActor
    subject: str = Field(min_length=1)
    decision: str = Field(min_length=1)
    alternatives: tuple[str, ...]
    rationale: str = Field(min_length=1)
    impact: AgentFlowMateriality
    authorization_timing: Literal[
        "before_action",
        "after_action",
        "not_applicable",
        "unknown",
    ]
    status: Literal[
        "proposed",
        "decided",
        "approved",
        "rejected",
        "revised",
        "unknown",
    ]
    based_on_record_ids: tuple[str, ...]


class AgentFlowClaimRecord(AgentFlowSemanticRecord):
    """One material assertion kept distinct from observed or corroborated state."""

    claimant: CanonicalActor
    claim_type: Literal[
        "completion",
        "success",
        "failure",
        "artifact_state",
        "verification",
        "quality",
        "requirement",
        "other",
    ]
    claim: str = Field(min_length=1)
    support_status: Literal[
        "unsupported_assertion",
        "self_assessment_only",
        "tool_observed",
        "independently_corroborated",
        "contradicted",
        "unknown",
    ]
    supporting_record_ids: tuple[str, ...]
    engineer_response: Literal[
        "no_observable_response",
        "accepted",
        "relied_on",
        "challenged",
        "rejected",
        "requested_support",
        "not_applicable",
    ]


class AgentFlowVerificationObligationRecord(AgentFlowSemanticRecord):
    """One grounded checkable obligation, including checks not yet performed."""

    target_kind: Literal[
        "success_condition",
        "observable_behavior",
        "artifact",
        "material_claim",
        "failure_condition",
        "risk",
        "corrective_change",
        "acceptance",
        "other",
    ]
    target: str = Field(min_length=1)
    basis: Literal[
        "explicit_request",
        "explicit_acceptance_condition",
        "produced_artifact",
        "runnable_behavior",
        "material_claim",
        "observed_failure",
        "material_risk",
        "corrective_change",
        "other",
    ]
    materiality: AgentFlowMateriality
    status: Literal[
        "open",
        "partially_checked",
        "checked",
        "waived_by_engineer",
        "not_observable",
        "unknown",
    ]
    verification_record_ids: tuple[str, ...]
    remaining_gap: str = Field(min_length=1)


class AgentFlowVerificationRecord(AgentFlowSemanticRecord):
    """One performed, attempted, or explicitly omitted evidence check."""

    requester: CanonicalActor
    performer: CanonicalActor
    method_kind: Literal[
        "execution",
        "test",
        "inspection",
        "reproduction",
        "evidence_search",
        "review",
        "other",
    ]
    method: str = Field(min_length=1)
    target_kind: Literal[
        "success_condition",
        "observable_behavior",
        "artifact",
        "material_claim",
        "failure_condition",
        "risk",
        "corrective_change",
        "other",
    ]
    target: str = Field(min_length=1)
    execution_status: Literal[
        "performed",
        "attempted",
        "not_run",
        "unknown",
    ]
    result: Literal[
        "passed",
        "failed",
        "inconclusive",
        "not_run",
        "unknown",
    ]
    result_basis: Literal[
        "tool_observed",
        "external_observed",
        "agent_report",
        "engineer_report",
        "cross_source",
        "no_result_visible",
    ]
    independence: Literal[
        "independent",
        "self_check",
        "not_applicable",
        "unknown",
    ]
    interpreted_by: CanonicalActor
    interpretation: str = Field(min_length=1)
    effect_on_work: str = Field(min_length=1)
    rechecks_record_id: str | None = Field(min_length=1)
    follow_up: str = Field(min_length=1)


class AgentFlowArtifactRecord(AgentFlowSemanticRecord):
    """One requested or produced artifact and its observable delivery state."""

    artifact_type: Literal[
        "code",
        "configuration",
        "documentation",
        "analysis",
        "answer",
        "report",
        "data",
        "other",
    ]
    description: str = Field(min_length=1)
    requested_form_or_location: str = Field(min_length=1)
    observed_form_or_location: str = Field(min_length=1)
    producer: CanonicalActor
    status: Literal[
        "requested",
        "created",
        "modified",
        "delivered",
        "missing",
        "unknown",
    ]
    inspection_status: Literal[
        "not_inspected",
        "inspected",
        "partially_inspected",
        "unknown",
    ]
    usability_status: Literal[
        "usable",
        "not_usable",
        "additional_work_required",
        "unknown",
    ]
    satisfies_requirement_ids: tuple[str, ...]


class AgentFlowContextRecord(AgentFlowSemanticRecord):
    """One material context item and who supplied it."""

    provider: CanonicalActor
    context_type: Literal[
        "human_only",
        "task",
        "constraint",
        "repository_or_trace",
        "external",
        "other",
    ]
    description: str = Field(min_length=1)
    materiality: AgentFlowMateriality
    used_by_record_ids: tuple[str, ...]


class AgentFlowImplementationRecord(AgentFlowSemanticRecord):
    """One observable implementation or artifact-quality fact."""

    implementation_type: Literal[
        "change",
        "design_choice",
        "correctness_evidence",
        "maintainability_evidence",
        "testability_evidence",
        "quality_risk",
        "other",
    ]
    actor: CanonicalActor
    description: str = Field(min_length=1)
    status: Literal["observed", "reported", "proposed", "unknown"]
    artifact_record_ids: tuple[str, ...]


class AgentFlowEvidenceLimitationRecord(AgentFlowSemanticRecord):
    """One concrete limitation on what the canonical evidence can establish."""

    limitation_type: Literal[
        "compacted_tool_trace",
        "missing_tool_result_body",
        "agent_report_only",
        "external_state_unobservable",
        "sequence_not_observable",
        "other",
    ]
    description: str = Field(min_length=1)
    affected_record_ids: tuple[str, ...]


class AgentFlowTerminalStateRecord(AgentFlowSemanticRecord):
    """Separate terminal report, observed state, and unresolved work."""

    reported_by: CanonicalActor
    reported_status: Literal[
        "complete",
        "partial",
        "failed",
        "blocked",
        "cancelled",
        "no_report",
    ]
    observed_status: Literal[
        "complete",
        "partial",
        "failed",
        "blocked",
        "cancelled",
        "in_progress",
        "unknown",
    ]
    stability: Literal["stable", "in_flight", "open_work", "unknown"]
    summary: str = Field(min_length=1)
    open_work_item_ids: tuple[str, ...]
    delivered_artifact_ids: tuple[str, ...]
    unmet_requirement_ids: tuple[str, ...]


class AgentFlowRoundView(BaseModel, frozen=True):
    """Typed, score-free semantic ledger for Agent Flow skill labels."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    schema_version: Literal["agent_flow_round_view_v2"]
    requirements: tuple[AgentFlowRequirementRecord, ...] = Field(min_length=1)
    plans: tuple[AgentFlowPlanRecord, ...]
    work_items: tuple[AgentFlowWorkItemRecord, ...]
    state_transitions: tuple[AgentFlowStateTransitionRecord, ...]
    parallel_workstreams: tuple[AgentFlowParallelWorkstreamRecord, ...]
    interactions: tuple[AgentFlowInteractionRecord, ...]
    decisions: tuple[AgentFlowDecisionRecord, ...]
    claims: tuple[AgentFlowClaimRecord, ...]
    verification_obligations: tuple[
        AgentFlowVerificationObligationRecord, ...
    ]
    verification: tuple[AgentFlowVerificationRecord, ...]
    artifacts: tuple[AgentFlowArtifactRecord, ...]
    context: tuple[AgentFlowContextRecord, ...]
    implementation: tuple[AgentFlowImplementationRecord, ...]
    evidence_limitations: tuple[AgentFlowEvidenceLimitationRecord, ...]
    terminal_state: AgentFlowTerminalStateRecord

    def semantic_records(self) -> tuple[AgentFlowSemanticRecord, ...]:
        raise NotImplementedError("not on the classifier serving path")

    @model_validator(mode="after")
    def _validate_semantic_record_ids(self) -> AgentFlowRoundView:
        raise NotImplementedError("not on the classifier serving path")


class HMMRouterAgentStep(BaseModel, frozen=True):
    """One observable step within the agent turn of a round."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    step_index: int = Field(ge=0)
    kind: Literal["response", "tool_activity", "observable_result", "other"]
    summary: str = Field(min_length=1)
    outcome: str = Field(min_length=1)
    evidence_refs: EvidenceRefs = Field(json_schema_extra={"minItems": 1})

    @model_validator(mode="before")
    @classmethod
    def _supply_missing_evidence_refs(cls, value: object) -> object:
        raise NotImplementedError("not on the classifier serving path")


class HMMRouterProblemShape(BaseModel, frozen=True):
    """Difficulty-relevant facts without a model tier or target label."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    solution_method: Literal[
        "explicit",
        "standard",
        "requires_selection",
        "unknown",
        "conflicting",
    ]
    scope: Literal[
        "single_target",
        "bounded_multi_target",
        "multi_component",
        "multi_system",
        "unknown",
    ]
    information_state: Literal[
        "complete",
        "partial",
        "missing",
        "conflicting",
        "unknown",
    ]
    dependency_state: Literal[
        "none_visible",
        "known_bounded",
        "hidden_or_unknown",
        "coupled",
    ]
    tradeoff_state: Literal["none_visible", "bounded", "material", "foundational"]
    risk: Literal["negligible", "limited", "material", "high_consequence"]
    reversibility: Literal[
        "easy",
        "moderate",
        "difficult",
        "irreversible",
        "unknown",
    ]
    error_detectability: Literal[
        "obvious",
        "standard_verification",
        "subtle",
        "poorly_observable",
        "unknown",
    ]
    verification_burden: Literal[
        "none",
        "minimal",
        "bounded",
        "multi_stage",
        "system_level",
    ]
    temporal_dependency: Literal[
        "independent",
        "depends_on_prior_round",
        "prior_failure_or_correction",
        "session_wide",
    ]
    prior_attempt_state: Literal[
        "none",
        "succeeded",
        "failed",
        "partial",
        "unknown",
    ]
    evidence: CanonicalEvidenceFacts = Field(json_schema_extra={"minItems": 1})

    @model_validator(mode="before")
    @classmethod
    def _supply_missing_evidence(cls, value: object) -> object:
        raise NotImplementedError("not on the classifier serving path")


class HMMRouterUserTurnView(BaseModel, frozen=True):
    """Causal canonical user turn built without agent or future evidence."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    user_request: CanonicalFact
    user_intent: CanonicalFact
    constraints_guidelines: tuple[CanonicalFact, ...] = Field(
        default_factory=tuple
    )
    context_supplied: tuple[CanonicalFact, ...] = Field(default_factory=tuple)
    clarifications_corrections: tuple[CanonicalFact, ...] = Field(
        default_factory=tuple
    )
    problem_shape: HMMRouterProblemShape


class HMMRouterAgentTurnView(BaseModel, frozen=True):
    """Canonical completed agent turn built without later-round evidence."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    objective: CanonicalFact
    steps: tuple[HMMRouterAgentStep, ...] = Field(default_factory=tuple)
    actions_taken: tuple[CanonicalFact, ...] = Field(default_factory=tuple)
    evidence_used: tuple[CanonicalFact, ...] = Field(default_factory=tuple)
    changes_made: tuple[CanonicalFact, ...] = Field(default_factory=tuple)
    verification: tuple[CanonicalFact, ...] = Field(default_factory=tuple)
    errors_or_blockers: tuple[CanonicalFact, ...] = Field(default_factory=tuple)
    omitted_detail: CanonicalFact
    observed_outcome: CanonicalFact
    open_state_after_turn: CanonicalFact


class HMMRouterRoundView(BaseModel, frozen=True):
    """Score-free round evidence curated only for router state features."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    schema_version: Literal["hmm_router_round_view_v1"] = (
        HMM_ROUTER_ROUND_VIEW_SCHEMA_VERSION
    )
    user_turn: HMMRouterUserTurnView
    agent_turn: HMMRouterAgentTurnView


class CanonicalRoundViewNamespaces(BaseModel, frozen=True):
    """Explicit namespaces prevent one consumer from serializing both views."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    agent_flow: AgentFlowRoundView
    hmm_router: HMMRouterRoundView


RoutingActionKind = Literal["user_turn_end", "agent_step_end"]


class CanonicalRoutingAction(BaseModel, frozen=True):
    """A host-identified point where the router may make a decision."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    action_id: str = Field(min_length=1)
    kind: RoutingActionKind
    round_index: int = Field(ge=0)
    agent_step_index: int | None = Field(default=None, ge=0)

    @model_validator(mode="after")
    def _validate_step_boundary(self) -> CanonicalRoutingAction:
        raise NotImplementedError("not on the classifier serving path")


class CanonicalRound(BaseModel, frozen=True):
    """One U/A round with both consumer-specific canonical views."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    schema_version: Literal["stage2_canonical_round_v2"] = (
        CANONICAL_ROUND_SCHEMA_VERSION
    )
    round_id: str = Field(min_length=1)
    round_index: int = Field(ge=0)
    user_turn_id: str = Field(min_length=1)
    agent_turn_id: str = Field(min_length=1)
    source_evidence: tuple[CanonicalSourceEvidence, ...] = Field(min_length=2)
    views: CanonicalRoundViewNamespaces
    routing_actions: tuple[CanonicalRoutingAction, ...] = Field(min_length=1)

    @model_validator(mode="after")
    def _validate_round_contract(self) -> CanonicalRound:
        raise NotImplementedError("not on the classifier serving path")


class CanonicalSession(BaseModel, frozen=True):
    """Shared Stage 2 artifact containing isolated Agent Flow and HMM views."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    schema_version: Literal["stage2_canonical_session_v2"] = (
        CANONICAL_SESSION_SCHEMA_VERSION
    )
    source: str = Field(min_length=1)
    session_id: str = Field(min_length=1)
    conversation_id: str = Field(min_length=1)
    rounds: tuple[CanonicalRound, ...] = Field(min_length=1)

    @model_validator(mode="after")
    def _validate_session_contract(self) -> CanonicalSession:
        raise NotImplementedError("not on the classifier serving path")

    def hmm_router_rounds(self) -> tuple[tuple[int, HMMRouterRoundView], ...]:
        raise NotImplementedError("not on the classifier serving path")

    def agent_flow_rounds(self) -> tuple[tuple[int, AgentFlowRoundView], ...]:
        raise NotImplementedError("not on the classifier serving path")


class RawTurnEvidence(BaseModel, frozen=True):
    """Raw per-turn text retained for audit and future feature contracts."""

    user_prompt: str
    agent_action_trace: str


class HMMRouterCognitiveRoleRequirement(BaseModel):
    """One cognitive role required at a routing Action and its minimum tier."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    role: HMMRouterCognitiveRole
    minimum_level: ComplexityClassification


def _validate_hmm_router_role_requirements(
    role_requirements: tuple[HMMRouterCognitiveRoleRequirement, ...],
    complexity_classification: ComplexityClassification,
) -> None:
    raise NotImplementedError("not on the classifier serving path")


class TurnClassification(BaseModel, frozen=True):
    """Persisted engineer and agent judgements for a turn.

    The optional field preserves read compatibility with artifacts that
    predate complexity labels. Fresh provider output uses
    :class:`FreshTurnClassification`, which requires a current label.
    """

    complexity_classification: ComplexityClassification | None = None
    canonical_turn: CanonicalTurn
    engineer: EngineerJudgement
    agent: AgentJudgement
    raw_turn_evidence: RawTurnEvidence | None = None


class HMMRouterTurnClassification(BaseModel):
    """Persisted score-free turn contract consumed by HMM-router training."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    schema_version: Literal[
        "hmm_router_stage2_turn_classification_v1",
        "hmm_router_stage2_turn_classification_v2",
        "hmm_router_stage2_turn_classification_v4",
        "hmm_router_stage2_turn_classification_v5",
    ]
    role_requirements: tuple[HMMRouterCognitiveRoleRequirement, ...] = Field(
        default_factory=tuple,
        max_length=len(HMM_ROUTER_COGNITIVE_ROLES),
    )
    complexity_classification: ComplexityClassification
    canonical_turn: CanonicalTurn | None = None
    canonical_round: CanonicalRound | None = None
    raw_turn_evidence: RawTurnEvidence | None = None
    raw_agent_flow_turn_evidence: dict[str, Any] | None = None

    @model_validator(mode="before")
    @classmethod
    def _forbid_incompatible_evidence_fields(
        cls,
        data: object,
    ) -> object:
        raise NotImplementedError("not on the classifier serving path")

    @model_validator(mode="after")
    def _validate_role_contract(self) -> HMMRouterTurnClassification:
        raise NotImplementedError("not on the classifier serving path")


class FreshHMMRouterTurnClassification(BaseModel):
    """Strict provider response before raw evidence is attached locally."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    schema_version: Literal["hmm_router_stage2_turn_classification_v2"]
    role_requirements: tuple[HMMRouterCognitiveRoleRequirement, ...] = Field(
        min_length=1,
        max_length=len(HMM_ROUTER_COGNITIVE_ROLES),
    )
    complexity_classification: ComplexityClassification
    canonical_turn: CanonicalTurn

    @model_validator(mode="after")
    def _validate_role_contract(self) -> FreshHMMRouterTurnClassification:
        raise NotImplementedError("not on the classifier serving path")


class FreshRawHMMRouterTurnClassification(BaseModel):
    """Strict raw-evidence-only provider response before evidence is attached."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    schema_version: Literal["hmm_router_stage2_turn_classification_v5"]
    role_requirements: tuple[HMMRouterCognitiveRoleRequirement, ...] = Field(
        min_length=1,
        max_length=len(HMM_ROUTER_COGNITIVE_ROLES),
    )
    complexity_classification: ComplexityClassification

    @model_validator(mode="after")
    def _validate_role_contract(self) -> FreshRawHMMRouterTurnClassification:
        raise NotImplementedError("not on the classifier serving path")


class HMMRouterTurnCanonicalization(BaseModel):
    """Strict score-free canonical turn returned by a dedicated normalizer."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    schema_version: Literal["hmm_router_stage2_turn_canonicalization_v1"]
    canonical_turn: CanonicalTurn


class HMMRouterRoundCanonicalization(BaseModel):
    """Strict score-free HMM view returned by its dedicated round pass."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    schema_version: Literal["hmm_router_round_canonicalization_v1"]
    hmm_router: HMMRouterRoundView


class HMMRouterUserTurnCanonicalization(BaseModel):
    """Strict causal user-turn view returned without agent evidence."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    schema_version: Literal["hmm_router_user_turn_canonicalization_v1"]
    user_turn: HMMRouterUserTurnView


class HMMRouterAgentTurnCanonicalization(BaseModel):
    """Strict agent-turn view returned without later-round evidence."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    schema_version: Literal["hmm_router_agent_turn_canonicalization_v1"]
    agent_turn: HMMRouterAgentTurnView


class AgentFlowRoundCanonicalization(BaseModel):
    """Strict score-free Agent Flow view returned by its dedicated round pass."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    schema_version: Literal["agent_flow_round_canonicalization_v2"]
    agent_flow: AgentFlowRoundView


class PartialAgentFlowRoundView(BaseModel, frozen=True):
    """Evidence present in one fragment; absence never means semantic absence."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    requirements: tuple[AgentFlowRequirementRecord, ...]
    plans: tuple[AgentFlowPlanRecord, ...]
    work_items: tuple[AgentFlowWorkItemRecord, ...]
    state_transitions: tuple[AgentFlowStateTransitionRecord, ...]
    parallel_workstreams: tuple[AgentFlowParallelWorkstreamRecord, ...]
    interactions: tuple[AgentFlowInteractionRecord, ...]
    decisions: tuple[AgentFlowDecisionRecord, ...]
    claims: tuple[AgentFlowClaimRecord, ...]
    verification_obligations: tuple[
        AgentFlowVerificationObligationRecord, ...
    ]
    verification: tuple[AgentFlowVerificationRecord, ...]
    artifacts: tuple[AgentFlowArtifactRecord, ...]
    context: tuple[AgentFlowContextRecord, ...]
    implementation: tuple[AgentFlowImplementationRecord, ...]
    evidence_limitations: tuple[AgentFlowEvidenceLimitationRecord, ...]
    terminal_state: AgentFlowTerminalStateRecord | None


class PartialHMMRouterUserTurnView(BaseModel, frozen=True):
    """User evidence present in one fragment before complete synthesis."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    user_request: CanonicalFact | None = None
    user_intent: CanonicalFact | None = None
    constraints_guidelines: tuple[CanonicalFact, ...] = Field(
        default_factory=tuple
    )
    context_supplied: tuple[CanonicalFact, ...] = Field(default_factory=tuple)
    clarifications_corrections: tuple[CanonicalFact, ...] = Field(
        default_factory=tuple
    )
    problem_shape: HMMRouterProblemShape | None = None


class PartialHMMRouterAgentTurnView(BaseModel, frozen=True):
    """Agent evidence present in one fragment before complete synthesis."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    objective: CanonicalFact | None = None
    steps: tuple[HMMRouterAgentStep, ...] = Field(default_factory=tuple)
    actions_taken: tuple[CanonicalFact, ...] = Field(default_factory=tuple)
    evidence_used: tuple[CanonicalFact, ...] = Field(default_factory=tuple)
    changes_made: tuple[CanonicalFact, ...] = Field(default_factory=tuple)
    verification: tuple[CanonicalFact, ...] = Field(default_factory=tuple)
    errors_or_blockers: tuple[CanonicalFact, ...] = Field(default_factory=tuple)
    omitted_detail: CanonicalFact | None = None
    observed_outcome: CanonicalFact | None = None
    open_state_after_turn: CanonicalFact | None = None


class HMMRouterTurnCanonicalizationChunk(BaseModel):
    """Strict partial canonical turn for one oversized evidence chunk."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    evidence_summary: str = Field(min_length=1)
    canonical_turn: CanonicalTurn


class HMMRouterRoundCanonicalizationChunk(BaseModel):
    """Strict partial HMM view for one complete-evidence chunk."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    evidence_summary: str = Field(min_length=1)
    hmm_router: HMMRouterRoundView


class HMMRouterUserTurnCanonicalizationChunk(BaseModel):
    """Strict partial causal user view from one source fragment."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    evidence_summary: str = Field(min_length=1)
    user_turn: PartialHMMRouterUserTurnView


class HMMRouterAgentTurnCanonicalizationChunk(BaseModel):
    """Strict partial agent view from one source fragment."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    evidence_summary: str = Field(min_length=1)
    agent_turn: PartialHMMRouterAgentTurnView


class AgentFlowRoundCanonicalizationChunk(BaseModel):
    """Strict partial Agent Flow view for one complete-evidence chunk."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    evidence_summary: str = Field(min_length=1)
    agent_flow: PartialAgentFlowRoundView


class HMMRouterComplexityClassification(BaseModel):
    """Strict router-cluster label returned after canonicalization."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    schema_version: Literal["hmm_router_stage2_complexity_classification_v2"]
    role_requirements: tuple[HMMRouterCognitiveRoleRequirement, ...] = Field(
        min_length=1,
        max_length=len(HMM_ROUTER_COGNITIVE_ROLES),
    )
    complexity_classification: ComplexityClassification

    @model_validator(mode="after")
    def _validate_role_contract(self) -> HMMRouterComplexityClassification:
        raise NotImplementedError("not on the classifier serving path")


class TurnCanonicalization(BaseModel, frozen=True):
    """Canonical turn text source without scorer labels."""

    canonical_turn: CanonicalTurn


class FreshTurnClassification(TurnClassification, frozen=True):
    """Strict current-contract classification returned by a provider."""

    complexity_classification: ComplexityClassification


class TurnScore(BaseModel, frozen=True):
    """Scorer labels for a previously canonicalized turn."""

    complexity_classification: ComplexityClassification
    engineer: EngineerJudgement
    agent: AgentJudgement


class TurnChunkClassification(BaseModel, frozen=True):
    """Partial turn classification for one oversized turn-evidence chunk."""

    evidence_summary: str = Field(min_length=1)
    complexity_classification: ComplexityClassification
    canonical_turn: CanonicalTurn
    engineer: EngineerJudgement
    agent: AgentJudgement


class HMMRouterTurnChunkClassification(BaseModel):
    """Score-free partial HMM turn classification for chunk synthesis."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    evidence_summary: str = Field(min_length=1)
    role_requirements: tuple[HMMRouterCognitiveRoleRequirement, ...] = Field(
        min_length=1,
        max_length=len(HMM_ROUTER_COGNITIVE_ROLES),
    )
    complexity_classification: ComplexityClassification
    canonical_turn: CanonicalTurn

    @model_validator(mode="after")
    def _validate_role_contract(self) -> HMMRouterTurnChunkClassification:
        raise NotImplementedError("not on the classifier serving path")


class HMMRouterRawTurnChunkClassification(BaseModel):
    """Partial raw-evidence label for one oversized V5 source turn."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    evidence_summary: str = Field(min_length=1)
    role_requirements: tuple[HMMRouterCognitiveRoleRequirement, ...] = Field(
        min_length=1,
        max_length=len(HMM_ROUTER_COGNITIVE_ROLES),
    )
    complexity_classification: ComplexityClassification

    @model_validator(mode="after")
    def _validate_role_contract(self) -> HMMRouterRawTurnChunkClassification:
        raise NotImplementedError("not on the classifier serving path")


class ToolActionGroup(BaseModel, frozen=True):
    """One ordered group of related observable tool actions."""

    order: int = Field(ge=1)
    tools: tuple[str, ...] = Field(default_factory=tuple)
    call_count: int = Field(ge=1)
    target_paths: tuple[str, ...] = Field(default_factory=tuple)
    operation: str = Field(min_length=1)
    evidence: str = Field(min_length=1)


class ToolActionTraceCompaction(BaseModel, frozen=True):
    """Structured, compacted observable tool-call trace for one agent turn."""

    schema_version: Literal["tool_action_trace_v1"] = "tool_action_trace_v1"
    tool_call_count: int = Field(ge=0)
    turn_goal: str = Field(min_length=1)
    action_groups: tuple[ToolActionGroup, ...] = Field(default_factory=tuple)
    verification_evidence: str = Field(
        min_length=1,
        validation_alias=AliasChoices("verification_evidence", "validation_evidence"),
    )
    error_evidence: str = Field(min_length=1)
    omitted_detail: str = Field(min_length=1)


class IndexedClassification(BaseModel, frozen=True):
    """A turn classification with its position index."""

    turn_index: int
    classification: TurnClassification | HMMRouterTurnClassification
