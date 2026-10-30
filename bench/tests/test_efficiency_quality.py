from __future__ import annotations

import math
from datetime import UTC, datetime, timedelta

import pytest

from weave_bench.efficiency.cells import Cell, Harness
from weave_bench.efficiency.estimates import TokenUsage
from weave_bench.efficiency.pooling import (
    MeasurementRun,
    QualityArms,
    QualityCell,
    ResetReason,
    quality_period,
    ratio_period,
)
from weave_bench.efficiency.quality import (
    ArmTasks,
    Interval,
    TaskOutcome,
    capped_pass_rate_penalty,
    kept_rate,
    quality_factor,
)


def outcomes(*attempts: tuple[float, bool | None]) -> list[TaskOutcome]:
    return [TaskOutcome(cost, solved) for cost, solved in attempts]


# Router: cheaper, solves t1 and half of t2. Baseline: dearer, solves t1 and t2.
ROUTER = ArmTasks(
    {"t1": outcomes((1, True), (1, True)), "t2": outcomes((2, True), (2, False)), "t3": outcomes((1, False))}
)
BASELINE = ArmTasks(
    {"t1": outcomes((4, True), (4, True)), "t2": outcomes((6, True), (6, True)), "t3": outcomes((3, False))}
)


def test_kept_rate_by_hand() -> None:
    # Per-task means: router cost 1+2+1=4, solved 1+0.5+0=1.5; baseline cost 4+6+3=13, solved 2.
    naive = 1 - 4 / 13
    realized = 1 - (4 / 1.5) / (13 / 2)
    assert kept_rate(ROUTER, BASELINE, ["t1", "t2", "t3"]) == pytest.approx(realized / naive)


def test_kept_rate_is_one_when_both_arms_solve_the_same() -> None:
    router = ArmTasks({"t": outcomes((1, True), (1, False))})
    baseline = ArmTasks({"t": outcomes((5, True), (5, False))})
    assert kept_rate(router, baseline, ["t"]) == pytest.approx(1.0)


def test_kept_rate_undefined_without_savings_or_solves() -> None:
    nothing_solved = ArmTasks({"t1": outcomes((1, False))})
    assert math.isnan(kept_rate(nothing_solved, BASELINE, ["t1"]))
    same_cost = ArmTasks({"t1": outcomes((4, True))})
    assert math.isnan(kept_rate(same_cost, BASELINE, ["t1"]))


def test_ungraded_attempts_cost_but_do_not_count_as_failures() -> None:
    arm = ArmTasks({"t": outcomes((2, True), (4, None))})
    assert arm.mean_cost("t") == 3
    assert arm.solve_rate("t") == 1.0
    assert (arm.solved(["t"]), arm.graded_count(["t"])) == (1, 1)


def test_quality_factor_counts_and_intervals() -> None:
    factor = quality_factor(ROUTER, BASELINE)
    assert factor is not None
    assert factor.tasks == 3
    assert (factor.router_solved, factor.router_graded, factor.router_attempts) == (3, 5, 5)
    assert (factor.baseline_solved, factor.baseline_graded) == (4, 5)
    assert factor.router_cost_usd == 7 and factor.baseline_cost_usd == 23
    assert factor.router_pass_rate.point == pytest.approx(1.5 / 3)
    assert factor.baseline_pass_rate.point == pytest.approx(2 / 3)
    for interval in (factor.kept_rate, factor.router_pass_rate, factor.baseline_pass_rate):
        assert interval.lower <= interval.upper
    assert 0 <= factor.router_pass_rate.lower and factor.router_pass_rate.upper <= 1


def test_quality_factor_skips_tasks_one_arm_never_graded() -> None:
    router = ArmTasks({**ROUTER.attempts, "broken": outcomes((9, None))})
    factor = quality_factor(router, BASELINE)
    assert factor is not None and factor.tasks == 3


