"""Clone the official task/grader sources at their pinned revisions."""

from __future__ import annotations

import hashlib
import subprocess
from pathlib import Path

from weave_bench.benchmarks import (
    ATLAS_COMMIT,
    ATLAS_QA_SUBDIR,
    ATLAS_REPO,
    PRO_GRADER_REPO,
    PRO_GRADER_REVISION,
    PRO_GRADER_SCRIPT,
    PRO_GRADER_SHA256,
)


def _git(*args: str, cwd: Path | None = None) -> None:
    subprocess.run(["git", *args], cwd=cwd, check=True)


def checkout_pinned(repo_url: str, revision: str, target: Path) -> None:
    if not (target / ".git").exists():
        target.parent.mkdir(parents=True, exist_ok=True)
        _git("clone", "--filter=blob:none", "--no-checkout", repo_url, str(target))
    _git("fetch", "--depth", "1", "origin", revision, cwd=target)
    _git("checkout", "--detach", revision, cwd=target)


def fetch_atlas(checkout_dir: Path) -> Path:
    checkout_pinned(ATLAS_REPO, ATLAS_COMMIT, checkout_dir)
    return checkout_dir / ATLAS_QA_SUBDIR


def sha256_of(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def verify_pro_grader(grader_dir: Path) -> Path:
    script = grader_dir / PRO_GRADER_SCRIPT
    digest = sha256_of(script)
    if digest != PRO_GRADER_SHA256:
        raise RuntimeError(
            f"{script} sha256 {digest} != pinned {PRO_GRADER_SHA256}; expected {PRO_GRADER_REPO}@{PRO_GRADER_REVISION}"
        )
    return script


def fetch_pro_grader(grader_dir: Path) -> Path:
    checkout_pinned(PRO_GRADER_REPO, PRO_GRADER_REVISION, grader_dir)
    return verify_pro_grader(grader_dir)
