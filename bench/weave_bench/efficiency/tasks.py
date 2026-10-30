"""The committed task lists and their digests.

A digest is the sha256 of the sorted task ids joined by newlines, so any change
to a task set changes it. Results carry the digest of the set they measured;
compare it before pooling or comparing numbers.
"""

from __future__ import annotations

import hashlib
import json
from collections.abc import Iterable
from dataclasses import dataclass
from enum import StrEnum
from functools import cache
from importlib import resources

SWE_TOGETHER_MANIFEST = "token_efficiency_swe_together_tasks.json"
TERMINAL_BENCH_MANIFEST = "token_efficiency_terminalbench4_tasks.json"
TB4_TASK_ORG = "terminal-bench"


class Tier(StrEnum):
    LOW = "low"
    MEDIUM = "medium"
    HIGH = "high"


@dataclass(frozen=True)
class SweTogetherTask:
    id: str
    tier: Tier
    # Every cell ran the core tasks; the rest ran only in the cells that carry
    # production traffic, so a credit above 1 needs the core set (CREDIT_MIN_TASKS).
    core: bool


def task_set_digest(task_ids: Iterable[str]) -> str:
    return hashlib.sha256("\n".join(sorted(task_ids)).encode()).hexdigest()


def _manifest(filename: str) -> dict[str, object]:
    return json.loads((resources.files("weave_bench") / "manifests" / filename).read_text())


@cache
def swe_together_manifest() -> dict[str, object]:
    return _manifest(SWE_TOGETHER_MANIFEST)


@cache
def terminal_bench_manifest() -> dict[str, object]:
    return _manifest(TERMINAL_BENCH_MANIFEST)


def swe_together_tasks() -> tuple[SweTogetherTask, ...]:
    return tuple(
        SweTogetherTask(str(task["id"]), Tier(task["tier"]), bool(task["core"]))
        for task in swe_together_manifest()["tasks"]
    )


def terminal_bench_task_names() -> tuple[str, ...]:
    """Bare task names; Harbor's ``--include-task-name`` takes them prefixed with ``terminal-bench/``."""
    return tuple(str(task["name"]) for task in terminal_bench_manifest()["tasks"])


def select_swe_together_tasks(*, core_only: bool, explicit: Iterable[str] = ()) -> tuple[str, ...]:
    known = {task.id for task in swe_together_tasks()}
    chosen = [task for task in explicit if task]
    unknown = sorted(set(chosen) - known)
    if unknown:
        raise ValueError(f"not in the SWE-Together efficiency manifest: {', '.join(unknown)}")
    if chosen:
        return tuple(chosen)
    return tuple(task.id for task in swe_together_tasks() if task.core or not core_only)
