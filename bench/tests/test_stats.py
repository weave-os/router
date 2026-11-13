from __future__ import annotations

import pytest

from weave_bench.stats import (
    Interval,
    McNemar,
    binomial_two_sided_p,
    bootstrap_mean,
    mcnemar,
    mean_pass_at_k,
    paired_delta,
    pass_at_k,
    percentile,
    sign_test_p,
    wilson,
    wins_ties_losses,
)


def test_wilson_matches_reference_values() -> None:
    interval = wilson(80, 124)
    assert interval.point == pytest.approx(80 / 124)
    # Wilson score interval computed by hand for 80/124, z=1.96.
    assert interval.low == pytest.approx(0.5577, abs=1e-4)
    assert interval.high == pytest.approx(0.7239, abs=1e-4)


def test_wilson_edges_stay_in_unit_interval() -> None:
    assert wilson(0, 0) == Interval(0.0, 0.0, 1.0)
    zero = wilson(0, 10)
    full = wilson(10, 10)
    assert zero.low == 0.0 and zero.high > 0.0
    assert full.high == pytest.approx(1.0) and full.low < 1.0


def test_bootstrap_is_deterministic_and_brackets_the_mean() -> None:
    values = [0.0, 0.5, 1.0, 1.0, 0.5, 0.0, 1.0]
    first, second = bootstrap_mean(values), bootstrap_mean(values)
    assert first == second
    assert first.low <= first.point <= first.high
    assert first.point == pytest.approx(sum(values) / len(values))
    assert bootstrap_mean([]) == Interval(0.0, 0.0, 0.0)
    constant = bootstrap_mean([1.0, 1.0, 1.0])
    assert constant.low == constant.high == 1.0


def test_paired_delta_uses_common_tasks_only() -> None:
    delta = paired_delta({"a": 1.0, "b": 0.0, "only_a": 1.0}, {"a": 0.0, "b": 0.0, "only_b": 1.0})
    assert delta.point == pytest.approx(0.5)


def test_wins_ties_losses_and_sign_test() -> None:
    outcome = wins_ties_losses({"a": 1.0, "b": 0.5, "c": 0.0, "d": 1.0}, {"a": 0.0, "b": 0.5, "c": 1.0, "d": 0.0})
    assert (outcome.wins, outcome.ties, outcome.losses) == (2, 1, 1)
    assert sign_test_p(outcome) == 1.0
    assert sign_test_p(wins_ties_losses({"t": 1.0}, {"t": 1.0})) == 1.0


def test_binomial_two_sided_p_reference_values() -> None:
    assert binomial_two_sided_p(0, 0) == 1.0
    assert binomial_two_sided_p(0, 5) == pytest.approx(2 / 32)
    assert binomial_two_sided_p(5, 5) == pytest.approx(2 / 32)
    assert binomial_two_sided_p(6, 8) == pytest.approx(2 * (1 + 8 + 28) / 256)
    assert binomial_two_sided_p(4, 8) == 1.0


def test_mcnemar_counts_discordant_pairs_by_pairing_key() -> None:
    result = mcnemar(
        {"t1/1": True, "t1/2": True, "t2/1": False, "t3/1": True, "unpaired/1": True},
        {"t1/1": False, "t1/2": True, "t2/1": True, "t3/1": False},
    )
    assert (result.a_only, result.b_only) == (2, 1)
    assert result.chi2_corrected == pytest.approx(0.0)
    assert result.exact_p == 1.0
    lopsided = McNemar(a_only=10, b_only=1)
    assert lopsided.chi2_corrected == pytest.approx(64 / 11)
    assert lopsided.exact_p == pytest.approx(2 * (1 + 11) / 2**11)
    assert McNemar(0, 0).chi2_corrected == 0.0


def test_pass_at_k() -> None:
    assert pass_at_k(2, 0, 1) == 0.0
    assert pass_at_k(2, 1, 1) == pytest.approx(0.5)
    assert pass_at_k(2, 1, 2) == 1.0
    assert pass_at_k(5, 2, 3) == pytest.approx(1 - 1 / 10)
    assert pass_at_k(1, 0, 2) == 0.0
    assert pass_at_k(0, 0, 2) == 0.0
    assert mean_pass_at_k({"a": [True, False], "b": [False, False]}, 1) == pytest.approx(0.25)
    assert mean_pass_at_k({}, 2) == 0.0


def test_percentile_nearest_rank() -> None:
    values = [5.0, 1.0, 3.0, 4.0, 2.0]
    assert percentile(values, 0.5) == 3.0
    assert percentile(values, 0.9) == 5.0
    assert percentile(values, 0.0) == 1.0
    assert percentile([], 0.5) == 0.0
