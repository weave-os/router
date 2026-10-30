"""The evidence tables: build them from trials, write and validate them, render Markdown.

The schema (``SCHEMA_VERSION``) is documented in ``bench/token-efficiency/README.md``.
"""

from __future__ import annotations

import json
import math
from collections.abc import Mapping, Sequence
from dataclasses import dataclass
from enum import StrEnum
from itertools import permutations
from pathlib import Path

from weave_bench.efficiency.cells import Cell, Harness
from weave_bench.efficiency.estimates import EfficiencyEstimate, WorkRatios
from weave_bench.efficiency.pooling import MeasurementRun, ratio_period
from weave_bench.efficiency.quality import Interval, QualityFactor, capped_pass_rate_penalty
from weave_bench.efficiency.tasks import swe_together_manifest, terminal_bench_manifest

SCHEMA_VERSION = 1


class EvidenceStatus(StrEnum):
    FINAL = "final"
    PRELIMINARY = "preliminary"
    PENDING = "pending"


def _interval(point: float, lower: float, upper: float) -> dict[str, float]:
    return {"point": point, "lower": lower, "upper": upper}


def ratio_row(comparison: Cell, served: Cell, work: WorkRatios) -> dict[str, object]:
    return {
        "harness": served.harness.value,
        "comparison_model": comparison.model,
        "served_model": served.model,
        "native": work.native,
        "tasks": work.input.tasks,
        "input": _interval(work.input.ratio, work.input.lower, work.input.upper),
        "output": _interval(work.output.ratio, work.output.lower, work.output.upper),
        "input_rule": work.input.bound_rule(native=work.native).value,
        "output_rule": work.output.bound_rule(native=work.native).value,
        "input_credited": work.input_credited,
        "output_credited": work.output_credited,
        "input_capped": work.input_capped,
        "output_capped": work.output_capped,
    }


def work_ratios_from_row(row: Mapping[str, object]) -> WorkRatios:
    def estimate(side: str) -> EfficiencyEstimate:
        values = row[side]
        return EfficiencyEstimate(values["point"], values["lower"], values["upper"], int(row["tasks"]))

    return WorkRatios(estimate("input"), estimate("output"), bool(row["native"]))


def ratio_rows(
    runs: Sequence[MeasurementRun], comparison_models: Mapping[Harness, str] | None = None
) -> tuple[list[dict[str, object]], list[str]]:
    """Every ordered (comparison, served) pair within a harness, or only pairs
    against ``comparison_models[harness]`` when given, each estimated from its
    current pooling period (``pooling.ratio_period``), with one note per row
    naming the runs pooled and why the period started."""
    cells = sorted({cell for run in runs for cell in run.trials})
    rows: list[dict[str, object]] = []
    notes: list[str] = []
    for comparison, served in permutations(cells, 2):
        if comparison.harness is not served.harness:
            continue
        if comparison_models is not None and comparison_models.get(comparison.harness) != comparison.model:
            continue
        period = ratio_period(runs, comparison, served)
        if period is None:
            continue
        rows.append(ratio_row(comparison, served, period.estimate))
        pooled = ", ".join(run.run_id for run in period.runs)
        notes.append(f"{served.harness}/{served.model}: {pooled} ({period.reset_reason})")
    return rows, notes


@dataclass(frozen=True)
class QualityComparison:
    harness: Harness
    router_arm: str
    baseline_arm: str
    baseline_model: str
    factor: QualityFactor
    served_mix: Mapping[str, int]
    # Finished trials with no router rows to price, left out of every count above.
    router_missing_telemetry: int
    baseline_missing_telemetry: int


def _interval_dict(interval: Interval) -> dict[str, float]:
    return _interval(interval.point, interval.lower, interval.upper)


