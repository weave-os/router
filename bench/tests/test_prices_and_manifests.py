from __future__ import annotations

import pytest

from weave_bench.benchmarks import PINS, TB4_CONTENT_SHA256, Benchmark
from weave_bench.manifests import harbor_task_names, load_manifest, smoke_task_names
from weave_bench.prices import ModelPrice, load_price_table


def test_price_table_is_generated_from_the_router_catalog() -> None:
    table = load_price_table()
    assert table.pricing_version.startswith("catalog-sha256:")
    sol = table.lookup("gpt-5.6-sol")
    assert sol is not None
    assert table.lookup("gpt-5.6-sol-20260901") is sol
    assert table.lookup("gpt-5.6-sol-2026") is None
    assert table.lookup("not-a-model") is None


def test_cost_mirrors_catalog_cost_math() -> None:
    price = ModelPrice(
        input_usd_per_million=2.0, output_usd_per_million=8.0, cache_read_multiplier=0.1, cache_write_multiplier=1.25
    )
    cost = price.cost_usd(
        input_tokens=1_000_000, cache_read_tokens=1_000_000, cache_write_tokens=1_000_000, output_tokens=500_000
    )
    assert cost == pytest.approx(2.0 + 0.2 + 2.5 + 4.0)


@pytest.mark.parametrize(
    ("benchmark", "prefix"),
    [
        (Benchmark.ATLAS_QNA, "task-"),
        (Benchmark.TERMINAL_BENCH_4, "terminal-bench/"),
    ],
)
def test_manifests_match_the_pinned_population(benchmark: Benchmark, prefix: str) -> None:
    names = harbor_task_names(benchmark)
    assert len(names) == len(set(names)) == PINS[benchmark].n_tasks
    assert all(name.startswith(prefix) for name in names)
    smoke = smoke_task_names(benchmark)
    assert 1 <= len(smoke) <= 3
    assert set(smoke) <= set(names)


def test_terminal_bench_manifest_pins_content_hash() -> None:
    manifest = load_manifest(Benchmark.TERMINAL_BENCH_4)
    assert manifest["content_sha256"] == TB4_CONTENT_SHA256
    assert all(task["ref"].startswith("sha256:") for task in manifest["tasks"])
    assert manifest["harbor_dataset"] == PINS[Benchmark.TERMINAL_BENCH_4].harbor_dataset


def test_atlas_manifest_pins_the_commit() -> None:
    manifest = load_manifest(Benchmark.ATLAS_QNA)
    assert len(manifest["commit"]) == 40
    assert manifest["path"] == "data/qa"
