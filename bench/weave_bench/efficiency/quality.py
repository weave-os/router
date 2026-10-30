"""Quality factors: does the router's cheaper mix still solve the tasks?

Work ratios say how many tokens the comparison model would have needed, not
whether either side solved the task. On a pass/fail benchmark the router arm
(unpinned routing) and an always-X arm (every request pinned to the comparison
model X) run the same tasks, and two numbers come out:

- the **kept rate**: savings on cost per *solved* task over savings on cost per
  attempt. 1 means task success changes nothing; 0.65 means 65% of the naive
  per-attempt savings survive once failures are paid for.
- each arm's **pass rate**, whose ratio is the penalty savings are divided by
  (``capped_pass_rate_penalty``).

Every statistic is per task (the mean over a task's attempts), and intervals
resample tasks, so repeated attempts on one task add no false precision.
"""

from __future__ import annotations

import math
import random
from collections.abc import Callable, Iterable, Mapping, Sequence
from dataclasses import dataclass

BOOTSTRAP_DRAWS = 10_000
BOOTSTRAP_INTERVAL = 0.90
BOOTSTRAP_SEED = 11
# The 95th percentile of the standard normal: the pass-rate intervals are
# two-sided 90%, so each half-width spans this many standard errors, and it is
# also the z of the penalty's one-sided 95% bound.
Z_ONE_SIDED_95 = 1.6448536269514722


@dataclass(frozen=True)
class TaskOutcome:
    """One attempt. ``solved`` is None when the attempt errored before the
    verifier ran (infrastructure, not the agent): its cost still counts, but it
    is neither a pass nor a failure."""

    cost_usd: float
    solved: bool | None


@dataclass(frozen=True)
class Interval:
    point: float
    lower: float
    upper: float

    def overlaps(self, other: Interval) -> bool:
        return self.lower <= other.upper and other.lower <= self.upper


def _mean(values: Sequence[float]) -> float:
    return sum(values) / len(values)


@dataclass(frozen=True)
class ArmTasks:
    attempts: Mapping[str, Sequence[TaskOutcome]]

    def mean_cost(self, task: str) -> float:
        return _mean([outcome.cost_usd for outcome in self.attempts[task]])

    def graded(self, task: str) -> list[bool]:
        return [outcome.solved for outcome in self.attempts[task] if outcome.solved is not None]

    def solve_rate(self, task: str) -> float:
        return _mean([float(solved) for solved in self.graded(task)])

    def solved(self, tasks: Iterable[str]) -> int:
        return sum(sum(self.graded(task)) for task in tasks)

    def graded_count(self, tasks: Iterable[str]) -> int:
        return sum(len(self.graded(task)) for task in tasks)

    def cost(self, tasks: Iterable[str]) -> float:
        return sum(outcome.cost_usd for task in tasks for outcome in self.attempts[task])


def kept_rate(router: ArmTasks, baseline: ArmTasks, sample: Sequence[str]) -> float:
    """Realized savings on cost per solved task over naive savings on cost per
    attempt; NaN where either is undefined (no naive savings, nothing solved)."""
    router_cost = sum(router.mean_cost(task) for task in sample)
    baseline_cost = sum(baseline.mean_cost(task) for task in sample)
    router_solved = sum(router.solve_rate(task) for task in sample)
    baseline_solved = sum(baseline.solve_rate(task) for task in sample)
    if not baseline_cost or not router_solved or not baseline_solved or router_cost == baseline_cost:
        return math.nan
    naive_savings = 1 - router_cost / baseline_cost
    realized_savings = 1 - (router_cost / router_solved) / (baseline_cost / baseline_solved)
    return realized_savings / naive_savings


def task_bootstrap(
    tasks: Sequence[str],
    statistic: Callable[[Sequence[str]], float],
    *,
    draws: int = BOOTSTRAP_DRAWS,
    interval: float = BOOTSTRAP_INTERVAL,
    seed: int = BOOTSTRAP_SEED,
) -> Interval:
    """Point estimate and percentile interval over resampled tasks. Resamples
    where the statistic is undefined (NaN) are dropped; the point must be defined."""
    generator = random.Random(seed)
    values = sorted(
        value
        for value in (statistic([generator.choice(tasks) for _ in tasks]) for _ in range(draws))
        if not math.isnan(value)
    )
    tail = (1 - interval) / 2
    return Interval(statistic(tasks), values[int(tail * len(values))], values[int((1 - tail) * len(values))])


@dataclass(frozen=True)
class QualityFactor:
    # None when undefined: the router solved nothing, or there were no naive savings.
    kept_rate: Interval | None
    router_pass_rate: Interval
    baseline_pass_rate: Interval
    tasks: int
    router_solved: int
    router_graded: int
    router_attempts: int
    router_cost_usd: float
    baseline_solved: int
    baseline_graded: int
    baseline_attempts: int
    baseline_cost_usd: float


def quality_factor(router: ArmTasks, baseline: ArmTasks) -> QualityFactor | None:
    """Over the tasks both arms graded at least once; None when they share none."""
    tasks = sorted(
        task for task in set(router.attempts) & set(baseline.attempts) if router.graded(task) and baseline.graded(task)
    )
    if not tasks:
        return None
    kept_rate_defined = not math.isnan(kept_rate(router, baseline, tasks))
    return QualityFactor(
        kept_rate=task_bootstrap(tasks, lambda sample: kept_rate(router, baseline, sample))
        if kept_rate_defined
        else None,
        router_pass_rate=task_bootstrap(tasks, lambda sample: _mean([router.solve_rate(task) for task in sample])),
        baseline_pass_rate=task_bootstrap(tasks, lambda sample: _mean([baseline.solve_rate(task) for task in sample])),
        tasks=len(tasks),
        router_solved=router.solved(tasks),
        router_graded=router.graded_count(tasks),
        router_attempts=sum(len(router.attempts[task]) for task in tasks),
        router_cost_usd=router.cost(tasks),
        baseline_solved=baseline.solved(tasks),
        baseline_graded=baseline.graded_count(tasks),
        baseline_attempts=sum(len(baseline.attempts[task]) for task in tasks),
        baseline_cost_usd=baseline.cost(tasks),
    )


def capped_pass_rate_penalty(baseline: Interval, router: Interval) -> float | None:
    """The comparison model's pass rate over the router's, as a one-sided 95%
    upper bound and never below 1: the comparison cost is divided by it to price
    the comparison per task the router solved, and a router that passes more
    often earns no extra credit. Each arm's standard error comes from its
    task-bootstrap interval; the delta method combines them on the log scale,
    ignoring the arms' shared tasks, which only widens the bound. None when the
    router solved nothing, where no finite penalty exists."""
    if router.point <= 0:
        return None
    if baseline.point <= 0:
        return 1.0
    baseline_relative_error = (baseline.upper - baseline.lower) / (2 * Z_ONE_SIDED_95) / baseline.point
    router_relative_error = (router.upper - router.lower) / (2 * Z_ONE_SIDED_95) / router.point
    log_ratio_error = math.hypot(baseline_relative_error, router_relative_error)
    upper_bound = math.exp(math.log(baseline.point / router.point) + Z_ONE_SIDED_95 * log_ratio_error)
    return max(upper_bound, 1.0)
