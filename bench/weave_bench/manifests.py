"""Committed task lists, resolved to the names Harbor's ``--include-task-name`` matches."""

from __future__ import annotations

import json
from importlib import resources
from pathlib import Path

from weave_bench.benchmarks import PINS, Benchmark

TB4_TASK_ORG = "terminal-bench"


def manifest_path(benchmark: Benchmark) -> Path:
    return Path(str(resources.files("weave_bench") / "manifests" / PINS[benchmark].manifest_filename))


def load_manifest(benchmark: Benchmark) -> dict[str, object]:
    return json.loads(manifest_path(benchmark).read_text())


def harbor_task_names(benchmark: Benchmark) -> list[str]:
    """Every task of the published subset, as Harbor names them.

    Registry package tasks (Terminal-Bench) are ``org/name``; local (Atlas)
    tasks are the bare directory name.
    """
    manifest = load_manifest(benchmark)
    if benchmark is Benchmark.TERMINAL_BENCH_4:
        return [f"{TB4_TASK_ORG}/{task['name']}" for task in manifest["tasks"]]
    return list(manifest["task_ids"])


def smoke_task_names(benchmark: Benchmark) -> list[str]:
    if benchmark is Benchmark.TERMINAL_BENCH_4:
        return [f"{TB4_TASK_ORG}/{name}" for name in PINS[benchmark].smoke_tasks]
    return list(PINS[benchmark].smoke_tasks)
