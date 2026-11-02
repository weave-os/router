"""Lossless raw turn evidence shared by Agent Flow judges and embeddings."""

from __future__ import annotations

from collections.abc import Callable, Iterable, Mapping
from dataclasses import dataclass
from typing import Any, Final, Literal

from pydantic import (
    BaseModel,
    ConfigDict,
    Field,
    field_validator,
)

from hmm_sidecar.raw_v5.cd_models import (
    AgentTurn,
    ConversationDatasetRow,
    ConversationTrace,
    UserMessage,
)
from hmm_sidecar.raw_v5.cd_schemas import (
    ToolActionTraceCompaction,
)


RAW_AGENT_FLOW_TURN_EVIDENCE_SCHEMA_VERSION: Final = "agent_flow_raw_turn_evidence_v1"
RAW_AGENT_FLOW_TURN_EVIDENCE_PREFIX: Final = "Raw Agent Flow turn evidence:\n"
TOOL_ACTION_TRACE_SCHEMA_VERSION: Final = "tool_action_trace_v1"
HMM_ROUTER_RAW_TURN_CLASSIFICATION_SCHEMA_VERSION: Final = (
    "hmm_router_stage2_turn_classification_v5"
)
_MODEL_PLACEHOLDERS: Final = frozenset({"auto", "default", "none", "null", "unknown"})

_TOOL_COMPACTION_REQUIRED_FIELDS: Final = frozenset(
    {
        "enabled",
        "schema_version",
        "model",
        "chunk_chars",
        "max_action_groups",
        "normalized_turns",
        "tool_call_count",
        "original_action_trace_chars",
        "normalized_action_trace_chars",
        "chunked_turns",
        "turns",
    }
)
_TOOL_COMPACTION_OPTIONAL_FIELDS: Final = frozenset({"cache_reuse", "error"})
_TOOL_TURN_REQUIRED_FIELDS: Final = frozenset(
    {
        "turn_index",
        "artifact_model",
        "original_tool_call_count",
        "original_action_trace_chars",
        "normalized_action_trace_chars",
        "chunked",
        "trace",
    }
)
_TOOL_TURN_OPTIONAL_FIELDS: Final = frozenset()


class RawAgentFlowToolActionTrace(BaseModel):
    """Exact persisted per-turn compaction record and its model provenance."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    turn_index: int = Field(strict=True, ge=0)
    artifact_model: str = Field(min_length=1)
    original_tool_call_count: int = Field(strict=True, ge=1)
    original_action_trace_chars: int = Field(strict=True, ge=0)
    normalized_action_trace_chars: int = Field(strict=True, ge=0)
    chunked: bool = Field(strict=True)
    trace: ToolActionTraceCompaction

    @field_validator("artifact_model")
    @classmethod
    def _artifact_model_must_not_be_blank(cls, value: str) -> str:
        raise NotImplementedError("not on the classifier serving path")


class RawAgentFlowTurnEvidence(BaseModel):
    """One lossless source U/A turn with optional compacted tool evidence."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    schema_version: Literal["agent_flow_raw_turn_evidence_v1"] = (
        RAW_AGENT_FLOW_TURN_EVIDENCE_SCHEMA_VERSION
    )
    turn_index: int = Field(strict=True, ge=0)
    user_prompt: str
    agent_response_blocks: tuple[str, ...]
    tool_action_trace: RawAgentFlowToolActionTrace | None


@dataclass(frozen=True, slots=True)
class ToolCompactionEvidenceRepair:
    """Validated, in-memory view repairs for one legacy row artifact."""

    artifact_models_by_turn: dict[int, str]
    normalized_action_trace_chars_by_turn: dict[int, int]


def _canonical_json(value: object) -> str:
    raise NotImplementedError("not on the classifier serving path")


def _canonical_sorted_json(value: object) -> str:
    raise NotImplementedError("not on the classifier serving path")


def _unique_json_object(pairs: list[tuple[str, Any]]) -> dict[str, Any]:
    raise NotImplementedError("not on the classifier serving path")


def _parse_json_object(raw_json: str, *, label: str) -> dict[str, Any]:
    raise NotImplementedError("not on the classifier serving path")


def _require_exact_fields(
    value: dict[str, Any],
    *,
    required: frozenset[str],
    optional: frozenset[str],
    label: str,
) -> None:
    raise NotImplementedError("not on the classifier serving path")


def _strict_int(
    value: object,
    *,
    label: str,
    minimum: int = 0,
) -> int:
    raise NotImplementedError("not on the classifier serving path")


def parse_raw_agent_flow_trace(raw_trace_json: str) -> ConversationTrace:
    raise NotImplementedError("not on the classifier serving path")


def _source_turns(
    trace: ConversationTrace,
) -> tuple[tuple[int, UserMessage, AgentTurn], ...]:
    raise NotImplementedError("not on the classifier serving path")


def _validated_tool_trace(
    payload: object,
    *,
    turn_index: int,
) -> ToolActionTraceCompaction:
    raise NotImplementedError("not on the classifier serving path")


def _structured_tool_trace_chars(
    trace: ToolActionTraceCompaction,
    *,
    compaction_model: str,
    original_tool_call_count: int,
    original_action_trace_chars: int,
) -> int:
    raise NotImplementedError("not on the classifier serving path")


def _concrete_turn_model(agent_turn: AgentTurn) -> str | None:
    raise NotImplementedError("not on the classifier serving path")


