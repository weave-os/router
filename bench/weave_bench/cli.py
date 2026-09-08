"""``weave-bench`` — plan, run, and report Codex-harness benchmark comparisons.

Subcommands::

  weave-bench probe                          # router version / roster / /beta ack
  weave-bench fetch atlas
  weave-bench tap serve                      # OpenRouter recording tap
  weave-bench run <benchmark> [--arms a,b] [--smoke|--tasks ...] [--dry-run]
  weave-bench report <benchmark> <run-id> [--no-analytics] [--tap-records PATH]

No subcommand spends money unless it is ``run`` without ``--dry-run``.
"""

from __future__ import annotations

import argparse
import json
import os
import re
import subprocess
import sys
from datetime import datetime, timedelta
from pathlib import Path

from weave_bench.analytics import AnalyticsUnavailable, DecisionRow, fetch_rows, write_ndjson
from weave_bench.arms import ArmSpec, parse_arms
from weave_bench.benchmarks import PINS, Benchmark
from weave_bench.config import BenchConfig, load_config, require_secret
from weave_bench.fetch import fetch_atlas
from weave_bench.harbor_command import job_name
from weave_bench.launch import LaunchPlan, plan, render_dry_run, run_job
from weave_bench.markdown import render_markdown
from weave_bench.probe import probe_router
from weave_bench.report import build_report, load_tap_rows
from weave_bench.trials import TrialRecord, load_job

DEFAULT_RUN_ID_FORMAT = "%Y%m%d-%H%M%S"
# A run id becomes a Harbor job name, a jobs-dir path segment, and the
# ``x-weave-rollout-id`` header value in the generated Codex config.
RUN_ID_PATTERN = re.compile(r"[A-Za-z0-9][A-Za-z0-9._-]{0,127}")
ANALYTICS_WINDOW_PAD = timedelta(minutes=5)
ANALYTICS_CACHE_STAMP = "%Y%m%dT%H%M%S"
REPORT_DIRNAME = "reports"
TAP_MODULE = "weave_bench.tap.app"


def _default_run_id(benchmark: Benchmark) -> str:
    return f"{benchmark}-{datetime.now().strftime(DEFAULT_RUN_ID_FORMAT)}"


def run_id_arg(value: str) -> str:
    if RUN_ID_PATTERN.fullmatch(value) is None:
        raise argparse.ArgumentTypeError(
            f"run id {value!r} must be 1-128 chars of letters, digits, '.', '_' or '-' and start alphanumeric"
        )
    return value


def _add_common(parser: argparse.ArgumentParser) -> None:
    parser.add_argument("--config", type=Path, default=None, help="bench.toml (default: ./bench.toml if present)")


def _load(args: argparse.Namespace) -> BenchConfig:
    path = args.config
    if path is None and Path("bench.toml").exists():
        path = Path("bench.toml")
    return load_config(path)


def _jobs_dir(config: BenchConfig) -> Path:
    return Path(config.harbor.jobs_dir)


def cmd_probe(args: argparse.Namespace) -> int:
    config = _load(args)
    probe = probe_router(config.router.base_url, require_secret(config.router.api_key_env))
    sys.stdout.write(probe.render())
    return 0 if probe.beta_enabled else 1


def cmd_fetch(args: argparse.Namespace) -> int:
    config = _load(args)
    print(fetch_atlas(Path(config.harbor.atlas_checkout_dir)))
    return 0


def cmd_tap_serve(args: argparse.Namespace) -> int:
    """Runs the tap as a child interpreter so the ``tap`` extra is only needed here."""
    tap = _load(args).openrouter_tap
    command = [
        sys.executable,
        "-m",
        TAP_MODULE,
        f"--records={tap.records_path}",
        f"--host={tap.listen_host}",
        f"--port={tap.listen_port}",
    ]
    return subprocess.call(command)


def _plan_from_args(args: argparse.Namespace, config: BenchConfig) -> LaunchPlan:
    benchmark = Benchmark(args.benchmark)
    return plan(
        benchmark,
        parse_arms(args.arms),
        run_id=args.run_id or _default_run_id(benchmark),
        config=config,
        smoke=args.smoke,
        n_attempts=args.n_attempts,
        n_concurrent=args.n_concurrent,
        codex_version=args.codex_version,
        task_names=args.tasks or None,
    )


def cmd_run(args: argparse.Namespace) -> int:
    config = _load(args)
    launch = _plan_from_args(args, config)
    sys.stdout.write(render_dry_run(launch))
    if args.dry_run:
        return 0
    exit_code = 0
    for job in launch.jobs:
        exit_code |= run_job(job, config)
    arm_names = ",".join(job.arm.name for job in launch.jobs)
    print(f"\nreport with: weave-bench report {launch.benchmark} {launch.run_id} --arms {arm_names}")
    return exit_code


def _load_trials(config: BenchConfig, run_id: str, arms: tuple[ArmSpec, ...]) -> dict[ArmSpec, list[TrialRecord]]:
    jobs_dir = _jobs_dir(config)
    trials_by_arm: dict[ArmSpec, list[TrialRecord]] = {}
    for arm in arms:
        job_dir = jobs_dir / job_name(run_id, arm)
        if not job_dir.is_dir():
            raise FileNotFoundError(f"no Harbor job dir {job_dir}; did `weave-bench run` finish?")
        trials_by_arm[arm] = load_job(arm, job_dir)
    return trials_by_arm


def _read_ndjson(path: Path) -> list[DecisionRow]:
    return [json.loads(line) for line in path.read_text().splitlines() if line.strip()]


