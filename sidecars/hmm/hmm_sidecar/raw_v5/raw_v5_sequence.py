"""Causal raw-text sequence projection for V5 router classifications.

V5 labels are audited against the original Agent Flow user/agent/tool evidence.
That evidence must not be collapsed back into a canonical summary when it is
used for direct router training.  At a routing boundary, however, the current
agent response is not yet observable.  This projection therefore emits the
raw user side as ``U_t`` and appends the raw agent/tool side as ``A_t`` only
after that boundary.  A causal Transformer can consume the resulting
``[U_0, A_0, U_1, A_1, ...]`` sequence without target-turn outcome leakage.

The exact functions in this module remain the Stage-2/audit serialization.
The legacy ``*_model_*`` helper layers Qwen's separately hashed, overflow-only
structural projection on top of it. Voyage model inputs use an independent
token-counted projection; its fixed-window helper preserves every selected
source field exactly when the complete provider document fits.
"""

from __future__ import annotations

import json
from collections.abc import Callable, Sequence
from typing import Any, Protocol


from hmm_sidecar.raw_v5.cd_models import ConversationTrace
from hmm_sidecar.raw_v5.cd_raw_agent_flow_evidence import (
    RawAgentFlowTurnEvidence,
)
from hmm_sidecar.raw_v5.conversation_sequence import (
    SequenceElement,
)
from hmm_sidecar.raw_v5.raw_v5_contract import (
    RAW_V5_FEEDBACK_SEQUENCE_CONTRACT_SHA256,
    RAW_V5_FEEDBACK_SEQUENCE_SERIALIZER_VERSION,
    RAW_V5_ROUTER_EMBEDDING_DIM,
    RAW_V5_ROUTER_EMBEDDING_MODEL,
    RAW_V5_ROUTER_MAX_INPUT_TOKENS,
    RAW_V5_ROUTER_SEQUENCE_CONTRACT_SHA256,
    RAW_V5_ROUTER_SEQUENCE_LAYOUT,
    RAW_V5_ROUTER_SERIALIZER_VERSION,
    RAW_V5_ROUTER_STATE_DEFINITION,
    RAW_V5_VOYAGE_ONE_TURN_MAX_TURNS,
    RAW_V5_VOYAGE_TEN_TURN_MAX_TURNS,
    RAW_V5_VOYAGE_TURN_WINDOW_DOCUMENT_SCHEMA_VERSION,
    RAW_V5_VOYAGE_TWO_TURN_MAX_TURNS,
    raw_v5_router_sequence_contract,
)
from hmm_sidecar.raw_v5 import gemini_tokens


RAW_V5_ROUTER_NO_RESPONSE_MARKER = "No agent response blocks recorded."
RAW_V5_ROUTER_NO_TOOL_TRACE_MARKER = "No tool calls recorded."


class RawV5SequenceError(ValueError):
    """The V5-only raw sequence contract cannot be satisfied."""


class RawV5BlankUserPromptError(RawV5SequenceError):
    """A raw V5 turn cannot define a routing boundary from blank user text."""


class RawV5NonCausalTraceError(RawV5SequenceError):
    """Recorded response timing cannot support the routing input sequence."""


def validate_raw_v5_agent_response_chronology(
    source_trace: ConversationTrace, *, last_target_turn_index: int
) -> None:
    raise NotImplementedError("not on the classifier serving path")


class RawV5UserAgentOnlyTurnEvidence(Protocol):
    """The minimum raw fields used by tool-free embedding projections.

    This deliberately omits ``tool_action_trace``.  Consumers that do not
    otherwise need tool evidence can pass a small structural record instead
    of constructing :class:`RawAgentFlowTurnEvidence`, whose validation owns
    the separate tool-trace contract.
    """

    turn_index: int
    user_prompt: str
    agent_response_blocks: Sequence[str]


def format_raw_v5_router_user_document(
    turn_index: int,
    evidence: RawAgentFlowTurnEvidence,
) -> str:
    raise NotImplementedError("not on the classifier serving path")


