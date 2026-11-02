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

import re
from enum import StrEnum

LOW_LABEL = "low"
MEDIUM_LABEL = "medium"
HIGH_LABEL = "high"
MAXIMUM_LABEL = "maximum"

# Deprecated source-level aliases. Keeping these names avoids an unrelated
# mechanical rename across callers while their values remain the *current*
# persisted taxonomy. Use LEGACY_* when the literal old artifact value is
# required.
FAST_LABEL = LOW_LABEL
BALANCED_LABEL = MEDIUM_LABEL

LEGACY_FAST_LABEL = "fast"
LEGACY_BALANCED_LABEL = "balanced"
LEGACY_EXPLORE_LABEL = "explore"


class ReasoningEffort(StrEnum):
    """Canonical reasoning-effort levels encoded in HMM roster arm IDs."""

    LOW = "low"
    MEDIUM = "medium"
    HIGH = "high"
    MAX = "max"
    XHIGH = "xhigh"


# Reasoning levels encoded as trailing model-ID suffixes in HMM roster arms.
REASONING_EFFORT_SUFFIXES = frozenset(ReasoningEffort)


def split_model_effort(model: object) -> tuple[str, ReasoningEffort | None]:
    raise NotImplementedError("not on the classifier serving path")


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
LEGACY_FOUR_CLASS_COMPLEXITY_CLASSES: tuple[str, ...] = (
    LEGACY_FAST_LABEL,
    LEGACY_BALANCED_LABEL,
    HIGH_LABEL,
    MAXIMUM_LABEL,
)

LEGACY_FIVE_CLASS_COMPLEXITY_CLASSES: tuple[str, ...] = (
    LEGACY_FAST_LABEL,
    LEGACY_EXPLORE_LABEL,
    LEGACY_BALANCED_LABEL,
    HIGH_LABEL,
    MAXIMUM_LABEL,
)

SUPPORTED_COMPLEXITY_CLASSES: tuple[str, ...] = tuple(
    dict.fromkeys(
        (
            *COMPLEXITY_CLASSES,
            *LEGACY_FOUR_CLASS_COMPLEXITY_CLASSES,
            LEGACY_EXPLORE_LABEL,
        )
    )
)

COMPLEXITY_CLASS_INDEX: dict[str, int] = {
    label: index for index, label in enumerate(COMPLEXITY_CLASSES)
}


class RouterTrainingTargetTaxonomy(StrEnum):
    """Target-label projections supported by offline Router experiments.

    These are deliberately training-only projections. ``COMPLEXITY_CLASSES``
    remains the sole current taxonomy for persisted Stage 2 labels, feedback,
    roster clusters, and serving outputs.
    """

    FOUR_CLASS = "four-class"
    BINARY_ROUTER = "binary-router"
    TRINARY_ROUTER = "trinary-router"


BINARY_ROUTER_CLASSES: tuple[str, ...] = (LOW_LABEL, MAXIMUM_LABEL)
TRINARY_ROUTER_CLASSES: tuple[str, ...] = (LOW_LABEL, MEDIUM_LABEL, HIGH_LABEL)

_ROUTER_TRAINING_TARGET_CLASSES: dict[RouterTrainingTargetTaxonomy, tuple[str, ...]] = {
    RouterTrainingTargetTaxonomy.FOUR_CLASS: COMPLEXITY_CLASSES,
    RouterTrainingTargetTaxonomy.BINARY_ROUTER: BINARY_ROUTER_CLASSES,
    RouterTrainingTargetTaxonomy.TRINARY_ROUTER: TRINARY_ROUTER_CLASSES,
}

_BINARY_ROUTER_TARGET_LABELS: dict[str, str] = {
    LOW_LABEL: LOW_LABEL,
    MEDIUM_LABEL: LOW_LABEL,
    HIGH_LABEL: MAXIMUM_LABEL,
    MAXIMUM_LABEL: MAXIMUM_LABEL,
}

