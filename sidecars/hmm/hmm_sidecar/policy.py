from __future__ import annotations

import json
import time
from dataclasses import dataclass
from typing import Any

from .artifacts import FrozenArtifacts
from .classifier import FrozenClassifier
from .embeddings import CachedEmbedder, Embedder, EmbedStats
from .features import (
    classifier_features,
    conversation_sequence,
    raw_hmm_features,
    tool_context_features,
)
from .hmm import FrozenHMM
from . import SCHEMA_VERSION
from .schemas import ClassificationResult


@dataclass(frozen=True, slots=True)
class RouteTimings:
    """Wall-clock cost of each stage of one routing decision, in milliseconds.

    The decision runs to completion before the router dispatches the upstream
    request, so this is time added to the caller's wait for a first token. It is
    reported per stage because the stages have very different characters: the
    embed hop is a network round trip whose cost tracks how many turns missed
    the cache, while the rest is local numpy/XGBoost work that tracks sequence
    length. Without the split, a regression in either is a single opaque number.

    Carries no text, ids, or model output — only durations and counts — so it is
    safe to log verbatim.
    """

    sequence_ms: float
    embed_ms: float
    hmm_ms: float
    classifier_ms: float
    total_ms: float
    turns: int
    embed_requested: int
    embed_cached: int
    embed_fetched: int

    @classmethod
    def build(
        cls,
        *,
        started: float,
        sequence_ms: float,
        embed_ms: float,
        hmm_ms: float,
        classifier_ms: float,
        turns: int,
        stats: EmbedStats,
    ) -> RouteTimings:
        return cls(
            sequence_ms=sequence_ms,
            embed_ms=embed_ms,
            hmm_ms=hmm_ms,
            classifier_ms=classifier_ms,
            total_ms=(time.perf_counter() - started) * 1000.0,
            turns=turns,
            embed_requested=stats.requested,
            embed_cached=stats.cached,
            embed_fetched=stats.fetched,
        )

    def log_fields(self) -> str:
        """Compact ``key=value`` rendering for one log line."""
        return (
            f"total_ms={self.total_ms:.1f} "
            f"sequence_ms={self.sequence_ms:.1f} "
            f"embed_ms={self.embed_ms:.1f} "
            f"hmm_ms={self.hmm_ms:.1f} "
            f"classifier_ms={self.classifier_ms:.1f} "
            f"turns={self.turns} "
            f"embed_requested={self.embed_requested} "
            f"embed_cached={self.embed_cached} "
            f"embed_fetched={self.embed_fetched}"
        )


class FrozenPolicy:
    def __init__(self, artifacts: FrozenArtifacts, embedder: Embedder) -> None:
        self.artifacts = artifacts
        manifest = artifacts.manifest
        self.embedder = CachedEmbedder(embedder, manifest.embedding_contract.dimensions)
        self.hmm = FrozenHMM(artifacts.root / manifest.hmm.path)
        self.classifier = FrozenClassifier(artifacts.root / manifest.classifier.path)
        cards = json.loads((artifacts.root / manifest.state_cards.path).read_text())
        self.state_cards = {
            int(card["state_id"]): card
            for card in cards
            if isinstance(card, dict) and isinstance(card.get("state_id"), int)
        }

    async def classify_with_timings(
        self, payload: dict[str, Any]
    ) -> tuple[ClassificationResult, RouteTimings]:
        """Classify one turn; timings stay out of the body the router validates."""
        started = time.perf_counter()

        mark = time.perf_counter()
        turns = conversation_sequence(payload)
        sequence_ms = (time.perf_counter() - mark) * 1000.0

        mark = time.perf_counter()
        embeddings, embed_stats = await self.embedder.embed_with_stats(
            [turn.text for turn in turns]
        )
        embed_ms = (time.perf_counter() - mark) * 1000.0

        mark = time.perf_counter()
        readout = self.hmm.posterior(raw_hmm_features(embeddings, turns))
        hmm_ms = (time.perf_counter() - mark) * 1000.0

        mark = time.perf_counter()
        feature_row = classifier_features(
            embedding=embeddings[-1],
            gamma=readout.gamma[-1],
            state=readout.state,
            previous_state=readout.previous_state,
            position=len(turns) - 1,
            prefix_length=len(turns),
            tool_context=tool_context_features(payload),
        )
        classification = self.classifier.predict(feature_row)
        classifier_ms = (time.perf_counter() - mark) * 1000.0

        timings = RouteTimings.build(
            started=started,
            sequence_ms=sequence_ms,
            embed_ms=embed_ms,
            hmm_ms=hmm_ms,
            classifier_ms=classifier_ms,
            turns=len(turns),
            stats=embed_stats,
        )
        debug = None
        if payload.get("debug_enabled") is True:
            card = self.state_cards.get(readout.state, {})
            debug = {
                "hmm_state_label": str(card.get("name") or f"state_{readout.state}"),
                "hmm_posterior": readout.confidence,
                "hmm_posterior_margin": readout.margin,
                "frozen_policy": True,
            }
        result = ClassificationResult(
            schema_version=SCHEMA_VERSION,
            route_id=str(payload.get("route_id") or ""),
            classifier_artifact_id=self.artifacts.manifest.model_id,
            classifier_artifact_sha256=self.artifacts.package_sha256,
            predicted_label=classification.label,
            class_order=tuple(self.classifier.classes),
            class_probabilities=classification.probabilities,
            classifier_confidence=classification.confidence,
            classifier_margin=classification.margin,
            hmm_state_id=readout.state,
            hmm_state_path=tuple(readout.state_path),
            hmm_state_probabilities=tuple(float(value) for value in readout.gamma[-1]),
            debug=debug,
        )
        return result, timings
