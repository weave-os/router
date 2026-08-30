from __future__ import annotations

import os
from pathlib import Path

import pytest

from hmm_sidecar.artifacts import resolve_artifacts, sha256_file
from hmm_sidecar.policy import (
    FrozenPolicy,
    select_roster_group,
    selected_margin,
)


class FixedEmbedder:
    def __init__(self, vector: list[float]) -> None:
        self.vector = vector

    async def embed(self, texts: list[str]) -> list[list[float]]:
        return [self.vector for _ in texts]


def test_roster_ids_dedupes_arms_across_clusters_in_first_seen_order() -> None:
    policy = object.__new__(FrozenPolicy)
    policy.clusters = {
        "fast": {"arms": ["deepseek/deepseek-v4-flash", "openai/gpt-5.4-nano"]},
        "balanced": {"arms": ["openai/gpt-5.6-luna", "deepseek/deepseek-v4-flash"]},
        "empty": {},
    }

    assert policy.roster_ids() == [
        "deepseek/deepseek-v4-flash",
        "openai/gpt-5.4-nano",
        "openai/gpt-5.6-luna",
    ]


def test_selected_margin_is_signed_against_the_best_alternative() -> None:
    probabilities = {"fast": 0.2, "maximum": 0.8}

    assert selected_margin(probabilities, "fast") == pytest.approx(-0.6)
    assert selected_margin(probabilities, "maximum") == pytest.approx(0.6)


def test_group_selection_uses_class_order_ties_and_returns_every_arm() -> None:
    group, arms, fallback = select_roster_group(
        probabilities={"fast": 0.5, "maximum": 0.5},
        classes=("fast", "maximum"),
        clusters={
            "fast": {"arms": ["provider/a", "provider/b"]},
            "maximum": {"arms": ["provider/c"]},
        },
        available_roster_ids={"provider/a", "provider/b", "provider/c"},
    )

    assert group == "fast"
    assert arms == ("provider/a", "provider/b")
    assert tuple(item.group for item in fallback) == ("fast", "maximum")


def test_group_selection_falls_back_and_retains_zero_arm_result() -> None:
    group, arms, fallback = select_roster_group(
        probabilities={"fast": 0.8, "maximum": 0.2},
        classes=("fast", "maximum"),
        clusters={
            "fast": {"arms": ["provider/a"]},
            "maximum": {"arms": ["provider/b", "provider/c"]},
        },
        available_roster_ids={"provider/b", "provider/c"},
    )

    assert group == "maximum"
    assert arms == ("provider/b", "provider/c")
    assert fallback[0].eligible_arms == ()

    empty_group, empty_arms, _ = select_roster_group(
        probabilities={"fast": 0.8, "maximum": 0.2},
        classes=("fast", "maximum"),
        clusters={"fast": {"arms": ["provider/a"]}, "maximum": {"arms": []}},
        available_roster_ids=set(),
    )
    assert empty_group is None
    assert empty_arms == ()


