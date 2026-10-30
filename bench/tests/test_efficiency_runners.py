from __future__ import annotations

import json
from datetime import UTC, datetime, timedelta
from pathlib import Path

import httpx
import pytest
from starlette.testclient import TestClient

from weave_bench.config import load_config
from weave_bench.efficiency import pin_proxy, swe_together, terminal_bench
from weave_bench.efficiency.cells import Cell, Harness
from weave_bench.efficiency.estimates import Violation
from weave_bench.prices import load_price_table

CONFIG = load_config(None, {"WEAVE_BENCH_ROUTER_BASE_URL": "https://router.example.com"})


def test_pin_path_round_trips_models_with_slashes() -> None:
    base = pin_proxy.pin_base_url("http://proxy:1/", rollout_id="trial-1", force_model="z-ai/glm-5.3")
    assert base == "http://proxy:1/pin/trial-1/z-ai~glm-5.3"
    pinned = pin_proxy.parse_pin_path(base.removeprefix("http://proxy:1") + "/v1/messages")
    assert pinned == pin_proxy.PinnedRequest("trial-1", "z-ai/glm-5.3", "/v1/messages")
    unpinned = pin_proxy.parse_pin_path("/pin/trial-2/-/v1/responses")
    assert unpinned is not None and unpinned.force_model is None
    assert pin_proxy.parse_pin_path("/v1/messages") is None


def test_pin_proxy_stamps_headers_and_overrides_client_supplied_ones() -> None:
    seen: list[httpx.Request] = []

    def router(request: httpx.Request) -> httpx.Response:
        seen.append(request)
        return httpx.Response(200, json={"ok": True})

    upstream = httpx.AsyncClient(base_url="https://router.example.com", transport=httpx.MockTransport(router))
    client = TestClient(pin_proxy.build_app(upstream))
    response = client.post(
        "/pin/trial-1/gpt-6-luna/v1/messages?beta=true",
        json={"model": "claude-opus-5-5"},
        headers={"x-api-key": "rk_test", "x-weave-force-model": "something-else"},
    )
    assert response.status_code == 200 and response.json() == {"ok": True}
    (request,) = seen
    assert request.url.path == "/v1/messages" and request.url.params["beta"] == "true"
    assert request.headers["x-weave-force-model"] == "gpt-6-luna"
    assert request.headers["x-weave-rollout-id"] == "trial-1"
    assert request.headers["x-api-key"] == "rk_test"
    assert json.loads(request.content) == {"model": "claude-opus-5-5"}


def test_swe_together_trial_points_its_harness_at_the_pinned_proxy_path(tmp_path: Path) -> None:
    trial = swe_together.Trial("eff-1", Cell(Harness.CODEX, "claude-opus-5-5"), "hyperswitch-8084", 2)
    command = swe_together.trial_command(
        trial,
        checkout=tmp_path,
        python="python",
        proxy_url="http://172.17.0.1:8788",
        trials_root=tmp_path,
        env_type="docker",
    )
    assert trial.trial_id == "eff-1-cx-claude-opus-5-5-hyperswitch-8084-a2"
    assert command.env["OPENAI_BASE_URL"] == f"http://172.17.0.1:8788/pin/{trial.trial_id}/claude-opus-5-5/v1"
    assert command.api_key_env_name == "OPENAI_API_KEY"
    argv = list(command.argv)
    assert argv[argv.index("--model") + 1] == "openai/gpt-6-astra"
    assert argv[argv.index("--tasks") + 1] == "hyperswitch-8084"


def test_versions_patch_ships_with_the_package() -> None:
    assert "SWE_TOGETHER_CLAUDE_CODE_VERSION" in swe_together.VERSIONS_PATCH.read_text()


T0 = datetime(2026, 10, 9, 12, tzinfo=UTC)


def decision(rollout_id: str, model: str, *, input_tokens: int = 100, output_tokens: int = 10) -> dict[str, object]:
    return {
        "rollout_id": rollout_id,
        "decision_model": model,
        "decision_provider": "openai",
        "upstream_status_code": 200,
        "turn_type": "main_loop",
        "input_tokens": input_tokens,
        "output_tokens": output_tokens,
        "cache_creation_tokens": 0,
        "cache_read_tokens": 0,
    }


