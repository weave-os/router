"""Versioned schemas for the HMM delegator router."""

from __future__ import annotations

from enum import Enum
from typing import Any, Literal

from pydantic import BaseModel, ConfigDict, Field

# Deterministic per-harness roster over the FIVE-class taxonomy prod's live
# classifier still emits (fast/explore/balanced/high/maximum). Same ranking
# semantics as v5; only the cluster set differs, so prod can serve deterministic
# AA-ranked arms without first retraining a four-class classifier.
# v6 carries per-arm WMI scores so Go can apply within-cluster hysteresis at
# decision time without re-computing them from the raw snapshot.


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


# Last generated five-class roster, kept as a fixture for the loaders that still
# accept the legacy taxonomy. Nothing generates or serves it.


class RouterHarness(str, Enum):
    CLAUDE_CODE = "claude_code"
    CODEX = "codex"
    PI = "pi"
    CURSOR = "cursor"
    OPENCODE = "opencode"
    API = "api"
    UNKNOWN = "unknown"


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
