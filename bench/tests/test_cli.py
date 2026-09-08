from __future__ import annotations

import json
import shlex
from datetime import datetime, timedelta
from pathlib import Path

import pytest

from tests.conftest import T0, TrialWriter
from weave_bench.cli import main


def test_run_dry_run_prints_the_harbor_commands_without_launching(
    tmp_path: Path, capsys: pytest.CaptureFixture[str], monkeypatch: pytest.MonkeyPatch
) -> None:
    monkeypatch.chdir(tmp_path)
    exit_code = main(
        ["run", "atlas-qna", "--arms", "router,luna,openrouter-beta", "--smoke", "--run-id", "dry", "--dry-run"]
    )
    out = capsys.readouterr().out
    assert exit_code == 0
    assert "# atlas-qna run_id=dry" in out
    assert "tasks=1 (manifest has 124)" in out
    assert "## arm router (router)" in out and "## arm luna (direct)" in out
    assert "## arm openrouter-beta (openrouter)" in out
    assert "requires `weave-bench tap serve`" in out
    assert "--include-task-name task-6905333b74f22949d97ba998" in out
    assert "--job-name dry--router" in out
    assert not (tmp_path / "jobs").exists()
    for line in out.splitlines():
        if line.startswith("harbor run"):
            assert shlex.split(line)[:2] == ["harbor", "run"]


def test_run_dry_run_honours_bench_toml_and_explicit_tasks(
    tmp_path: Path, capsys: pytest.CaptureFixture[str], monkeypatch: pytest.MonkeyPatch
) -> None:
    monkeypatch.chdir(tmp_path)
    (tmp_path / "bench.toml").write_text('[router]\nbase_url = "https://my-router.test"\n[harbor]\njobs_dir = "out"\n')
    assert (
        main(
            [
                "run",
                "terminal-bench-4",
                "--tasks",
                "terminal-bench/cad-model,terminal-bench/bun-sourcemap-leak",
                "--dry-run",
            ]
        )
        == 0
    )
    out = capsys.readouterr().out
    assert "tasks=2 (manifest has 66)" in out
    assert "--include-task-name terminal-bench/cad-model --include-task-name terminal-bench/bun-sourcemap-leak" in out
    assert "--allow-agent-host my-router.test" in out
    assert 'base_url = "https://my-router.test/v1"' in out
    assert "--jobs-dir out" in out


def test_run_report_hint_names_the_arms_that_ran(
    tmp_path: Path, capsys: pytest.CaptureFixture[str], monkeypatch: pytest.MonkeyPatch
) -> None:
    monkeypatch.chdir(tmp_path)
    monkeypatch.setattr("weave_bench.cli.run_job", lambda job, config: 0)
    assert main(["run", "terminal-bench-4", "--arms", "router,luna", "--smoke", "--run-id", "r1"]) == 0
    assert "weave-bench report terminal-bench-4 r1 --arms router,luna" in capsys.readouterr().out


def test_report_without_analytics_writes_markdown_and_json(
    tmp_path: Path, write_trial: TrialWriter, capsys: pytest.CaptureFixture[str], monkeypatch: pytest.MonkeyPatch
) -> None:
    monkeypatch.chdir(tmp_path)
    jobs = tmp_path / "jobs"
    write_trial(jobs / "r1--router", task="terminal-bench/cad-model", trial_name="cad__a", reward=1.0)
    write_trial(jobs / "r1--sol", task="terminal-bench/cad-model", trial_name="cad__b", reward=0.0)
    assert main(["report", "terminal-bench-4", "r1", "--no-analytics"]) == 0
    out = capsys.readouterr().out
    assert "| `router` |" in out
    report = json.loads((jobs / "reports" / "r1" / "report.json").read_text())
    assert report["run_id"] == "r1"
    assert [arm["arm"] for arm in report["arms"]] == ["router", "sol"]
    assert report["arms"][0]["router_billed_usd"] is None
    assert (jobs / "reports" / "r1" / "report.md").exists()


