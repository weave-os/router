from __future__ import annotations

from datetime import timedelta
from pathlib import Path

from tests.conftest import T0, TrialWriter
from weave_bench.arms import ARMS
from weave_bench.trials import CodexUsage, ErrorCategory, load_job

TURN = {"type": "turn.completed", "usage": {"input_tokens": 1000, "cached_input_tokens": 600, "output_tokens": 50}}
THREAD = {"type": "thread.started", "thread_id": "thread-from-stream"}


def test_attempts_are_assigned_chronologically_per_task(tmp_path: Path, write_trial: TrialWriter) -> None:
    job = tmp_path / "job"
    write_trial(job, task="t1", trial_name="t1__zzz", reward=1.0, started_at=T0)
    write_trial(job, task="t1", trial_name="t1__aaa", reward=0.0, started_at=T0 + timedelta(minutes=1))
    write_trial(job, task="t2", trial_name="t2__mmm", reward=1.0, started_at=T0 + timedelta(minutes=2))
    (job / "not-a-trial").mkdir()
    records = load_job(ARMS["sol"], job)
    assert [(r.task, r.trial_name, r.attempt) for r in records] == [
        ("t1", "t1__zzz", 1),
        ("t1", "t1__aaa", 2),
        ("t2", "t2__mmm", 1),
    ]
    assert [r.pairing_key for r in records] == ["t1/1", "t1/2", "t2/1"]
    assert [r.passed for r in records] == [True, False, True]


def test_codex_usage_session_and_beta_ack(tmp_path: Path, write_trial: TrialWriter) -> None:
    job = tmp_path / "job"
    write_trial(
        job,
        task="t1",
        trial_name="t1__abc",
        reward=1.0,
        session_id="sess-from-trajectory",
        beta_output='{"type":"item.completed","text":"Beta enabled. Type /beta again to turn it off."}\n',
        codex_events=(THREAD, TURN, TURN, {"type": "item.completed"}),
        agent_cost_usd=1.25,
        rubric_score=0.8,
    )
    (record,) = load_job(ARMS["router"], job)
    assert record.session_id == "sess-from-trajectory"
    assert record.beta_acknowledged is True
    assert record.codex_turns == (CodexUsage(1000, 600, 50), CodexUsage(1000, 600, 50))
    assert record.agent_cost_usd == 1.25
    assert record.rubric_score == 0.8
    assert record.agent_seconds == 120.0
    assert record.error_category is ErrorCategory.NONE


def test_session_falls_back_to_thread_started_event(tmp_path: Path, write_trial: TrialWriter) -> None:
    job = tmp_path / "job"
    write_trial(job, task="t1", trial_name="t1__abc", reward=None, codex_events=(THREAD,))
    (record,) = load_job(ARMS["router"], job)
    assert record.session_id == "thread-from-stream"
    assert record.beta_acknowledged is False
    assert record.passed is False


def test_direct_arm_has_no_beta_verdict(tmp_path: Path, write_trial: TrialWriter) -> None:
    job = tmp_path / "job"
    write_trial(job, task="t1", trial_name="t1__abc", reward=1.0, beta_output="Beta enabled")
    (record,) = load_job(ARMS["sol"], job)
    assert record.beta_acknowledged is None


def test_error_categories(tmp_path: Path, write_trial: TrialWriter) -> None:
    job = tmp_path / "job"
    write_trial(
        job,
        task="setup",
        trial_name="setup__1",
        reward=None,
        exception={"exception_type": "EnvironmentSetupError", "exception_message": "build failed"},
    )
    write_trial(
        job,
        task="killed",
        trial_name="killed__1",
        reward=None,
        exception={"exception_type": "AgentTimeout", "exception_message": "exit code 137"},
    )
    write_trial(
        job,
        task="refused",
        trial_name="refused__1",
        reward=None,
        exception={"exception_type": "RuntimeError", "exception_message": "agent failed"},
        codex_events=({"type": "error", "message": "This request was flagged for possible cybersecurity risk"},),
    )
    write_trial(
        job,
        task="unauthorized",
        trial_name="unauthorized__1",
        reward=None,
        exception={"exception_type": "NonZeroAgentExitCodeError", "exception_message": "Command failed (exit 1)"},
        codex_events=(
            {"type": "turn.failed", "error": {"message": "unexpected status 401 Unauthorized: Incorrect API key"}},
        ),
    )
    write_trial(
        job,
        task="other",
        trial_name="other__1",
        reward=None,
        exception={"exception_type": "RuntimeError", "exception_message": "boom"},
    )
    write_trial(job, task="fine", trial_name="fine__1", reward=1.0)
    by_task = {r.task: r for r in load_job(ARMS["sol"], job)}
    assert by_task["setup"].error_category is ErrorCategory.ENVIRONMENT_SETUP
    assert by_task["killed"].error_category is ErrorCategory.AGENT_KILLED
    assert by_task["refused"].error_category is ErrorCategory.CYBER_REFUSAL
    assert by_task["unauthorized"].error_category is ErrorCategory.UPSTREAM_AUTH
    assert by_task["other"].error_category is ErrorCategory.OTHER
    assert by_task["fine"].error_category is ErrorCategory.NONE
    assert by_task["setup"].errored and not by_task["fine"].errored
