"""``weave-bench efficiency`` — the token-efficiency savings measurement.

  weave-bench efficiency tasks                                   # task lists + digests
  weave-bench efficiency pin-proxy [--host H --port P]           # for SWE-Together trials
  weave-bench efficiency swe-together run --run-id R [--dry-run] # work-ratio matrix
  weave-bench efficiency swe-together collect --run-id R         # join trials to the export
  weave-bench efficiency terminal-bench run --run-id R [--dry-run]
  weave-bench efficiency report --swe-together R ... --terminal-bench R --out results.json

Only ``run`` without ``--dry-run`` spends anything.
"""

from __future__ import annotations

import argparse
import os
import shlex
import subprocess
import sys
from collections.abc import Callable
from datetime import datetime, timedelta
from pathlib import Path

from weave_bench.analytics import DecisionRow, by_session, fetch_rows, parse_ndjson, write_ndjson
from weave_bench.config import BenchConfig, require_secret
from weave_bench.efficiency import swe_together, terminal_bench
from weave_bench.efficiency.cells import REQUESTED_MODEL, Cell, Harness, published_cells
from weave_bench.efficiency.quality import ArmTasks, quality_factor
from weave_bench.efficiency.results import (
    EvidenceStatus,
    QualityComparison,
    quality_row,
    quality_section,
    ratio_rows,
    read_results,
    render_markdown,
    results_document,
    work_ratio_section,
    write_results,
)
from weave_bench.efficiency.tasks import (
    select_swe_together_tasks,
    swe_together_manifest,
    terminal_bench_manifest,
)
from weave_bench.harbor_command import AGENT_API_KEY_ENV, INHERITED_ENV_TO_CLEAR

EFFICIENCY_DIRNAME = "token-efficiency"
# Run as a module so the proxy's server packages (the ``tap`` extra) are needed only here.
PIN_PROXY_MODULE = "weave_bench.efficiency.pin_proxy"
DEFAULT_PROXY_HOST = "172.17.0.1"
DEFAULT_PROXY_PORT = 8788
ANALYTICS_STAMP = "%Y%m%dT%H%M%S"

ConfigLoader = Callable[[argparse.Namespace], BenchConfig]


def _run_dir(config: BenchConfig, run_id: str) -> Path:
    return Path(config.harbor.jobs_dir) / EFFICIENCY_DIRNAME / run_id


def parse_cells(spec: str) -> tuple[Cell, ...]:
    """``harness:model`` pairs, comma-separated; empty selects the published cells."""
    if not spec:
        return published_cells()
    cells: list[Cell] = []
    for item in spec.split(","):
        harness, separator, model = item.strip().partition(":")
        if not separator or not model:
            raise ValueError(f"cell {item!r} is not harness:model")
        cells.append(Cell(Harness(harness), model))
    return tuple(cells)


def _analytics_fetcher(
    config: BenchConfig, replay: Path | None, save_dir: Path
) -> Callable[[datetime, datetime], list[DecisionRow]]:
    def fetch(window_start: datetime, window_end: datetime) -> list[DecisionRow]:
        if replay is not None:
            return parse_ndjson(replay.read_text().splitlines())
        rows = fetch_rows(
            base_url=config.router.analytics_url,
            api_key=require_secret(config.router.analytics_key_env),
            window_start=window_start - timedelta(minutes=5),
            window_end=window_end + timedelta(minutes=5),
        )
        stamp = f"{window_start.strftime(ANALYTICS_STAMP)}-{window_end.strftime(ANALYTICS_STAMP)}"
        write_ndjson(rows, save_dir / f"analytics-{stamp}.ndjson")
        return rows

    return fetch


def cmd_tasks(_args: argparse.Namespace, _load: ConfigLoader) -> int:
    for manifest in (swe_together_manifest(), terminal_bench_manifest()):
        print(f"{manifest['benchmark']}: {len(manifest['tasks'])} tasks, digest {manifest['digest']}")
    return 0


