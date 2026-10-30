"""Terminal-Bench 4 quality arms: router vs always-X, both through the router.

A *router* arm leaves routing to the router's default policy (no ``/beta``
turn, no pin); an *always-X* arm pins every request to X with
``x-weave-force-model``. Both go through the same router and key, so the only
difference between arms is the pin. Each arm is one Harbor job; per-trial cost
comes from the router's analytics export, joined on the trial's session id.
"""

from __future__ import annotations

import json
from collections import defaultdict
from collections.abc import Iterable, Mapping
from dataclasses import dataclass
from datetime import datetime
from pathlib import Path
from urllib.parse import urlsplit

from weave_bench.analytics import AnalyticsColumn, DecisionRow, row_fresh_input_tokens, row_int
from weave_bench.benchmarks import PINS, Benchmark
from weave_bench.codex_config import (
    ROUTER_CLIENT_APP,
    ROUTER_CLIENT_APP_HEADER,
    ROUTER_FORCE_MODEL_HEADER,
    ROUTER_ROLLOUT_ID_HEADER,
    CodexProviderName,
    render_codex_config,
)
from weave_bench.config import BenchConfig
from weave_bench.efficiency.cells import REQUESTED_MODEL, Harness
from weave_bench.efficiency.quality import TaskOutcome
from weave_bench.efficiency.tasks import TB4_TASK_ORG, terminal_bench_task_names
from weave_bench.harbor_command import AGENT_API_KEY_ENV, BETA_CODEX_AGENT
from weave_bench.prices import PriceTable, load_price_table

CLAUDE_CODE_AGENT = "claude-code"
# Opus 5.5 rejects Claude Code releases before this one.
CLAUDE_CODE_VERSION = "2.1.280"
CODEX_VERSION = PINS[Benchmark.TERMINAL_BENCH_4].codex_version
N_ATTEMPTS = 2
N_CONCURRENT = 4
CLAUDE_CODE_STREAM_FILENAME = "claude-code.txt"
CLAUDE_SESSION_KEY = "session_id"
CODEX_STREAM_FILENAME = "codex.txt"
CODEX_THREAD_STARTED = "thread.started"
PASSING_REWARD = 1.0


@dataclass(frozen=True)
class QualityArm:
    name: str
    harness: Harness
    force_model: str | None = None

    @property
    def requested_model(self) -> str:
        return REQUESTED_MODEL[self.harness]

    @property
    def is_router(self) -> bool:
        return self.force_model is None

    @property
    def baseline_model(self) -> str:
        """The model this arm measures: the router arm stands for its harness's requested model."""
        return self.force_model or self.requested_model


ARMS: dict[str, QualityArm] = {
    arm.name: arm
    for arm in (
        QualityArm("cc-router", Harness.CLAUDE_CODE),
        QualityArm("cc-opus-5-5", Harness.CLAUDE_CODE, "claude-opus-5-5"),
        QualityArm("cc-fable-5-1", Harness.CLAUDE_CODE, "claude-fable-5-1"),
        QualityArm("cx-router", Harness.CODEX),
        QualityArm("cx-astra", Harness.CODEX, "gpt-6-astra"),
        QualityArm("cx-sol-6-1", Harness.CODEX, "gpt-6.1-sol"),
    )
}
DEFAULT_ARMS = ("cc-router", "cc-opus-5-5", "cx-router", "cx-astra", "cx-sol-6-1")


def parse_quality_arms(names: str) -> tuple[QualityArm, ...]:
    chosen = tuple(name.strip() for name in names.split(",") if name.strip()) or DEFAULT_ARMS
    unknown = [name for name in chosen if name not in ARMS]
    if unknown:
        raise ValueError(f"unknown arms {unknown}; known: {sorted(ARMS)}")
    return tuple(ARMS[name] for name in chosen)


def job_name(run_id: str, arm: QualityArm) -> str:
    return f"{run_id}--{arm.name}"


def router_headers(arm: QualityArm, run_id: str) -> dict[str, str]:
    headers = {ROUTER_ROLLOUT_ID_HEADER: job_name(run_id, arm)}
    if arm.force_model is not None:
        headers[ROUTER_FORCE_MODEL_HEADER] = arm.force_model
    return headers


