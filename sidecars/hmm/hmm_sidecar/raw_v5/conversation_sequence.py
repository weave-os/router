"""The session sequence: one embedding per user or agent turn.

A session is a sequence of canonical turn documents grouped into rounds:
U_r for the user turn of round r, then A_r for that round's agent turn
(its compacted tool actions and observed outcome).  Offline Stage 2 labels
exist only at user-turn Action boundaries.  The live v2 contract may also
end at a cumulative in-progress agent turn when the router takes another
Action between agent steps.

The Stage 2 v3 artifact has two explicitly isolated namespaces. This module
validates and formats only ``views.hmm_router``; it does not construct the
historical flattened ``canonical_turn``. Agent Flow evidence is never
embedded. Legacy ``canonical_turn`` rows remain readable. A synthetic
augmentation may additionally carry one explicitly canonicalized, standalone
user turn at its terminal routing boundary. That field emits only ``U_t``;
it never fabricates a completed ``A_t``.
Historical agent-step Actions come only from explicit canonical step records;
the v2 live path also forbids hidden input truncation.

Element texts:
- U_t: the user-side fields of the Stage 2 canonical turn (request, intent,
  constraints, context, clarifications), formatted deterministically.
- A_t: the gemma-compacted tool action trace (turn goal + action groups)
  plus the observed outcome / open state. Text-only replies (no tool calls)
  still emit an A element with an explicit "No tool calls recorded." marker
  so the U/A alternation stays regular for the HMM.
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import Any

CONVERSATION_SEQUENCE_STATE_DEFINITION = "hmm-router-conversation-sequence-v1"
# Metadata value for canonical U/A conversation sequences.  Keep this beside
# the sequence contract because both the HMM trainer and dataset builders need
# to validate it without importing one another.
# v1 bundles route only at user-turn boundaries.  v2 retains the identical
# offline U/A sequence and supervision but additionally permits the live
# prefix to end at a cumulative in-progress agent turn at an agent-step
# Action boundary.  Existing artifacts remain immutable and loadable.
USER_TURN_ACTION_BOUNDARY = "user_turn"
AGENT_STEP_ACTION_BOUNDARY = "agent_step"
USER_ELEMENT = "user"
AGENT_ELEMENT = "agent"
# Categorical slots for the element kind in HMM raw features; shared by the
# HMM trainer and every consumer that computes posteriors from a
# conversation-sequence bundle.
ELEMENT_KIND_SLOTS = {USER_ELEMENT: 0, AGENT_ELEMENT: 1}
NOT_VISIBLE = "None visible."


def _section(title: str, values: Any) -> str:
    raise NotImplementedError("not on the classifier serving path")


def format_user_message_document(
    turn_index: int,
    canonical_turn: dict[str, Any],
) -> str:
    raise NotImplementedError("not on the classifier serving path")


def format_agent_reply_document(
    turn_index: int,
    *,
    trace: dict[str, Any] | None,
    canonical_turn: dict[str, Any] | None = None,
) -> str:
    raise NotImplementedError("not on the classifier serving path")


@dataclass(frozen=True)
class SequenceElement:
    """One user or agent turn in the session sequence.

    ``turn_index`` is the persisted v1 field name.  Semantically it is a
    round index shared by U_r and A_r; use ``round_index`` in new code.
    """

    kind: str  # USER_ELEMENT | AGENT_ELEMENT
    turn_index: int
    text: str
    # Complexity label index; carried by user elements only (routing decides
    # at U_t). None for agent elements and unlabeled user turns.
    label_index: int | None = None

    @property
    def round_index(self) -> int:
        raise NotImplementedError("not on the classifier serving path")


def conversation_sequence(
    turn_entries: list[dict[str, Any]],
    *,
    tool_compaction: Any = None,
    label_index_for: Any = None,
) -> list[SequenceElement]:
    raise NotImplementedError("not on the classifier serving path")