def cmd_pin_proxy(args: argparse.Namespace, load: ConfigLoader) -> int:
    config = load(args)
    print(f"pin proxy on {args.host}:{args.port} -> {config.router.base_url}", file=sys.stderr)
    command = [sys.executable, "-m", PIN_PROXY_MODULE, "--router", config.router.base_url]
    return subprocess.run([*command, "--host", args.host, "--port", str(args.port)], check=False).returncode


def cmd_swe_run(args: argparse.Namespace, load: ConfigLoader) -> int:
    config = load(args)
    tasks = select_swe_together_tasks(core_only=args.core_only, explicit=(args.tasks or "").split(","))
    trials = swe_together.plan_trials(args.run_id, parse_cells(args.cells), tasks, args.n_attempts)
    run_dir = _run_dir(config, args.run_id)
    ledger = swe_together.read_ledger(run_dir / swe_together.LEDGER_FILENAME)
    commands = [
        swe_together.trial_command(
            trial,
            rollout_id=swe_together.next_rollout_id(trial, ledger),
            checkout=args.checkout,
            python=args.python,
            proxy_url=args.proxy_url,
            trials_root=run_dir / "trials",
            env_type=args.env_type,
        )
        for trial in trials
    ]
    if args.dry_run:
        print(f"# {len(commands)} trials; {len(tasks)} tasks; run dir {run_dir}")
        print(
            f"# checkout {args.checkout}: {swe_together.UPSTREAM_REPO} at {swe_together.UPSTREAM_REVISION}"
            f" + {swe_together.VERSIONS_PATCH.name}"
        )
        print("# each trial also gets the router key as its harness's provider key (Anthropic or OpenAI)")
        for command in commands:
            env = " ".join(f"{name}={shlex.quote(value)}" for name, value in command.env.items())
            print(f"{env} {shlex.join(command.argv)}")
        return 0
    swe_together.run_matrix(
        commands, router_api_key=require_secret(config.router.api_key_env), run_dir=run_dir, workers=args.workers
    )
    return 0


def cmd_swe_collect(args: argparse.Namespace, load: ConfigLoader) -> int:
    config = load(args)
    run_dir = _run_dir(config, args.run_id)
    usages = swe_together.collect(run_dir, _analytics_fetcher(config, args.analytics_ndjson, run_dir))
    rejected = [usage for usage in usages if not usage.measures_pinned_model]
    print(f"{len(usages) - len(rejected)} of {len(usages)} trials measured their pinned model", file=sys.stderr)
    for usage in rejected:
        print(f"  {usage.trial_id}: {', '.join(usage.violations)}", file=sys.stderr)
    print(f"wrote {run_dir / swe_together.USAGE_FILENAME}", file=sys.stderr)
    return 0


def cmd_tb_run(args: argparse.Namespace, load: ConfigLoader) -> int:
    config = load(args)
    tasks = terminal_bench.select_tasks((args.tasks or "").split(","))
    jobs = [
        terminal_bench.build_quality_job(
            arm,
            run_id=args.run_id,
            config=config,
            task_names=tasks,
            n_attempts=args.n_attempts,
            n_concurrent=args.n_concurrent,
        )
        for arm in terminal_bench.parse_quality_arms(args.arms)
    ]
    if args.dry_run:
        print("# harbor reads the router key from the variable [router] api_key_env names in bench.toml")
        for job in jobs:
            print(f"\n## arm {job.arm.name} (pin: {job.arm.force_model or 'none, router policy'})")
            print(f"# writes {job.config_path}:")
            print("\n".join(f"#   {line}" for line in job.config_text.splitlines()))
            print(shlex.join(job.argv))
        return 0
    env = {name: value for name, value in os.environ.items() if name not in INHERITED_ENV_TO_CLEAR}
    env[AGENT_API_KEY_ENV] = require_secret(config.router.api_key_env)
    Path(config.harbor.jobs_dir).mkdir(parents=True, exist_ok=True)
    failed = 0
    for job in jobs:
        job.config_path.write_text(job.config_text)
        failed |= subprocess.run(list(job.argv), env=env, check=False).returncode
    return failed