@pytest.mark.skipif(
    not os.environ.get("HMM_TEST_PACKAGE"),
    reason="published package is supplied by the release-artifact CI step",
)
async def test_published_package_routes_an_offered_candidate(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    package = Path(os.environ["HMM_TEST_PACKAGE"])
    monkeypatch.setenv("HMM_PACKAGE_PATH", str(package))
    monkeypatch.delenv("HMM_PACKAGE_URL", raising=False)
    monkeypatch.setenv("HMM_PACKAGE_SHA256", sha256_file(package))
    artifacts = resolve_artifacts()
    policy = FrozenPolicy(artifacts, FixedEmbedder(artifacts.probe_vector.tolist()))
    roster_id = policy.clusters["maximum"]["arms"][0]

    result = await policy.route(
        {
            "schema_version": "policy_router_v3",
            "route_id": "release-smoke-route",
            "prompt_text": "Implement the requested change.",
            "conversation_messages": [
                {"role": "user", "text": "Implement the requested change."}
            ],
            "candidates": [
                {
                    "roster_id": roster_id,
                    "catalog_id": roster_id,
                    "provider": roster_id.split("/", 1)[0],
                    "capabilities": {},
                }
            ],
        }
    )

    assert result.route_id == "release-smoke-route"
    assert result.selected_roster_id is None
    assert result.model is None
    assert result.policy_artifact_sha256 == sha256_file(package)
    # ranked_fallback carries the whole ranking: it is the only input the Go
    # router selects an arm from, and what per-key cluster overrides apply to.
    assert result.ranked_fallback
    assert result.policy_label == result.ranked_fallback[0].group
    assert any(group.group == "maximum" for group in result.ranked_fallback)


async def test_stage_timings_attribute_slow_work_to_the_stage_that_did_it() -> None:
    """A slow embed must show up as embed_ms, not smeared across the total.

    This is the property the whole change exists for: the router only ever saw
    one opaque number, so a regression in the network hop and a regression in
    the local model work looked identical. The test makes a stage deliberately
    slow and asserts the split points at it.
    """
    import asyncio
    import types

    import numpy as np

    from hmm_sidecar.embeddings import CachedEmbedder

    dims = 4
    embed_delay_s = 0.05

    class SlowEmbedder:
        async def embed(self, texts: list[str]) -> list[list[float]]:
            await asyncio.sleep(embed_delay_s)
            return [[0.1] * dims for _ in texts]

    class FakeHMM:
        n_states = 2

        def posterior(self, features: np.ndarray):
            steps = features.shape[0]
            return types.SimpleNamespace(
                gamma=np.full((steps, self.n_states), 0.5),
                state=1,
                previous_state=0,
                state_path=[0] * steps,
                confidence=0.5,
                margin=0.0,
            )

    class FakeClassifier:
        classes = ("fast", "maximum")

        def predict(self, row: np.ndarray):
            del row
            return types.SimpleNamespace(
                label="fast", probabilities={"fast": 0.7, "maximum": 0.3}
            )

    policy = object.__new__(FrozenPolicy)
    policy.embedder = CachedEmbedder(SlowEmbedder(), dimensions=dims)
    policy.hmm = FakeHMM()
    policy.classifier = FakeClassifier()

    payload = {
        "candidates": [
            {
                "roster_id": "provider/a",
                "catalog_id": "model-a",
                "provider": "provider",
            }
        ],
        "conversation_messages": [
            {"role": "user", "text": "Inspect the repository."},
            {"role": "assistant", "text": "Done."},
            {"role": "user", "text": "Now fix the bug."},
        ],
    }

    _, _, _, timings = await policy._evaluate(payload, allow_empty_candidates=False)

    assert timings.embed_ms >= embed_delay_s * 1000 * 0.9
    assert timings.hmm_ms < timings.embed_ms
    assert timings.classifier_ms < timings.embed_ms
    assert timings.total_ms >= timings.embed_ms
    assert timings.turns == 3
    assert timings.embed_requested == 3
    assert timings.embed_fetched == 3
    assert timings.embed_cached == 0


async def test_stage_timings_report_cache_hits_on_a_continuing_conversation() -> None:
    """Second turn of a session re-sends earlier turns; those must count as hits."""
    import types

    import numpy as np

    from hmm_sidecar.embeddings import CachedEmbedder

    dims = 4

    class CountingEmbedder:
        def __init__(self) -> None:
            self.batches: list[int] = []

        async def embed(self, texts: list[str]) -> list[list[float]]:
            self.batches.append(len(texts))
            return [[0.2] * dims for _ in texts]

    class FakeHMM:
        def posterior(self, features: np.ndarray):
            steps = features.shape[0]
            return types.SimpleNamespace(
                gamma=np.full((steps, 2), 0.5),
                state=0,
                previous_state=0,
                state_path=[0] * steps,
                confidence=0.5,
                margin=0.0,
            )

    class FakeClassifier:
        classes = ("fast", "maximum")

        def predict(self, row: np.ndarray):
            del row
            return types.SimpleNamespace(
                label="fast", probabilities={"fast": 0.6, "maximum": 0.4}
            )

    inner = CountingEmbedder()
    policy = object.__new__(FrozenPolicy)
    policy.embedder = CachedEmbedder(inner, dimensions=dims)
    policy.hmm = FakeHMM()
    policy.classifier = FakeClassifier()

    candidates = [
        {"roster_id": "provider/a", "catalog_id": "model-a", "provider": "provider"}
    ]
    first = {
        "candidates": candidates,
        "conversation_messages": [{"role": "user", "text": "Inspect the repository."}],
    }
    second = {
        "candidates": candidates,
        "conversation_messages": [
            {"role": "user", "text": "Inspect the repository."},
            {"role": "assistant", "text": "Done."},
            {"role": "user", "text": "Now fix the bug."},
        ],
    }

    *_, first_timings = await policy._evaluate(first, allow_empty_candidates=False)
    *_, second_timings = await policy._evaluate(second, allow_empty_candidates=False)

    assert (first_timings.embed_cached, first_timings.embed_fetched) == (0, 1)
    # The opening turn's text repeats verbatim, so only the new turns are fetched.
    assert second_timings.embed_cached == 1
    assert second_timings.embed_fetched == 2
    assert inner.batches == [1, 2]
