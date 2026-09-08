"""Pinned SWE-Bench Pro ``test`` split as the official grader's ``--raw_sample_path``.

Needs the ``pro`` extra (``datasets``/``pandas``); everything else in the
package stays importable without it.
"""

from __future__ import annotations

from pathlib import Path

from weave_bench.benchmarks import PRO_HF_DATASET, PRO_HF_REVISION, PRO_HF_SPLIT

try:
    from datasets import load_dataset
except ImportError as exc:
    _MISSING_EXTRA: ImportError | None = exc
else:
    _MISSING_EXTRA = None

RAW_SAMPLES_FILENAME = "swe_bench_pro_test.jsonl"


def fetch_raw_samples(out_dir: Path) -> Path:
    if _MISSING_EXTRA is not None:
        raise RuntimeError("SWE-Bench Pro grading needs `pip install -e 'bench[pro]'`") from _MISSING_EXTRA
    out_dir.mkdir(parents=True, exist_ok=True)
    raw_samples = out_dir / RAW_SAMPLES_FILENAME
    if not raw_samples.exists():
        load_dataset(PRO_HF_DATASET, split=PRO_HF_SPLIT, revision=PRO_HF_REVISION).to_json(str(raw_samples), lines=True)
    return raw_samples