def test_penalty_is_a_one_sided_upper_bound_never_below_one() -> None:
    baseline = Interval(0.6, 0.5, 0.7)
    router = Interval(0.5, 0.4, 0.6)
    z = 1.6448536269514722
    log_error = math.hypot(0.2 / (2 * z) / 0.6, 0.2 / (2 * z) / 0.5)
    expected = math.exp(math.log(0.6 / 0.5) + z * log_error)
    assert capped_pass_rate_penalty(baseline, router) == pytest.approx(expected)
    assert capped_pass_rate_penalty(baseline, router) > 0.6 / 0.5
    # A router that passes far more often earns no credit below 1.
    assert capped_pass_rate_penalty(Interval(0.2, 0.19, 0.21), Interval(0.9, 0.89, 0.91)) == 1.0


def test_penalty_edge_cases() -> None:
    assert capped_pass_rate_penalty(Interval(0.5, 0.4, 0.6), Interval(0.0, 0.0, 0.0)) is None
    assert capped_pass_rate_penalty(Interval(0.0, 0.0, 0.0), Interval(0.5, 0.4, 0.6)) == 1.0


T0 = datetime(2026, 10, 1, tzinfo=UTC)
COMPARISON = Cell(Harness.CODEX, "gpt-6-astra")
SERVED = Cell(Harness.CODEX, "gpt-6-luna")


# Per-task spread, so each run's ratio interval has realistic width.
TASK_SPREAD = (0.7, 1.3, 0.9, 1.2, 0.8, 1.1)


def ratio_run(run_id: str, day: int, served_scale: float, version: str = "0.133.0") -> MeasurementRun:
    comparison_inputs = [100.0 * (index + 1) for index in range(len(TASK_SPREAD))]
    return MeasurementRun(
        run_id=run_id,
        measured_at=T0 + timedelta(days=day),
        harness_versions={Harness.CODEX: version},
        trials={
            COMPARISON: {f"t{index}": [TokenUsage(value, 0, 0, 10)] for index, value in enumerate(comparison_inputs)},
            SERVED: {
                f"t{index}": [TokenUsage(value * spread * served_scale, 0, 0, 10)]
                for index, (value, spread) in enumerate(zip(comparison_inputs, TASK_SPREAD, strict=True))
            },
        },
    )


def test_overlapping_runs_pool_into_one_period() -> None:
    period = ratio_period([ratio_run("r2", 2, 1.02), ratio_run("r1", 1, 1.0)], COMPARISON, SERVED)
    assert period is not None
    assert [run.run_id for run in period.runs] == ["r1", "r2"]
    assert period.reset_reason is ResetReason.INITIAL


def test_non_overlapping_run_resets_for_drift() -> None:
    period = ratio_period([ratio_run("r1", 1, 1.0), ratio_run("r2", 2, 4.0)], COMPARISON, SERVED)
    assert period is not None
    assert [run.run_id for run in period.runs] == ["r2"]
    assert period.reset_reason is ResetReason.DRIFT


def test_harness_upgrade_resets() -> None:
    runs = [ratio_run("r1", 1, 1.0), ratio_run("r2", 2, 1.0, version="0.150.0")]
    period = ratio_period(runs, COMPARISON, SERVED)
    assert period is not None and period.reset_reason is ResetReason.HARNESS_CHANGE


def test_quality_period_resets_on_router_policy_change() -> None:
    cell = QualityCell(Harness.CODEX, "gpt-6-astra")
    arms = QualityArms(ROUTER.attempts, BASELINE.attempts)

    def run(run_id: str, day: int, policy: str) -> MeasurementRun:
        return MeasurementRun(
            run_id,
            T0 + timedelta(days=day),
            {Harness.CODEX: "0.150.0"},
            quality={cell: arms},
            router_policy_version=policy,
        )

    pooled = quality_period([run("q1", 1, "p1"), run("q2", 2, "p1")], cell)
    assert pooled is not None and len(pooled.runs) == 2
    assert pooled.estimate.router_attempts == 10
    reset = quality_period([run("q1", 1, "p1"), run("q2", 2, "p2")], cell)
    assert reset is not None and reset.reset_reason is ResetReason.HARNESS_CHANGE
