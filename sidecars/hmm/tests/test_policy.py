from __future__ import annotations

import os
from pathlib import Path

import pytest

from hmm_sidecar.artifacts import resolve_artifacts, sha256_file
from hmm_sidecar.policy import FrozenPolicy


class FixedEmbedder:
    def __init__(self, vector: list[float]) -> None:
        self.vector = vector

    async def embed(self, texts: list[str]) -> list[list[float]]:
        return [self.vector for _ in texts]


@pytest.mark.skipif(
    not os.environ.get("HMM_TEST_PACKAGE"),
    reason="published package is supplied by the release-artifact CI step",
)
async def test_published_package_classifies_a_turn(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    package = Path(os.environ["HMM_TEST_PACKAGE"])
    monkeypatch.setenv("HMM_PACKAGE_PATH", str(package))
    monkeypatch.delenv("HMM_PACKAGE_URL", raising=False)
    monkeypatch.setenv("HMM_PACKAGE_SHA256", sha256_file(package))
    artifacts = resolve_artifacts()
    policy = FrozenPolicy(artifacts, FixedEmbedder(artifacts.probe_vector.tolist()))

    result, _ = await policy.classify_with_timings(
        {
            "schema_version": "policy_router_v4",
            "route_id": "release-smoke-route",
            "prompt_text": "Implement the requested change.",
            "conversation_messages": [
                {"role": "user", "text": "Implement the requested change."}
            ],
        }
    )

    assert result.schema_version == "policy_router_v4"
    assert result.route_id == "release-smoke-route"
    assert result.classifier_artifact_sha256 == sha256_file(package)
    assert result.class_order == tuple(policy.classifier.classes)
    assert result.predicted_label in result.class_order


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
                label="fast",
                probabilities={"fast": 0.7, "maximum": 0.3},
                confidence=0.7,
                margin=0.4,
            )

    policy = object.__new__(FrozenPolicy)
    policy.embedder = CachedEmbedder(SlowEmbedder(), dimensions=dims)
    policy.hmm = FakeHMM()
    policy.classifier = FakeClassifier()
    policy.state_cards = {}
    policy.artifacts = types.SimpleNamespace(
        manifest=types.SimpleNamespace(model_id="artifact"), package_sha256="a" * 64
    )

    payload = {
        "conversation_messages": [
            {"role": "user", "text": "Inspect the repository."},
            {"role": "assistant", "text": "Done."},
            {"role": "user", "text": "Now fix the bug."},
        ],
    }

    _, timings = await policy.classify_with_timings(payload)

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
                label="fast",
                probabilities={"fast": 0.6, "maximum": 0.4},
                confidence=0.6,
                margin=0.2,
            )

    inner = CountingEmbedder()
    policy = object.__new__(FrozenPolicy)
    policy.embedder = CachedEmbedder(inner, dimensions=dims)
    policy.hmm = FakeHMM()
    policy.classifier = FakeClassifier()
    policy.state_cards = {}
    policy.artifacts = types.SimpleNamespace(
        manifest=types.SimpleNamespace(model_id="artifact"), package_sha256="a" * 64
    )

    first = {
        "conversation_messages": [{"role": "user", "text": "Inspect the repository."}],
    }
    second = {
        "conversation_messages": [
            {"role": "user", "text": "Inspect the repository."},
            {"role": "assistant", "text": "Done."},
            {"role": "user", "text": "Now fix the bug."},
        ],
    }

    _, first_timings = await policy.classify_with_timings(first)
    _, second_timings = await policy.classify_with_timings(second)

    assert (first_timings.embed_cached, first_timings.embed_fetched) == (0, 1)
    # The opening turn's text repeats verbatim, so only the new turns are fetched.
    assert second_timings.embed_cached == 1
    assert second_timings.embed_fetched == 2
    assert inner.batches == [1, 2]