def test_collect_joins_rows_by_rollout_id_and_flags_unpinned_trials(tmp_path: Path) -> None:
    ledger = tmp_path / swe_together.LEDGER_FILENAME
    for attempt in (1, 2):
        entry = swe_together.LedgerEntry(
            f"eff-cx-luna-t-a{attempt}", "eff", "codex", "gpt-6-luna", "t", attempt,
            T0.isoformat(), (T0 + timedelta(minutes=30)).isoformat(), 0,
        )  # fmt: skip
        ledger.open("a").write(json.dumps(entry.__dict__) + "\n")
    rows = [
        decision("eff-cx-luna-t-a1", "gpt-6-luna", input_tokens=300),
        decision("eff-cx-luna-t-a2", "gpt-6-sol"),
        decision("someone-else", "gpt-6-luna", input_tokens=10_000),
    ]
    windows: list[tuple[datetime, datetime]] = []

    def fetch(start: datetime, end: datetime) -> list[dict[str, object]]:
        windows.append((start, end))
        return rows

    usages = swe_together.collect(tmp_path, fetch)
    assert windows == [(T0, T0 + timedelta(minutes=30))]
    by_id = {usage.trial_id: usage for usage in usages}
    assert by_id["eff-cx-luna-t-a1"].usage.fresh_input == 300 and by_id["eff-cx-luna-t-a1"].measures_pinned_model
    assert by_id["eff-cx-luna-t-a2"].violations == (Violation.WRONG_MODEL,)
    assert swe_together.read_usage(tmp_path / swe_together.USAGE_FILENAME) == usages
    measured = swe_together.measured_trials(usages)
    assert (
        list(measured) == [Cell(Harness.CODEX, "gpt-6-luna")]
        and len(measured[Cell(Harness.CODEX, "gpt-6-luna")]["t"]) == 1
    )


def test_claude_code_quality_arm_carries_the_pin_in_its_settings_layer() -> None:
    job = terminal_bench.build_quality_job(
        terminal_bench.ARMS["cc-opus-5-5"], run_id="tb4eff-1", config=CONFIG, task_names=["payments-pipeline-fix"]
    )
    argv = list(job.argv)
    assert argv[argv.index("--agent") + 1] == "claude-code"
    assert "ANTHROPIC_BASE_URL=https://router.example.com" in argv
    assert "terminal-bench/payments-pipeline-fix" in argv
    headers = json.loads(job.config_text)["env"]["ANTHROPIC_CUSTOM_HEADERS"].splitlines()
    assert headers == ["x-weave-rollout-id: tb4eff-1--cc-opus-5-5", "x-weave-force-model: claude-opus-5-5"]


def test_router_arm_has_no_pin_and_no_beta_turn() -> None:
    job = terminal_bench.build_quality_job(
        terminal_bench.ARMS["cx-router"], run_id="tb4eff-1", config=CONFIG, task_names=["payments-pipeline-fix"]
    )
    assert "x-weave-force-model" not in job.config_text
    assert "beta_toggle=true" not in job.argv
    assert terminal_bench.ARMS["cx-router"].baseline_model == "gpt-6-astra"


def test_unknown_terminal_bench_task_is_refused() -> None:
    with pytest.raises(ValueError):
        terminal_bench.select_tasks(["cad-model"])


def write_harbor_trial(job_dir: Path, name: str, *, reward: float | None, errored: bool, session: str) -> None:
    trial_dir = job_dir / name
    (trial_dir / "agent").mkdir(parents=True)
    (trial_dir / "result.json").write_text(
        json.dumps(
            {
                "task_name": "terminal-bench/payments-pipeline-fix",
                "trial_name": name,
                "started_at": T0.isoformat(),
                "finished_at": (T0 + timedelta(minutes=5)).isoformat(),
                "verifier_result": {"rewards": {"reward": reward}} if reward is not None else None,
                "exception_info": {"exception_type": "AgentTimeoutError"} if errored else None,
            }
        )
    )
    (trial_dir / "agent" / "claude-code.txt").write_text(json.dumps({"type": "system", "session_id": session}) + "\n")


def test_terminal_bench_outcomes_price_rows_at_catalog_and_leave_infrastructure_errors_ungraded(tmp_path: Path) -> None:
    write_harbor_trial(tmp_path, "pass", reward=1.0, errored=False, session="s-pass")
    write_harbor_trial(tmp_path, "infra", reward=None, errored=True, session="s-infra")
    trials = terminal_bench.load_harbor_trials(tmp_path)
    assert {trial.session_id for trial in trials} == {"s-pass", "s-infra"}
    row = decision("tb", "gpt-6-luna", input_tokens=1_000_000, output_tokens=0)
    rows_by_session = {"s-pass": [row], "s-infra": [row, row]}
    outcomes = terminal_bench.arm_outcomes(trials, rows_by_session)["payments-pipeline-fix"]
    luna = load_price_table().lookup("gpt-6-luna")
    assert luna is not None
    by_solved = {outcome.solved: outcome.cost_usd for outcome in outcomes}
    assert by_solved[True] == pytest.approx(luna.input_usd_per_million)
    assert by_solved[None] == pytest.approx(2 * luna.input_usd_per_million)


def test_unpriced_served_model_is_an_error_not_a_zero() -> None:
    with pytest.raises(ValueError, match="no catalog price"):
        terminal_bench.catalog_cost_usd([decision("tb", "not-a-model")])