def _quality_comparisons(args: argparse.Namespace, config: BenchConfig) -> list[QualityComparison]:
    jobs_dir = Path(config.harbor.jobs_dir)
    arms = terminal_bench.parse_quality_arms(args.arms)
    trials = {
        arm.name: terminal_bench.load_harbor_trials(jobs_dir / terminal_bench.job_name(args.terminal_bench, arm))
        for arm in arms
    }
    every_trial = [trial for arm_trials in trials.values() for trial in arm_trials]
    if not every_trial:
        raise ValueError(f"no finished Terminal-Bench trials for run {args.terminal_bench} in {jobs_dir}")
    report_dir = jobs_dir / EFFICIENCY_DIRNAME / args.terminal_bench
    fetch = _analytics_fetcher(config, args.analytics_ndjson, report_dir)
    rows_by_session = by_session(
        fetch(min(trial.started_at for trial in every_trial), max(trial.finished_at for trial in every_trial))
    )
    outcomes = {arm.name: terminal_bench.arm_outcomes(trials[arm.name], rows_by_session) for arm in arms}
    for name, arm_outcomes in outcomes.items():
        if arm_outcomes.missing_telemetry:
            print(
                f"warning: {name}: {len(arm_outcomes.missing_telemetry)} trials have no router rows and are left out:"
                f" {', '.join(arm_outcomes.missing_telemetry)}",
                file=sys.stderr,
            )
    comparisons: list[QualityComparison] = []
    for router_arm in (arm for arm in arms if arm.is_router):
        router_outcomes = outcomes[router_arm.name]
        for baseline_arm in (arm for arm in arms if not arm.is_router and arm.harness is router_arm.harness):
            baseline_outcomes = outcomes[baseline_arm.name]
            factor = quality_factor(ArmTasks(router_outcomes.by_task), ArmTasks(baseline_outcomes.by_task))
            if factor is None:
                print(f"no task graded by both {router_arm.name} and {baseline_arm.name}", file=sys.stderr)
                continue
            comparisons.append(
                QualityComparison(
                    router_arm.harness,
                    router_arm.name,
                    baseline_arm.name,
                    baseline_arm.baseline_model,
                    factor,
                    terminal_bench.served_mix(trials[router_arm.name], rows_by_session),
                    len(router_outcomes.missing_telemetry),
                    len(baseline_outcomes.missing_telemetry),
                )
            )
    return comparisons


def cmd_report(args: argparse.Namespace, load: ConfigLoader) -> int:
    config = load(args)
    status = EvidenceStatus(args.status)
    ratio_section = None
    if args.swe_together:
        runs = [
            swe_together.measurement_run(
                run_id, swe_together.read_usage(_run_dir(config, run_id) / swe_together.USAGE_FILENAME)
            )
            for run_id in args.swe_together
        ]
        rows, period_notes = ratio_rows(runs, None if args.all_pairs else REQUESTED_MODEL)
        ratio_section = work_ratio_section(rows, status, note="pooled runs per cell: " + "; ".join(period_notes))
    quality = None
    if args.terminal_bench:
        rows = [quality_row(comparison) for comparison in _quality_comparisons(args, config)]
        quality = quality_section(rows, status, note=f"run: {args.terminal_bench}")
    document = results_document(work_ratios=ratio_section, quality_factors=quality, measured=args.measured)
    write_results(document, args.out)
    sys.stdout.write(render_markdown(document))
    print(f"\nwrote {args.out}", file=sys.stderr)
    return 0


def cmd_render(args: argparse.Namespace, _load: ConfigLoader) -> int:
    sys.stdout.write(render_markdown(read_results(args.results)))
    return 0


