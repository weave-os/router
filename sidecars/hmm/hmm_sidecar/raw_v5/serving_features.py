"""Shared HMM-router feature builders safe for serving imports.

This module is intentionally lightweight. It must not import Torch, embedding
clients, or dataset-building code because the live proxy loads XGBoost models
during startup, and Torch-before-XGBoost can segfault in the local macOS venv.
"""

from __future__ import annotations

import re
from enum import StrEnum
from typing import Any

import numpy as np

CATEGORICAL_DIM = 13
# Raw HMM rows are [embedding | element-kind one-hots]. Bundles fit before the
# prefix-normalized position column was dropped declare a width of one.
RELATIVE_POSITION_DIM = 0
LEGACY_RELATIVE_POSITION_DIM = 1
SCALAR_FEATURE_NAMES = (
    "relative_position",
    "log1p_prefix_length",
    "gamma_top",
    "gamma_margin",
    "is_first",
    "is_last",
)
TOOL_FAMILY_NAMES = (
    "bash",
    "read",
    "search",
    "edit",
    "task",
)
TOOL_CONTEXT_FEATURE_NAMES = (
    "has_tool_affordance",
    "requires_tool_use",
    "available_tool_count_log1p",
    *(f"available_tool_family_{name}" for name in TOOL_FAMILY_NAMES),
    "current_intent_none",
    "current_intent_tool_call",
    "current_intent_search_read",
    "current_intent_explore",
    "current_intent_edit",
    "current_intent_run_test",
    "prior_tool_call_count_log1p",
    "prior_tool_turn_count_log1p",
    "prior_tool_result_count_log1p",
    "prior_tool_error_count_log1p",
    *(f"prior_tool_family_{name}" for name in TOOL_FAMILY_NAMES),
)

# Sending-harness one-hot. The harness changes tool vocabulary and turn shape,
# so it belongs in the representation the complexity classifier and the arm
# selector both consume — a bandit prior keyed on (cluster, harness, arm) can
# only shift arms WITHIN an already-chosen cluster, and cannot influence which
# cluster a turn lands in at all.
#
# Three buckets, not the full RouterHarness enum: claude_code and codex are the
# harnesses we model, everything else pools. A bucket with no traffic is a dead
# column, and every extra column costs width in a layout whose alignment is only
# width-checked at serve time.
HARNESS_BLOCK_NAME = "harness_one_hot"


class BeliefDynamicsFeature(StrEnum):
    """What the filter recursion knows that a single filtered row does not.

    Trajectory, surprise, and the fitted transition diagonal nothing downstream
    reads today. Every value is a function of the prefix alone, so entry t is
    unchanged by turn t+1 arriving. Declaration order is layout order: a
    reordering silently reinterprets every trained classifier.
    """

    ENTROPY = "belief_entropy"
    MOVEMENT = "belief_movement"
    SELF_TRANSITION_MASS = "belief_self_transition_mass"
    EXPECTED_DWELL_LOG1P = "belief_expected_dwell_log1p"
    TURN_LOG_LIKELIHOOD = "turn_log_likelihood"
    TURN_LOG_LIKELIHOOD_PREFIX_MEAN = "turn_log_likelihood_prefix_mean"
    TURN_LOG_LIKELIHOOD_DELTA = "turn_log_likelihood_delta"


BELIEF_DYNAMICS_FEATURE_NAMES = tuple(
    feature.value for feature in BeliefDynamicsFeature
)
BELIEF_DYNAMICS_DIM = len(BELIEF_DYNAMICS_FEATURE_NAMES)
BELIEF_DYNAMICS_BLOCK_NAME = "hmm_belief_dynamics"


def belief_dynamics_layout_block() -> dict[str, Any]:
    return {
        "block": BELIEF_DYNAMICS_BLOCK_NAME,
        "dim": BELIEF_DYNAMICS_DIM,
        "names": list(BELIEF_DYNAMICS_FEATURE_NAMES),
    }


