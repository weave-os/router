"""Slice conversations into typed work items for the processing queue."""

from __future__ import annotations

from typing import TYPE_CHECKING, Final

from pydantic import BaseModel

from hmm_sidecar.raw_v5.cd_models import (
    AgentTurn,
    ConversationDatasetRow,
    Failure,
    MessageType,
    Result,
)
from hmm_sidecar.raw_v5.cd_schemas import (  # noqa: TC001
    CanonicalTurn,
    HMMRouterSessionSummary,
    SessionSummary,
)

NO_TOOL_CALLS_TRACE = "No tool calls recorded."
USER_AGENT_SESSION_SUMMARY_INPUT_CONTRACT_VERSION: Final = (
    "stage2_user_agent_session_summary_input_v1"
)
_RESPONSE_MESSAGE_TYPE: Final[MessageType] = "response"

if TYPE_CHECKING:
    from collections.abc import Sequence

    from hmm_sidecar.raw_v5.cd_schemas import IndexedClassification


class SummarizerInput(BaseModel, frozen=True):
    """Input payload for the session summarizer LLM call."""

    row_index: int
    conversation_id: str
    interactions: tuple[str, ...]


class ClassifierInput(BaseModel, frozen=True):
    """Input payload for a single turn classification LLM call."""

    row_index: int
    turn_index: int
    conversation_id: str
    summary: SessionSummary | HMMRouterSessionSummary
    user_prompt: str
    agent_action_trace: str


def format_agent_action_trace(agent_turn: AgentTurn) -> str:
    raise NotImplementedError("not on the classifier serving path")


def agent_tool_call_count(row: ConversationDatasetRow) -> int:
    raise NotImplementedError("not on the classifier serving path")


def _format_list_section(title: str, values: Sequence[str]) -> str:
    raise NotImplementedError("not on the classifier serving path")


def format_canonical_turn_document(
    turn_index: int,
    canonical_turn: CanonicalTurn,
) -> str:
    raise NotImplementedError("not on the classifier serving path")


def extract_summarizer_input(
    row_index: int,
    row: ConversationDatasetRow,
    *,
    include_tool_calls: bool = True,
) -> SummarizerInput:
    raise NotImplementedError("not on the classifier serving path")


def extract_classifier_inputs(
    row_index: int,
    row: ConversationDatasetRow,
    summary: SessionSummary | HMMRouterSessionSummary,
) -> tuple[ClassifierInput, ...]:
    raise NotImplementedError("not on the classifier serving path")


def reassemble_classifications(
    results: Sequence[Result[IndexedClassification]],
) -> tuple[IndexedClassification | Failure, ...]:
    raise NotImplementedError("not on the classifier serving path")
