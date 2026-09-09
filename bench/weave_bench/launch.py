"""Run (or dry-run) the Harbor jobs for one benchmark comparison."""

from __future__ import annotations

import os
import shlex
import subprocess
from collections.abc import Mapping
from dataclasses import dataclass
from pathlib import Path

from weave_bench.arms import ArmSpec, Upstream
from weave_bench.benchmarks import HARBOR_VERSION, PINS, Benchmark
from weave_bench.config import BenchConfig, require_secret
from weave_bench.harbor_command import INHERITED_ENV_TO_CLEAR, HarborJob, build_job
from weave_bench.manifests import harbor_task_names, smoke_task_names


@dataclass(frozen=True)
class LaunchPlan:
    benchmark: Benchmark
    run_id: str
    jobs: tuple[HarborJob, ...]
    task_names: tuple[str, ...]


def plan(
    benchmark: Benchmark,
    arms: tuple[ArmSpec, ...],
    *,
    run_id: str,
    config: BenchConfig,
    smoke: bool,
    n_attempts: int | None,
    n_concurrent: int | None,
    codex_version: str | None,
    task_names: list[str] | None,
) -> LaunchPlan:
    pins = PINS[benchmark]
    selected = select_task_names(benchmark, task_names, smoke=smoke)
    jobs = tuple(
        build_job(
            benchmark,
            arm,
            run_id=run_id,
            config=config,
            task_names=selected,
            n_attempts=n_attempts or pins.n_attempts,
            n_concurrent=n_concurrent or pins.n_concurrent,
            codex_version=codex_version,
        )
        for arm in arms
    )
    return LaunchPlan(benchmark, run_id, jobs, tuple(selected))


def select_task_names(benchmark: Benchmark, explicit: list[str] | None, *, smoke: bool) -> list[str]:
    """Explicit names (space- or comma-separated) must be in the pinned manifest so a typo can't run off-manifest."""
    manifest = harbor_task_names(benchmark)
    if not explicit:
        return smoke_task_names(benchmark) if smoke else manifest
    selected = [name for chunk in explicit for name in chunk.split(",") if name]
    unknown = sorted(set(selected) - set(manifest))
    if unknown:
        raise ValueError(f"{benchmark} manifest does not contain: {', '.join(unknown)}")
    return selected


def render_dry_run(launch: LaunchPlan) -> str:
    pins = PINS[launch.benchmark]
    lines = [
        f"# {launch.benchmark} run_id={launch.run_id}",
        f"# tasks={len(launch.task_names)} (manifest has {pins.n_tasks}); "
        f"harbor={HARBOR_VERSION} (published: {pins.published_harbor_version}) codex={pins.codex_version}",
    ]
    for job in launch.jobs:
        lines += ["", f"## arm {job.arm.name} ({job.arm.upstream})"]
        lines += [f'export {name}="${sourced}"' for name, sourced in job.env_from.items()]
        if job.codex_config_path is not None:
            lines += [f"# writes {job.codex_config_path}:"] + [
                f"#   {config_line}" for config_line in (job.codex_config or "").splitlines()
            ]
        if job.arm.upstream is Upstream.OPENROUTER:
            lines.append("# requires `weave-bench tap serve` running at openrouter_tap.public_url")
        lines.append(shlex.join(job.argv))
    return "\n".join(lines) + "\n"


def coordinator_env(job: HarborJob, env: Mapping[str, str] = os.environ) -> dict[str, str]:
    """Process environment for ``harbor run``: arm/judge keys under the names the
    argv templates reference, inherited OpenAI base-URL overrides removed."""
    launch_env = {name: value for name, value in env.items() if name not in INHERITED_ENV_TO_CLEAR}
    for name, sourced in job.env_from.items():
        launch_env[name] = require_secret(sourced, env)
    return launch_env


def run_job(job: HarborJob, config: BenchConfig) -> int:
    Path(config.harbor.jobs_dir).mkdir(parents=True, exist_ok=True)
    if job.codex_config_path is not None and job.codex_config is not None:
        job.codex_config_path.write_text(job.codex_config)
    return subprocess.run(list(job.argv), env=coordinator_env(job), check=False).returncode
