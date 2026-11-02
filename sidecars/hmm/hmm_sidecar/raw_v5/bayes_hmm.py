"""Finite-truncation Bayesian sticky HDP-HMM approximation."""

from __future__ import annotations

import logging
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Callable, Literal

import numpy as np


def _regularize_full_covariance(covariance: np.ndarray, min_covar: float) -> np.ndarray:
    """Symmetrize and ridge-regularize a full covariance matrix."""
    covariance = 0.5 * (covariance + covariance.T)
    return covariance + np.eye(covariance.shape[0]) * min_covar


def _full_gaussian_logpdf(
    x: np.ndarray,
    means: np.ndarray,
    covariances: np.ndarray,
) -> np.ndarray:
    """Return log p(x_t | z_t=k) for full-covariance Gaussian states."""
    log_probs = np.empty((x.shape[0], means.shape[0]), dtype=float)
    const = x.shape[1] * np.log(2.0 * np.pi)
    for state in range(means.shape[0]):
        covariance = covariances[state]
        jitter = 0.0
        for _ in range(5):
            try:
                chol = np.linalg.cholesky(
                    covariance + np.eye(covariance.shape[0]) * jitter
                )
                break
            except np.linalg.LinAlgError:
                jitter = 1e-8 if jitter == 0.0 else jitter * 10.0
        else:
            chol = np.linalg.cholesky(_regularize_full_covariance(covariance, 1e-6))
        diff = x - means[state]
        solved = np.linalg.solve(chol, diff.T).T
        quad = np.sum(solved * solved, axis=1)
        log_det = 2.0 * np.log(np.diag(chol)).sum()
        log_probs[:, state] = -0.5 * (const + log_det + quad)
    return log_probs


def _gaussian_logpdf(
    x: np.ndarray,
    means: np.ndarray,
    covariances: np.ndarray,
    *,
    covariance_type: str,
) -> np.ndarray:
    """Return Gaussian emission log likelihoods for one covariance mode."""
    if covariance_type == "diagonal":
        return _diag_gaussian_logpdf(x, means, covariances)
    if covariance_type == "full":
        return _full_gaussian_logpdf(x, means, covariances)
    raise ValueError(f"Unsupported covariance_type: {covariance_type!r}")


try:
    from tqdm.auto import tqdm  # type: ignore[import-not-found]
except ImportError:  # pragma: no cover - progress bars are optional.
    tqdm = None  # type: ignore[assignment]

log = logging.getLogger(__name__)


@dataclass(frozen=True)
class BayesianHMMPosterior:
    """Posterior state responsibilities for one sequence."""

    gamma: np.ndarray
    xi: np.ndarray | None
    log_likelihood: float
    expected_transition_counts: np.ndarray | None = None


@dataclass(frozen=True)
class _ParameterSample:
    """One sampled finite HMM parameter set."""

    startprob: np.ndarray
    transmat: np.ndarray
    means: np.ndarray
    variances: np.ndarray
    log_likelihood: float | None = None
    iteration: int | None = None


class BayesianStickyHDPHMM:
    """Weak-limit sticky HDP-HMM with configurable Gaussian emissions.

    This is a finite truncation of the HDP-HMM rather than an infinite beam
    sampler. It samples transition rows, start probabilities, Gaussian emission
    parameters, and state paths, then uses the retained sample with the highest
    train log likelihood for posterior inference so latent state IDs remain
    semantically stable.
    """

    def __init__(
        self,
        *,
        n_states: int,
        transition_alpha: float,
        sticky_kappa: float,
        start_alpha: float,
        global_alpha: float,
        min_covar: float,
        emission_kappa: float,
        emission_alpha: float,
        seed: int,
        covariance_type: str = "diagonal",
    ) -> None:
        raise NotImplementedError(
            "unsupported by the served Gemini per-message raw-V5 profile"
        )

    def __setstate__(self, state: dict[str, Any]) -> None:
        """Backfill fields added after older HMM artifacts were pickled."""
        self.__dict__.update(state)
        if not hasattr(self, "covariance_type"):
            self.covariance_type = "diagonal"
        if not hasattr(self, "global_cov_"):
            self.global_cov_ = None
        if not hasattr(self, "posterior_inference_"):
            self.posterior_inference_ = "map"
        if not hasattr(self, "map_sample_index_"):
            self.map_sample_index_ = None
        if not hasattr(self, "map_log_likelihood_"):
            self.map_log_likelihood_ = None
        if not hasattr(self, "posterior_state_usage_"):
            self.posterior_state_usage_ = None
        if not hasattr(self, "active_state_count_"):
            self.active_state_count_ = None
        if not hasattr(self, "diagnostics_schema_version_"):
            self.diagnostics_schema_version_ = 0
        if not hasattr(self, "initialization_diagnostics_"):
            self.initialization_diagnostics_ = None
        if not hasattr(self, "iteration_diagnostics_"):
            self.iteration_diagnostics_ = []
        if not hasattr(self, "validation_log_likelihood_history_"):
            self.validation_log_likelihood_history_ = []
        if not hasattr(self, "diagnostics_completion_"):
            self.diagnostics_completion_ = None

    def fit(
        self,
        sequences: list[np.ndarray],
        *,
        iterations: int,
        burn_in: int,
        sample_stride: int,
        progress_desc: str | None = None,
        validation_sequences: list[np.ndarray] | None = None,
        diagnostics_path: str | Path | None = None,
        diagnostics_flush_callback: Callable[[], None] | None = None,
    ) -> "BayesianStickyHDPHMM":
        raise NotImplementedError(
            "unsupported by the served Gemini per-message raw-V5 profile"
        )

    def posterior(self, x: np.ndarray) -> BayesianHMMPosterior:
        """Return posterior responsibilities from the MAP retained sample."""
        if not self.parameter_samples_:
            raise RuntimeError("Bayesian HMM has no parameter samples")
        sample = self.map_sample()
        gamma, xi, loglik = self._posterior_with_sample(x, sample)
        return BayesianHMMPosterior(
            gamma=gamma,
            xi=xi,
            log_likelihood=loglik,
        )

    def map_sample(self) -> _ParameterSample:
        """Return the retained sample with highest train log likelihood."""
        if not self.parameter_samples_:
            raise RuntimeError("Bayesian HMM has no parameter samples")
        if self.map_sample_index_ is None:
            self._select_map_sample()
        if self.map_sample_index_ is None:
            raise RuntimeError("Bayesian HMM MAP sample selection failed")
        return self.parameter_samples_[self.map_sample_index_]

    def _select_map_sample(self) -> None:
        raise NotImplementedError(
            "unsupported by the served Gemini per-message raw-V5 profile"
        )