def format_raw_v5_router_agent_document(
    turn_index: int,
    evidence: RawAgentFlowTurnEvidence,
) -> str:
    raise NotImplementedError("not on the classifier serving path")


def format_raw_v5_router_agent_response_only_document(
    turn_index: int,
    agent_response_blocks: Sequence[str],
) -> str:
    """Serialize only exact response blocks for the tool-free raw-V5 mode.

    This intentionally accepts the selected raw field rather than a complete
    ``RawAgentFlowTurnEvidence`` instance. Constructing that model validates
    the separate tool trace, which this representation must neither require
    nor inspect.
    """

    if isinstance(turn_index, bool) or not isinstance(turn_index, int) or turn_index < 0:
        raise RawV5SequenceError(
            "raw V5 user-agent-only agent document has an invalid turn index"
        )
    if not isinstance(agent_response_blocks, (list, tuple)) or any(
        not isinstance(block, str) for block in agent_response_blocks
    ):
        raise RawV5SequenceError(
            "raw V5 user-agent-only agent document requires response-block text"
        )
    sections = [f"Raw agent reply (turn {turn_index}):"]
    if agent_response_blocks:
        sections.append(
            "Response blocks:\n"
            + "\n\n".join(
                f"[{index}]\n{block}"
                for index, block in enumerate(agent_response_blocks, start=1)
            )
        )
    else:
        sections.append("Response blocks:\n" + RAW_V5_ROUTER_NO_RESPONSE_MARKER)
    return "\n\n".join(sections)


def _raw_v5_user_agent_only_conversation_sequence(
    turn_entries: Sequence[dict[str, Any]],
    *,
    label_index_for: Callable[[object], int | None],
) -> list[SequenceElement]:
    raise NotImplementedError("not on the classifier serving path")


def raw_v5_conversation_sequence(
    turn_entries: Sequence[dict[str, Any]],
    *,
    label_index_for: Callable[[object], int | None],
) -> list[SequenceElement]:
    raise NotImplementedError("not on the classifier serving path")


def _project_model_input_elements(
    elements: Sequence[SequenceElement],
) -> list[SequenceElement]:
    raise NotImplementedError("not on the classifier serving path")


def raw_v5_model_conversation_sequence(
    turn_entries: Sequence[dict[str, Any]],
    *,
    label_index_for: Callable[[object], int | None],
) -> list[SequenceElement]:
    raise NotImplementedError("not on the classifier serving path")


def raw_v5_voyage_model_conversation_sequence(
    turn_entries: Sequence[dict[str, Any]],
    *,
    label_index_for: Callable[[object], int | None],
) -> list[SequenceElement]:
    raise NotImplementedError("not on the classifier serving path")


def _validate_raw_v5_voyage_window_turns(max_turns: int) -> None:
    """Reject unpinned raw-V5 Voyage history windows."""

    if (
        isinstance(max_turns, bool)
        or not isinstance(max_turns, int)
        or max_turns
        not in (
            RAW_V5_VOYAGE_ONE_TURN_MAX_TURNS,
            RAW_V5_VOYAGE_TWO_TURN_MAX_TURNS,
            RAW_V5_VOYAGE_TEN_TURN_MAX_TURNS,
        )
    ):
        raise RawV5SequenceError(
            "raw V5 Voyage turn-window max_turns must be one of "
            f"{RAW_V5_VOYAGE_ONE_TURN_MAX_TURNS}, "
            f"{RAW_V5_VOYAGE_TWO_TURN_MAX_TURNS}, "
            f"{RAW_V5_VOYAGE_TEN_TURN_MAX_TURNS}; got {max_turns!r}"
        )