def _analytics_rows(
    config: BenchConfig,
    trials_by_arm: dict[ArmSpec, list[TrialRecord]],
    report_dir: Path,
) -> list[DecisionRow] | None:
    """The export is cached per trial window, so a report re-run after more
    trials land (``harbor jobs resume``) fetches again instead of replaying a
    dump that predates them."""
    if not any(arm.via_router for arm in trials_by_arm):
        return None
    trials = [t for arm_trials in trials_by_arm.values() for t in arm_trials]
    if not trials:
        return None
    window_start = min(t.started_at for t in trials) - ANALYTICS_WINDOW_PAD
    window_end = max(t.finished_at for t in trials) + ANALYTICS_WINDOW_PAD
    cache_path = report_dir / (
        f"analytics-{window_start.strftime(ANALYTICS_CACHE_STAMP)}-{window_end.strftime(ANALYTICS_CACHE_STAMP)}.ndjson"
    )
    if cache_path.exists():
        return _read_ndjson(cache_path)
    analytics_key = os.environ.get(config.router.analytics_key_env, "")
    if not analytics_key:
        print(
            f"[report] {config.router.analytics_key_env} unset: router-billed cost and served-model mix omitted",
            file=sys.stderr,
        )
        return None
    try:
        rows = fetch_rows(
            base_url=config.router.analytics_url,
            api_key=analytics_key,
            window_start=window_start,
            window_end=window_end,
        )
    except AnalyticsUnavailable as exc:
        print(f"[report] analytics export unavailable: {exc}", file=sys.stderr)
        return None
    write_ndjson(rows, cache_path)
    return rows


def cmd_report(args: argparse.Namespace) -> int:
    config = _load(args)
    benchmark = Benchmark(args.benchmark)
    arms = parse_arms(args.arms)
    trials_by_arm = _load_trials(config, args.run_id, arms)
    report_dir = _jobs_dir(config) / REPORT_DIRNAME / args.run_id
    report_dir.mkdir(parents=True, exist_ok=True)
    if args.no_analytics:
        analytics_rows = None
    elif args.analytics_ndjson is not None:
        analytics_rows = _read_ndjson(args.analytics_ndjson)
    else:
        analytics_rows = _analytics_rows(config, trials_by_arm, report_dir)
    tap_records = args.tap_records or Path(config.openrouter_tap.records_path)
    report = build_report(
        benchmark=str(benchmark),
        run_id=args.run_id,
        trials_by_arm=trials_by_arm,
        k=args.k or PINS[benchmark].n_attempts,
        analytics_rows=analytics_rows,
        tap_rows=load_tap_rows(tap_records),
    )
    markdown = render_markdown(report)
    (report_dir / "report.md").write_text(markdown)
    (report_dir / "report.json").write_text(report.to_json())
    sys.stdout.write(markdown)
    print(f"\nwrote {report_dir / 'report.md'} and {report_dir / 'report.json'}", file=sys.stderr)
    return 0


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="weave-bench", description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter
    )
    subparsers = parser.add_subparsers(dest="command", required=True)

    probe = subparsers.add_parser("probe", help="check router version, roster sha, and /beta availability")
    _add_common(probe)
    probe.set_defaults(func=cmd_probe)

    fetch = subparsers.add_parser("fetch", help="check out pinned sources")
    _add_common(fetch)
    fetch.add_argument("source", choices=("atlas",))
    fetch.set_defaults(func=cmd_fetch)

    tap = subparsers.add_parser("tap", help="OpenRouter recording tap")
    tap_sub = tap.add_subparsers(dest="tap_command", required=True)
    tap_serve = tap_sub.add_parser("serve")
    _add_common(tap_serve)
    tap_serve.set_defaults(func=cmd_tap_serve)

    run = subparsers.add_parser("run", help="launch Harbor jobs for every arm (or print them with --dry-run)")
    _add_common(run)
    run.add_argument("benchmark", choices=[b.value for b in Benchmark])
    run.add_argument("--arms", default="", help="comma-separated arm names (default: router,sol)")
    run.add_argument("--run-id", type=run_id_arg, default=None)
    run.add_argument("--smoke", action="store_true", help="the pinned <=3-task smoke subset")
    run.add_argument("--tasks", nargs="*", default=None, help="explicit Harbor task names")
    run.add_argument("--n-attempts", type=int, default=None)
    run.add_argument("--n-concurrent", type=int, default=None)
    run.add_argument("--codex-version", default=None)
    run.add_argument("--dry-run", action="store_true")
    run.set_defaults(func=cmd_run)

    report = subparsers.add_parser("report", help="aggregate Harbor results into Markdown + JSON")
    _add_common(report)
    report.add_argument("benchmark", choices=[b.value for b in Benchmark])
    report.add_argument("run_id", type=run_id_arg)
    report.add_argument("--arms", default="")
    report.add_argument("--k", type=int, default=None, help="pass@k (default: the benchmark's n_attempts)")
    report.add_argument("--no-analytics", action="store_true", help="skip the router analytics export")
    report.add_argument("--analytics-ndjson", type=Path, default=None, help="replay a saved export instead of fetching")
    report.add_argument("--tap-records", type=Path, default=None)
    report.set_defaults(func=cmd_report)

    return parser


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    try:
        return args.func(args)
    except (ValueError, RuntimeError, FileNotFoundError, subprocess.CalledProcessError) as exc:
        print(f"weave-bench: {exc}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    sys.exit(main())
