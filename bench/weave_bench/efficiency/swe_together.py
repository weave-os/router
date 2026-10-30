"""SWE-Together work-ratio matrix: one trial per (cell, task, attempt).

Each trial runs the pinned SWE-Together harness on one task with its base URL
pointed at the pin proxy (``pin_proxy``), which stamps the cell's
``x-weave-force-model`` and the trial's own ``x-weave-rollout-id``. The trial's
router rows are then exactly the export rows carrying that rollout id, whatever
else the installation served at the time.

A ledger (``trials.jsonl``) records every attempt's window and exit status.
A rerun of a trial gets a fresh rollout id (``next_rollout_id``), so an earlier
attempt's rows never join the retry. ``collect`` joins each trial's latest
attempt to the export, applies the guard (``estimates.trial_violations``) and
writes ``usage.json``, the raw per-trial token usage the ratios are computed
from.
"""

from __future__ import annotations

import json
import os
import subprocess
import sys
from collections import Counter
from collections.abc import Callable, Iterable, Mapping, Sequence
from concurrent.futures import ThreadPoolExecutor
from dataclasses import asdict, dataclass
from datetime import UTC, datetime
from pathlib import Path

from weave_bench.analytics import DecisionRow
from weave_bench.efficiency.cells import REQUESTED_MODEL, Cell, Harness
from weave_bench.efficiency.estimates import TokenUsage, Violation, trial_usage, trial_violations
from weave_bench.efficiency.pin_path import pin_base_url
from weave_bench.efficiency.pooling import MeasurementRun

UPSTREAM_REPO = "https://github.com/Togetherbench/SWE-Together.git"
UPSTREAM_REVISION = "811a70a28ff20bfbeabf9a8b5ec42152d16c9b4f"
# Upstream hardcodes the agent CLI versions; this patch makes them settable.
VERSIONS_PATCH = Path(__file__).with_name("swe_together_versions.patch")
CLAUDE_CODE_VERSION = "2.1.280"
CODEX_VERSION = "0.133.0"
USER_SIMULATOR_MODEL = "gemini/gemini-3.1-pro-preview"
AGENT_TIMEOUT_SECONDS = 80 * 60
N_ATTEMPTS = 2
LEDGER_FILENAME = "trials.jsonl"
ROWS_DIRNAME = "rows"
USAGE_FILENAME = "usage.json"
ROLLOUT_ID_COLUMN = "rollout_id"
HARNESS_PROVIDER = {Harness.CLAUDE_CODE: "anthropic", Harness.CODEX: "openai"}
HARNESS_VERSION = {Harness.CLAUDE_CODE: CLAUDE_CODE_VERSION, Harness.CODEX: CODEX_VERSION}


@dataclass(frozen=True)
class Trial:
    run_id: str
    cell: Cell
    task: str
    attempt: int

    @property
    def trial_id(self) -> str:
        return f"{self.run_id}-{self.cell.slug}-{self.task}-a{self.attempt}"


def plan_trials(run_id: str, cells: Iterable[Cell], tasks: Sequence[str], n_attempts: int) -> list[Trial]:
    return [
        Trial(run_id, cell, task, attempt) for cell in cells for task in tasks for attempt in range(1, n_attempts + 1)
    ]


@dataclass(frozen=True)
class TrialCommand:
    trial: Trial
    rollout_id: str
    argv: tuple[str, ...]
    # Non-secret settings; the router key is added at launch under the provider's key name.
    env: Mapping[str, str]
    api_key_env_name: str


def next_rollout_id(trial: Trial, ledger: Iterable[LedgerEntry]) -> str:
    """The trial id for its first attempt; ``<trial id>-r<n>`` for the n-th rerun."""
    reruns = sum(1 for entry in ledger if entry.trial_id == trial.trial_id)
    return f"{trial.trial_id}-r{reruns}" if reruns else trial.trial_id


