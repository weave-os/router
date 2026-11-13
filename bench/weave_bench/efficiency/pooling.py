"""How repeated measurements of one cell combine over time.

Each cell pools its trials over a *period* of runs, so a refresh tightens the
estimate instead of replacing it. A run starts a new period, measured from that
run alone, when:

- ``DRIFT``: one of its intervals does not overlap the pooled period's
  (ratios: input and output; quality: kept rate and both pass rates), or
- ``HARNESS_CHANGE``: the harness version, a provider revision behind either
  model, or (quality only) the router's policy version changed.

A cell's first measurement opens an ``INITIAL`` period. Replay every run the
cell was ever measured in, oldest first, or the current period is misdated.
"""

from __future__ import annotations

from collections.abc import Callable, Hashable, Iterable, Mapping, Sequence
from dataclasses import dataclass, field
from datetime import datetime
from enum import StrEnum

from weave_bench.efficiency.cells import Cell, Harness
from weave_bench.efficiency.estimates import TokenUsage, WorkRatios, mean_usage, work_ratios
from weave_bench.efficiency.quality import ArmTasks, Interval, QualityFactor, TaskOutcome, quality_factor


class ResetReason(StrEnum):
    INITIAL = "initial"
    DRIFT = "drift"
    HARNESS_CHANGE = "harness_change"


@dataclass(frozen=True)
class QualityArms:
    router: Mapping[str, Sequence[TaskOutcome]]
    baseline: Mapping[str, Sequence[TaskOutcome]]


@dataclass(frozen=True)
class QualityCell:
    harness: Harness
    baseline_model: str


@dataclass(frozen=True)
class MeasurementRun:
    run_id: str
    measured_at: datetime
    harness_versions: Mapping[Harness, str]
    trials: Mapping[Cell, Mapping[str, Sequence[TokenUsage]]] = field(default_factory=dict)
    quality: Mapping[QualityCell, QualityArms] = field(default_factory=dict)
    # Provider revision behind a model id, where one is known.
    model_revisions: Mapping[str, str] = field(default_factory=dict)
    router_policy_version: str | None = None


@dataclass(frozen=True)
class Period[Estimate]:
    runs: tuple[MeasurementRun, ...]
    reset_reason: ResetReason
    estimate: Estimate


def current_period[Estimate](
    runs: Sequence[MeasurementRun],
    *,
    measures: Callable[[MeasurementRun], bool],
    estimate: Callable[[Sequence[MeasurementRun]], Estimate],
    identity: Callable[[MeasurementRun], Hashable],
    intervals: Callable[[Estimate], Sequence[Interval | None]],
) -> Period[Estimate] | None:
    period: Period[Estimate] | None = None
    for run in sorted(runs, key=lambda run: run.measured_at):
        if not measures(run):
            continue
        run_estimate = estimate([run])
        if period is None:
            period = Period((run,), ResetReason.INITIAL, run_estimate)
        elif identity(run) != identity(period.runs[-1]):
            period = Period((run,), ResetReason.HARNESS_CHANGE, run_estimate)
        elif not all(
            pooled is None or latest is None or pooled.overlaps(latest)
            for pooled, latest in zip(intervals(period.estimate), intervals(run_estimate), strict=True)
        ):
            period = Period((run,), ResetReason.DRIFT, run_estimate)
        else:
            pooled_runs = (*period.runs, run)
            period = Period(pooled_runs, period.reset_reason, estimate(pooled_runs))
    return period


def pooled_usage(runs: Iterable[MeasurementRun], cell: Cell) -> dict[str, TokenUsage]:
    """Mean usage per task over every attempt the runs hold."""
    by_task: dict[str, list[TokenUsage]] = {}
    for run in runs:
        for task, attempts in run.trials.get(cell, {}).items():
            by_task.setdefault(task, []).extend(attempts)
    return {task: mean_usage(attempts) for task, attempts in by_task.items() if attempts}


def ratio_period(runs: Sequence[MeasurementRun], comparison: Cell, served: Cell) -> Period[WorkRatios] | None:
    return current_period(
        runs,
        measures=lambda run: bool(set(run.trials.get(comparison, {})) & set(run.trials.get(served, {}))),
        estimate=lambda period_runs: work_ratios(
            pooled_usage(period_runs, comparison), pooled_usage(period_runs, served), native=served.native
        ),
        identity=lambda run: (
            run.harness_versions.get(served.harness),
            run.model_revisions.get(comparison.model),
            run.model_revisions.get(served.model),
        ),
        intervals=lambda work: (
            Interval(work.input.ratio, work.input.lower, work.input.upper),
            Interval(work.output.ratio, work.output.lower, work.output.upper),
        ),
    )


def _pooled_arms(runs: Iterable[MeasurementRun], cell: QualityCell) -> tuple[ArmTasks, ArmTasks]:
    router: dict[str, list[TaskOutcome]] = {}
    baseline: dict[str, list[TaskOutcome]] = {}
    for run in runs:
        arms = run.quality.get(cell)
        if arms is None:
            continue
        for task, outcomes in arms.router.items():
            router.setdefault(task, []).extend(outcomes)
        for task, outcomes in arms.baseline.items():
            baseline.setdefault(task, []).extend(outcomes)
    return ArmTasks(router), ArmTasks(baseline)


def pooled_quality_factor(runs: Sequence[MeasurementRun], cell: QualityCell) -> QualityFactor | None:
    return quality_factor(*_pooled_arms(runs, cell))


def quality_period(runs: Sequence[MeasurementRun], cell: QualityCell) -> Period[QualityFactor] | None:
    def estimate(period_runs: Sequence[MeasurementRun]) -> QualityFactor:
        factor = pooled_quality_factor(period_runs, cell)
        if factor is None:
            raise ValueError(f"{cell} has no task both arms graded over {[run.run_id for run in period_runs]}")
        return factor

    return current_period(
        runs,
        measures=lambda run: pooled_quality_factor([run], cell) is not None,
        estimate=estimate,
        identity=lambda run: (
            run.harness_versions.get(cell.harness),
            run.model_revisions.get(cell.baseline_model),
            run.router_policy_version,
        ),
        intervals=lambda factor: (factor.kept_rate, factor.router_pass_rate, factor.baseline_pass_rate),
    )