def format_raw_v5_voyage_turn_window_document(
    *,
    previous_turns: Sequence[tuple[str, str]],
    current_user_text: str,
    current_agent_text: str | None = None,
    max_turns: int,
) -> str:
    """Build one marked causal raw-V5 Voyage provider document.

    The input texts are already source-element projected, if necessary.  The
    caller controls the fixed-size history; this formatter is intentionally
    strict so it cannot accidentally emit a half-completed prior turn or a
    future/current agent response at a user routing boundary.
    """

    _validate_raw_v5_voyage_window_turns(max_turns)
    if len(previous_turns) > max_turns - 1:
        raise RawV5SequenceError(
            "raw V5 Voyage turn window contains too many completed turns"
        )
    if not isinstance(current_user_text, str) or not current_user_text.strip():
        raise RawV5SequenceError(
            "raw V5 Voyage turn window requires a non-empty current user document"
        )
    if current_agent_text is not None and (
        not isinstance(current_agent_text, str) or not current_agent_text.strip()
    ):
        raise RawV5SequenceError(
            "raw V5 Voyage turn window current agent document must be non-empty text or None"
        )

    turns: list[dict[str, object]] = []
    first_offset = -len(previous_turns)
    for index, turn in enumerate(previous_turns):
        if (
            not isinstance(turn, tuple)
            or len(turn) != 2
            or any(not isinstance(value, str) or not value.strip() for value in turn)
        ):
            raise RawV5SequenceError(
                "raw V5 Voyage turn-window history must contain complete non-empty U/A pairs"
            )
        turns.append(
            {
                "turn_offset": first_offset + index,
                "turn_marker": "previous",
                "user_input": turn[0],
                "agent_output": turn[1],
            }
        )
    turns.append(
        {
            "turn_offset": 0,
            "turn_marker": "current",
            "user_input": current_user_text,
            "agent_output": current_agent_text,
        }
    )
    return json.dumps(
        {
            "schema_version": RAW_V5_VOYAGE_TURN_WINDOW_DOCUMENT_SCHEMA_VERSION,
            "state_boundary": "agent" if current_agent_text is not None else "user",
            "turn_window_size": max_turns,
            "turns": turns,
        },
        ensure_ascii=False,
        sort_keys=True,
        separators=(",", ":"),
    )


def _bounded_raw_v5_voyage_turn_window_document(
    *,
    previous_turns: Sequence[tuple[str, str]],
    current_user_text: str,
    current_agent_text: str | None,
    max_turns: int,
) -> str:
    raise NotImplementedError("not on the classifier serving path")


_RAW_V5_GEMINI_PREFIX_TRUNCATION_MARKER = (
    "\n[truncated for gemini-embedding-2 input limit]"
)


def _gemini_prefix_with_marker(text: str, *, max_chars: int) -> str:
    raise NotImplementedError("not on the classifier serving path")


def _bounded_raw_v5_gemini_turn_window_document(
    *,
    previous_turns: Sequence[tuple[str, str]],
    current_user_text: str,
    current_agent_text: str | None,
    max_turns: int,
) -> str:
    """Fit a raw-V5 causal document within Gemini's local 8,000-byte budget.

    Full selected rounds remain byte-identical when they fit. On overflow,
    oldest completed rounds are removed first. Only when the current partial
    round cannot fit alone are its available fields reduced to marked UTF-8
    prefixes. Every size decision measures the complete UTF-8 JSON, including
    markers, against 8,000 bytes. No token-count API is used.
    """

    _validate_raw_v5_voyage_window_turns(max_turns)
    max_completed_turns = max_turns - 1
    selected_previous_turns = list(
        previous_turns[-max_completed_turns:] if max_completed_turns else ()
    )

    def render(user_text: str, agent_text: str | None) -> str:
        return format_raw_v5_voyage_turn_window_document(
            previous_turns=selected_previous_turns,
            current_user_text=user_text,
            current_agent_text=agent_text,
            max_turns=max_turns,
        )

    document = render(current_user_text, current_agent_text)
    while (
        not gemini_tokens.gemini_input_fits_byte_budget(document)
        and selected_previous_turns
    ):
        del selected_previous_turns[0]
        document = render(current_user_text, current_agent_text)
    if gemini_tokens.gemini_input_fits_byte_budget(document):
        return document

    low = 1
    high = max(
        len(current_user_text),
        len(current_agent_text) if current_agent_text else 0,
    )
    best: str | None = None
    while low <= high:
        field_budget = (low + high) // 2
        candidate = render(
            _gemini_prefix_with_marker(current_user_text, max_chars=field_budget),
            (
                _gemini_prefix_with_marker(current_agent_text, max_chars=field_budget)
                if current_agent_text is not None
                else None
            ),
        )
        if gemini_tokens.gemini_input_fits_byte_budget(candidate):
            best = candidate
            low = field_budget + 1
        else:
            high = field_budget - 1
    if best is None:
        raise RawV5SequenceError(
            "raw V5 Gemini current turn cannot fit the 8000-byte input budget"
        )
    return best


