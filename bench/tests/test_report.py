from __future__ import annotations

import json
from datetime import timedelta
from pathlib import Path

import pytest

from tests.conftest import T0, TrialWriter
from weave_bench.arms import ARMS
from weave_bench.markdown import render_markdown
from weave_bench.prices import load_price_table
from weave_bench.report import build_report, load_tap_rows
from weave_bench.trials import load_job

TURN = {"type": "turn.completed", "usage": {"input_tokens": 1000, "cached_input_tokens": 600, "output_tokens": 50}}
BETA_ACK = '{"type":"item.completed","text":"Beta enabled. Type /beta again to turn it off."}\n'
# One TURN of gpt-5.6-sol at catalog list price: 400 fresh + 600 cached-read input, 50 output.
SOL_TURN_LIST_USD = (
    load_price_table()
    .models["gpt-5.6-sol"]
    .cost_usd(input_tokens=400, cache_read_tokens=600, cache_write_tokens=0, output_tokens=50)
)


def _analytics_row(session: str, model: str, cost: float, provider: str = "openai") -> dict[str, object]:
    return {
        "id": hash((session, model, cost)) & 0xFFFF,
        "session_id": session,
        "requested_at": T0.isoformat(),
        "decision_model": model,
        "decision_provider": provider,
        "actual_input_cost_usd": cost,
        "actual_output_cost_usd": 0.0,
        "input_tokens": 1000,
        "cache_read_tokens": 600,
        "cache_creation_tokens": 0,
        "output_tokens": 50,
    }


@pytest.fixture
def two_arm_job(tmp_path: Path, write_trial: TrialWriter) -> dict[str, Path]:
    """router beats sol on t1, ties on t2, loses on t3; sol errors on t4 where router passes."""
    router_dir, sol_dir = tmp_path / "run--router", tmp_path / "run--sol"
    outcomes = {"t1": (1.0, 0.0), "t2": (1.0, 1.0), "t3": (0.0, 1.0)}
    for offset, (task, (router_reward, sol_reward)) in enumerate(outcomes.items()):
        started = T0 + timedelta(minutes=offset)
        write_trial(
            router_dir,
            task=task,
            trial_name=f"{task}__r1",
            reward=router_reward,
            started_at=started,
            session_id=f"router-{task}",
            beta_output=BETA_ACK,
            codex_events=(TURN,),
            agent_seconds=100 + offset,
        )
        write_trial(
            sol_dir,
            task=task,
            trial_name=f"{task}__s1",
            reward=sol_reward,
            started_at=started,
            session_id=f"sol-{task}",
            codex_events=(TURN,),
            agent_cost_usd=2.0,
            agent_seconds=200,
        )
    write_trial(router_dir, task="t4", trial_name="t4__r1", reward=1.0, session_id="router-t4", beta_output="nope")
    write_trial(
        sol_dir,
        task="t4",
        trial_name="t4__s1",
        reward=None,
        exception={"exception_type": "EnvironmentSetupError", "exception_message": "image build failed"},
    )
    return {"router": router_dir, "sol": sol_dir}


def _report(two_arm_job: dict[str, Path], *, analytics_rows: list[dict[str, object]] | None, tap_rows=()):
    trials_by_arm = {ARMS[name]: load_job(ARMS[name], path) for name, path in two_arm_job.items()}
    return build_report(
        benchmark="terminal-bench-4",
        run_id="run",
        trials_by_arm=trials_by_arm,
        k=1,
        analytics_rows=analytics_rows,
        tap_rows=list(tap_rows),
    )


def test_pairing_statistics_and_error_categories(two_arm_job: dict[str, Path]) -> None:
    report = _report(two_arm_job, analytics_rows=None)
    router, sol = report.arms
    assert (router.n_tasks, router.n_passed, sol.n_passed, sol.n_errored) == (4, 3, 2, 1)
    assert router.trial_pass_rate.point == pytest.approx(0.75)
    assert sol.error_categories == {"harbor-environment-setup-failure": 1}
    assert router.n_beta_acknowledged == 3
    assert sol.n_beta_acknowledged is None
    assert router.agent_seconds_median == 101.0
    assert router.codex_client_tokens.input_tokens == 1200  # Codex's input_tokens include the cached share
    assert router.codex_client_tokens.cache_read_tokens == 1800
    # Direct arms are repriced from their own transcript, not Harbor's LiteLLM figure (2.0/trial).
    assert sol.agent_cost_usd == pytest.approx(3 * SOL_TURN_LIST_USD)
    assert router.agent_cost_usd is None

    (pair,) = report.pairs
    assert (pair.router_arm, pair.control_arm) == ("router", "sol")
    assert pair.n_common_tasks == 4
    assert pair.delta_task_mean.point == pytest.approx(0.25)
    assert (pair.wins_ties_losses.wins, pair.wins_ties_losses.ties, pair.wins_ties_losses.losses) == (2, 1, 1)
    assert (pair.mcnemar.a_only, pair.mcnemar.b_only) == (2, 1)
    assert pair.cost_ratio is None


def test_router_billed_costs_stay_blank_without_analytics(two_arm_job: dict[str, Path]) -> None:
    router = _report(two_arm_job, analytics_rows=None).arms[0]
    assert router.router_billed_usd is None
    assert router.router_list_price_usd is None
    assert router.router_served_models == {}
    assert router.sessions_without_router_rows == []


