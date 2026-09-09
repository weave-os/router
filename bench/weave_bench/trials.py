"""Load Harbor trial directories into a flat, arm-tagged record per trial."""

from __future__ import annotations

import json
from collections.abc import Iterator
from dataclasses import dataclass, replace
from datetime import datetime
from enum import StrEnum
from pathlib import Path

from weave_bench.agent.beta_turn import BETA_TURN_OUTPUT_FILENAME, beta_acknowledged
from weave_bench.arms import ArmSpec

CODEX_STREAM_FILENAMES = (BETA_TURN_OUTPUT_FILENAME, "codex.txt")
ATLAS_JUDGE_OUTPUT = "verifier/evaluation_results.json"


class CodexEvent(StrEnum):
    THREAD_STARTED = "thread.started"
    TURN_COMPLETED = "turn.completed"


class ErrorCategory(StrEnum):
    NONE = "none"
    CYBER_REFUSAL = "openai-cyber-filter-refusal"
    ENVIRONMENT_SETUP = "harbor-environment-setup-failure"
    AGENT_KILLED = "agent-killed-exit-137"
    UPSTREAM_AUTH = "upstream-auth-error"
    OTHER = "other"


CYBER_REFUSAL_MARKER = "flagged for possible cybersecurity risk"
ENVIRONMENT_SETUP_MARKERS = ("EnvironmentSetup", "environment setup", "exit code 100")
AGENT_KILLED_MARKERS = ("exit code 137", "exit status 137")
UPSTREAM_AUTH_MARKERS = ("401 Unauthorized", "invalid_api_key", '"invalid_key"')


@dataclass(frozen=True)
class CodexUsage:
    input_tokens: int
    cached_input_tokens: int
    output_tokens: int


@dataclass(frozen=True)
class TrialRecord:
    arm: ArmSpec
    task: str
    trial_name: str
    attempt: int
    reward: float | None
    rubric_score: float | None
    errored: bool
    error_category: ErrorCategory
    session_id: str | None
    beta_acknowledged: bool | None
    started_at: datetime
    finished_at: datetime
    agent_seconds: float
    agent_cost_usd: float | None
    n_input_tokens: int | None
    n_cache_tokens: int | None
    n_output_tokens: int | None
    codex_turns: tuple[CodexUsage, ...]

    @property
    def passed(self) -> bool:
        return self.reward is not None and self.reward >= 0.5

    @property
    def pairing_key(self) -> str:
        return f"{self.task}/{self.attempt}"


def _codex_events(trial_dir: Path) -> Iterator[dict[str, object]]:
    for stream in (trial_dir / "agent" / name for name in CODEX_STREAM_FILENAMES):
        if not stream.exists():
            continue
        for line in stream.read_text(errors="replace").splitlines():
            if not line.startswith("{"):
                continue
            try:
                event = json.loads(line)
            except json.JSONDecodeError:
                continue
            if isinstance(event, dict):
                yield event


def _session_id(trial_dir: Path, events: list[dict[str, object]]) -> str | None:
    trajectory = trial_dir / "agent" / "trajectory.json"
    if trajectory.exists():
        session_id = json.loads(trajectory.read_text()).get("session_id")
        if isinstance(session_id, str) and session_id:
            return session_id
    for event in events:
        thread_id = event.get("thread_id")
        if event.get("type") == CodexEvent.THREAD_STARTED and isinstance(thread_id, str):
            return thread_id
    return None


def _codex_turns(events: list[dict[str, object]]) -> tuple[CodexUsage, ...]:
    turns: list[CodexUsage] = []
    for event in events:
        usage = event.get("usage")
        if event.get("type") != CodexEvent.TURN_COMPLETED or not isinstance(usage, dict):
            continue
        turns.append(
            CodexUsage(
                input_tokens=int(usage.get("input_tokens", 0)),
                cached_input_tokens=int(usage.get("cached_input_tokens", 0)),
                output_tokens=int(usage.get("output_tokens", 0)),
            )
        )
    return tuple(turns)