def format_raw_v5_previous_element_document(
    *,
    previous_element: tuple[str, str] | None,
    current_kind: str,
    current_text: str,
) -> str:
    raise NotImplementedError("not on the classifier serving path")


def _bounded_raw_v5_gemini_previous_element_document(
    *,
    previous_element: tuple[str, str] | None,
    current_kind: str,
    current_text: str,
) -> str:
    raise NotImplementedError("not on the classifier serving path")


def raw_v5_gemini_user_agent_only_last_state_model_conversation_sequence(
    turn_entries: Sequence[dict[str, Any]],
    *,
    label_index_for: Callable[[object], int | None],
) -> list[SequenceElement]:
    raise NotImplementedError("not on the classifier serving path")


def _raw_v5_turn_window_model_conversation_sequence(
    turn_entries: Sequence[dict[str, Any]],
    *,
    label_index_for: Callable[[object], int | None],
    max_turns: int,
    document_builder: Callable[..., str],
    source_sequence_builder: Callable[..., list[SequenceElement]] = raw_v5_conversation_sequence,
) -> list[SequenceElement]:
    raise NotImplementedError("not on the classifier serving path")


def raw_v5_voyage_turn_window_model_conversation_sequence(
    turn_entries: Sequence[dict[str, Any]],
    *,
    label_index_for: Callable[[object], int | None],
    max_turns: int,
) -> list[SequenceElement]:
    raise NotImplementedError("not on the classifier serving path")


def raw_v5_gemini_turn_window_model_conversation_sequence(
    turn_entries: Sequence[dict[str, Any]],
    *,
    label_index_for: Callable[[object], int | None],
    max_turns: int,
) -> list[SequenceElement]:
    raise NotImplementedError("not on the classifier serving path")


def raw_v5_gemini_user_agent_only_model_conversation_sequence(
    turn_entries: Sequence[dict[str, Any]],
    *,
    label_index_for: Callable[[object], int | None],
    max_turns: int = RAW_V5_VOYAGE_ONE_TURN_MAX_TURNS,
) -> list[SequenceElement]:
    raise NotImplementedError("not on the classifier serving path")


def raw_v5_voyage_user_agent_only_model_conversation_sequence(
    turn_entries: Sequence[dict[str, Any]],
    *,
    label_index_for: Callable[[object], int | None],
    max_turns: int = RAW_V5_VOYAGE_ONE_TURN_MAX_TURNS,
) -> list[SequenceElement]:
    raise NotImplementedError("not on the classifier serving path")


def _raw_v5_user_agent_only_evidence_fields(
    evidence: RawV5UserAgentOnlyTurnEvidence,
) -> tuple[int, str, tuple[str, ...]]:
    raise NotImplementedError("not on the classifier serving path")


def raw_v5_gemini_user_agent_only_documents_from_evidence(
    turn_evidence: Sequence[RawV5UserAgentOnlyTurnEvidence],
    *,
    max_turns: int = RAW_V5_VOYAGE_ONE_TURN_MAX_TURNS,
) -> dict[int, tuple[str, str]]:
    raise NotImplementedError("not on the classifier serving path")


def raw_v5_gemini_element_documents(
    turn_index: int,
    user_prompt: str,
    agent_response_blocks: Sequence[str],
) -> tuple[str, str]:
    raise NotImplementedError("not on the classifier serving path")