_SEARCH_READ_RE = re.compile(
    r"\b(search|grep|rg|ripgrep|find|read|open|cat|ls|list|inspect|scan|look through|look at)\b",
    re.IGNORECASE,
)
_EXPLORE_RE = re.compile(
    r"\b(explore|scour|inventory|survey|map out|read through|background agent|subagent)\b",
    re.IGNORECASE,
)
_EDIT_RE = re.compile(
    r"\b(edit|write|create|implement|fix|patch|change|refactor|update)\b",
    re.IGNORECASE,
)
_RUN_TEST_RE = re.compile(
    r"\b(run|execute|test|pytest|npm test|smoke test|validate|verify|check)\b",
    re.IGNORECASE,
)
_TOOL_CALL_RE = re.compile(
    r"(^\s*/|\b(tool|tool call|bash|shell|command|script)\b)",
    re.IGNORECASE,
)


def raw_relative_position_dim(schema: Any) -> int:
    """Width of the trailing relative-position column a bundle was fit with.

    Bundles fit before the column was dropped do not declare the key, so an
    absent value means the legacy width of one.
    """
    if not isinstance(schema, dict):
        return LEGACY_RELATIVE_POSITION_DIM
    declared = schema.get("relative_position_dim", LEGACY_RELATIVE_POSITION_DIM)
    if declared not in (0, 1):
        raise ValueError(
            f"unsupported relative_position_dim {declared!r}; expected 0 or 1"
        )
    return int(declared)


def _raw_prefix_matrix(
    embeddings: list[list[float]],
    kinds: list[str] | None = None,
    *,
    relative_position_dim: int = RELATIVE_POSITION_DIM,
) -> np.ndarray:
    raise NotImplementedError("not on the classifier serving path")


def belief_dynamics_features(
    *,
    beliefs: np.ndarray,
    step_log_likelihoods: np.ndarray,
    self_transition: np.ndarray,
) -> np.ndarray:
    """Causal belief-dynamics features for the last turn of a filtered prefix.

    ``beliefs`` is the ``(t, K)`` filtered belief matrix for the prefix (row i
    is ``p(z_i | x_{1:i})``), ``step_log_likelihoods`` is the matching
    ``log p(x_i | x_{1:i-1})`` per turn, and ``self_transition`` is the
    transition matrix diagonal. Filtering never conditions on a future turn, so
    the row this returns for a prefix is the row the same prefix produces inside
    a longer conversation.

    The seven values are the current belief's entropy; how far the belief moved
    from the previous turn (``1 - <gamma_t, gamma_t-1>``, zero on the first
    turn, near one when the regime just flipped); the self-transition mass the
    current belief sits on and the expected remaining dwell that implies; and
    the current turn's predictive log likelihood, the prefix mean, and the
    difference -- an "is this turn surprising, for this conversation" signal.
    """
    beliefs = np.asarray(beliefs, dtype=float)
    step_log_likelihoods = np.asarray(step_log_likelihoods, dtype=float)
    self_transition = np.asarray(self_transition, dtype=float)
    if beliefs.ndim != 2 or beliefs.shape[0] == 0:
        raise ValueError(f"belief matrix must be (t, K) with t >= 1: {beliefs.shape}")
    if step_log_likelihoods.shape != (beliefs.shape[0],):
        raise ValueError(
            "step log likelihoods must carry one value per filtered turn: "
            f"{step_log_likelihoods.shape} for beliefs {beliefs.shape}"
        )
    if self_transition.shape != (beliefs.shape[1],):
        raise ValueError(
            "self-transition diagonal must carry one value per state: "
            f"{self_transition.shape} for beliefs {beliefs.shape}"
        )
    current_belief = beliefs[-1]
    entropy = -float(
        np.sum(current_belief * np.log(np.maximum(current_belief, 1e-300)))
    )
    movement = (
        1.0 - float(current_belief @ beliefs[-2]) if beliefs.shape[0] > 1 else 0.0
    )
    self_transition_mass = float(current_belief @ self_transition)
    expected_dwell = float(
        current_belief @ (1.0 / np.maximum(1.0 - self_transition, 1e-6))
    )
    turn_log_likelihood = float(step_log_likelihoods[-1])
    prefix_mean_log_likelihood = float(step_log_likelihoods.mean())
    return np.asarray(
        [
            entropy,
            movement,
            self_transition_mass,
            float(np.log1p(expected_dwell)),
            turn_log_likelihood,
            prefix_mean_log_likelihood,
            turn_log_likelihood - prefix_mean_log_likelihood,
        ],
        dtype=float,
    )


def _tool_family_flags(tool_names: list[str] | tuple[str, ...]) -> list[float]:
    raise NotImplementedError("not on the classifier serving path")


