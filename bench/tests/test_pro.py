from __future__ import annotations

import json
import sys
from pathlib import Path

import pytest

from tests.conftest import TrialWriter
from weave_bench.arms import ARMS
from weave_bench.benchmarks import Benchmark
from weave_bench.fetch import PRO_GRADER_SHA256, verify_pro_grader
from weave_bench.manifests import harbor_task_names, load_manifest, pro_instance_id
from weave_bench.pro import (
    VerdictAgreement,
    compare_verdicts,
    grade_officially,
    grader_command,
    predictions_from_trials,
    write_predictions,
)
from weave_bench.trials import load_job


def test_pro_manifest_round_trips_official_instance_ids() -> None:
    manifest = load_manifest(Benchmark.SWE_BENCH_PRO)
    names = harbor_task_names(Benchmark.SWE_BENCH_PRO)
    assert len(names) == len(set(names)) == 300
    assert [pro_instance_id(name) for name in names] == manifest["task_ids"]
    assert all(task_id.startswith("instance_") for task_id in manifest["task_ids"])


def test_predictions_group_by_arm_and_attempt_and_skip_empty_patches(tmp_path: Path, write_trial: TrialWriter) -> None:
    first, second = harbor_task_names(Benchmark.SWE_BENCH_PRO)[:2]
    job = tmp_path / "run--sol"
    a1 = write_trial(job, task=first, trial_name="a__1", reward=1.0)
    a2 = write_trial(job, task=first, trial_name="a__2", reward=0.0)
    b1 = write_trial(job, task=second, trial_name="b__1", reward=0.0)
    write_trial(job, task=second, trial_name="b__2", reward=0.0)
    (a1 / "agent" / "model.patch").write_text("diff --git a/x b/x\n+1\n")
    (a2 / "agent" / "model.patch").write_text("diff --git a/x b/x\n+2\n")
    (b1 / "agent" / "model.patch").write_text("   \n")
    trials = load_job(ARMS["sol"], job)
    # a__1/b__1 started at the same instant; chronological order falls back to trial name.
    by_prefix = predictions_from_trials(trials, job)
    assert {prefix: [p["instance_id"] for p in preds] for prefix, preds in by_prefix.items()} == {
        "sol__attempt1": [pro_instance_id(first)],
        "sol__attempt2": [pro_instance_id(first)],
    }
    paths = write_predictions(tmp_path / "preds", by_prefix)
    written = json.loads(paths["sol__attempt1"].read_text())
    assert written == [
        {"instance_id": pro_instance_id(first), "patch": "diff --git a/x b/x\n+1\n", "prefix": "sol__attempt1"}
    ]

    verdicts = compare_verdicts(
        trials,
        {"sol__attempt1": {pro_instance_id(first): False}, "sol__attempt2": {pro_instance_id(first): False}},
    )
    by_key = {(v.instance_id, v.attempt): v for v in verdicts}
    assert by_key[(pro_instance_id(first), 1)].agreement is VerdictAgreement.HARBOR_ONLY
    assert by_key[(pro_instance_id(first), 2)].agreement is VerdictAgreement.AGREE
    assert by_key[(pro_instance_id(second), 1)].agreement is VerdictAgreement.NOT_GRADED


def test_grader_command_matches_official_cli() -> None:
    command = grader_command(
        raw_sample_path=Path("/d/raw.parquet"),
        patch_path=Path("/p/sol__attempt1.json"),
        output_dir=Path("/o/sol__attempt1"),
        dockerhub_username="jefzda",
        num_workers=4,
        docker_platform="linux/amd64",
    )
    assert command[:2] == [sys.executable, "swe_bench_pro_eval.py"]
    assert "--use_local_docker" in command
    assert "--scripts_dir=run_scripts" in command
    assert "--dockerhub_username=jefzda" in command
    assert "--num_workers=4" in command
    assert command[-1] == "--docker_platform=linux/amd64"
    relative = grader_command(
        raw_sample_path=Path("r"),
        patch_path=Path("p"),
        output_dir=Path("o"),
        dockerhub_username="u",
        num_workers=1,
        docker_platform=None,
    )
    assert not any(arg.startswith("--docker_platform") for arg in relative)
    # The grader runs with cwd=grader clone, so bench-relative paths must be absolutized.
    assert f"--patch_path={Path.cwd() / 'p'}" in relative
    assert f"--output_dir={Path.cwd() / 'o'}" in relative


def test_grader_hash_is_verified_before_running(tmp_path: Path) -> None:
    (tmp_path / "swe_bench_pro_eval.py").write_text("print('tampered')\n")
    with pytest.raises(RuntimeError, match=PRO_GRADER_SHA256):
        verify_pro_grader(tmp_path)
    with pytest.raises(RuntimeError):
        grade_officially(
            grader_dir=tmp_path,
            raw_sample_path=tmp_path / "raw",
            patch_paths={},
            out_dir=tmp_path / "out",
            dockerhub_username="u",
            num_workers=1,
            docker_platform=None,
            dry_run=True,
        )
