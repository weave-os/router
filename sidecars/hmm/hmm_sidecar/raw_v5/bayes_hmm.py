"""Finite-truncation Bayesian sticky HDP-HMM approximation."""

from __future__ import annotations

import logging
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Callable, Iterator, Literal

import numpy as np

try:
    from tqdm.auto import tqdm  # type: ignore[import-not-found]
except ImportError:  # pragma: no cover - progress bars are optional.
    tqdm = None  # type: ignore[assignment]

log = logging.getLogger(__name__)

ACTIVE_STATE_USAGE_THRESHOLD = 0.001
BAYESIAN_HMM_DIAGNOSTICS_SCHEMA_VERSION = 1


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


@dataclass(frozen=True)
class _DiagnosticSequenceLayout:
    """Flat views used to compute bounded path diagnostics efficiently."""

    observations: np.ndarray
    start_offsets: np.ndarray
    transition_mask: np.ndarray
    sequence_lengths: np.ndarray


@dataclass(frozen=True)
class _ForwardBatch:
    """Forward-recursion state for one longest-first batch of sequences.

    ``indices`` maps batch rows back to corpus positions; ``alpha`` and
    ``log_emit`` are padded to ``lengths[0]`` and only valid up to each row's
    own length.
    """

    indices: np.ndarray
    lengths: np.ndarray
    active_counts: np.ndarray
    log_emit: np.ndarray
    alpha: np.ndarray
    log_likelihoods: np.ndarray
    log_trans: np.ndarray


