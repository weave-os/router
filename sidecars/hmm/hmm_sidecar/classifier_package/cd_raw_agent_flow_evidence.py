"""Lossless raw turn evidence shared by Agent Flow judges and embeddings."""

from __future__ import annotations

from typing import Final, Literal

from pydantic import (
    BaseModel,
    ConfigDict,
    Field,
)

from hmm_sidecar.classifier_package.cd_schemas import (
    ToolActionTraceCompaction,
)

RAW_AGENT_FLOW_TURN_EVIDENCE_SCHEMA_VERSION: Final = "agent_flow_raw_turn_evidence_v1"


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
