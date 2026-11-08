"""Live conversation-sequence turn construction shared by serving and replay.

The router-hmm-sidecar builds ``LiveFlowTurn`` sequences from the router's
``conversation_messages`` payload before HMM inference. Offline feedback
dataset builds must replay the SAME construction on stored messages, so the
logic lives here — importable by both the sidecar (``server.py``) and
``build_feedback_dataset`` — instead of being duplicated and drifting.

No embedding model is loaded here. Raw-V5 Gemini construction uses a local
8,000-byte limit and needs no Google credentials or token-count API request.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum
from typing import Any

from hmm_sidecar.classifier_package.conversation_sequence import (
    AGENT_STEP_ACTION_BOUNDARY,
    AGENT_ELEMENT,
    ELEMENT_KIND_SLOTS,
    USER_TURN_ACTION_BOUNDARY,
    USER_ELEMENT,
    format_agent_reply_document,
    format_user_message_document,
)
from hmm_sidecar.classifier_package.contract import (
    GEMINI_EMBEDDING_2_MAX_INPUT_TOKENS,
    GEMINI_EMBEDDING_2_MAX_INPUT_BYTES,
)
from hmm_sidecar.classifier_package.documents import (
    DocumentError,
    # Intentionally share the trainer's one exact budget/window implementation;
    # reproducing it here would create a second model-input serializer.
    _bounded_gemini_agent_message_document,
    _bounded_gemini_turn_window_document,
    _bounded_gemini_user_message_document,
    format_router_agent_response_only_document,
)
from hmm_sidecar.classifier_package.serving_profile import (
    DocumentWindow,
    EmbeddingProvider,
    ServingProfile,
    ToolEvidenceMode,
)
from hmm_sidecar.classifier_package.serving_features import CATEGORICAL_DIM

_DOCUMENT_BOUNDARY_MARKER = "\n[content omitted at HMM embedding boundary]"


MAX_LIVE_CONTEXT_CHARS = 1_500


MAX_LIVE_EMBEDDING_DOCUMENT_BYTES = 7_500


LIVE_EMBEDDING_DOCUMENT_POLICY = "hmm_router_live_embedding_document_v1"


FULL_CANONICAL_DOCUMENT_POLICY = (
    "hmm_router_full_canonical_embedding_document_no_truncation_v1"
)


def bound_embedding_document(text: str) -> str:
    """Bound one complete live HMM document by UTF-8 bytes.

    The semantic fields above normally keep deterministic documents below this
    ceiling.  This final boundary also covers LLM-canonicalized cache entries,
    whose generated field lengths are not otherwise schema-bounded.
    """
    encoded = text.encode("utf-8")
    if len(encoded) <= MAX_LIVE_EMBEDDING_DOCUMENT_BYTES:
        return text

    prefix_budget = MAX_LIVE_EMBEDDING_DOCUMENT_BYTES - len(
        _DOCUMENT_BOUNDARY_MARKER.encode("utf-8")
    )
    prefix = encoded[:prefix_budget].decode("utf-8", errors="ignore").rstrip()
    return prefix + _DOCUMENT_BOUNDARY_MARKER


DEFAULT_LIVE_FLOW_MAX_ELEMENTS = 48

# Legacy v1 bundles retain the original live-proxy boundaries below for exact
# compatibility. v2 Action bundles bypass every one of these limits and pass
# the full canonical Session; the embedding provider must reject an oversized
# document rather than receiving a clipped substitute.
MAX_LIVE_USER_REQUEST_CHARS = 3_000
MAX_LIVE_USER_INTENT_CHARS = 1_000
MAX_LIVE_AGENT_TEXT_CHARS = 1_500
MAX_LIVE_TOOL_GROUPS = 8
MAX_LIVE_TOOL_INPUT_KEYS = 8
MAX_LIVE_TOOL_ITEM_CHARS = 300

GEMINI_DOCUMENT_POLICY = "raw_v5_gemini_serialized_8000_byte_budget_v4"
_CONTEXT_OMISSION_MARKER_RESERVE = 80


class RouteMessageRole(StrEnum):
    SYSTEM = "system"
    DEVELOPER = "developer"
    USER = "user"
    ASSISTANT = "assistant"
    MODEL = "model"


_ROUTE_MESSAGE_ROLE_VALUES = frozenset(role.value for role in RouteMessageRole)


@dataclass(frozen=True)
class LiveFlowTurn:
    """One live conversation timestep in the agent_flow sequence format."""

    turn_text: str
    categorical: tuple[float, ...] = ()


def clean_text(text: str) -> str:
    return (text or "").strip()


def bound_text(text: str, *, limit: int) -> str:
    """Apply an explicit semantic-field boundary with an audit marker."""
    stripped = clean_text(text)
    if len(stripped) <= limit:
        return stripped
    if limit <= 0:
        return ""

    short_marker = "[omitted]"
    if limit <= len(short_marker):
        return short_marker[:limit]

    prefix_budget = limit
    marker = short_marker
    for _attempt in range(4):
        omitted = len(stripped) - prefix_budget
        marker = f"\n[omitted {omitted} chars by HMM live document boundary]"
        next_prefix_budget = max(limit - len(marker), 0)
        if next_prefix_budget == prefix_budget:
            break
        prefix_budget = next_prefix_budget
    if prefix_budget <= 0:
        return short_marker[:limit]
    return stripped[:prefix_budget] + marker


def bound_recent_context(lines: list[str]) -> list[str]:
    """Older lines that do not fit become one omission marker, reserved from the budget."""
    cleaned = [clean_text(line) for line in lines if clean_text(line)]
    if not cleaned:
        return []

    remaining = MAX_LIVE_CONTEXT_CHARS
    selected_reversed: list[str] = []
    for index in range(len(cleaned) - 1, -1, -1):
        older_lines_remain = index > 0
        marker_reserve = _CONTEXT_OMISSION_MARKER_RESERVE if older_lines_remain else 0
        available = remaining - marker_reserve
        if available <= 0:
            break
        line = cleaned[index]
        bounded_line = bound_text(line, limit=available)
        selected_reversed.append(bounded_line)
        remaining -= len(bounded_line)
        if len(line) > available:
            break

    selected = list(reversed(selected_reversed))
    omitted_lines = len(cleaned) - len(selected)
    if omitted_lines:
        marker = f"[{omitted_lines} older context message(s) omitted by HMM boundary]"
        selected.insert(0, marker)
    assert sum(len(line) for line in selected) <= MAX_LIVE_CONTEXT_CHARS
    return selected


def finalize_embedding_documents(
    elements: list[LiveFlowTurn],
    debug: dict[str, Any],
    *,
    preserve_full_canonical_data: bool = False,
) -> tuple[list[LiveFlowTurn], dict[str, Any]]:
    """Diagnostics carry only byte counts and policy labels, never document text."""
    source_sizes = [len(element.turn_text.encode("utf-8")) for element in elements]
    bounded_elements: list[LiveFlowTurn] = []
    bounded_count = 0
    for element, source_size in zip(elements, source_sizes, strict=True):
        turn_text = (
            element.turn_text
            if preserve_full_canonical_data
            else bound_embedding_document(element.turn_text)
        )
        if len(turn_text.encode("utf-8")) < source_size:
            bounded_count += 1
        bounded_elements.append(
            LiveFlowTurn(
                turn_text=turn_text,
                categorical=element.categorical,
            )
        )

    return bounded_elements, {
        **debug,
        "hmm_embedding_document_policy": (
            FULL_CANONICAL_DOCUMENT_POLICY
            if preserve_full_canonical_data
            else LIVE_EMBEDDING_DOCUMENT_POLICY
        ),
        "hmm_embedding_document_byte_limit": (
            None if preserve_full_canonical_data else MAX_LIVE_EMBEDDING_DOCUMENT_BYTES
        ),
        "hmm_embedding_input_truncation": (
            "forbidden_provider_must_reject_oversize"
            if preserve_full_canonical_data
            else "legacy_explicit_document_boundary"
        ),
        "hmm_embedding_documents_bounded": bounded_count,
        "hmm_embedding_source_max_bytes": max(source_sizes, default=0),
        "hmm_embedding_document_max_bytes": max(
            (len(element.turn_text.encode("utf-8")) for element in bounded_elements),
            default=0,
        ),
    }


def kind_categorical(kind: str) -> tuple[float, ...]:
    values = [0.0] * CATEGORICAL_DIM
    values[ELEMENT_KIND_SLOTS[kind]] = 1.0
    return tuple(values)


def user_categorical() -> tuple[float, ...]:
    return kind_categorical(USER_ELEMENT)


def prompt_to_turn(
    prompt_text: str,
    *,
    preserve_full_canonical_data: bool = False,
) -> LiveFlowTurn:
    text = (prompt_text or "[missing prompt text]").strip()
    canonical_turn = {
        "user_request": (
            text
            if preserve_full_canonical_data
            else bound_text(text, limit=MAX_LIVE_USER_REQUEST_CHARS)
        ),
        "user_intent": (
            text
            if preserve_full_canonical_data
            else bound_text(text, limit=MAX_LIVE_USER_INTENT_CHARS)
        ),
        "constraints_guidelines": (),
        "context_supplied": (),
        "clarifications_corrections": (),
    }
    return LiveFlowTurn(
        turn_text=format_user_message_document(0, canonical_turn),
        categorical=user_categorical(),
    )


def route_message_text(value: Any) -> str:
    if not isinstance(value, dict):
        return ""
    return str(value.get("text") or "").strip()


def route_message_role(value: Any) -> str:
    if not isinstance(value, dict):
        return ""
    role = str(value.get("role") or "").strip().lower()
    if role == RouteMessageRole.MODEL.value:
        return RouteMessageRole.ASSISTANT.value
    return role


def message_tool_groups(
    value: Any,
    *,
    preserve_full_canonical_data: bool = False,
) -> list[dict[str, Any]]:
    if not isinstance(value, dict):
        return []
    groups: list[dict[str, Any]] = []
    raw_calls = [
        call for call in value.get("tool_calls") or [] if isinstance(call, dict)
    ]
    if not preserve_full_canonical_data:
        raw_calls = raw_calls[-MAX_LIVE_TOOL_GROUPS:]
    for call in raw_calls:
        raw_name = str(call.get("name") or "tool").strip() or "tool"
        name = (
            raw_name
            if preserve_full_canonical_data
            else bound_text(raw_name, limit=MAX_LIVE_TOOL_ITEM_CHARS)
        )
        input_keys = [
            (
                str(key).strip()
                if preserve_full_canonical_data
                else bound_text(
                    str(key).strip(),
                    limit=MAX_LIVE_TOOL_ITEM_CHARS,
                )
            )
            for key in call.get("input_keys") or []
            if str(key).strip()
        ]
        if not preserve_full_canonical_data:
            input_keys = input_keys[-MAX_LIVE_TOOL_INPUT_KEYS:]
        operation = "tool call"
        if input_keys:
            operation = "input keys: " + ", ".join(sorted(input_keys))
        if not preserve_full_canonical_data:
            operation = bound_text(operation, limit=MAX_LIVE_TOOL_ITEM_CHARS)
        groups.append(
            {
                "order": len(groups) + 1,
                "tools": [name],
                "call_count": 1,
                "operation": operation,
            }
        )
    return groups


def message_tool_result_summary(value: Any) -> str:
    if not isinstance(value, dict):
        return ""
    results = [
        result for result in value.get("tool_results") or [] if isinstance(result, dict)
    ]
    if not results:
        return ""
    error_count = sum(1 for result in results if bool(result.get("is_error")))
    if len(results) == 1:
        return (
            "Tool result returned with error."
            if error_count
            else "Tool result returned."
        )
    if error_count:
        return f"{len(results)} tool results returned, {error_count} with errors."
    return f"{len(results)} tool results returned."


def truncate_sequence_elements(
    elements: list[Any],
    max_elements: int,
) -> list[Any]:
    """Keep the trailing window, snapped to start at a USER element.

    Prefer the first USER element inside the window; if the window has none
    (pathological all-agent tail), widen backward to the nearest USER element
    so the sequence never starts mid-turn.
    """
    if max_elements <= 0 or len(elements) <= max_elements:
        return elements
    window = elements[-max_elements:]
    for start, element in enumerate(window):
        categorical = getattr(element, "categorical", ())
        if (
            len(categorical) > ELEMENT_KIND_SLOTS[USER_ELEMENT]
            and categorical[ELEMENT_KIND_SLOTS[USER_ELEMENT]] == 1.0
        ):
            return window[start:]
    window_start = len(elements) - max_elements
    for start in range(window_start - 1, -1, -1):
        element = elements[start]
        categorical = getattr(element, "categorical", ())
        if (
            len(categorical) > ELEMENT_KIND_SLOTS[USER_ELEMENT]
            and categorical[ELEMENT_KIND_SLOTS[USER_ELEMENT]] == 1.0
        ):
            return elements[start:]
    return []


def latest_user_text_from_messages(messages: Any) -> str:
    if not isinstance(messages, list):
        return ""
    for msg in reversed(messages):
        if route_message_role(msg) != "user":
            continue
        text = route_message_text(msg)
        if text:
            return text
    return ""


def tool_context_metadata(messages: Any) -> dict[str, Any]:
    if not isinstance(messages, list):
        return {}
    tool_names: list[str] = []
    tool_call_count = 0
    tool_turn_count = 0
    tool_result_count = 0
    tool_error_count = 0
    for msg in messages:
        if not isinstance(msg, dict):
            continue
        message_tool_calls = 0
        for call in msg.get("tool_calls") or ():
            if not isinstance(call, dict):
                continue
            name = str(call.get("name") or "").strip()
            if name:
                tool_names.append(name)
            tool_call_count += 1
            message_tool_calls += 1
        for result in msg.get("tool_results") or ():
            if not isinstance(result, dict):
                continue
            tool_result_count += 1
            if bool(result.get("is_error")):
                tool_error_count += 1
        if message_tool_calls:
            tool_turn_count += 1
    return {
        "prior_tool_names": tool_names,
        "prior_tool_call_count": tool_call_count,
        "prior_tool_turn_count": tool_turn_count,
        "prior_tool_result_count": tool_result_count,
        "prior_tool_error_count": tool_error_count,
    }


def _user_document(turn_index: int, user_text: str) -> str:
    text = clean_text(user_text)
    if not text:
        raise DocumentError(
            f"raw V5 live user prompt is blank: turn_index={turn_index}"
        )
    return f"Raw user message (turn {turn_index}):\n{text}"


def _agent_document(
    turn_index: int,
    *,
    response_blocks: list[str],
) -> str:
    """Serialize ``A_t`` as response blocks only: the served profile was
    trained on documents with no tool-action section, not even an empty marker.
    """

    return format_router_agent_response_only_document(turn_index, response_blocks)


def _window_turns(
    source_turns: list[LiveFlowTurn],
    profile: ServingProfile,
) -> list[LiveFlowTurn]:
    """Validate causal U/A order and apply the pinned window representation.

    ``source_turns`` are the per-element raw documents in causal order, ending
    at the routed user boundary.
    """

    if not source_turns or len(source_turns) % 2 != 1:
        raise DocumentError("raw V5 live sequence must end at one causal user boundary")
    for offset in range(0, len(source_turns) - 1, 2):
        if source_turns[offset].categorical != user_categorical() or source_turns[
            offset + 1
        ].categorical != kind_categorical(AGENT_ELEMENT):
            raise DocumentError(
                "raw V5 live sequence requires alternating user/agent turns"
            )
    if source_turns[-1].categorical != user_categorical():
        raise DocumentError("raw V5 live sequence final element is not a user boundary")
    if profile.config.document_window is DocumentWindow.PER_MESSAGE:
        return _per_message_turns(source_turns)
    if (
        profile.config.provider is not EmbeddingProvider.GEMINI
        or profile.config.tool_evidence is not ToolEvidenceMode.USER_AGENT_ONLY
        or profile.config.document_window is not DocumentWindow.TURN_WINDOW
        or profile.config.lookback_rounds != 1
    ):
        raise DocumentError("unsupported legacy raw-V5 turn-window geometry")

    windowed_turns: list[LiveFlowTurn] = []
    for offset in range(0, len(source_turns) - 1, 2):
        user_turn = source_turns[offset]
        agent_turn = source_turns[offset + 1]
        windowed_turns.extend(
            (
                LiveFlowTurn(
                    turn_text=_bounded_gemini_turn_window_document(
                        previous_turns=(),
                        current_user_text=user_turn.turn_text,
                        current_agent_text=None,
                        max_turns=profile.config.lookback_rounds,
                    ),
                    categorical=user_turn.categorical,
                ),
                LiveFlowTurn(
                    turn_text=_bounded_gemini_turn_window_document(
                        previous_turns=(),
                        current_user_text=user_turn.turn_text,
                        current_agent_text=agent_turn.turn_text,
                        max_turns=profile.config.lookback_rounds,
                    ),
                    categorical=agent_turn.categorical,
                ),
            )
        )
    final_user = source_turns[-1]
    windowed_turns.append(
        LiveFlowTurn(
            turn_text=_bounded_gemini_turn_window_document(
                previous_turns=(),
                current_user_text=final_user.turn_text,
                current_agent_text=None,
                max_turns=profile.config.lookback_rounds,
            ),
            categorical=final_user.categorical,
        )
    )
    return windowed_turns


def _per_message_turns(source_turns: list[LiveFlowTurn]) -> list[LiveFlowTurn]:
    """Embed each U and A document alone, as the element store did.

    These are the bounding helpers ``gemini_element_documents`` and the
    training sequence builder apply, so a live document and its stored vector
    share one serializer: the user document is the one-round routing document
    and the agent reply is its response blocks, alone, within 8,000 bytes.
    """

    return [
        LiveFlowTurn(
            turn_text=(
                _bounded_gemini_user_message_document(turn.turn_text)
                if turn.categorical == user_categorical()
                else _bounded_gemini_agent_message_document(turn.turn_text)
            ),
            categorical=turn.categorical,
        )
        for turn in source_turns
    ]


def _finalize_live_turns(
    turns: list[LiveFlowTurn],
    debug: dict[str, Any],
    profile: ServingProfile,
) -> tuple[list[LiveFlowTurn], dict[str, Any]]:
    sizes = [len(turn.turn_text.encode("utf-8")) for turn in turns]
    return turns, {
        **debug,
        "hmm_sequence_length": len(turns),
        "hmm_action_boundary": USER_TURN_ACTION_BOUNDARY,
        "hmm_open_agent_turn_included": False,
        "hmm_embedding_document_policy": GEMINI_DOCUMENT_POLICY,
        "hmm_embedding_document_serializer_version": (
            profile.document_serializer_version
        ),
        "hmm_embedding_document_byte_limit": GEMINI_EMBEDDING_2_MAX_INPUT_BYTES,
        "hmm_embedding_document_token_limit": None,
        "hmm_embedding_document_assumed_token_budget": (
            GEMINI_EMBEDDING_2_MAX_INPUT_TOKENS
        ),
        "hmm_embedding_input_truncation": "raw_v5_contractual_byte_budget",
        "hmm_embedding_documents_bounded": 0,
        "hmm_embedding_source_max_bytes": max(sizes),
        "hmm_embedding_document_max_bytes": max(sizes),
        "raw_v5_serving_config": {
            "provider": profile.config.provider.value,
            "tool_evidence": profile.config.tool_evidence.value,
            "document_window": profile.config.document_window.value,
            "lookback_rounds": profile.config.lookback_rounds,
        },
        "raw_v5_sequence_representation": profile.representation.value,
    }


def conversation_sequence_turns(
    messages: Any,
    *,
    fallback_text: str,
    max_elements: int,
    doc_resolver: Any = None,
    include_current_agent_action: bool = False,
    preserve_full_canonical_data: bool = False,
    package_profile: ServingProfile | None = None,
    first_turn_index: int = 0,
) -> tuple[list[LiveFlowTurn], dict[str, Any]]:
    """Build the live U/A turn sequence from router conversation messages.

    ``first_turn_index`` numbers the first visible user turn, so documents keep
    their absolute "turn N" when the router's window dropped earlier turns.

    ``doc_resolver`` lets serving substitute canonicalizer-cached documents;
    replay passes ``None`` for the deterministic-doc path (identical to what
    serving produces on a canonicalizer cache miss).

    v1 callers leave ``include_current_agent_action`` false and receive a
    prefix ending at the latest user turn.  v2 callers set it true; when the
    current agent turn has emitted at least one step, the prefix additionally
    ends in ONE cumulative open-agent element.  Each later Action recomputes
    and replaces that open element with A_r[1..current], rather than adding
    repeated observations for the same turn. v2 also sets
    ``preserve_full_canonical_data``: no field, document, tool-group, or
    Session-history cap is permitted.

    ``package_profile`` reuses this same message parser and tool-group
    compaction, but swaps only the U/A document formatter and then applies the
    lookback geometry that profile's representation was trained with. Tool-free
    profiles never serialize a tool-action section. It never invokes
    ``doc_resolver``.
    """
    package_documents = package_profile is not None
    if package_documents and (
        doc_resolver is not None
        or include_current_agent_action
        or preserve_full_canonical_data
    ):
        raise ValueError(
            "raw V5 live documents forbid canonical resolvers and Action inputs"
        )
    if preserve_full_canonical_data and max_elements > 0:
        raise ValueError("full canonical Action inputs forbid max_elements truncation")
    if not isinstance(messages, list):
        if package_profile is not None:
            return _finalize_live_turns(
                _window_turns(
                    [
                        LiveFlowTurn(
                            turn_text=_user_document(first_turn_index, fallback_text),
                            categorical=user_categorical(),
                        )
                    ],
                    package_profile,
                ),
                {
                    "history_message_count": 0,
                    "history_user_turn_count": 1,
                    "history_agent_step_count": 0,
                    "hmm_sequence_source": "route_text",
                },
                package_profile,
            )
        return finalize_embedding_documents(
            [
                prompt_to_turn(
                    fallback_text,
                    preserve_full_canonical_data=preserve_full_canonical_data,
                )
            ],
            {
                "history_message_count": 0,
                "hmm_sequence_length": 1,
                "hmm_sequence_source": "route_text",
                "history_user_turn_count": 1 if fallback_text.strip() else 0,
                "history_agent_step_count": 0,
                "hmm_action_boundary": USER_TURN_ACTION_BOUNDARY,
                "hmm_open_agent_turn_included": False,
            },
            preserve_full_canonical_data=preserve_full_canonical_data,
        )

    context_lines: list[str] = []
    elements: list[LiveFlowTurn] = []
    turn_index = first_turn_index - 1
    pending_user_turn_index: int | None = None
    pending_user_canonical_turn: dict[str, Any] | None = None
    pending_user_raw_text = ""
    agent_goal = ""
    agent_last_text = ""
    agent_response_blocks: list[str] = []
    agent_groups: list[dict[str, Any]] = []
    agent_step_observations: list[str] = []
    current_agent_step_count = 0
    total_agent_step_count = 0
    user_turn_count = 0

    def close_pending_turn() -> None:
        nonlocal agent_goal, agent_last_text, agent_groups, agent_response_blocks
        nonlocal agent_step_observations, current_agent_step_count
        nonlocal pending_user_turn_index, pending_user_canonical_turn
        nonlocal pending_user_raw_text
        if pending_user_turn_index is None:
            agent_goal = ""
            agent_last_text = ""
            agent_response_blocks = []
            agent_groups = []
            agent_step_observations = []
            current_agent_step_count = 0
            return
        trace = None
        if agent_groups or agent_goal:
            trace = {"turn_goal": agent_goal, "action_groups": agent_groups}
        agent_canonical_turn = {
            "observed_outcome": (
                (
                    agent_last_text
                    if preserve_full_canonical_data
                    else bound_text(
                        agent_last_text,
                        limit=MAX_LIVE_AGENT_TEXT_CHARS,
                    )
                )
                if agent_last_text
                else "Conversation continued."
            ),
            "open_state_after_turn": "Later turns may refine or supersede this request.",
        }
        assert pending_user_canonical_turn is not None
        if package_profile is not None:
            user_doc = _user_document(
                pending_user_turn_index,
                pending_user_raw_text,
            )
            agent_doc = _agent_document(
                pending_user_turn_index,
                response_blocks=agent_response_blocks,
            )
        elif doc_resolver is not None:
            user_doc, agent_doc = doc_resolver(
                turn_index=pending_user_turn_index,
                user_canonical_turn=pending_user_canonical_turn,
                trace=trace,
                agent_canonical_turn=agent_canonical_turn,
            )
        else:
            user_doc = format_user_message_document(
                pending_user_turn_index,
                pending_user_canonical_turn,
            )
            agent_doc = format_agent_reply_document(
                pending_user_turn_index,
                trace=trace,
                canonical_turn=agent_canonical_turn,
            )
        elements.append(
            LiveFlowTurn(
                turn_text=user_doc,
                categorical=kind_categorical(USER_ELEMENT),
            )
        )
        elements.append(
            LiveFlowTurn(
                turn_text=agent_doc,
                categorical=kind_categorical(AGENT_ELEMENT),
            )
        )
        pending_user_turn_index = None
        pending_user_canonical_turn = None
        pending_user_raw_text = ""
        agent_goal = ""
        agent_last_text = ""
        agent_response_blocks = []
        agent_groups = []
        agent_step_observations = []
        current_agent_step_count = 0

    def cumulative_open_agent_outcome() -> str:
        if not agent_step_observations:
            return agent_last_text or "Agent step emitted the recorded tool action(s)."
        if preserve_full_canonical_data:
            return "\n".join(agent_step_observations)
        # Retain the newest observations when the explicit live-document
        # boundary is reached.  The omission marker makes this visible rather
        # than silently changing the action state.
        selected_reversed: list[str] = []
        remaining = MAX_LIVE_AGENT_TEXT_CHARS
        for index in range(len(agent_step_observations) - 1, -1, -1):
            older_remain = index > 0
            marker_reserve = _CONTEXT_OMISSION_MARKER_RESERVE if older_remain else 0
            available = remaining - marker_reserve
            if available <= 0:
                break
            observation = agent_step_observations[index]
            bounded = bound_text(observation, limit=available)
            selected_reversed.append(bounded)
            remaining -= len(bounded)
            if len(observation) > available:
                break
        selected = list(reversed(selected_reversed))
        omitted = len(agent_step_observations) - len(selected)
        if omitted:
            selected.insert(
                0,
                f"[{omitted} older agent-step observation(s) omitted by HMM boundary]",
            )
        return "\n".join(selected)

    for raw_message in messages:
        role = route_message_role(raw_message)
        text = route_message_text(raw_message)
        tool_result_summary = message_tool_result_summary(raw_message)
        if role == "user":
            if not text:
                if tool_result_summary:
                    if pending_user_turn_index is not None:
                        agent_last_text = clean_text(tool_result_summary)
                        agent_step_observations.append(
                            "Tool result after agent step "
                            f"{max(current_agent_step_count, 1)}: "
                            + clean_text(tool_result_summary)
                        )
                    else:
                        context_lines.append(
                            "tool context: " + clean_text(tool_result_summary)
                        )
                continue
            if pending_user_turn_index is not None:
                close_pending_turn()
            turn_index += 1
            user_turn_count += 1
            pending_user_turn_index = turn_index
            full_text = clean_text(text)
            pending_user_raw_text = full_text
            pending_user_canonical_turn = {
                "user_request": (
                    full_text
                    if preserve_full_canonical_data or package_documents
                    else bound_text(
                        full_text,
                        limit=MAX_LIVE_USER_REQUEST_CHARS,
                    )
                ),
                "user_intent": (
                    full_text
                    if preserve_full_canonical_data or package_documents
                    else bound_text(
                        full_text,
                        limit=MAX_LIVE_USER_INTENT_CHARS,
                    )
                ),
                "context_supplied": (
                    [clean_text(line) for line in context_lines if clean_text(line)]
                    if preserve_full_canonical_data or package_documents
                    else bound_recent_context(context_lines)
                ),
            }
            context_lines = []
            continue
        if role == "assistant":
            message_groups = message_tool_groups(
                raw_message,
                preserve_full_canonical_data=(
                    preserve_full_canonical_data or package_documents
                ),
            )
            if pending_user_turn_index is not None and (text or message_groups):
                current_agent_step_count += 1
                total_agent_step_count += 1
            agent_groups.extend(message_groups)
            if (
                not preserve_full_canonical_data
                and not package_documents
                and len(agent_groups) > MAX_LIVE_TOOL_GROUPS
            ):
                del agent_groups[:-MAX_LIVE_TOOL_GROUPS]
            if text:
                cleaned = clean_text(text)
                if package_documents:
                    agent_response_blocks.append(cleaned)
                if not agent_goal:
                    agent_goal = (
                        cleaned
                        if preserve_full_canonical_data or package_documents
                        else bound_text(
                            cleaned,
                            limit=MAX_LIVE_AGENT_TEXT_CHARS,
                        )
                    )
                agent_last_text = (
                    cleaned
                    if preserve_full_canonical_data or package_documents
                    else bound_text(
                        cleaned,
                        limit=MAX_LIVE_AGENT_TEXT_CHARS,
                    )
                )
                if pending_user_turn_index is not None:
                    agent_step_observations.append(
                        f"Agent step {current_agent_step_count}: "
                        + (
                            cleaned
                            if preserve_full_canonical_data or package_documents
                            else bound_text(
                                cleaned,
                                limit=MAX_LIVE_AGENT_TEXT_CHARS,
                            )
                        )
                    )
            elif message_groups and pending_user_turn_index is not None:
                tool_names = ", ".join(
                    str(group["tools"][0])
                    for group in message_groups
                    if group.get("tools")
                )
                agent_step_observations.append(
                    f"Agent step {current_agent_step_count}: "
                    f"issued tool action(s){f' via {tool_names}' if tool_names else ''}."
                )
            continue
        if role in {"system", "developer"} and text:
            context_lines.append(f"{role} context: " + clean_text(text))

    open_agent_included = False
    if pending_user_turn_index is not None:
        assert pending_user_canonical_turn is not None
        elements.append(
            LiveFlowTurn(
                turn_text=(
                    _user_document(
                        pending_user_turn_index,
                        pending_user_raw_text,
                    )
                    if package_documents
                    else format_user_message_document(
                        pending_user_turn_index,
                        pending_user_canonical_turn,
                    )
                ),
                categorical=kind_categorical(USER_ELEMENT),
            )
        )
        has_open_agent_evidence = bool(
            current_agent_step_count
            or agent_groups
            or agent_goal
            or agent_last_text
            or agent_step_observations
        )
        if include_current_agent_action and has_open_agent_evidence:
            trace = None
            if agent_groups or agent_goal:
                trace = {
                    "turn_goal": agent_goal,
                    "action_groups": agent_groups,
                }
            elements.append(
                LiveFlowTurn(
                    turn_text=format_agent_reply_document(
                        pending_user_turn_index,
                        trace=trace,
                        canonical_turn={
                            "observed_outcome": cumulative_open_agent_outcome(),
                            "open_state_after_turn": (
                                "Agent turn is in progress at this Action boundary."
                            ),
                        },
                    ),
                    categorical=kind_categorical(AGENT_ELEMENT),
                )
            )
            open_agent_included = True

    if not elements:
        if package_profile is not None:
            return _finalize_live_turns(
                _window_turns(
                    [
                        LiveFlowTurn(
                            turn_text=_user_document(first_turn_index, fallback_text),
                            categorical=user_categorical(),
                        )
                    ],
                    package_profile,
                ),
                {
                    "history_message_count": len(messages),
                    "history_user_turn_count": user_turn_count,
                    "history_agent_step_count": total_agent_step_count,
                    "hmm_sequence_source": "route_text",
                },
                package_profile,
            )
        return finalize_embedding_documents(
            [
                prompt_to_turn(
                    fallback_text,
                    preserve_full_canonical_data=preserve_full_canonical_data,
                )
            ],
            {
                "history_message_count": len(messages),
                "hmm_sequence_length": 1,
                "hmm_sequence_source": "route_text",
                "history_user_turn_count": user_turn_count,
                "history_agent_step_count": total_agent_step_count,
                "hmm_action_boundary": USER_TURN_ACTION_BOUNDARY,
                "hmm_open_agent_turn_included": False,
            },
            preserve_full_canonical_data=preserve_full_canonical_data,
        )

    sequence_length_before_truncation = len(elements)
    elements = truncate_sequence_elements(elements, max_elements)
    if package_profile is not None:
        return _finalize_live_turns(
            _window_turns(elements, package_profile),
            {
                "history_message_count": len(messages),
                "hmm_sequence_length_before_truncation": (
                    sequence_length_before_truncation
                ),
                "hmm_sequence_truncated_elements": max(
                    sequence_length_before_truncation - len(elements),
                    0,
                ),
                "hmm_sequence_max_elements": max_elements,
                "hmm_sequence_source": "conversation_messages",
                "history_user_turn_count": user_turn_count,
                "history_agent_step_count": total_agent_step_count,
                "current_agent_step_index": None,
            },
            package_profile,
        )
    return finalize_embedding_documents(
        elements,
        {
            "history_message_count": len(messages),
            "hmm_sequence_length": len(elements),
            "hmm_sequence_length_before_truncation": sequence_length_before_truncation,
            "hmm_sequence_truncated_elements": max(
                sequence_length_before_truncation - len(elements),
                0,
            ),
            "hmm_sequence_max_elements": max_elements,
            "hmm_sequence_source": "conversation_messages",
            "history_user_turn_count": user_turn_count,
            "history_agent_step_count": total_agent_step_count,
            "current_agent_step_index": (
                current_agent_step_count if open_agent_included else None
            ),
            "hmm_action_boundary": (
                AGENT_STEP_ACTION_BOUNDARY
                if open_agent_included
                else USER_TURN_ACTION_BOUNDARY
            ),
            "hmm_open_agent_turn_included": open_agent_included,
        },
        preserve_full_canonical_data=preserve_full_canonical_data,
    )


def live_sequence_turns(
    messages: Any,
    *,
    fallback_text: str,
    profile: ServingProfile,
    max_elements: int = DEFAULT_LIVE_FLOW_MAX_ELEMENTS,
    first_turn_index: int = 0,
) -> tuple[list[LiveFlowTurn], dict[str, Any]]:
    """Build raw-V5 inputs for ``profile`` from the live-message snapshot.

    This calls :func:`conversation_sequence_turns` so role handling and
    ``message_tool_groups`` remain exactly shared with the deployed canonical
    lane. Only the U/A formatter and lookback geometry change, and both come
    from the package's declared serving profile rather than a fixed
    representation. The current agent outcome remains excluded.

    ``conversation_messages`` remains optional at the route API boundary.  A
    missing or empty snapshot therefore becomes the request's exact raw
    ``fallback_text`` as a one-user sequence; a supplied malformed snapshot
    fails closed instead of being replaced.
    """

    if messages is not None and not isinstance(messages, list):
        raise DocumentError("raw V5 live conversation_messages is not a list")
    prompt = clean_text(fallback_text)
    if not messages and not prompt:
        raise DocumentError(
            "raw V5 live sequence requires conversation_messages or raw route text"
        )
    if messages:
        for message_index, message in enumerate(messages):
            if not isinstance(message, dict):
                raise DocumentError(
                    "raw V5 live conversation message is not an object: "
                    f"index={message_index}"
                )
            role = message.get("role")
            if (
                not isinstance(role, str)
                or role.strip().lower() not in _ROUTE_MESSAGE_ROLE_VALUES
            ):
                raise DocumentError(
                    "raw V5 live conversation message has an invalid role: "
                    f"index={message_index}"
                )
            text = message.get("text")
            if text is not None and not isinstance(text, str):
                raise DocumentError(
                    "raw V5 live conversation message text is not a string: "
                    f"index={message_index}"
                )
            tool_calls = message.get("tool_calls")
            if tool_calls is not None and (
                not isinstance(tool_calls, list)
                or any(
                    not isinstance(call, dict)
                    or (
                        call.get("input_keys") is not None
                        and not isinstance(call.get("input_keys"), list)
                    )
                    for call in tool_calls
                )
            ):
                raise DocumentError(
                    "raw V5 live conversation message has invalid tool_calls: "
                    f"index={message_index}"
                )
            tool_results = message.get("tool_results")
            if tool_results is not None and (
                not isinstance(tool_results, list)
                or any(not isinstance(value, dict) for value in tool_results)
            ):
                raise DocumentError(
                    "raw V5 live conversation message has invalid tool_results: "
                    f"index={message_index}"
                )
        if not latest_user_text_from_messages(messages):
            raise DocumentError(
                "raw V5 live conversation_messages has no usable user boundary"
            )
    turns, debug = conversation_sequence_turns(
        messages or None,
        fallback_text=prompt,
        max_elements=max_elements,
        doc_resolver=None,
        include_current_agent_action=False,
        preserve_full_canonical_data=False,
        package_profile=profile,
        first_turn_index=first_turn_index,
    )
    debug["raw_v5_live_input"] = debug["hmm_sequence_source"]
    return turns, debug