_TRINARY_ROUTER_TARGET_LABELS: dict[str, str] = {
    LOW_LABEL: LOW_LABEL,
    MEDIUM_LABEL: MEDIUM_LABEL,
    HIGH_LABEL: HIGH_LABEL,
    MAXIMUM_LABEL: HIGH_LABEL,
}


def router_training_target_classes(
    taxonomy: RouterTrainingTargetTaxonomy,
) -> tuple[str, ...]:
    raise NotImplementedError("not on the classifier serving path")


def router_training_target_label(
    label: object,
    taxonomy: RouterTrainingTargetTaxonomy,
) -> str | None:
    raise NotImplementedError("not on the classifier serving path")


def router_training_target_taxonomy_for_classes(
    classes: object,
) -> RouterTrainingTargetTaxonomy:
    raise NotImplementedError("not on the classifier serving path")


LEGACY_COMPLEXITY_LABEL_ALIASES: dict[str, str] = {
    LEGACY_FAST_LABEL: LOW_LABEL,
    LEGACY_EXPLORE_LABEL: LOW_LABEL,
    LEGACY_BALANCED_LABEL: MEDIUM_LABEL,
    "Simple Tool Call Request": LOW_LABEL,
    "SPAWN_EXPLORE": LOW_LABEL,
    "Simple Followup": MEDIUM_LABEL,
    "Complex Followup": HIGH_LABEL,
    "Complex Design": MAXIMUM_LABEL,
}

COMPLEXITY_DISPLAY_LABELS: dict[str, str] = {
    LOW_LABEL: LEGACY_FAST_LABEL,
    MEDIUM_LABEL: LEGACY_BALANCED_LABEL,
    HIGH_LABEL: HIGH_LABEL,
    MAXIMUM_LABEL: MAXIMUM_LABEL,
}


# Session-scoped router modes (`/router-mode`). Each maps a set of canonical
# classes to clamp toward a single destination class. `AUTO` is the default
# and never clamps. The sidecar parser validates wire values independently;
# `balanced` is accepted there as a legacy/human-facing alias for `auto` and
# is never itself a key here.
class RouterMode(StrEnum):
    """Session-scoped cluster-selection policies."""

    AUTO = "auto"
    INTELLIGENT = "intelligent"
    COST = "cost"


AUTO_ROUTER_MODE = RouterMode.AUTO

# Canonical classes each mode clamps toward HIGH_LABEL. Both modes clamp to
# the same destination -- `intelligent` raises the floor, `cost` lowers the
# ceiling, and HIGH is the shared boundary between them.
_ROUTER_MODE_CLAMPED_CLASSES: dict[RouterMode, frozenset[str]] = {
    RouterMode.INTELLIGENT: frozenset({LOW_LABEL, MEDIUM_LABEL}),
    RouterMode.COST: frozenset({MAXIMUM_LABEL}),
}


def canonical_complexity_label(label: object) -> str | None:
    raise NotImplementedError("not on the classifier serving path")


def complexity_display_label(label: object) -> str | None:
    raise NotImplementedError("not on the classifier serving path")


_DISPLAY_TAXONOMY_TOKEN_RE = re.compile(r"(?<![\w-])(low|medium)(?![\w-])")


def complexity_display_text(value: object) -> str:
    raise NotImplementedError("not on the classifier serving path")


def complexity_class_index(label: object) -> int | None:
    raise NotImplementedError("not on the classifier serving path")


def _router_mode_key(mode: RouterMode | None) -> RouterMode | None:
    raise NotImplementedError("not on the classifier serving path")


def clamp_label_for_mode(
    label: object, mode: RouterMode | None
) -> tuple[str | None, bool]:
    raise NotImplementedError("not on the classifier serving path")


def clamped_classes_for_mode(mode: RouterMode | None) -> frozenset[str]:
    raise NotImplementedError("not on the classifier serving path")