def trial_command(
    trial: Trial,
    *,
    rollout_id: str,
    checkout: Path,
    python: str,
    proxy_url: str,
    trials_root: Path,
    env_type: str,
) -> TrialCommand:
    harness = trial.cell.harness
    base_url = pin_base_url(proxy_url, rollout_id=rollout_id, force_model=trial.cell.model)
    argv = (
        python,
        str(checkout / "src" / "run_eval.py"),
        "--model", f"{HARNESS_PROVIDER[harness]}/{REQUESTED_MODEL[harness]}",
        "--user-model", USER_SIMULATOR_MODEL,
        "--tag", rollout_id,
        "--agent-type", harness.value,
        "--env-type", env_type,
        "--workers", "1",
        "--agent-timeout", str(AGENT_TIMEOUT_SECONDS),
        "--trials-dir", str(trials_root / rollout_id),
        "--tasks", trial.task,
        "--skip-existing",
    )  # fmt: skip
    if harness is Harness.CODEX:
        env = {"OPENAI_BASE_URL": f"{base_url}/v1", "SWE_TOGETHER_CODEX_VERSION": CODEX_VERSION}
        return TrialCommand(trial, rollout_id, argv, env, "OPENAI_API_KEY")
    env = {"ANTHROPIC_BASE_URL": base_url, "SWE_TOGETHER_CLAUDE_CODE_VERSION": CLAUDE_CODE_VERSION}
    return TrialCommand(trial, rollout_id, argv, env, "ANTHROPIC_API_KEY")


@dataclass(frozen=True)
class LedgerEntry:
    trial_id: str
    rollout_id: str
    run_id: str
    harness: str
    model: str
    task: str
    attempt: int
    started_at: str
    finished_at: str
    returncode: int
    harness_version: str


def _utc_now() -> str:
    return datetime.now(UTC).isoformat()


def run_trial(command: TrialCommand, router_api_key: str, ledger: Path) -> LedgerEntry:
    # Inherited base URLs would point the agent past the proxy.
    env = {key: value for key, value in os.environ.items() if key not in ("ANTHROPIC_BASE_URL", "OPENAI_BASE_URL")}
    env.update(command.env)
    env[command.api_key_env_name] = router_api_key
    started_at = _utc_now()
    returncode = subprocess.run(list(command.argv), env=env, check=False).returncode
    trial = command.trial
    entry = LedgerEntry(
        trial.trial_id,
        command.rollout_id,
        trial.run_id,
        trial.cell.harness.value,
        trial.cell.model,
        trial.task,
        trial.attempt,
        started_at,
        _utc_now(),
        returncode,
        HARNESS_VERSION[trial.cell.harness],
    )
    with ledger.open("a") as handle:
        handle.write(json.dumps(asdict(entry)) + "\n")
    return entry


def run_matrix(
    commands: Sequence[TrialCommand], *, router_api_key: str, run_dir: Path, workers: int
) -> list[LedgerEntry]:
    """Skips trials the ledger already records as finished with exit 0, so a rerun resumes."""
    run_dir.mkdir(parents=True, exist_ok=True)
    ledger = run_dir / LEDGER_FILENAME
    done = {entry.trial_id for entry in read_ledger(ledger) if entry.returncode == 0}
    pending = [command for command in commands if command.trial.trial_id not in done]
    print(f"[efficiency] {len(pending)} trials to run ({len(commands) - len(pending)} already done)", file=sys.stderr)
    with ThreadPoolExecutor(max_workers=workers) as pool:
        return list(pool.map(lambda command: run_trial(command, router_api_key, ledger), pending))


def read_ledger(ledger: Path) -> list[LedgerEntry]:
    if not ledger.exists():
        return []
    return [LedgerEntry(**json.loads(line)) for line in ledger.read_text().splitlines() if line.strip()]


@dataclass(frozen=True)
class TrialUsage:
    trial_id: str
    rollout_id: str
    harness: str
    model: str
    task: str
    attempt: int
    usage: TokenUsage
    violations: tuple[str, ...]
    rows: int
    finished_at: str
    harness_version: str

    @property
    def measures_pinned_model(self) -> bool:
        return not self.violations


