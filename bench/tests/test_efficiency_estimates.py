from __future__ import annotations

import pytest

from weave_bench.efficiency.cells import Cell, Harness
from weave_bench.efficiency.estimates import (
    CREDIT_MIN_TASKS,
    BoundRule,
    EfficiencyEstimate,
    TokenUsage,
    Violation,
    trial_usage,
    trial_violations,
    volume_ratio,
    work_ratios,
)


def usage(input_tokens: float, output_tokens: float) -> TokenUsage:
    return TokenUsage(fresh_input=input_tokens, cache_write=0, cache_read=0, output=output_tokens)


COMPARISON = {"a": usage(100, 10), "b": usage(300, 30), "c": usage(50, 5), "only-comparison": usage(1, 1)}
SERVED = {"a": usage(200, 10), "b": usage(300, 60), "c": usage(100, 5), "only-served": usage(1, 1)}


def test_volume_ratio_is_ratio_of_sums_over_shared_tasks() -> None:
    estimate = volume_ratio(COMPARISON, SERVED, lambda u: u.input_total, draws=500)
    assert estimate.ratio == pytest.approx(450 / 600)
    assert estimate.tasks == 3


def test_volume_ratio_interval_brackets_the_task_ratios_and_is_deterministic() -> None:
    first = volume_ratio(COMPARISON, SERVED, lambda u: u.input_total, draws=2_000)
    second = volume_ratio(COMPARISON, SERVED, lambda u: u.input_total, draws=2_000)
    assert first == second
    # Every resample is a ratio of sums of per-task pairs, so it lies between the extreme per-task ratios.
    assert 0.5 <= first.lower <= first.ratio <= first.upper <= 1.0
    assert first.lower < first.upper


def test_volume_ratio_on_identical_tasks_has_no_spread() -> None:
    same = {"a": usage(10, 1), "b": usage(10, 1)}
    estimate = volume_ratio(same, {"a": usage(20, 1), "b": usage(20, 1)}, lambda u: u.input_total, draws=200)
    assert (estimate.ratio, estimate.lower, estimate.upper) == (0.5, 0.5, 0.5)


def test_volume_ratio_without_shared_tasks_raises() -> None:
    with pytest.raises(ValueError):
        volume_ratio({"a": usage(1, 1)}, {"b": usage(1, 1)}, lambda u: u.output)


def test_work_ratios_keep_input_and_output_apart() -> None:
    work = work_ratios(COMPARISON, SERVED, native=True)
    assert work.input.ratio == pytest.approx(450 / 600)
    assert work.output.ratio == pytest.approx(45 / 75)


@pytest.mark.parametrize(
    ("estimate", "native", "rule", "credited", "capped"),
    [
        # Below 1: the point in the model's own harness, the lower bound elsewhere.
        (EfficiencyEstimate(0.8, 0.6, 0.9, 25), True, BoundRule.POINT, 0.8, 0.8),
        (EfficiencyEstimate(0.8, 0.6, 0.9, 25), False, BoundRule.LOWER, 0.6, 0.6),
        # Above 1 with an interval reaching 1: held at 1.
        (EfficiencyEstimate(1.2, 0.95, 1.4, 25), True, BoundRule.HELD_AT_ONE, 1.0, 1.0),
        # Above 1 everywhere on enough tasks: credited at the lower bound, capped at 1.
        (EfficiencyEstimate(1.5, 1.1, 2.0, CREDIT_MIN_TASKS), True, BoundRule.LOWER, 1.1, 1.0),
        # Above 1 everywhere but too few tasks: held.
        (EfficiencyEstimate(1.5, 1.1, 2.0, CREDIT_MIN_TASKS - 1), True, BoundRule.HELD_AT_ONE, 1.0, 1.0),
        # Non-native uses its lower bound to decide, so a point above 1 with a lower below 1 is applied at the lower.
        (EfficiencyEstimate(1.3, 0.7, 1.9, 25), False, BoundRule.LOWER, 0.7, 0.7),
    ],
)
def test_bound_rules(
    estimate: EfficiencyEstimate, native: bool, rule: BoundRule, credited: float, capped: float
) -> None:
    assert estimate.bound_rule(native=native) is rule
    assert estimate.credited(native=native) == pytest.approx(credited)
    assert estimate.capped(native=native) == pytest.approx(capped)


def row(model: str, provider: str, **fields: object) -> dict[str, object]:
    return {
        "decision_model": model,
        "decision_provider": provider,
        "upstream_status_code": 200,
        "turn_type": "main_loop",
        "input_tokens": 0,
        "output_tokens": 0,
        "cache_creation_tokens": 0,
        "cache_read_tokens": 0,
        **fields,
    }


def test_trial_usage_normalizes_cache_inclusive_input_and_skips_utility_turns() -> None:
    rows = [
        # Anthropic reports fresh input only.
        row("claude-opus-5-5", "anthropic", input_tokens=10, cache_read_tokens=90, output_tokens=5),
        # Everyone else includes cached tokens in input_tokens.
        row("gpt-6-luna", "openai", input_tokens=100, cache_read_tokens=80, output_tokens=7),
        row("claude-haiku-5-5", "anthropic", input_tokens=1_000, output_tokens=1_000, turn_type="title_gen"),
    ]
    total = trial_usage(rows)
    assert total == TokenUsage(fresh_input=30, cache_write=0, cache_read=170, output=12)


CELL = Cell(Harness.CLAUDE_CODE, "gpt-6-luna")


def test_clean_trial_has_no_violations() -> None:
    assert trial_violations(CELL, [row("gpt-6-luna", "openai")] * 3) == []


def test_unpinned_model_and_failed_requests_are_violations() -> None:
    rows = [row("gpt-6-luna", "openai"), row("gpt-6-sol", "openai", upstream_status_code=500)]
    rows.append(row("gpt-6-luna", "openai", upstream_status_code=429))
    assert trial_violations(CELL, rows) == [Violation.FAILED_REQUESTS, Violation.WRONG_MODEL]


def test_a_few_anthropic_side_requests_are_allowed_but_not_many() -> None:
    side = row("claude-haiku-5-5", "anthropic")
    assert trial_violations(CELL, [row("gpt-6-luna", "openai")] * 19 + [side]) == []
    assert trial_violations(CELL, [row("gpt-6-luna", "openai")] * 3 + [side]) == [Violation.WRONG_MODEL]


def test_trial_with_only_utility_rows_has_no_measurement() -> None:
    assert trial_violations(CELL, [row("gpt-6-luna", "openai", turn_type="compaction")]) == [Violation.NO_ROWS]
