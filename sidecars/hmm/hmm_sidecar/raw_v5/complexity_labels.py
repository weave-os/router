"""Pinned routing class order shared by dataset, trainer, and serving.

These values must match the Stage 2 ``ComplexityClassification`` literal in
``ml_dev/agent_flow/extraction/conversation_dataset/schemas.py`` exactly. The
same four values are used as classifier labels, roster clusters, feedback
labels, and serving output. Never introduce a second current naming layer.

Persisted Stage 2 inputs and immutable serving artifacts may use the original
``fast``/``balanced`` names or the retired five-class ``explore`` label.
Readers normalize those exact legacy values only at explicit compatibility
boundaries. Fresh producers and serving artifacts carry the four current
capability tiers; human-facing displays translate them back to the familiar
``fast``/``balanced`` wording.
"""

from __future__ import annotations

LOW_LABEL = "low"
MEDIUM_LABEL = "medium"
HIGH_LABEL = "high"
MAXIMUM_LABEL = "maximum"

# Deprecated source-level aliases. Keeping these names avoids an unrelated
# mechanical rename across callers while their values remain the *current*
# persisted taxonomy. Use LEGACY_* when the literal old artifact value is
# required.


# Reasoning levels encoded as trailing model-ID suffixes in HMM roster arms.


# Pinned class order: index i here is class i everywhere (dataset y values,
# XGBoost heads, classifier probs, roster clusters). Never reorder.
COMPLEXITY_CLASSES: tuple[str, ...] = (
    LOW_LABEL,
    MEDIUM_LABEL,
    HIGH_LABEL,
    MAXIMUM_LABEL,
)

# Exact predecessor orders. These are immutable artifact compatibility
# contracts, not supported output vocabularies for new models.


# Session-scoped router modes (`/router-mode`). Each maps a set of canonical
# classes to clamp toward a single destination class. `AUTO` is the default
# and never clamps. The sidecar parser validates wire values independently;
# `balanced` is accepted there as a legacy/human-facing alias for `auto` and
# is never itself a key here.


# Canonical classes each mode clamps toward HIGH_LABEL. Both modes clamp to
# the same destination -- `intelligent` raises the floor, `cost` lowers the
# ceiling, and HIGH is the shared boundary between them.