def register(
    subparsers: argparse._SubParsersAction,
    add_common: Callable[[argparse.ArgumentParser], None],
    run_id_arg: Callable[[str], str],
    load: ConfigLoader,
) -> None:
    efficiency = subparsers.add_parser("efficiency", help="token-efficiency savings measurement (work ratios, quality)")
    commands = efficiency.add_subparsers(dest="efficiency_command", required=True)

    def bind(parser: argparse.ArgumentParser, handler: Callable[[argparse.Namespace, ConfigLoader], int]) -> None:
        add_common(parser)
        parser.set_defaults(func=lambda args: handler(args, load))

    bind(commands.add_parser("tasks", help="print the task lists' sizes and digests"), cmd_tasks)

    proxy = commands.add_parser("pin-proxy", help="serve the pin proxy SWE-Together trials point at")
    proxy.add_argument("--host", default=DEFAULT_PROXY_HOST)
    proxy.add_argument("--port", type=int, default=DEFAULT_PROXY_PORT)
    bind(proxy, cmd_pin_proxy)

    swe_together_parser = commands.add_parser("swe-together", help="work-ratio matrix on SWE-Together")
    swe_together_commands = swe_together_parser.add_subparsers(dest="swe_command", required=True)
    swe_together_run = swe_together_commands.add_parser("run")
    swe_together_run.add_argument("--run-id", type=run_id_arg, required=True)
    swe_together_run.add_argument("--cells", default="", help="harness:model,... (default: the published cells)")
    swe_together_run.add_argument("--tasks", default="", help="comma-separated task ids (default: all 25)")
    swe_together_run.add_argument("--core-only", action="store_true", help="only the 9 core tasks every cell ran")
    swe_together_run.add_argument("--n-attempts", type=int, default=swe_together.N_ATTEMPTS)
    swe_together_run.add_argument("--workers", type=int, default=4)
    swe_together_run.add_argument("--checkout", type=Path, default=Path("vendor/SWE-Together"))
    swe_together_run.add_argument("--python", default=sys.executable, help="the SWE-Together environment's interpreter")
    swe_together_run.add_argument("--proxy-url", default=f"http://{DEFAULT_PROXY_HOST}:{DEFAULT_PROXY_PORT}")
    swe_together_run.add_argument("--env-type", default="docker")
    swe_together_run.add_argument("--dry-run", action="store_true")
    bind(swe_together_run, cmd_swe_run)
    swe_together_collect = swe_together_commands.add_parser("collect")
    swe_together_collect.add_argument("--run-id", type=run_id_arg, required=True)
    swe_together_collect.add_argument("--analytics-ndjson", type=Path, default=None, help="replay a saved export")
    bind(swe_together_collect, cmd_swe_collect)

    terminal_bench_parser = commands.add_parser("terminal-bench", help="quality arms on Terminal-Bench 4")
    terminal_bench_commands = terminal_bench_parser.add_subparsers(dest="tb_command", required=True)
    terminal_bench_run = terminal_bench_commands.add_parser("run")
    terminal_bench_run.add_argument("--run-id", type=run_id_arg, required=True)
    terminal_bench_run.add_argument("--arms", default="", help=f"default: {','.join(terminal_bench.DEFAULT_ARMS)}")
    terminal_bench_run.add_argument("--tasks", default="", help="comma-separated task names (default: all 33)")
    terminal_bench_run.add_argument("--n-attempts", type=int, default=terminal_bench.N_ATTEMPTS)
    terminal_bench_run.add_argument("--n-concurrent", type=int, default=terminal_bench.N_CONCURRENT)
    terminal_bench_run.add_argument("--dry-run", action="store_true")
    bind(terminal_bench_run, cmd_tb_run)

    report = commands.add_parser("report", help="compute ratios and quality factors into a results file")
    report.add_argument(
        "--swe-together", nargs="*", default=[], type=run_id_arg, help="run ids, pooled per cell by the reset rules"
    )
    report.add_argument(
        "--all-pairs", action="store_true", help="every model as comparison, not just the requested one"
    )
    report.add_argument("--terminal-bench", type=run_id_arg, default=None)
    report.add_argument("--arms", default="")
    report.add_argument("--analytics-ndjson", type=Path, default=None, help="replay a saved export")
    report.add_argument("--status", default=EvidenceStatus.PRELIMINARY.value, choices=[s.value for s in EvidenceStatus])
    report.add_argument("--measured", required=True, help="measurement window, e.g. 2026-10-08/2026-10-22")
    report.add_argument("--out", type=Path, required=True)
    bind(report, cmd_report)

    render = commands.add_parser("render", help="print a results file as Markdown tables")
    render.add_argument("results", type=Path)
    bind(render, cmd_render)
