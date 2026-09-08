"""Committed task lists, resolved to the names Harbor's ``--include-task-name`` matches."""

from __future__ import annotations

import json
from functools import cache
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

    Registry package tasks (Terminal-Bench) are ``org/name``; local (Atlas) and
    git-backed (SWE-Bench Pro) tasks are the bare directory name.
    """
    manifest = load_manifest(benchmark)
    if benchmark is Benchmark.TERMINAL_BENCH_4:
        return [f"{TB4_TASK_ORG}/{task['name']}" for task in manifest["tasks"]]
    if benchmark is Benchmark.SWE_BENCH_PRO:
        return [manifest["harbor_task_names"][task_id] for task_id in manifest["task_ids"]]
    return list(manifest["task_ids"])


@cache
def _pro_instance_ids_by_harbor_name() -> dict[str, str]:
    manifest = load_manifest(Benchmark.SWE_BENCH_PRO)
    return {name: task_id for task_id, name in manifest["harbor_task_names"].items()}


def pro_instance_id(harbor_task_name: str) -> str:
    return _pro_instance_ids_by_harbor_name()[harbor_task_name]


def smoke_task_names(benchmark: Benchmark) -> list[str]:
    if benchmark is Benchmark.SWE_BENCH_PRO:
        manifest = load_manifest(benchmark)
        return [manifest["harbor_task_names"][task_id] for task_id in manifest["smoke_task_ids"][:3]]
    if benchmark is Benchmark.TERMINAL_BENCH_4:
        return [f"{TB4_TASK_ORG}/{name}" for name in PINS[benchmark].smoke_tasks]
    return list(PINS[benchmark].smoke_tasks)