@dataclass(frozen=True)
class _SampledPaths:
    """One Gibbs sweep's sampled paths plus the totals the loop reports."""

    paths: list[np.ndarray]
    total_log_likelihood: float
    emission_logprob: float
    previous_emission_logprob: float | None


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

    def fit_continue(
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

    def _reset_diagnostics(self) -> None:
        raise NotImplementedError("not on the classifier serving path")

    @staticmethod
    def _validate_sampling_schedule(
        *,
        iterations: int,
        burn_in: int,
        sample_stride: int,
    ) -> None:
        raise NotImplementedError("not on the classifier serving path")

    def _run_gibbs(
        self,
        sequences: list[np.ndarray],
        paths: list[np.ndarray],
        *,
        iterations: int,
        burn_in: int,
        sample_stride: int,
        progress_desc: str | None,
        validation_sequences: list[np.ndarray] | None,
        diagnostics_path: Path | None,
        diagnostics_flush_callback: Callable[[], None] | None,
    ) -> "BayesianStickyHDPHMM":
        raise NotImplementedError("not on the classifier serving path")

    def diagnostics_summary(self) -> dict[str, Any]:
        raise NotImplementedError("not on the classifier serving path")

    def write_diagnostics(self, path: str | Path) -> None:
        raise NotImplementedError("not on the classifier serving path")

    def _state_usage_diagnostics(
        self,
        layout: _DiagnosticSequenceLayout,
        flat_paths: np.ndarray,
    ) -> dict[str, Any]:
        raise NotImplementedError("not on the classifier serving path")

    def _path_structure_diagnostics(
        self,
        layout: _DiagnosticSequenceLayout,
        flat_paths: np.ndarray,
    ) -> dict[str, Any]:
        raise NotImplementedError("not on the classifier serving path")

    def _path_movement_diagnostics(
        self,
        layout: _DiagnosticSequenceLayout,
        previous_paths: np.ndarray,
        current_paths: np.ndarray,
    ) -> dict[str, Any]:
        raise NotImplementedError("not on the classifier serving path")

    def _conditioned_start_transition_log_likelihood(
        self,
        layout: _DiagnosticSequenceLayout,
        flat_paths: np.ndarray,
    ) -> tuple[float, float]:
        raise NotImplementedError("not on the classifier serving path")

    def _conditioned_emission_log_likelihood(
        self,
        layout: _DiagnosticSequenceLayout,
        flat_paths: np.ndarray,
    ) -> float:
        raise NotImplementedError("not on the classifier serving path")

    def _marginal_log_likelihood(self, sequences: list[np.ndarray]) -> float:
        raise NotImplementedError("not on the classifier serving path")

    def _sequence_log_likelihood(self, x: np.ndarray) -> float:
        raise NotImplementedError("not on the classifier serving path")

    def _iter_forward_batches(
        self,
        sequences: list[np.ndarray],
    ) -> Iterator[_ForwardBatch]:
        raise NotImplementedError("not on the classifier serving path")

    def _transition_diagnostics(
        self,
        *,
        previous_global_beta: np.ndarray | None,
        previous_startprob: np.ndarray | None,
        previous_transmat: np.ndarray | None,
        state_counts: np.ndarray,
    ) -> dict[str, Any]:
        raise NotImplementedError("not on the classifier serving path")

    def _emission_diagnostics(
        self,
        *,
        previous_means: np.ndarray | None,
        previous_variances: np.ndarray | None,
    ) -> dict[str, Any]:
        raise NotImplementedError("not on the classifier serving path")

    def posterior(self, x: np.ndarray) -> BayesianHMMPosterior:
        raise NotImplementedError("not on the classifier serving path")

    def posterior_sufficient_statistics(self, x: np.ndarray) -> BayesianHMMPosterior:
        raise NotImplementedError("not on the classifier serving path")

    def filter_start(self, observation: np.ndarray) -> tuple[np.ndarray, float]:
        raise NotImplementedError("not on the classifier serving path")

    def filter_step(
        self,
        belief: np.ndarray,
        observation: np.ndarray,
    ) -> tuple[np.ndarray, float]:
        raise NotImplementedError("not on the classifier serving path")

    def forward_filter(self, x: np.ndarray) -> tuple[np.ndarray, float]:
        raise NotImplementedError("not on the classifier serving path")

    def forward_filter_steps(self, x: np.ndarray) -> tuple[np.ndarray, np.ndarray]:
        raise NotImplementedError("not on the classifier serving path")

    def self_transition_probabilities(self) -> np.ndarray:
        raise NotImplementedError("not on the classifier serving path")

    def _filter_update(
        self,
        log_prior: np.ndarray,
        observation: np.ndarray,
        sample: _ParameterSample,
    ) -> tuple[np.ndarray, float]:
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

    def _map_state_usage(
        self,
        sequences: list[np.ndarray],
        *,
        progress_desc: str | None = None,
    ) -> np.ndarray:
        raise NotImplementedError("not on the classifier serving path")

    def _posterior_with_sample(
        self,
        x: np.ndarray,
        sample: _ParameterSample,
        *,
        transition_output: Literal["full", "sum", "none"] = "full",
    ) -> tuple[np.ndarray, np.ndarray | None, float]:
        raise NotImplementedError("not on the classifier serving path")

    def _initialize(self, sequences: list[np.ndarray]) -> list[np.ndarray]:
        raise NotImplementedError("not on the classifier serving path")

    def _assign_paths(self, sequences: list[np.ndarray]) -> list[np.ndarray]:
        raise NotImplementedError("not on the classifier serving path")

    def _sample_transition_model(self, paths: list[np.ndarray]) -> None:
        raise NotImplementedError("not on the classifier serving path")

    def _sample_global_beta(
        self,
        trans_counts: np.ndarray,
        start_counts: np.ndarray,
        *,
        kappa: float,
    ) -> None:
        raise NotImplementedError("not on the classifier serving path")

    def _sample_table_counts(
        self,
        counts: np.ndarray,
        concentrations: np.ndarray,
    ) -> np.ndarray:
        raise NotImplementedError("not on the classifier serving path")

    def _sample_emissions(
        self,
        sequences: list[np.ndarray],
        paths: list[np.ndarray],
        *,
        flat_observations: np.ndarray | None = None,
        flat_paths: np.ndarray | None = None,
    ) -> None:
        raise NotImplementedError("not on the classifier serving path")

    def _sample_full_emissions(self, members_by_state: list[np.ndarray]) -> None:
        raise NotImplementedError("not on the classifier serving path")

    def _sample_path(self, x: np.ndarray) -> tuple[np.ndarray, float]:
        raise NotImplementedError("not on the classifier serving path")

    def _sample_paths(
        self,
        sequences: list[np.ndarray],
        *,
        previous_paths: list[np.ndarray] | None = None,
        progress: Any | None = None,
    ) -> _SampledPaths:
        raise NotImplementedError("not on the classifier serving path")


# Per-batch byte budgets for the padded forward alphas and for the transient
# (batch, states, states) transition tensor built at every recursion step.
_FFBS_ALPHA_BYTES = 64 * 1024**2
_FFBS_STEP_BYTES = 32 * 1024**2
_FFBS_MAX_BATCH_SEQUENCES = 4096


def _ffbs_batch_bounds(
    sorted_lengths: np.ndarray,
    n_states: int,
) -> list[tuple[int, int]]:
    raise NotImplementedError("not on the classifier serving path")


def _active_sequence_counts(sorted_lengths: np.ndarray) -> np.ndarray:
    raise NotImplementedError("not on the classifier serving path")


def _batched_forward_alphas(
    log_emit: np.ndarray,
    active_counts: np.ndarray,
    *,
    log_start: np.ndarray,
    log_trans: np.ndarray,
) -> np.ndarray:
    raise NotImplementedError("not on the classifier serving path")


def _sample_categorical_rows(
    log_probs: np.ndarray,
    uniforms: np.ndarray,
) -> np.ndarray:
    raise NotImplementedError("not on the classifier serving path")


def _gathered_emission_logprob(log_emit: np.ndarray, path: np.ndarray) -> float:
    raise NotImplementedError("not on the classifier serving path")


def _corpus_order_sum(values: np.ndarray) -> float:
    raise NotImplementedError("not on the classifier serving path")


def _diagnostic_sequence_layout(
    sequences: list[np.ndarray],
) -> _DiagnosticSequenceLayout:
    raise NotImplementedError("not on the classifier serving path")


def _complete_data_score(
    start_logprob: float,
    transition_logprob: float,
    emission_logprob: float,
) -> dict[str, float]:
    raise NotImplementedError("not on the classifier serving path")


def _entropy(probabilities: np.ndarray) -> float:
    raise NotImplementedError("not on the classifier serving path")


def _rms_delta(current: np.ndarray, previous: np.ndarray | None) -> float | None:
    raise NotImplementedError("not on the classifier serving path")


def _l1_delta(current: np.ndarray, previous: np.ndarray | None) -> float | None:
    raise NotImplementedError("not on the classifier serving path")


def _max_abs_delta(
    current: np.ndarray,
    previous: np.ndarray | None,
) -> float | None:
    raise NotImplementedError("not on the classifier serving path")


def _relative_frobenius_delta(
    current: np.ndarray,
    previous: np.ndarray | None,
) -> float | None:
    raise NotImplementedError("not on the classifier serving path")


def _covariance_diagonal(
    variances: np.ndarray,
    *,
    covariance_type: str,
) -> np.ndarray:
    raise NotImplementedError("not on the classifier serving path")


def _compact_state_usage(state_usage: dict[str, Any]) -> dict[str, Any]:
    raise NotImplementedError("not on the classifier serving path")


def _format_optional_float(
    value: float | None,
    *,
    precision: int,
    signed: bool = False,
) -> str:
    raise NotImplementedError("not on the classifier serving path")


def _append_jsonl(path: Path, event: dict[str, Any]) -> None:
    raise NotImplementedError("not on the classifier serving path")


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


def _full_gaussian_logpdf(
    x: np.ndarray,
    means: np.ndarray,
    covariances: np.ndarray,
) -> np.ndarray:
    raise NotImplementedError("not on the classifier serving path")


def _gaussian_logpdf(
    x: np.ndarray,
    means: np.ndarray,
    covariances: np.ndarray,
    *,
    covariance_type: str,
) -> np.ndarray:
    raise NotImplementedError("not on the classifier serving path")


def _empirical_covariance(x: np.ndarray, mean: np.ndarray) -> np.ndarray:
    raise NotImplementedError("not on the classifier serving path")


def _regularize_full_covariance(covariance: np.ndarray, min_covar: float) -> np.ndarray:
    raise NotImplementedError("not on the classifier serving path")


def _sample_wishart(
    scale: np.ndarray,
    *,
    df: float,
    rng: np.random.Generator,
) -> np.ndarray:
    raise NotImplementedError("not on the classifier serving path")


def _sample_inverse_wishart(
    scale: np.ndarray,
    *,
    df: float,
    rng: np.random.Generator,
    min_covar: float,
) -> np.ndarray:
    raise NotImplementedError("not on the classifier serving path")


def _logsumexp(x: np.ndarray, axis: int) -> np.ndarray:
    raise NotImplementedError("not on the classifier serving path")


def _normalize_rows(x: np.ndarray) -> np.ndarray:
    raise NotImplementedError("not on the classifier serving path")


def _sample_log_probs(log_probs: np.ndarray, rng: np.random.Generator) -> int:
    raise NotImplementedError("not on the classifier serving path")


def posterior_sequences(
    model: BayesianStickyHDPHMM,
    records: list[dict[str, Any]],
    *,
    posterior_split: str | None = None,
) -> list[Any]:
    raise NotImplementedError("not on the classifier serving path")
