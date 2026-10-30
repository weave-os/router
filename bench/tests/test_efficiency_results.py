from __future__ import annotations

import copy
import json
from pathlib import Path

import pytest

from weave_bench.benchmarks import Benchmark
from weave_bench.efficiency.cells import REQUESTED_MODEL, Cell, Harness
from weave_bench.efficiency.estimates import EfficiencyEstimate, TokenUsage, WorkRatios
from weave_bench.efficiency.results import (
    EvidenceStatus,
    quality_section,
    ratio_rows,
    read_results,
    render_markdown,
    results_document,
    validate_results,
    work_ratios_from_row,
)
from weave_bench.efficiency.savings import BoundSet, RequestCost, adjusted_savings, naive_savings
from weave_bench.efficiency.tasks import (
    select_swe_together_tasks,
    swe_together_manifest,
    swe_together_tasks,
    task_set_digest,
    terminal_bench_manifest,
    terminal_bench_task_names,
)
from weave_bench.manifests import harbor_task_names

RESULTS = Path(__file__).resolve().parents[1] / "token-efficiency" / "results" / "token_efficiency_results.json"


def test_manifest_digests_match_their_task_lists() -> None:
    assert swe_together_manifest()["digest"] == task_set_digest(task.id for task in swe_together_tasks())
    assert terminal_bench_manifest()["digest"] == task_set_digest(terminal_bench_task_names())
    assert len(swe_together_tasks()) == 25 and len(terminal_bench_task_names()) == 33


def test_core_set_is_three_tasks_per_tier() -> None:
    core = select_swe_together_tasks(core_only=True)
    assert len(core) == 9
    with pytest.raises(ValueError):
        select_swe_together_tasks(core_only=False, explicit=["not-a-task"])


def test_terminal_bench_tasks_are_in_the_pinned_tb4_dataset() -> None:
    dataset = {name.removeprefix("terminal-bench/") for name in harbor_task_names(Benchmark.TERMINAL_BENCH_4)}
    assert set(terminal_bench_task_names()) <= dataset


def test_committed_results_validate_and_render() -> None:
    document = read_results(RESULTS)
    assert document["quality_factors"]["status"] == EvidenceStatus.PENDING
    assert "| Codex | gpt-6-astra | claude-opus-5-5 | 25 |" in render_markdown(document)


def test_committed_factors_follow_from_the_published_intervals() -> None:
    for row in read_results(RESULTS)["work_ratios"]["rows"]:
        work = work_ratios_from_row(row)
        assert work.native == Cell(Harness(row["harness"]), row["served_model"]).native
        assert (row["input_credited"], row["output_credited"]) == (work.input_credited, work.output_credited)
        assert (row["input_capped"], row["output_capped"]) == (work.input_capped, work.output_capped)
        assert row["comparison_model"] == REQUESTED_MODEL[Harness(row["harness"])]


def test_validation_rejects_a_changed_task_set_and_pending_rows() -> None:
    document = json.loads(RESULTS.read_text())
    tampered = copy.deepcopy(document)
    tampered["work_ratios"]["task_set_digest"] = "0" * 64
    with pytest.raises(ValueError, match="task_set_digest"):
        validate_results(tampered)
    pending_with_rows = copy.deepcopy(document)
    pending_with_rows["quality_factors"]["rows"] = [{}]
    with pytest.raises(ValueError, match="pending"):
        validate_results(pending_with_rows)


def test_ratio_rows_pair_each_served_cell_with_its_harness_comparison() -> None:
    def tasks(scale: float) -> dict[str, list[TokenUsage]]:
        return {
            f"t{i}": [TokenUsage(scale * (i + 1), 0, 0, scale), TokenUsage(scale * (i + 1), 0, 0, scale)]
            for i in range(4)
        }

    trials = {
        Cell(Harness.CODEX, "gpt-6-astra"): tasks(1),
        Cell(Harness.CODEX, "gpt-6-luna"): tasks(2),
        Cell(Harness.CLAUDE_CODE, "gpt-6-luna"): tasks(3),
    }
    rows = ratio_rows(trials, REQUESTED_MODEL)
    assert [(row["comparison_model"], row["served_model"]) for row in rows] == [("gpt-6-astra", "gpt-6-luna")]
    assert rows[0]["input"]["point"] == pytest.approx(0.5)
    assert rows[0]["tasks"] == 4
    assert len(ratio_rows(trials)) == 2
    validate_results(
        results_document(
            work_ratios={
                "benchmark": "swe-together",
                "status": "preliminary",
                "task_set_digest": swe_together_manifest()["digest"],
                "interval": "task bootstrap, 90%",
                "note": "",
                "rows": rows,
            },
            quality_factors=quality_section([], EvidenceStatus.PENDING),
            measured="test",
        )
    )


WORK = WorkRatios(EfficiencyEstimate(0.8, 0.6, 1.0, 25), EfficiencyEstimate(1.5, 1.2, 1.9, 25), native=True)
COST = RequestCost(comparison_input_usd=10.0, comparison_output_usd=4.0, actual_usd=3.0)


def test_adjusted_savings_scales_each_side_then_divides_by_the_penalty() -> None:
    assert naive_savings(COST) == 11.0
    assert adjusted_savings(COST, WORK, BoundSet.CREDITED) == pytest.approx(10 * 0.8 + 4 * 1.2 - 3)
    assert adjusted_savings(COST, WORK, BoundSet.CAPPED) == pytest.approx(10 * 0.8 + 4 * 1.0 - 3)
    assert adjusted_savings(COST, WORK, BoundSet.CAPPED, penalty=1.2) == pytest.approx((8 + 4) / 1.2 - 3)


def test_unmeasured_requests_get_no_savings_and_penalty_below_one_is_refused() -> None:
    assert adjusted_savings(COST, None, BoundSet.CREDITED) == 0.0
    with pytest.raises(ValueError):
        adjusted_savings(COST, WORK, BoundSet.CREDITED, penalty=0.9)