# Per-batch byte budgets for the padded forward alphas and for the transient
# (batch, states, states) transition tensor built at every recursion step.


def _forward_backward(
    x: np.ndarray,
    startprob: np.ndarray,
    transmat: np.ndarray,
    means: np.ndarray,
    variances: np.ndarray,
    *,
    covariance_type: str = "diagonal",
    transition_output: Literal["full", "sum", "none"] = "full",
) -> tuple[np.ndarray, np.ndarray | None, float]:
    """Return gamma, requested transition statistics, and log likelihood.

    ``transition_output="full"`` preserves the legacy per-timestep xi API.
    ``"sum"`` accumulates the mathematically identical ``xi.sum(axis=0)``
    sufficient statistic in bounded memory, while ``"none"`` skips transition
    output when a caller only needs state responsibilities.
    """
    if transition_output not in {"full", "sum", "none"}:
        raise ValueError(f"Unsupported transition_output={transition_output!r}")
    n_states = len(startprob)
    log_start = np.log(np.maximum(startprob, 1e-300))
    log_trans = np.log(np.maximum(transmat, 1e-300))
    log_emit = _gaussian_logpdf(
        x,
        means,
        variances,
        covariance_type=covariance_type,
    )
    timesteps = x.shape[0]

    alpha = np.empty((timesteps, n_states), dtype=float)
    alpha[0] = log_start + log_emit[0]
    for time in range(1, timesteps):
        alpha[time] = log_emit[time] + _logsumexp(
            alpha[time - 1][:, None] + log_trans,
            axis=0,
        )

    beta = np.zeros((timesteps, n_states), dtype=float)
    for time in range(timesteps - 2, -1, -1):
        beta[time] = _logsumexp(
            log_trans + log_emit[time + 1][None, :] + beta[time + 1][None, :],
            axis=1,
        )

    loglik = float(_logsumexp(alpha[-1], axis=0))
    gamma = np.exp(alpha + beta - loglik)
    gamma = np.maximum(gamma, 1e-300)
    gamma /= gamma.sum(axis=1, keepdims=True)

    transitions: np.ndarray | None = (
        np.zeros((n_states, n_states), dtype=float)
        if transition_output == "sum"
        else None
    )
    if timesteps > 1 and transition_output != "none":
        if transition_output == "full":
            transitions = np.empty(
                (timesteps - 1, n_states, n_states),
                dtype=float,
            )
        for time in range(timesteps - 1):
            log_xi = (
                alpha[time][:, None]
                + log_trans
                + log_emit[time + 1][None, :]
                + beta[time + 1][None, :]
                - loglik
            )
            xi_t = np.exp(log_xi)
            normalized_xi = xi_t / max(xi_t.sum(), 1e-300)
            if transition_output == "sum":
                transitions += normalized_xi
            else:
                transitions[time] = normalized_xi

    return gamma, transitions, loglik


def _diag_gaussian_logpdf(
    x: np.ndarray,
    means: np.ndarray,
    variances: np.ndarray,
) -> np.ndarray:
    """Return log p(x_t | z_t=k) for diagonal Gaussian states."""
    diff = x[:, None, :] - means[None, :, :]
    log_det = np.log(np.maximum(variances, 1e-300)).sum(axis=1)
    quad = ((diff * diff) / np.maximum(variances[None, :, :], 1e-300)).sum(axis=2)
    return -0.5 * (x.shape[1] * np.log(2.0 * np.pi) + log_det[None, :] + quad)


def _logsumexp(x: np.ndarray, axis: int) -> np.ndarray:
    """Small NumPy logsumexp helper."""
    max_value = np.max(x, axis=axis, keepdims=True)
    summed = np.sum(np.exp(x - max_value), axis=axis, keepdims=True)
    return np.squeeze(max_value + np.log(np.maximum(summed, 1e-300)), axis=axis)