def agent_config(arm: QualityArm, run_id: str, router_base_url: str) -> str:
    """Codex ``config.toml`` or a Claude Code settings layer carrying the router headers."""
    headers = router_headers(arm, run_id)
    if arm.harness is Harness.CODEX:
        return render_codex_config(
            base_url=f"{router_base_url.rstrip('/')}/v1",
            provider_name=CodexProviderName.OPENAI,
            headers={ROUTER_CLIENT_APP_HEADER: ROUTER_CLIENT_APP, **headers},
        )
    custom_headers = "\n".join(f"{name}: {value}" for name, value in headers.items())
    return json.dumps({"env": {"ANTHROPIC_CUSTOM_HEADERS": custom_headers}}, indent=2) + "\n"


@dataclass(frozen=True)
class QualityJob:
    arm: QualityArm
    argv: tuple[str, ...]
    config_path: Path
    config_text: str


def build_quality_job(
    arm: QualityArm,
    *,
    run_id: str,
    config: BenchConfig,
    task_names: Iterable[str],
    n_attempts: int = N_ATTEMPTS,
    n_concurrent: int = N_CONCURRENT,
) -> QualityJob:
    jobs_dir = Path(config.harbor.jobs_dir)
    name = job_name(run_id, arm)
    suffix = ".codex-config.toml" if arm.harness is Harness.CODEX else ".claude-settings.json"
    config_path = jobs_dir / f"{name}{suffix}"
    argv = ["harbor", "run", "--dataset", str(PINS[Benchmark.TERMINAL_BENCH_4].harbor_dataset)]
    argv += ["--env", config.harbor.environment.value]
    if arm.harness is Harness.CODEX:
        argv += ["--agent", BETA_CODEX_AGENT, "--model", f"openai/{arm.requested_model}"]
        argv += ["--agent-kwarg", f"version={CODEX_VERSION}"]
        argv += ["--agent-env", f"OPENAI_API_KEY=${{{AGENT_API_KEY_ENV}}}"]
    else:
        argv += ["--agent", CLAUDE_CODE_AGENT, "--model", arm.requested_model]
        argv += ["--agent-kwarg", f"version={CLAUDE_CODE_VERSION}"]
        argv += ["--agent-env", f"ANTHROPIC_API_KEY=${{{AGENT_API_KEY_ENV}}}"]
        argv += ["--agent-env", f"ANTHROPIC_BASE_URL={config.router.base_url.rstrip('/')}"]
    argv += ["--agent-kwarg", f"config={config_path}"]
    host = urlsplit(config.router.base_url).hostname
    if host:
        argv += ["--allow-agent-host", host]
    argv += ["--n-attempts", str(n_attempts), "--n-concurrent", str(n_concurrent)]
    argv += ["--jobs-dir", str(jobs_dir), "--job-name", name, "--yes", "--quiet"]
    for task in task_names:
        argv += ["--include-task-name", f"{TB4_TASK_ORG}/{task}"]
    return QualityJob(arm, tuple(argv), config_path, agent_config(arm, run_id, config.router.base_url))


def select_tasks(explicit: Iterable[str] = ()) -> tuple[str, ...]:
    manifest = terminal_bench_task_names()
    chosen = [task.removeprefix(f"{TB4_TASK_ORG}/") for task in explicit if task]
    unknown = sorted(set(chosen) - set(manifest))
    if unknown:
        raise ValueError(f"not in the Terminal-Bench efficiency manifest: {', '.join(unknown)}")
    return tuple(chosen) or manifest


@dataclass(frozen=True)
class HarborTrial:
    task: str
    trial_name: str
    reward: float | None
    errored: bool
    session_id: str | None
    started_at: datetime
    finished_at: datetime


