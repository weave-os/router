from __future__ import annotations

import time
from typing import Any

from . import SCHEMA_VERSION
from .artifacts import FrozenArtifacts
from .embeddings import EmbedStats
from .policy import RouteTimings
from .raw_v5.serving import RawV5Classifier
from .schemas import ClassificationResult


class RawV5Policy:
    """Serves a configured raw-V5 classifier package on the v4 wire contract."""

    def __init__(self, artifacts: FrozenArtifacts) -> None:
        if artifacts.raw_v5_manifest is None:
            raise ValueError("RawV5Policy requires a raw-V5 classifier package")
        self.artifacts = artifacts
        self.runtime = RawV5Classifier(artifacts.root, artifacts.raw_v5_manifest)
        self.classifier = self.runtime.classifier

    async def classify_with_timings(
        self, payload: dict[str, Any]
    ) -> tuple[ClassificationResult, RouteTimings]:
        started = time.perf_counter()
        readout, infer = await self.runtime.classify(payload)
        total_ms = (time.perf_counter() - started) * 1000.0
        fetched = int(infer.embed_cache_misses)
        cached = int(infer.embed_cache_hits)
        timings = RouteTimings.build(
            started=started,
            sequence_ms=float(infer.documents_ms),
            embed_ms=float(infer.embed_ms),
            hmm_ms=float(infer.posterior_ms),
            classifier_ms=max(total_ms - float(infer.total_ms), 0.0),
            turns=readout.turns,
            stats=EmbedStats(
                requested=cached + fetched,
                unique=cached + fetched,
                cached=cached,
                fetched=fetched,
            ),
        )
        debug = None
        if payload.get("debug_enabled") is True:
            debug = {**readout.debug, "raw_v5_policy": True}
        result = ClassificationResult(
            schema_version=SCHEMA_VERSION,
            route_id=str(payload.get("route_id") or ""),
            classifier_artifact_id=self.artifacts.model_id,
            classifier_artifact_sha256=self.artifacts.package_sha256,
            predicted_label=readout.label,
            class_order=tuple(self.classifier.classes),
            class_probabilities=readout.probabilities,
            classifier_confidence=readout.confidence,
            classifier_margin=readout.margin,
            hmm_state_id=readout.state_id,
            hmm_state_path=readout.state_path,
            hmm_state_probabilities=readout.state_probabilities,
            debug=debug,
        )
        return result, timings