def quality_row(comparison: QualityComparison) -> dict[str, object]:
    factor = comparison.factor
    return {
        "harness": comparison.harness.value,
        "baseline_model": comparison.baseline_model,
        "router_arm": comparison.router_arm,
        "baseline_arm": comparison.baseline_arm,
        "tasks": factor.tasks,
        "kept_rate": None if factor.kept_rate is None else _interval_dict(factor.kept_rate),
        "router_pass_rate": _interval_dict(factor.router_pass_rate),
        "baseline_pass_rate": _interval_dict(factor.baseline_pass_rate),
        "pass_rate_penalty": capped_pass_rate_penalty(factor.baseline_pass_rate, factor.router_pass_rate),
        "router": {
            "solved": factor.router_solved,
            "graded": factor.router_graded,
            "attempts": factor.router_attempts,
            "catalog_cost_usd": factor.router_cost_usd,
            "missing_telemetry": comparison.router_missing_telemetry,
        },
        "baseline": {
            "solved": factor.baseline_solved,
            "graded": factor.baseline_graded,
            "attempts": factor.baseline_attempts,
            "catalog_cost_usd": factor.baseline_cost_usd,
            "missing_telemetry": comparison.baseline_missing_telemetry,
        },
        "router_served_mix": dict(comparison.served_mix),
    }


def work_ratio_section(rows: list[dict[str, object]], status: EvidenceStatus, note: str = "") -> dict[str, object]:
    return {
        "benchmark": "swe-together",
        "status": status.value,
        "task_set_digest": swe_together_manifest()["digest"],
        "interval": "task bootstrap, 90%",
        "note": note,
        "rows": rows,
    }


def quality_section(rows: list[dict[str, object]], status: EvidenceStatus, note: str = "") -> dict[str, object]:
    return {
        "benchmark": "terminal-bench-4",
        "status": status.value,
        "task_set_digest": terminal_bench_manifest()["digest"],
        "interval": "task bootstrap, 90%",
        "note": note,
        "rows": rows,
    }


def results_document(
    *, work_ratios: Mapping[str, object] | None, quality_factors: Mapping[str, object] | None, measured: str
) -> dict[str, object]:
    document: dict[str, object] = {"schema_version": SCHEMA_VERSION, "measured": measured}
    if work_ratios is not None:
        document["work_ratios"] = dict(work_ratios)
    if quality_factors is not None:
        document["quality_factors"] = dict(quality_factors)
    return document


def write_results(document: Mapping[str, object], path: Path) -> None:
    validate_results(document)
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(document, indent=1, allow_nan=False) + "\n")


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise ValueError(f"results schema: {message}")


def _require_interval(value: object, where: str) -> None:
    _require(isinstance(value, dict) and set(value) == {"point", "lower", "upper"}, f"{where} is not an interval")
    numbers = [value["point"], value["lower"], value["upper"]]
    _require(
        all(isinstance(number, (int, float)) and math.isfinite(number) for number in numbers), f"{where} not finite"
    )
    # A percentile interval need not contain the full-sample point, so only its ends are ordered.
    _require(value["lower"] <= value["upper"], f"{where} has lower above upper")


RATIO_KEYS = frozenset(
    {
        "harness", "comparison_model", "served_model", "native", "tasks", "input", "output", "input_rule",
        "output_rule", "input_credited", "output_credited", "input_capped", "output_capped",
    }
)  # fmt: skip
QUALITY_KEYS = frozenset(
    {
        "harness", "baseline_model", "router_arm", "baseline_arm", "tasks", "kept_rate", "router_pass_rate",
        "baseline_pass_rate", "pass_rate_penalty", "router", "baseline", "router_served_mix",
    }
)  # fmt: skip
SECTION_KEYS = frozenset({"benchmark", "status", "task_set_digest", "interval", "note", "rows"})