def collect(run_dir: Path, fetch: Callable[[datetime, datetime], list[DecisionRow]]) -> list[TrialUsage]:
    """Joins each trial's latest attempt to its export rows (by that attempt's
    rollout id), saves them under ``rows/``, and writes ``usage.json``. An
    attempt whose harness exited non-zero is ``UNFINISHED``: its partial tokens
    would understate the work."""
    latest: dict[str, LedgerEntry] = {}
    for entry in read_ledger(run_dir / LEDGER_FILENAME):
        latest[entry.trial_id] = entry
    if not latest:
        raise ValueError(f"no trials recorded in {run_dir / LEDGER_FILENAME}")
    window_start = min(datetime.fromisoformat(entry.started_at) for entry in latest.values())
    window_end = max(datetime.fromisoformat(entry.finished_at) for entry in latest.values())
    rows_by_rollout: dict[str, list[DecisionRow]] = {entry.rollout_id: [] for entry in latest.values()}
    for row in fetch(window_start, window_end):
        rollout_id = row.get(ROLLOUT_ID_COLUMN)
        if isinstance(rollout_id, str) and rollout_id in rows_by_rollout:
            rows_by_rollout[rollout_id].append(row)
    rows_dir = run_dir / ROWS_DIRNAME
    rows_dir.mkdir(parents=True, exist_ok=True)
    usages: list[TrialUsage] = []
    for trial_id, entry in sorted(latest.items()):
        rows = rows_by_rollout[entry.rollout_id]
        (rows_dir / f"{entry.rollout_id}.json").write_text(json.dumps(rows, sort_keys=True))
        violations = trial_violations(Cell(Harness(entry.harness), entry.model), rows)
        if entry.returncode != 0:
            violations.append(Violation.UNFINISHED)
        usages.append(
            TrialUsage(
                trial_id=trial_id,
                rollout_id=entry.rollout_id,
                harness=entry.harness,
                model=entry.model,
                task=entry.task,
                attempt=entry.attempt,
                usage=trial_usage(rows),
                violations=tuple(violations),
                rows=len(rows),
                finished_at=entry.finished_at,
                harness_version=entry.harness_version,
            )
        )
    write_usage(run_dir / USAGE_FILENAME, usages)
    return usages


def write_usage(path: Path, usages: Iterable[TrialUsage]) -> None:
    path.write_text(json.dumps([asdict(usage) for usage in usages], indent=1) + "\n")


def read_usage(path: Path) -> list[TrialUsage]:
    return [
        TrialUsage(**{**record, "usage": TokenUsage(**record["usage"]), "violations": tuple(record["violations"])})
        for record in json.loads(path.read_text())
    ]


def measured_trials(usages: Iterable[TrialUsage]) -> dict[Cell, dict[str, list[TokenUsage]]]:
    """Usage per attempt, per task, per cell, for the trials that measured their pinned model."""
    trials: dict[Cell, dict[str, list[TokenUsage]]] = {}
    for usage in usages:
        if usage.measures_pinned_model:
            cell = Cell(Harness(usage.harness), usage.model)
            trials.setdefault(cell, {}).setdefault(usage.task, []).append(usage.usage)
    return trials


def measurement_run(run_id: str, usages: Sequence[TrialUsage]) -> MeasurementRun:
    """One run for the pooling rules: its trials, when it finished, and each harness's version."""
    versions: dict[Harness, Counter[str]] = {}
    for usage in usages:
        versions.setdefault(Harness(usage.harness), Counter())[usage.harness_version] += 1
    return MeasurementRun(
        run_id=run_id,
        measured_at=max(datetime.fromisoformat(usage.finished_at) for usage in usages),
        harness_versions={harness: counts.most_common(1)[0][0] for harness, counts in versions.items()},
        trials=measured_trials(usages),
    )
