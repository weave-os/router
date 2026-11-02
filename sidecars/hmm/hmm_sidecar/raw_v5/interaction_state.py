"""Interaction-state chain and lookback windows over per-element embeddings.

Every user and agent message is embedded once (U1 -> e1, A1 -> e2, U2 -> e3, ...).
Two things are built from those stored vectors without another embedding call:

* The interaction-state HMM: one hidden state per round, emitting
  ``o_r = [P_user(U_r) | P_agent(A_r)]``. The element chain (U1, A1, U2, ...)
  spends its transition matrix on role alternation -- production package
  64f27f96 fits a 0.086 mean self-transition with 86% of user-state mass moving
  to agent states -- so its sticky prior never expresses "the same kind of
  interaction continues". Here a self-transition means exactly that.
* Lookback windows: previous rounds' stored vectors in fixed, most-recent-first
  slots, so any window length is a slice of stored vectors. PCA lives only in
  the HMM's emission space; the classifier never receives projected vectors,
  except the one legacy artifact ElementLookbackSource.PCA_PROJECTION serves.

A round whose agent message has not been observed yet (the first request of a
turn) keeps a NaN agent block. With diagonal emissions that block marginalizes
out exactly, so the routing belief is p(z_t | o_{1:t-1}, U_t).
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import Sequence

import numpy as np
from sklearn.decomposition import PCA

from hmm_sidecar.raw_v5.bayes_hmm import (
    BayesianStickyHDPHMM,
    _diag_gaussian_logpdf,
)
from hmm_sidecar.raw_v5.conversation_sequence import AGENT_ELEMENT, USER_ELEMENT
# The layout vocabulary lives in the dependency-free module so contract
# validators need not import this one; it stays importable from here.
from hmm_sidecar.raw_v5.hmm_feature_arms import (  # noqa: F401
    ELEMENT_LOOKBACK_BLOCK_NAME,
    ElementLookback,
    element_lookback_layout_block,
    hmm_chain_of,
    resolve_element_lookback,
    stored_vector_lookback_layout_block,
)
from hmm_sidecar.raw_v5.serving_features import belief_dynamics_features

@dataclass(frozen=True)
class RoleProjector:
    """Separate PCA bases for user and agent messages."""

    user: PCA
    agent: PCA

    @property
    def user_dim(self) -> int:
        return int(self.user.n_components_)

    @property
    def agent_dim(self) -> int:
        return int(self.agent.n_components_)

    @property
    def observation_dim(self) -> int:
        return self.user_dim + self.agent_dim


def fit_role_projector(
    user_rows: np.ndarray, agent_rows: np.ndarray, *, pca_dim: int, seed: int,
) -> RoleProjector:
    raise NotImplementedError("not on the classifier serving path")


def round_count(kinds: Sequence[str]) -> int:
    """Rounds in a strict ``U, A, U, A, ...`` sequence; a trailing ``U`` opens a round."""
    for position, kind in enumerate(kinds):
        expected = USER_ELEMENT if position % 2 == 0 else AGENT_ELEMENT
        if kind != expected:
            raise ValueError(
                "interaction-state chain needs alternating user/agent elements "
                f"starting with a user; position {position} is {kind!r}"
            )
    return (len(kinds) + 1) // 2


def round_observations(
    projector: RoleProjector,
    embeddings: Sequence[Sequence[float]] | np.ndarray,
    kinds: Sequence[str],
) -> np.ndarray:
    """``(rounds, user_dim + agent_dim)`` observations; an unobserved agent block is NaN."""
    rounds = round_count(kinds)
    vectors = np.asarray(embeddings, dtype=float)
    observations = np.full((rounds, projector.observation_dim), np.nan)
    observations[:, : projector.user_dim] = projector.user.transform(vectors[0::2])
    agent_vectors = vectors[1::2]
    if len(agent_vectors):
        observations[: len(agent_vectors), projector.user_dim :] = projector.agent.transform(agent_vectors)
    return observations


def raw_round_vectors(embeddings: Sequence[Sequence[float]] | np.ndarray, kinds: Sequence[str]) -> np.ndarray:
    raise NotImplementedError("not on the classifier serving path")


def completed_rounds(observations: np.ndarray) -> np.ndarray:
    raise NotImplementedError("not on the classifier serving path")


@dataclass(frozen=True)
class InteractionFilterReadout:
    """Causal readouts per round; entry r never depends on a later round.

    ``routing_beliefs[r]`` is p(z_r | o_{1:r-1}, U_r), the belief at round r's
    user boundary. ``completed_beliefs[r]`` also absorbs A_r and is NaN when A_r
    is unobserved. ``routing_log_likelihoods[r]`` is log p(U_r | o_{1:r-1}).
    """

    routing_beliefs: np.ndarray
    completed_beliefs: np.ndarray
    routing_log_likelihoods: np.ndarray
    self_transition: np.ndarray


def _normalized(log_joint: np.ndarray) -> tuple[np.ndarray, float]:
    peak = float(log_joint.max())
    weights = np.exp(log_joint - peak)
    total = float(weights.sum())
    return weights / total, peak + float(np.log(total))


def interaction_filter(
    model: BayesianStickyHDPHMM, observations: np.ndarray, *, user_dim: int,
) -> InteractionFilterReadout:
    if not isinstance(model, BayesianStickyHDPHMM) or model.covariance_type != "diagonal":
        raise ValueError(
            "interaction-state filtering needs a diagonal Bayesian sticky HDP-HMM: "
            "only diagonal emissions let an unobserved agent block marginalize exactly"
        )
    sample = model.map_sample()
    rounds = observations.shape[0]
    agent_observed = ~np.isnan(observations[:, user_dim:]).any(axis=1)
    if not agent_observed[:-1].all():
        raise ValueError("only the final round may lack its agent message")
    user_log_emission = _diag_gaussian_logpdf(
        observations[:, :user_dim], sample.means[:, :user_dim], sample.variances[:, :user_dim],
    )
    round_log_emission = np.full_like(user_log_emission, np.nan)
    if agent_observed.any():
        round_log_emission[agent_observed] = _diag_gaussian_logpdf(
            observations[agent_observed], sample.means, sample.variances,
        )
    routing = np.empty((rounds, model.n_states))
    completed = np.full((rounds, model.n_states), np.nan)
    routing_log_likelihoods = np.empty(rounds)
    log_prior = np.log(np.maximum(sample.startprob, 1e-300))
    for index in range(rounds):
        routing[index], routing_log_likelihoods[index] = _normalized(log_prior + user_log_emission[index])
        if agent_observed[index]:
            completed[index], _ = _normalized(log_prior + round_log_emission[index])
            log_prior = np.log(np.maximum(sample.transmat.T @ completed[index], 1e-300))
    return InteractionFilterReadout(
        routing_beliefs=routing,
        completed_beliefs=completed,
        routing_log_likelihoods=routing_log_likelihoods,
        self_transition=np.diag(sample.transmat).copy(),
    )


@dataclass(frozen=True)
class RoutingStateFeatures:
    belief: np.ndarray
    current_state: int
    previous_state: int
    belief_dynamics: np.ndarray


def routing_state_features(readout: InteractionFilterReadout, round_index: int) -> RoutingStateFeatures:
    """HMM features at round ``round_index``'s user boundary.

    The previous state is the last completed (U, A) interaction, which the
    element chain could only reach through an agent-element state.
    """
    belief = readout.routing_beliefs[round_index]
    current = int(np.argmax(belief))
    previous = int(np.argmax(readout.completed_beliefs[round_index - 1])) if round_index else current
    return RoutingStateFeatures(
        belief=belief,
        current_state=current,
        previous_state=previous,
        belief_dynamics=belief_dynamics_features(
            beliefs=np.vstack([readout.completed_beliefs[:round_index], belief[None, :]]),
            step_log_likelihoods=readout.routing_log_likelihoods[: round_index + 1],
            self_transition=readout.self_transition,
        ),
    )


def element_lookback_block(
    observations: np.ndarray, *, round_index: int, lookback_rounds: int,
) -> np.ndarray:
    """Previous rounds' ``[user | agent]`` vectors, most recent first.

    ``lookback_rounds`` counts the current round, as the provider-text lookbacks
    do (1 = current turn only), so the block holds ``lookback_rounds - 1`` slots
    of ``[user | agent | present]``; slots before the conversation start are zero.
    """
    width = observations.shape[1]
    slots = np.zeros((lookback_rounds - 1, width + 1))
    for slot in range(min(lookback_rounds - 1, round_index)):
        slots[slot, :width] = observations[round_index - 1 - slot]
        slots[slot, width] = 1.0
    return slots.reshape(-1)