def validate_results(document: Mapping[str, object]) -> None:
    _require(document.get("schema_version") == SCHEMA_VERSION, f"schema_version must be {SCHEMA_VERSION}")
    sections = {
        "work_ratios": (RATIO_KEYS, swe_together_manifest()),
        "quality_factors": (QUALITY_KEYS, terminal_bench_manifest()),
    }
    for name, (row_keys, manifest) in sections.items():
        section = document.get(name)
        if section is None:
            continue
        _require(isinstance(section, dict) and set(section) == SECTION_KEYS, f"{name} has keys {sorted(section)}")
        EvidenceStatus(section["status"])
        _require(
            section["task_set_digest"] == manifest["digest"], f"{name} task_set_digest does not match the manifest"
        )
        if section["status"] == EvidenceStatus.PENDING:
            _require(not section["rows"], f"{name} is pending but has rows")
        for index, row in enumerate(section["rows"]):
            where = f"{name}.rows[{index}]"
            _require(set(row) == row_keys, f"{where} keys {sorted(set(row) ^ row_keys)} differ from the schema")
            Harness(row["harness"])
            for interval_key in ("input", "output", "kept_rate", "router_pass_rate", "baseline_pass_rate"):
                # An undefined kept rate is null; every other interval is required.
                if interval_key in row and not (interval_key == "kept_rate" and row[interval_key] is None):
                    _require_interval(row[interval_key], f"{where}.{interval_key}")
            _require(isinstance(row["tasks"], int) and row["tasks"] > 0, f"{where}.tasks must be a positive int")


def read_results(path: Path) -> dict[str, object]:
    document = json.loads(path.read_text())
    validate_results(document)
    return document


def _fmt(interval: Mapping[str, float] | None) -> str:
    if interval is None:
        return "—"
    return f"{interval['point']:.2f} [{interval['lower']:.2f}–{interval['upper']:.2f}]"


HARNESS_LABEL = {Harness.CLAUDE_CODE.value: "Claude Code", Harness.CODEX.value: "Codex"}


def render_markdown(document: Mapping[str, object]) -> str:
    lines: list[str] = []
    ratios = document.get("work_ratios")
    if ratios:
        lines += [
            f"## Work ratios (SWE-Together, {ratios['status']})",
            "",
            "Comparison model's token volume over the served model's, same harness and tasks; 90% task-bootstrap"
            " interval. Credited and capped are the factors applied as input / output.",
            "",
            "| Harness | Comparison | Served | Tasks | Input | Output | Credited | Capped |",
            "|---|---|---|---|---|---|---|---|",
        ]
        for row in ratios["rows"]:
            lines.append(
                f"| {HARNESS_LABEL[row['harness']]} | {row['comparison_model']} | {row['served_model']} | {row['tasks']} "
                f"| {_fmt(row['input'])} | {_fmt(row['output'])} "
                f"| {row['input_credited']:.2f} / {row['output_credited']:.2f} "
                f"| {row['input_capped']:.2f} / {row['output_capped']:.2f} |"
            )
        lines.append("")
    quality = document.get("quality_factors")
    if quality:
        lines += [f"## Quality factors (Terminal-Bench 4, {quality['status']})", ""]
        if not quality["rows"]:
            lines += [quality["note"] or "No rows yet.", ""]
        else:
            lines += [
                "| Harness | Router vs | Tasks | Router solved / graded | Baseline solved / graded "
                "| Router pass rate | Baseline pass rate | Kept rate | Penalty |",
                "|---|---|---|---|---|---|---|---|---|",
            ]
            for row in quality["rows"]:
                penalty = row["pass_rate_penalty"]
                lines.append(
                    f"| {HARNESS_LABEL[row['harness']]} | always-{row['baseline_model']} | {row['tasks']} "
                    f"| {row['router']['solved']} / {row['router']['graded']} "
                    f"| {row['baseline']['solved']} / {row['baseline']['graded']} "
                    f"| {_fmt(row['router_pass_rate'])} | {_fmt(row['baseline_pass_rate'])} | {_fmt(row['kept_rate'])} "
                    f"| {'—' if penalty is None else f'{penalty:.2f}'} |"
                )
            lines.append("")
    return "\n".join(lines)