def _stream_session_id(trial_dir: Path) -> str | None:
    for filename in (CLAUDE_CODE_STREAM_FILENAME, CODEX_STREAM_FILENAME):
        stream = trial_dir / "agent" / filename
        if not stream.exists():
            continue
        for line in stream.read_text(errors="replace").splitlines():
            try:
                event = json.loads(line)
            except json.JSONDecodeError:
                continue
            if not isinstance(event, dict):
                continue
            if event.get("type") == CODEX_THREAD_STARTED and event.get("thread_id"):
                return str(event["thread_id"])
            if isinstance(event.get(CLAUDE_SESSION_KEY), str) and event[CLAUDE_SESSION_KEY]:
                return str(event[CLAUDE_SESSION_KEY])
    return None


def trial_session_id(trial_dir: Path) -> str | None:
    """The router session a trial ran under: Harbor's trajectory records it;
    the agent's own event stream is the fallback."""
    trajectory = trial_dir / "agent" / "trajectory.json"
    if trajectory.exists():
        session_id = json.loads(trajectory.read_text()).get("session_id")
        if isinstance(session_id, str) and session_id:
            return session_id
    return _stream_session_id(trial_dir)


def load_harbor_trials(job_dir: Path) -> list[HarborTrial]:
    """Finished trials only: Harbor writes ``result.json`` at start and sets ``finished_at`` at the end."""
    trials: list[HarborTrial] = []
    for result_path in sorted(job_dir.glob("*/result.json")):
        result = json.loads(result_path.read_text())
        if not result.get("finished_at"):
            continue
        rewards = (result.get("verifier_result") or {}).get("rewards") or {}
        trials.append(
            HarborTrial(
                task=str(result["task_name"]).rsplit("/", 1)[-1],
                trial_name=str(result["trial_name"]),
                reward=rewards.get("reward"),
                errored=result.get("exception_info") is not None,
                session_id=trial_session_id(result_path.parent),
                started_at=datetime.fromisoformat(result["started_at"]),
                finished_at=datetime.fromisoformat(result["finished_at"]),
            )
        )
    return trials


def catalog_cost_usd(rows: Iterable[DecisionRow], prices: PriceTable | None = None) -> float:
    """Every row's tokens at its served model's catalog rates. The export's own
    ``actual_*`` cost is $0 for subscription-served rows, so it cannot price an
    arm that ran on subscriptions."""
    table = prices or load_price_table()
    total = 0.0
    for row in rows:
        model = str(row.get(AnalyticsColumn.DECISION_MODEL) or "")
        price = table.lookup(model)
        if price is None:
            raise ValueError(f"no catalog price for served model {model!r}; regenerate prices.generated.json")
        total += price.cost_usd(
            input_tokens=row_fresh_input_tokens(row),
            cache_read_tokens=row_int(row, AnalyticsColumn.CACHE_READ_TOKENS),
            cache_write_tokens=row_int(row, AnalyticsColumn.CACHE_CREATION_TOKENS),
            output_tokens=row_int(row, AnalyticsColumn.OUTPUT_TOKENS),
        )
    return total


def trial_outcome(trial: HarborTrial, rows: Iterable[DecisionRow], prices: PriceTable | None = None) -> TaskOutcome:
    """An errored trial with no verifier reward is infrastructure, not a failure:
    it keeps its cost but is not graded."""
    solved = None if trial.errored and trial.reward is None else trial.reward == PASSING_REWARD
    return TaskOutcome(cost_usd=catalog_cost_usd(rows, prices), solved=solved)


def arm_outcomes(
    trials: Iterable[HarborTrial], rows_by_session: Mapping[str, list[DecisionRow]], prices: PriceTable | None = None
) -> dict[str, list[TaskOutcome]]:
    outcomes: dict[str, list[TaskOutcome]] = defaultdict(list)
    for trial in trials:
        rows = rows_by_session.get(trial.session_id or "", [])
        outcomes[trial.task].append(trial_outcome(trial, rows, prices))
    return dict(outcomes)


def served_mix(trials: Iterable[HarborTrial], rows_by_session: Mapping[str, list[DecisionRow]]) -> dict[str, int]:
    counts: dict[str, int] = defaultdict(int)
    for trial in trials:
        for row in rows_by_session.get(trial.session_id or "", []):
            counts[str(row.get(AnalyticsColumn.DECISION_MODEL) or "")] += 1
    return dict(sorted(counts.items(), key=lambda item: -item[1]))