def test_report_replays_a_saved_analytics_export(
    tmp_path: Path, write_trial: TrialWriter, capsys: pytest.CaptureFixture[str], monkeypatch: pytest.MonkeyPatch
) -> None:
    monkeypatch.chdir(tmp_path)
    jobs = tmp_path / "jobs"
    write_trial(jobs / "r1--router", task="t", trial_name="t__a", reward=1.0, session_id="sess")
    write_trial(jobs / "r1--sol", task="t", trial_name="t__b", reward=1.0)
    export = tmp_path / "saved.ndjson"
    export.write_text(
        json.dumps(
            {
                "id": 1,
                "session_id": "sess",
                "decision_model": "gpt-5.6-sol",
                "actual_input_cost_usd": 0.4,
                "actual_output_cost_usd": 0.1,
                "input_tokens": 10,
                "cache_read_input_tokens": 0,
                "cache_creation_input_tokens": 0,
                "output_tokens": 5,
            }
        )
        + "\n"
    )
    assert main(["report", "terminal-bench-4", "r1", "--analytics-ndjson", str(export)]) == 0
    capsys.readouterr()
    report = json.loads((jobs / "reports" / "r1" / "report.json").read_text())
    assert report["arms"][0]["router_billed_usd"] == pytest.approx(0.5)
    assert report["arms"][0]["router_served_models"] == {"gpt-5.6-sol": 1}


def test_report_refetches_analytics_when_the_run_grows(
    tmp_path: Path, write_trial: TrialWriter, capsys: pytest.CaptureFixture[str], monkeypatch: pytest.MonkeyPatch
) -> None:
    monkeypatch.chdir(tmp_path)
    monkeypatch.setenv("WEAVE_ANALYTICS_KEY", "ra_test")
    jobs = tmp_path / "jobs"
    write_trial(jobs / "r1--router", task="t1", trial_name="t1__a", reward=1.0, session_id="s1")
    write_trial(jobs / "r1--sol", task="t1", trial_name="t1__b", reward=1.0)
    windows: list[tuple[datetime, datetime]] = []

    def fake_fetch(
        *, base_url: str, api_key: str, window_start: datetime, window_end: datetime
    ) -> list[dict[str, object]]:
        windows.append((window_start, window_end))
        return [
            {
                "id": len(windows),
                "session_id": f"s{len(windows)}",
                "decision_model": "gpt-5.6-sol",
                "decision_provider": "openai",
                "actual_input_cost_usd": 1.0,
                "actual_output_cost_usd": 0.0,
            }
        ]

    monkeypatch.setattr("weave_bench.cli.fetch_rows", fake_fetch)
    assert main(["report", "terminal-bench-4", "r1"]) == 0
    assert main(["report", "terminal-bench-4", "r1"]) == 0
    assert len(windows) == 1  # same trial span: the cached export is replayed

    write_trial(
        jobs / "r1--router",
        task="t2",
        trial_name="t2__a",
        reward=1.0,
        session_id="s2",
        started_at=T0 + timedelta(hours=2),
    )
    assert main(["report", "terminal-bench-4", "r1"]) == 0
    capsys.readouterr()
    assert len(windows) == 2 and windows[1][1] > windows[0][1]
    report = json.loads((jobs / "reports" / "r1" / "report.json").read_text())
    assert report["arms"][0]["sessions_without_router_rows"] == ["t1__a"]
    assert report["arms"][0]["router_billed_usd"] == pytest.approx(1.0)
    assert len(list((jobs / "reports" / "r1").glob("analytics-*.ndjson"))) == 2


@pytest.mark.parametrize("run_id", ["../escape", 'x" evil', "has space", ".hidden", ""])
def test_run_ids_are_restricted_to_path_and_header_safe_names(run_id: str, capsys: pytest.CaptureFixture[str]) -> None:
    with pytest.raises(SystemExit):
        main(["run", "terminal-bench-4", "--dry-run", "--run-id", run_id])
    assert "run id" in capsys.readouterr().err
    with pytest.raises(SystemExit):
        main(["report", "terminal-bench-4", run_id, "--no-analytics"])


def test_run_rejects_tasks_outside_the_pinned_manifest(tmp_path: Path, capsys: pytest.CaptureFixture[str]) -> None:
    assert main(["run", "terminal-bench-4", "--tasks", "terminal-bench/not-a-task", "--dry-run"]) == 2
    captured = capsys.readouterr()
    assert "not-a-task" in captured.err
    assert "harbor run" not in captured.out


def test_report_of_missing_run_fails_loudly(
    tmp_path: Path, capsys: pytest.CaptureFixture[str], monkeypatch: pytest.MonkeyPatch
) -> None:
    monkeypatch.chdir(tmp_path)
    assert main(["report", "terminal-bench-4", "r-missing", "--no-analytics"]) == 2
    assert "r-missing--router" in capsys.readouterr().err
