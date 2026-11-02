"""Exact live-input adapter for the raw-V5 Voyage HMM serving lane.

The raw-V5 classifier was trained on ``RawAgentFlowTurnEvidence`` rather than
the canonical route summary.  This module is deliberately narrow: it accepts
only that stored evidence shape, produces the already-pinned two-turn Voyage
documents, and rejects any current-turn agent evidence.  It never derives raw
evidence from a lossy route-context snapshot and never falls back to canonical
documents.
"""

from __future__ import annotations

from enum import StrEnum


class RawV5DocumentKind(StrEnum):
    USER = "raw_user_text"
    AGENT_WITH_LIVE_TOOL_TRACE = (
        "raw_agent_text_with_existing_live_structural_tool_trace"
    )


# These values are persisted by the trainer in the raw-V5 HMM bundle. Keep
# their exact stored semantics in this lightweight serving module instead of
# importing the full training entry point (and its native ML dependencies) at
# sidecar startup.


class RawV5ServingInputError(ValueError):
    """Raised when a live request cannot satisfy the raw-V5 model input."""