def test_analytics_rows_join_on_codex_session(two_arm_job: dict[str, Path]) -> None:
    rows = [
        _analytics_row("router-t1", "gpt-5.6-sol", 0.30),
        _analytics_row("router-t1", "claude-haiku-4-5", 0.01, provider="anthropic"),
        _analytics_row("router-t2", "gpt-5.6-sol", 0.20),
        _analytics_row("sol-t1", "gpt-5.6-sol", 9.99),
    ]
    report = _report(two_arm_job, analytics_rows=rows)
    router, sol = report.arms
    assert router.router_billed_usd == pytest.approx(0.51)
    assert router.router_served_models == {"gpt-5.6-sol": 2, "claude-haiku-4-5": 1}
    assert sorted(router.sessions_without_router_rows) == ["t3__r1", "t4__r1"]
    assert router.router_tokens is not None and router.router_tokens.requests == 3
    # OpenAI rows fold the 600 cached tokens into input_tokens (400 fresh each); Anthropic's are fresh already.
    assert router.router_tokens.input_tokens == 400 + 400 + 1000
    prices = load_price_table()
    haiku = prices.models["claude-haiku-4-5"].cost_usd(
        input_tokens=1000, cache_read_tokens=600, cache_write_tokens=0, output_tokens=50
    )
    assert router.router_list_price_usd == pytest.approx(2 * SOL_TURN_LIST_USD + haiku)
    assert sol.router_billed_usd is None
    (pair,) = report.pairs
    assert pair.cost_ratio == pytest.approx(0.51 / (3 * SOL_TURN_LIST_USD))


def test_anthropic_rows_report_fresh_input_tokens_as_is(two_arm_job: dict[str, Path]) -> None:
    rows = [_analytics_row("router-t1", "claude-haiku-4-5", 0.01, provider="anthropic")]
    router = _report(two_arm_job, analytics_rows=rows).arms[0]
    assert router.router_tokens is not None and router.router_tokens.input_tokens == 1000
    haiku = load_price_table().models["claude-haiku-4-5"]
    assert router.router_list_price_usd == pytest.approx(
        haiku.cost_usd(input_tokens=1000, cache_read_tokens=600, cache_write_tokens=0, output_tokens=50)
    )


def test_direct_arm_spend_survives_missing_harbor_cost(tmp_path: Path, write_trial: TrialWriter) -> None:
    sol_dir = tmp_path / "run--sol"
    write_trial(sol_dir, task="t1", trial_name="t1__s1", reward=1.0, codex_events=(TURN, TURN), agent_cost_usd=None)
    trials = load_job(ARMS["sol"], sol_dir)
    assert trials[0].agent_cost_usd is None
    report = build_report(
        benchmark="terminal-bench-4",
        run_id="run",
        trials_by_arm={ARMS["sol"]: trials},
        k=1,
        analytics_rows=None,
        tap_rows=[],
    )
    assert report.arms[0].agent_cost_usd == pytest.approx(2 * SOL_TURN_LIST_USD)


def test_tap_rows_attach_to_openrouter_arm(tmp_path: Path, write_trial: TrialWriter) -> None:
    job = tmp_path / "run--openrouter-beta"
    write_trial(job, task="t1", trial_name="t1__o1", reward=1.0, session_id="thread-1")
    records = tmp_path / "tap.jsonl"
    records.write_text(
        "\n".join(
            json.dumps(row)
            for row in (
                {"session_id": "thread-1", "served_model": "anthropic/claude-sonnet-4.5", "cost_usd": 0.4},
                {"session_id": "thread-1", "served_model": "openai/gpt-5.6", "cost_usd": 0.1},
                {"session_id": "someone-else", "served_model": "x", "cost_usd": 100.0},
            )
        )
    )
    arm = ARMS["openrouter-beta"]
    report = build_report(
        benchmark="terminal-bench-4",
        run_id="run",
        trials_by_arm={arm: load_job(arm, job)},
        k=1,
        analytics_rows=None,
        tap_rows=load_tap_rows(records),
    )
    (summary,) = report.arms
    assert summary.tap_requests == 2
    assert summary.tap_cost_usd == pytest.approx(0.5)
    assert summary.tap_served_models == {"anthropic/claude-sonnet-4.5": 1, "openai/gpt-5.6": 1}
    assert report.pairs == []
    assert load_tap_rows(None) == [] and load_tap_rows(tmp_path / "missing.jsonl") == []


def test_markdown_and_json_render_every_arm_and_pair(two_arm_job: dict[str, Path]) -> None:
    report = _report(two_arm_job, analytics_rows=None)
    markdown = render_markdown(report)
    assert "| `router` |" in markdown and "| `sol` |" in markdown
    assert "harbor-environment-setup-failure" in markdown
    assert "75.0%" in markdown
    assert "router-billed" not in markdown  # no analytics: nothing is presented as router-billed
    parsed = json.loads(report.to_json())
    assert [arm["arm"] for arm in parsed["arms"]] == ["router", "sol"]
    assert parsed["pairs"][0]["wins_ties_losses"] == {"wins": 2, "ties": 1, "losses": 1}