def _beta_acknowledged(arm: ArmSpec, trial_dir: Path) -> bool | None:
    if not arm.beta_preflight:
        return None
    beta_output = trial_dir / "agent" / BETA_TURN_OUTPUT_FILENAME
    return beta_output.exists() and beta_acknowledged(beta_output.read_text(errors="replace"))


def _error_category(exception_info: dict[str, object] | None, trial_dir: Path) -> ErrorCategory:
    if exception_info is None:
        return ErrorCategory.NONE
    haystack = f"{exception_info.get('exception_type', '')} {exception_info.get('exception_message', '')}"
    if any(marker in haystack for marker in ENVIRONMENT_SETUP_MARKERS):
        return ErrorCategory.ENVIRONMENT_SETUP
    if any(marker in haystack for marker in AGENT_KILLED_MARKERS):
        return ErrorCategory.AGENT_KILLED
    codex_stream = trial_dir / "agent" / "codex.txt"
    stream_text = codex_stream.read_text(errors="replace") if codex_stream.exists() else ""
    if CYBER_REFUSAL_MARKER in stream_text:
        return ErrorCategory.CYBER_REFUSAL
    if any(marker in stream_text for marker in UPSTREAM_AUTH_MARKERS):
        return ErrorCategory.UPSTREAM_AUTH
    return ErrorCategory.OTHER


def _rubric_score(trial_dir: Path) -> float | None:
    judge_output = trial_dir / ATLAS_JUDGE_OUTPUT
    if not judge_output.exists():
        return None
    agg_score = json.loads(judge_output.read_text()).get("agg_score")
    return float(agg_score) if isinstance(agg_score, (int, float)) else None


def _timing_seconds(timing: dict[str, object] | None) -> float:
    if not timing:
        return 0.0
    started, finished = timing.get("started_at"), timing.get("finished_at")
    if not (isinstance(started, str) and isinstance(finished, str)):
        return 0.0
    return (datetime.fromisoformat(finished) - datetime.fromisoformat(started)).total_seconds()


def load_trial(arm: ArmSpec, trial_dir: Path, attempt: int) -> TrialRecord:
    """``attempt`` is the trial's 1-based start order within its task; Harbor's
    trial names carry a random suffix, so attempts pair across arms by order."""
    trial = json.loads((trial_dir / "result.json").read_text())
    agent_result = trial.get("agent_result") or {}
    rewards = (trial.get("verifier_result") or {}).get("rewards") or {}
    events = list(_codex_events(trial_dir))
    exception_info = trial.get("exception_info")
    return TrialRecord(
        arm=arm,
        task=trial["task_name"],
        trial_name=trial["trial_name"],
        attempt=attempt,
        reward=rewards.get("reward"),
        rubric_score=_rubric_score(trial_dir),
        errored=exception_info is not None,
        error_category=_error_category(exception_info, trial_dir),
        session_id=_session_id(trial_dir, events),
        beta_acknowledged=_beta_acknowledged(arm, trial_dir),
        started_at=datetime.fromisoformat(trial["started_at"]),
        finished_at=datetime.fromisoformat(trial["finished_at"]),
        agent_seconds=_timing_seconds(trial.get("agent_execution")),
        agent_cost_usd=agent_result.get("cost_usd"),
        n_input_tokens=agent_result.get("n_input_tokens"),
        n_cache_tokens=agent_result.get("n_cache_tokens"),
        n_output_tokens=agent_result.get("n_output_tokens"),
        codex_turns=_codex_turns(events),
    )


def load_job(arm: ArmSpec, job_dir: Path) -> list[TrialRecord]:
    unordered = [
        load_trial(arm, trial_dir, attempt=0)
        for trial_dir in sorted(job_dir.iterdir())
        if (trial_dir / "result.json").exists()
    ]
    next_attempt: dict[str, int] = {}
    records: list[TrialRecord] = []
    for record in sorted(unordered, key=lambda r: (r.task, r.started_at)):
        attempt = next_attempt.get(record.task, 0) + 1
        next_attempt[record.task] = attempt
        records.append(replace(record, attempt=attempt))
    return records
