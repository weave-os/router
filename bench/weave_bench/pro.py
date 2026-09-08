"""SWE-Bench Pro official grading: patch JSON, grader invocation, verdict join.

One official run per arm:
``eval_results.json`` is keyed by ``instance_id`` alone, so arms sharing a run
would overwrite each other's verdicts.
"""

from __future__ import annotations

import json
import subprocess
import sys
from dataclasses import dataclass
from enum import StrEnum
from pathlib import Path
from typing import TypedDict

from weave_bench.agent.pro_codex import MODEL_PATCH_FILENAME
from weave_bench.benchmarks import PRO_GRADER_SCRIPT
from weave_bench.fetch import verify_pro_grader
from weave_bench.manifests import pro_instance_id
from weave_bench.trials import TrialRecord

PRO_GRADER_SCRIPTS_DIR = "run_scripts"
PRO_GRADER_RESULTS = "eval_results.json"


class OfficialPrediction(TypedDict):
    """One entry of the patch JSON ``swe_bench_pro_eval.py`` consumes."""

    instance_id: str
    patch: str
    prefix: str


class VerdictAgreement(StrEnum):
    AGREE = "agree"
    OFFICIAL_ONLY = "official_only"
    HARBOR_ONLY = "harbor_only"
    NOT_GRADED = "not_graded"


@dataclass(frozen=True)
class OfficialVerdict:
    arm: str
    instance_id: str
    attempt: int
    harbor_resolved: bool
    official_resolved: bool | None

    @property
    def agreement(self) -> VerdictAgreement:
        if self.official_resolved is None:
            return VerdictAgreement.NOT_GRADED
        if self.official_resolved == self.harbor_resolved:
            return VerdictAgreement.AGREE
        return VerdictAgreement.OFFICIAL_ONLY if self.official_resolved else VerdictAgreement.HARBOR_ONLY


def prediction_prefix(arm: str, attempt: int) -> str:
    return f"{arm}__attempt{attempt}"


def predictions_from_trials(trials: list[TrialRecord], job_dir: Path) -> dict[str, list[OfficialPrediction]]:
    """Group exported patches into one official patch list per (arm, attempt).

    Trials without a non-empty ``model.patch`` are omitted: the grader can only
    grade patches, and they count as unresolved when verdicts are compared.
    """
    by_prefix: dict[str, list[OfficialPrediction]] = {}
    for trial in trials:
        patch_path = job_dir / trial.trial_name / "agent" / MODEL_PATCH_FILENAME
        if not patch_path.exists():
            continue
        patch = patch_path.read_text(errors="replace")
        if not patch.strip():
            continue
        prefix = prediction_prefix(trial.arm.name, trial.attempt)
        by_prefix.setdefault(prefix, []).append(
            OfficialPrediction(instance_id=pro_instance_id(trial.task), patch=patch, prefix=prefix)
        )
    return by_prefix


def write_predictions(predictions_dir: Path, by_prefix: dict[str, list[OfficialPrediction]]) -> dict[str, Path]:
    predictions_dir.mkdir(parents=True, exist_ok=True)
    paths: dict[str, Path] = {}
    for prefix, predictions in by_prefix.items():
        path = predictions_dir / f"{prefix}.json"
        path.write_text(json.dumps(predictions, indent=2))
        paths[prefix] = path
    return paths


def grader_command(
    *,
    raw_sample_path: Path,
    patch_path: Path,
    output_dir: Path,
    dockerhub_username: str,
    num_workers: int,
    docker_platform: str | None,
) -> list[str]:
    """The exact ``swe_bench_pro_eval.py`` invocation; cwd must be the grader
    clone because it reads ``dockerfiles/`` and ``run_scripts/`` relative to it,
    so our own paths are made absolute to survive that cwd."""
    command = [
        sys.executable,
        PRO_GRADER_SCRIPT,
        f"--raw_sample_path={raw_sample_path.resolve()}",
        f"--patch_path={patch_path.resolve()}",
        f"--output_dir={output_dir.resolve()}",
        f"--scripts_dir={PRO_GRADER_SCRIPTS_DIR}",
        f"--dockerhub_username={dockerhub_username}",
        f"--num_workers={num_workers}",
        "--use_local_docker",
    ]
    if docker_platform:
        command.append(f"--docker_platform={docker_platform}")
    return command


def grade_officially(
    *,
    grader_dir: Path,
    raw_sample_path: Path,
    patch_paths: dict[str, Path],
    out_dir: Path,
    dockerhub_username: str,
    num_workers: int,
    docker_platform: str | None,
    dry_run: bool,
) -> dict[str, dict[str, bool]]:
    verify_pro_grader(grader_dir)
    verdicts_by_prefix: dict[str, dict[str, bool]] = {}
    for prefix, patch_path in sorted(patch_paths.items()):
        output_dir = out_dir / prefix
        command = grader_command(
            raw_sample_path=raw_sample_path,
            patch_path=patch_path,
            output_dir=output_dir,
            dockerhub_username=dockerhub_username,
            num_workers=num_workers,
            docker_platform=docker_platform,
        )
        print(f"[official-grader] {prefix}: (cd {grader_dir} && {' '.join(command)})", flush=True)
        if dry_run:
            continue
        subprocess.run(command, cwd=grader_dir, check=True)
        verdicts_by_prefix[prefix] = json.loads((output_dir / PRO_GRADER_RESULTS).read_text())
    return verdicts_by_prefix


def compare_verdicts(
    trials: list[TrialRecord], official_by_prefix: dict[str, dict[str, bool]]
) -> list[OfficialVerdict]:
    verdicts = [
        OfficialVerdict(
            arm=trial.arm.name,
            instance_id=pro_instance_id(trial.task),
            attempt=trial.attempt,
            harbor_resolved=trial.passed,
            official_resolved=official_by_prefix.get(prediction_prefix(trial.arm.name, trial.attempt), {}).get(
                pro_instance_id(trial.task)
            ),
        )
        for trial in trials
    ]
    return sorted(verdicts, key=lambda v: (v.arm, v.attempt, v.instance_id))
