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
from collections.abc import Sequence


from hmm_sidecar.raw_v5.raw_v5_contract import (
    RAW_V5_VOYAGE_ONE_TURN_MAX_TURNS,
    RAW_V5_VOYAGE_TEN_TURN_MAX_TURNS,
    RAW_V5_VOYAGE_TURN_WINDOW_DOCUMENT_SCHEMA_VERSION,
    RAW_V5_VOYAGE_TWO_TURN_MAX_TURNS,
)
from hmm_sidecar.raw_v5 import gemini_tokens

RAW_V5_ROUTER_NO_RESPONSE_MARKER = "No agent response blocks recorded."


class RawV5SequenceError(ValueError):
    """The V5-only raw sequence contract cannot be satisfied."""


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

    if (
        isinstance(turn_index, bool)
        or not isinstance(turn_index, int)
        or turn_index < 0
    ):
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