def _bounded_raw_v5_gemini_user_message_document(user_text: str) -> str:
    return _bounded_raw_v5_gemini_turn_window_document(
        previous_turns=(),
        current_user_text=user_text,
        current_agent_text=None,
        max_turns=RAW_V5_VOYAGE_ONE_TURN_MAX_TURNS,
    )


def _bounded_raw_v5_gemini_agent_message_document(agent_document: str) -> str:
    """The agent reply alone, fit to Gemini's byte budget as a marked prefix."""

    if gemini_tokens.gemini_input_fits_byte_budget(agent_document):
        return agent_document
    low, high = 1, len(agent_document)
    fitted: str | None = None
    while low <= high:
        keep_chars = (low + high) // 2
        candidate = _gemini_prefix_with_marker(agent_document, max_chars=keep_chars)
        if gemini_tokens.gemini_input_fits_byte_budget(candidate):
            fitted = candidate
            low = keep_chars + 1
        else:
            high = keep_chars - 1
    if fitted is None:
        raise RawV5SequenceError(
            "raw V5 Gemini agent message cannot fit the 8000-byte input budget"
        )
    return fitted


def _clip_voyage_code_4_element_document(document: str) -> str:
    raise NotImplementedError("not on the classifier serving path")


def raw_v5_voyage_code_4_element_documents(
    turn_index: int,
    user_prompt: str,
    agent_response_blocks: Sequence[str],
) -> tuple[str, str]:
    raise NotImplementedError("not on the classifier serving path")


def raw_v5_voyage_code_4_element_model_conversation_sequence(
    turn_entries: Sequence[dict[str, Any]],
    *,
    label_index_for: Callable[[object], int | None],
) -> list[SequenceElement]:
    raise NotImplementedError("not on the classifier serving path")


def raw_v5_gemini_element_model_conversation_sequence(
    turn_entries: Sequence[dict[str, Any]],
    *,
    label_index_for: Callable[[object], int | None],
) -> list[SequenceElement]:
    raise NotImplementedError("not on the classifier serving path")


def raw_v5_voyage_user_agent_only_documents_from_evidence(
    turn_evidence: Sequence[RawV5UserAgentOnlyTurnEvidence],
    *,
    max_turns: int = RAW_V5_VOYAGE_ONE_TURN_MAX_TURNS,
) -> dict[int, tuple[str, str]]:
    raise NotImplementedError("not on the classifier serving path")


def _raw_v5_user_agent_only_documents_from_evidence(
    turn_evidence: Sequence[RawV5UserAgentOnlyTurnEvidence],
    *,
    max_turns: int,
    document_builder: Callable[..., str],
) -> dict[int, tuple[str, str]]:
    raise NotImplementedError("not on the classifier serving path")


def raw_v5_gemini_user_agent_only_last_state_documents_from_evidence(
    turn_evidence: Sequence[RawV5UserAgentOnlyTurnEvidence],
) -> dict[int, tuple[str, str]]:
    raise NotImplementedError("not on the classifier serving path")


def _raw_v5_turn_window_documents_from_evidence(
    turn_evidence: Sequence[RawAgentFlowTurnEvidence],
    *,
    max_turns: int,
    document_builder: Callable[..., str],
    provider_name: str,
) -> dict[int, tuple[str, str]]:
    raise NotImplementedError("not on the classifier serving path")


def raw_v5_gemini_turn_window_documents_from_evidence(
    turn_evidence: Sequence[RawAgentFlowTurnEvidence],
    *,
    max_turns: int,
) -> dict[int, tuple[str, str]]:
    raise NotImplementedError("not on the classifier serving path")


def raw_v5_voyage_turn_window_documents_from_evidence(
    turn_evidence: Sequence[RawAgentFlowTurnEvidence],
    *,
    max_turns: int,
) -> dict[int, tuple[str, str]]:
    raise NotImplementedError("not on the classifier serving path")


