"""Everything that determines a published number, pinned per benchmark.

Task lists live in ``weave_bench/manifests/`` (exact ids the published runs
used); dataset/grader revisions, Harbor and Codex versions, attempt counts and
concurrency are here. Change any of these and you are running a different
experiment — record it in the run id.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum


class Benchmark(StrEnum):
    ATLAS_QNA = "atlas-qna"
    TERMINAL_BENCH_4 = "terminal-bench-4"
    SWE_BENCH_PRO = "swe-bench-pro"


HARBOR_VERSION = "0.22.0"


@dataclass(frozen=True)
class BenchmarkPins:
    benchmark: Benchmark
    # Harbor release the published numbers were produced with. This package
    # drives every benchmark with HARBOR_VERSION; Atlas ran on 0.18.0 plus a
    # private patch that added what BetaCodex + the `config` agent kwarg do now.
    published_harbor_version: str
    codex_version: str
    # Harbor ``--dataset name@version`` (registry) or None for a local ``--path``.
    harbor_dataset: str | None
    manifest_filename: str
    n_tasks: int
    n_attempts: int
    n_concurrent: int
    smoke_tasks: tuple[str, ...]


ATLAS_REPO = "https://github.com/scaleapi/SWE-Atlas.git"
ATLAS_COMMIT = "49e4af3b6c803dd54a1cd60ead703aac25de4e21"
ATLAS_QA_SUBDIR = "data/qa"
# Rubric judge the tasks' ``[verifier.env]`` defaults to; any OpenAI-compatible
# endpoint serving it works. Prompt/rubrics ship inside each task's ``tests/``.
ATLAS_JUDGE_MODEL = "anthropic/claude-opus-4-5-20251101"
ATLAS_JUDGE_BASE_URL = "https://api.anthropic.com/v1"

TB4_CONTENT_SHA256 = "39d9f44b40420cde8fdcc087579c0d72a7e14fa3656d603c3f0d22fb35e27732"

PRO_HF_DATASET = "ScaleAI/SWE-bench_Pro"
PRO_HF_SPLIT = "test"
PRO_HF_REVISION = "7ab5114912baf22bb098818e604c02fe7ad2c11f"
PRO_SUBSET_SEED = 20260904
PRO_GRADER_REPO = "https://github.com/scaleapi/SWE-bench_Pro-os.git"
PRO_GRADER_REVISION = "ca10a60a5fcae51e6948ffe1485d4153d421e6c5"
PRO_GRADER_SCRIPT = "swe_bench_pro_eval.py"
PRO_GRADER_SHA256 = "bb5d4c5486be296e464e695df3747064aaa3bb197394bc6d39980634afec2034"

PINS: dict[Benchmark, BenchmarkPins] = {
    Benchmark.ATLAS_QNA: BenchmarkPins(
        Benchmark.ATLAS_QNA,
        published_harbor_version="0.18.0",
        codex_version="0.153.0",
        harbor_dataset=None,
        manifest_filename="sweatlas_qna_tasks.json",
        n_tasks=124,
        n_attempts=2,
        n_concurrent=24,
        smoke_tasks=("task-6905333b74f22949d97ba998",),
    ),
    Benchmark.TERMINAL_BENCH_4: BenchmarkPins(
        Benchmark.TERMINAL_BENCH_4,
        published_harbor_version=HARBOR_VERSION,
        codex_version="0.150.0",
        harbor_dataset="terminal-bench/terminal-bench@4.0.0",
        manifest_filename="terminalbench4_tasks.json",
        n_tasks=66,
        # The tbench.ai leaderboard protocol is k=5; the published comparison
        # ran k=2 for budget.
        n_attempts=2,
        n_concurrent=12,
        smoke_tasks=("cad-model",),
    ),
    Benchmark.SWE_BENCH_PRO: BenchmarkPins(
        Benchmark.SWE_BENCH_PRO,
        published_harbor_version=HARBOR_VERSION,
        codex_version="0.150.0",
        harbor_dataset="swebenchpro@1.0",
        manifest_filename="swebench_pro_subset.json",
        n_tasks=300,
        n_attempts=2,
        n_concurrent=12,
        smoke_tasks=(),
    ),
}
