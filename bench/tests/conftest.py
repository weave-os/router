"""Shared fixtures: a Harbor-shaped trial directory builder."""

from __future__ import annotations

import json
from collections.abc import Callable
from datetime import UTC, datetime, timedelta
from pathlib import Path

import pytest

T0 = datetime(2026, 9, 6, 12, 0, tzinfo=UTC)

TrialWriter = Callable[..., Path]


def codex_json_lines(*events: dict[str, object]) -> str:
    return "\n".join(json.dumps(event) for event in events) + "\n"


@pytest.fixture
def write_trial(tmp_path: Path) -> TrialWriter:
    """Writes ``<job_dir>/<trial_name>/{result.json,agent/*}`` the way Harbor does."""

    def _write(
        job_dir: Path,
        *,
        task: str,
        trial_name: str,
        reward: float | None,
        started_at: datetime = T0,
        agent_seconds: float = 120.0,
        session_id: str | None = None,
        beta_output: str | None = None,
        codex_events: tuple[dict[str, object], ...] = (),
        exception: dict[str, str] | None = None,
        agent_cost_usd: float | None = None,
        rubric_score: float | None = None,
    ) -> Path:
        trial_dir = job_dir / trial_name
        agent_dir = trial_dir / "agent"
        agent_dir.mkdir(parents=True)
        finished_at = started_at + timedelta(seconds=agent_seconds + 30)
        agent_finished = started_at + timedelta(seconds=agent_seconds)
        trial: dict[str, object] = {
            "task_name": task,
            "trial_name": trial_name,
            "started_at": started_at.isoformat(),
            "finished_at": finished_at.isoformat(),
            "agent_execution": {"started_at": started_at.isoformat(), "finished_at": agent_finished.isoformat()},
            "agent_result": {
                "cost_usd": agent_cost_usd,
                "n_input_tokens": 10,
                "n_cache_tokens": 4,
                "n_output_tokens": 2,
            },
            "verifier_result": {"rewards": {"reward": reward}} if reward is not None else None,
            "exception_info": exception,
        }
        (trial_dir / "result.json").write_text(json.dumps(trial))
        if session_id is not None:
            (agent_dir / "trajectory.json").write_text(json.dumps({"session_id": session_id}))
        if beta_output is not None:
            (agent_dir / "codex-beta.txt").write_text(beta_output)
        if codex_events:
            (agent_dir / "codex.txt").write_text(codex_json_lines(*codex_events))
        if rubric_score is not None:
            (trial_dir / "verifier").mkdir()
            (trial_dir / "verifier" / "evaluation_results.json").write_text(json.dumps({"agg_score": rubric_score}))
        return trial_dir

    return _write
