"""Paired-comparison statistics over per-task pass rates (stdlib only)."""

from __future__ import annotations

import math
import random
from collections.abc import Sequence
from dataclasses import dataclass

Z_95 = 1.959963984540054
BOOTSTRAP_RESAMPLES = 10_000
BOOTSTRAP_SEED = 20260904


@dataclass(frozen=True)
class Interval:
    point: float
    low: float
    high: float


def wilson(successes: int, trials: int, z: float = Z_95) -> Interval:
    """Score interval for a binomial proportion; stays inside [0, 1]."""
    if trials == 0:
        return Interval(0.0, 0.0, 1.0)
    p = successes / trials
    denominator = 1 + z * z / trials
    center = (p + z * z / (2 * trials)) / denominator
    half_width = z * math.sqrt(p * (1 - p) / trials + z * z / (4 * trials * trials)) / denominator
    return Interval(p, max(0.0, center - half_width), min(1.0, center + half_width))


def bootstrap_mean(
    values: Sequence[float], *, resamples: int = BOOTSTRAP_RESAMPLES, seed: int = BOOTSTRAP_SEED
) -> Interval:
    """Percentile bootstrap of the mean, resampling the given units (tasks)."""
    if not values:
        return Interval(0.0, 0.0, 0.0)
    rng = random.Random(seed)
    n = len(values)
    means = sorted(sum(rng.choices(values, k=n)) / n for _ in range(resamples))
    return Interval(sum(values) / n, means[int(0.025 * resamples)], means[min(resamples - 1, int(0.975 * resamples))])


def paired_delta(rates_a: dict[str, float], rates_b: dict[str, float]) -> Interval:
    """Bootstrap CI of mean(a - b) over tasks both arms attempted."""
    common = sorted(set(rates_a) & set(rates_b))
    return bootstrap_mean([rates_a[task] - rates_b[task] for task in common])


@dataclass(frozen=True)
class WinsTiesLosses:
    wins: int
    ties: int
    losses: int


def wins_ties_losses(rates_a: dict[str, float], rates_b: dict[str, float]) -> WinsTiesLosses:
    common = set(rates_a) & set(rates_b)
    wins = sum(rates_a[task] > rates_b[task] for task in common)
    losses = sum(rates_a[task] < rates_b[task] for task in common)
    return WinsTiesLosses(wins, len(common) - wins - losses, losses)


def binomial_two_sided_p(successes: int, trials: int) -> float:
    """Exact two-sided p-value for ``successes`` out of ``trials`` at p=0.5."""
    if trials == 0:
        return 1.0
    extreme = min(successes, trials - successes)
    tail = sum(math.comb(trials, k) for k in range(extreme + 1)) / 2**trials
    return min(1.0, 2 * tail)


def sign_test_p(outcome: WinsTiesLosses) -> float:
    """Exact sign test on the non-tied tasks."""
    return binomial_two_sided_p(outcome.wins, outcome.wins + outcome.losses)


@dataclass(frozen=True)
class McNemar:
    """Discordant trial pairs: ``a_only`` passed in A but not B for the same
    (task, attempt) pair; ``b_only`` the reverse."""

    a_only: int
    b_only: int

    @property
    def chi2_corrected(self) -> float:
        discordant = self.a_only + self.b_only
        if discordant == 0:
            return 0.0
        return (abs(self.a_only - self.b_only) - 1) ** 2 / discordant

    @property
    def exact_p(self) -> float:
        return binomial_two_sided_p(self.a_only, self.a_only + self.b_only)


def mcnemar(passes_a: dict[str, bool], passes_b: dict[str, bool]) -> McNemar:
    """``passes_*`` map a pairing key (``task/attempt``) to pass/fail."""
    common = set(passes_a) & set(passes_b)
    return McNemar(
        a_only=sum(passes_a[key] and not passes_b[key] for key in common),
        b_only=sum(passes_b[key] and not passes_a[key] for key in common),
    )


def pass_at_k(n: int, c: int, k: int) -> float:
    """Unbiased pass@k (Chen et al. 2021) for ``c`` passes in ``n`` samples; ``k`` is capped at ``n``."""
    if n == 0:
        return 0.0
    k = min(k, n)
    if n - c < k:
        return 1.0
    return 1.0 - math.comb(n - c, k) / math.comb(n, k)


def mean_pass_at_k(attempts_by_task: dict[str, Sequence[bool]], k: int) -> float:
    if not attempts_by_task:
        return 0.0
    return sum(pass_at_k(len(a), sum(a), k) for a in attempts_by_task.values()) / len(attempts_by_task)


def percentile(values: Sequence[float], q: float) -> float:
    """Nearest-rank percentile, ``q`` in [0, 1]."""
    if not values:
        return 0.0
    ordered = sorted(values)
    return ordered[min(len(ordered) - 1, max(0, math.ceil(q * len(ordered)) - 1))]
