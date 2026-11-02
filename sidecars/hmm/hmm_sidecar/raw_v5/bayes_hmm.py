"""Finite-truncation Bayesian sticky HDP-HMM approximation."""

from __future__ import annotations

import logging
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Callable, Literal

import numpy as np

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
        raise NotImplementedError("not on the classifier serving path")

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
        raise NotImplementedError("not on the classifier serving path")

    def posterior(self, x: np.ndarray) -> BayesianHMMPosterior:
        raise NotImplementedError("not on the classifier serving path")

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
        raise NotImplementedError("not on the classifier serving path")


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
    raise NotImplementedError("not on the classifier serving path")


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
    raise NotImplementedError("not on the classifier serving path")