def _current_intent_flags(text: str) -> list[float]:
    tool_call = 1.0 if _TOOL_CALL_RE.search(text) else 0.0
    search_read = 1.0 if _SEARCH_READ_RE.search(text) else 0.0
    explore = 1.0 if _EXPLORE_RE.search(text) else 0.0
    edit = 1.0 if _EDIT_RE.search(text) else 0.0
    run_test = 1.0 if _RUN_TEST_RE.search(text) else 0.0
    none = 1.0 if not any((tool_call, search_read, explore, edit, run_test)) else 0.0
    return [none, tool_call, search_read, explore, edit, run_test]


def current_text_has_tool_intent(text: str) -> bool:
    return _current_intent_flags(text)[0] == 0.0


def tool_context_features_from_packet(packet: Any | None) -> np.ndarray:
    raise NotImplementedError("not on the classifier serving path")


def harness_features_from_packet(packet: Any | None) -> np.ndarray:
    raise NotImplementedError("not on the classifier serving path")


def _feature_row(
    *,
    embedding: list[float],
    gamma_row: np.ndarray,
    current_state: int,
    previous_state: int,
    position: int,
    prefix_length: int,
    is_last: float,
    n_states: int,
    projected_row: np.ndarray | None,
    seqae_latent: list[float] | None = None,
    include_embedding: bool = True,
    tool_context_features: np.ndarray | None = None,
    harness_features: np.ndarray | None = None,
    belief_dynamics: np.ndarray | None = None,
    state_identity: np.ndarray | None = None,
) -> np.ndarray:
    # n_states == 0 is the HMM-free layout: the caller has no bundle, so there
    # is no posterior to summarise. hmm_gamma / one_hot_current_state /
    # one_hot_previous_state collapse to zero-width blocks and the two
    # gamma-derived scalars are dropped, leaving embedding + conversation-shape
    # scalars + tool_context. Measured at -0.0001 macro-F1 versus the full
    # layout (RESULTS_hmm_embedding_ablation_20260722.md, McNemar p=0.49), so
    # this is a supported layout rather than a degraded one.
    hmm_free = n_states == 0
    eye = np.eye(n_states, dtype=float) if state_identity is None else state_identity
    if hmm_free:
        gamma_top = 0.0
        gamma_margin = 0.0
    else:
        order = np.argsort(gamma_row)[::-1]
        gamma_top = float(gamma_row[order[0]])
        gamma_margin = (
            float(gamma_row[order[0]] - gamma_row[order[1]])
            if n_states > 1
            else gamma_top
        )
    denominator = max(prefix_length - 1, 1)
    scalar_values = [
        float(position) / float(denominator),
        float(np.log1p(prefix_length)),
    ]
    if not hmm_free:
        scalar_values.extend([gamma_top, gamma_margin])
    scalar_values.extend([1.0 if position == 0 else 0.0, is_last])
    scalars = np.asarray(scalar_values, dtype=float)
    blocks = []
    if include_embedding:
        blocks.append(np.asarray(embedding, dtype=float))
    if hmm_free:
        blocks.append(scalars)
    else:
        blocks.extend(
            [
                gamma_row,
                eye[current_state],
                eye[previous_state],
                scalars,
            ]
        )
    if tool_context_features is not None:
        blocks.append(np.asarray(tool_context_features, dtype=float))
    if projected_row is not None:
        blocks.append(np.asarray(projected_row, dtype=float))
    if seqae_latent is not None:
        blocks.append(np.asarray(seqae_latent, dtype=float))
    # Appended last so an existing bundle's block order is untouched: a
    # harness-aware layout is the old layout plus one trailing block, which
    # keeps the layout diff auditable and lets both coexist during rollout.
    if harness_features is not None:
        blocks.append(np.asarray(harness_features, dtype=float))
    # Also appended after every pre-existing block, for the same reason: a
    # belief-dynamics layout is the old layout plus one trailing block.
    if belief_dynamics is not None:
        dynamics = np.asarray(belief_dynamics, dtype=float)
        if dynamics.shape != (BELIEF_DYNAMICS_DIM,):
            raise ValueError(
                f"belief dynamics block must be {BELIEF_DYNAMICS_DIM} wide: "
                f"{dynamics.shape}"
            )
        blocks.append(dynamics)
    return np.concatenate(blocks)