def _compacted_action_trace_for_model(
    agent_turn: AgentTurn,
    turn_payload: Mapping[str, object],
    artifact_model: str,
) -> str:
    raise NotImplementedError("not on the classifier serving path")


def _tool_compaction_turn_key(
    agent_turn: AgentTurn,
    *,
    chunk_chars: int | None,
    max_action_groups: int,
) -> str:
    raise NotImplementedError("not on the classifier serving path")


def _canonical_tool_evidence_by_turn(
    turn_classifications_json: str,
) -> dict[int, tuple[int, str]]:
    raise NotImplementedError("not on the classifier serving path")


def _raw_v5_evidence_by_turn(
    turn_classifications_json: str,
) -> dict[int, RawAgentFlowTurnEvidence] | None:
    raise NotImplementedError("not on the classifier serving path")


def _raw_v5_tool_compaction_evidence_repair(
    *,
    row_index: int,
    source_trace: ConversationTrace,
    tool_compaction_json: str,
    raw_evidence_by_turn: Mapping[int, RawAgentFlowTurnEvidence],
) -> ToolCompactionEvidenceRepair:
    raise NotImplementedError("not on the classifier serving path")


def resolve_tool_compaction_source_keyed_evidence_repair(
    *,
    row_index: int,
    source_trace: ConversationTrace,
    tool_compaction_json: str,
    cached_turn_lookup: Callable[[str], str | None] | None,
    allow_model_agnostic_local_reuse: bool = False,
) -> ToolCompactionEvidenceRepair:
    raise NotImplementedError("not on the classifier serving path")


def resolve_tool_compaction_evidence_repair(
    *,
    row_index: int,
    source_trace: ConversationTrace,
    tool_compaction_json: str,
    turn_classifications_json: str,
    cached_turn_lookup: Callable[[str], str | None] | None,
) -> ToolCompactionEvidenceRepair:
    raise NotImplementedError("not on the classifier serving path")


def resolve_tool_compaction_artifact_models(
    *,
    row_index: int,
    source_trace: ConversationTrace,
    tool_compaction_json: str,
    turn_classifications_json: str,
    cached_turn_lookup: Callable[[str], str | None] | None,
) -> dict[int, str]:
    raise NotImplementedError("not on the classifier serving path")


def _tool_traces_by_turn(
    source_turns: tuple[tuple[int, UserMessage, AgentTurn], ...],
    tool_compaction_json: str,
    *,
    artifact_models_by_turn: Mapping[int, str] | None = None,
    normalized_action_trace_chars_by_turn: Mapping[int, int] | None = None,
) -> dict[int, RawAgentFlowToolActionTrace]:
    raise NotImplementedError("not on the classifier serving path")


def build_raw_agent_flow_turn_evidence(
    row: ConversationDatasetRow,
    tool_compaction_json: str,
    *,
    artifact_models_by_turn: Mapping[int, str] | None = None,
    normalized_action_trace_chars_by_turn: Mapping[int, int] | None = None,
) -> tuple[RawAgentFlowTurnEvidence, ...]:
    raise NotImplementedError("not on the classifier serving path")


def build_raw_agent_flow_turn_evidence_from_trace(
    trace: ConversationTrace,
    tool_compaction_json: str,
    *,
    artifact_models_by_turn: Mapping[int, str] | None = None,
    normalized_action_trace_chars_by_turn: Mapping[int, int] | None = None,
) -> tuple[RawAgentFlowTurnEvidence, ...]:
    raise NotImplementedError("not on the classifier serving path")


def raw_agent_flow_turn_content_sha256_from_evidence(
    records: Iterable[RawAgentFlowTurnEvidence],
) -> str:
    raise NotImplementedError("not on the classifier serving path")


def raw_agent_flow_turn_content_sha256_from_trace(
    trace: ConversationTrace,
) -> str:
    raise NotImplementedError("not on the classifier serving path")


def format_raw_agent_flow_turn_evidence(
    record: RawAgentFlowTurnEvidence,
) -> str:
    raise NotImplementedError("not on the classifier serving path")


def validate_raw_agent_flow_turn_evidence(
    text: str,
) -> RawAgentFlowTurnEvidence:
    raise NotImplementedError("not on the classifier serving path")


def raw_agent_flow_input_text_contract(
    *,
    tool_compaction_enabled: bool = True,
) -> dict[str, Any]:
    raise NotImplementedError("not on the classifier serving path")


__all__ = [
    "RAW_AGENT_FLOW_TURN_EVIDENCE_PREFIX",
    "RAW_AGENT_FLOW_TURN_EVIDENCE_SCHEMA_VERSION",
    "RawAgentFlowToolActionTrace",
    "RawAgentFlowTurnEvidence",
    "ToolCompactionEvidenceRepair",
    "build_raw_agent_flow_turn_evidence",
    "build_raw_agent_flow_turn_evidence_from_trace",
    "format_raw_agent_flow_turn_evidence",
    "parse_raw_agent_flow_trace",
    "raw_agent_flow_input_text_contract",
    "raw_agent_flow_turn_content_sha256_from_evidence",
    "raw_agent_flow_turn_content_sha256_from_trace",
    "resolve_tool_compaction_artifact_models",
    "resolve_tool_compaction_evidence_repair",
    "validate_raw_agent_flow_turn_evidence",
]
