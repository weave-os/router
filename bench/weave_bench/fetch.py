"""Clone the official task sources at their pinned revisions."""

from __future__ import annotations

import subprocess
from pathlib import Path

from weave_bench.benchmarks import ATLAS_COMMIT, ATLAS_QA_SUBDIR, ATLAS_REPO


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