def validate_raw_v5_voyage_turn_window_document(
    text: str,
    *,
    max_turns: int,
    state_boundary: str,
) -> dict[str, object]:
    raise NotImplementedError("not on the classifier serving path")


def _feedback_raw_message(
    value: object,
    *,
    message_index: int,
) -> dict[str, Any]:
    raise NotImplementedError("not on the classifier serving path")


def _feedback_agent_document(
    turn_index: int,
    messages: Sequence[dict[str, Any]],
) -> str:
    raise NotImplementedError("not on the classifier serving path")


def raw_v5_feedback_conversation_sequence(
    messages: Sequence[object],
) -> list[SequenceElement]:
    raise NotImplementedError("not on the classifier serving path")


def raw_v5_feedback_model_conversation_sequence(
    messages: Sequence[object],
) -> list[SequenceElement]:
    raise NotImplementedError("not on the classifier serving path")


def raw_v5_feedback_turn_window_model_conversation_sequence(
    messages: Sequence[object],
    *,
    max_turns: int,
    document_builder: Callable[..., str] = _bounded_raw_v5_voyage_turn_window_document,
) -> list[SequenceElement]:
    raise NotImplementedError("not on the classifier serving path")


def raw_v5_feedback_voyage_turn_window_model_conversation_sequence(
    messages: Sequence[object],
    *,
    max_turns: int,
) -> list[SequenceElement]:
    raise NotImplementedError("not on the classifier serving path")


def raw_v5_feedback_gemini_turn_window_model_conversation_sequence(
    messages: Sequence[object],
    *,
    max_turns: int,
) -> list[SequenceElement]:
    raise NotImplementedError("not on the classifier serving path")


__all__ = [
    "RAW_V5_ROUTER_SEQUENCE_LAYOUT",
    "RAW_V5_ROUTER_SEQUENCE_CONTRACT_SHA256",
    "RAW_V5_FEEDBACK_SEQUENCE_CONTRACT_SHA256",
    "RAW_V5_FEEDBACK_SEQUENCE_SERIALIZER_VERSION",
    "RAW_V5_ROUTER_SERIALIZER_VERSION",
    "RAW_V5_ROUTER_STATE_DEFINITION",
    "RAW_V5_ROUTER_EMBEDDING_DIM",
    "RAW_V5_ROUTER_EMBEDDING_MODEL",
    "RAW_V5_ROUTER_MAX_INPUT_TOKENS",
    "RawV5BlankUserPromptError",
    "RawV5UserAgentOnlyTurnEvidence",
    "RawV5SequenceError",
    "format_raw_v5_router_agent_document",
    "format_raw_v5_router_agent_response_only_document",
    "format_raw_v5_router_user_document",
    "raw_v5_conversation_sequence",
    "raw_v5_feedback_conversation_sequence",
    "raw_v5_feedback_gemini_turn_window_model_conversation_sequence",
    "raw_v5_feedback_model_conversation_sequence",
    "raw_v5_feedback_turn_window_model_conversation_sequence",
    "raw_v5_feedback_voyage_turn_window_model_conversation_sequence",
    "raw_v5_model_conversation_sequence",
    "raw_v5_voyage_model_conversation_sequence",
    "raw_v5_gemini_turn_window_documents_from_evidence",
    "raw_v5_gemini_turn_window_model_conversation_sequence",
    "raw_v5_gemini_user_agent_only_documents_from_evidence",
    "raw_v5_voyage_user_agent_only_documents_from_evidence",
    "raw_v5_voyage_user_agent_only_model_conversation_sequence",
    "raw_v5_gemini_user_agent_only_last_state_documents_from_evidence",
    "raw_v5_gemini_user_agent_only_last_state_model_conversation_sequence",
    "raw_v5_gemini_user_agent_only_model_conversation_sequence",
    "raw_v5_router_sequence_contract",
    "raw_v5_voyage_turn_window_documents_from_evidence",
    "raw_v5_voyage_turn_window_model_conversation_sequence",
    "format_raw_v5_voyage_turn_window_document",
    "validate_raw_v5_voyage_turn_window_document",
]
