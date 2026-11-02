"""LLM output validation schemas."""

from __future__ import annotations

from typing import Literal

from pydantic import (
    AliasChoices,
    BaseModel,
    Field,
)

ComplexityClassification = Literal[
    "low",
    "medium",
    "high",
    "maximum",
]


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
