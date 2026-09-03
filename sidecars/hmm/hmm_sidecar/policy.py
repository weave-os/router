from __future__ import annotations

import hashlib
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
from .schemas import Candidate, RankedFallback, RoutePreviewResult, RouteResult


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


def select_roster_group(
    *,
    probabilities: dict[str, float],
    classes: tuple[str, ...],
    clusters: dict[str, Any],
    available_roster_ids: set[str],
) -> tuple[str | None, tuple[str, ...], tuple[RankedFallback, ...]]:
    """Select the first nonempty ranked group and retain every eligible arm."""
    ranked_labels = sorted(
        probabilities,
        key=lambda label: (-probabilities[label], classes.index(label)),
    )
    selected_group: str | None = None
    selected_arms: tuple[str, ...] = ()
    fallback: list[RankedFallback] = []
    for label in ranked_labels:
        cluster = clusters.get(label) or {}
        roster_arms = tuple(str(value) for value in cluster.get("arms") or [])
        eligible_arms = tuple(
            roster_id for roster_id in roster_arms if roster_id in available_roster_ids
        )
        fallback.append(
            RankedFallback(
                group=label,
                probability=float(probabilities[label]),
                roster_arms=roster_arms,
                eligible_arms=eligible_arms,
            )
        )
        if selected_group is None and eligible_arms:
            selected_group = label
            selected_arms = eligible_arms
    return selected_group, selected_arms, tuple(fallback)


def selected_margin(probabilities: dict[str, float], selected_label: str) -> float:
    selected = float(probabilities[selected_label])
    alternatives = [
        float(score)
        for label, score in probabilities.items()
        if label != selected_label
    ]
    return selected - max(alternatives) if alternatives else selected


class FrozenPolicy:
    def __init__(self, artifacts: FrozenArtifacts, embedder: Embedder) -> None:
        self.artifacts = artifacts
        manifest = artifacts.manifest
        self.embedder = CachedEmbedder(embedder, manifest.embedding_contract.dimensions)
        self.hmm = FrozenHMM(artifacts.root / manifest.hmm.path)
        self.classifier = FrozenClassifier(artifacts.root / manifest.classifier.path)
        roster_path = artifacts.root / manifest.roster.path
        roster_raw = roster_path.read_bytes()
        roster = json.loads(roster_raw)
        self.roster_version = hashlib.sha256(roster_raw).hexdigest()
        self.clusters = roster["clusters"]
        cards = json.loads((artifacts.root / manifest.state_cards.path).read_text())
        self.state_cards = {
            int(card["state_id"]): card
            for card in cards
            if isinstance(card, dict) and isinstance(card.get("state_id"), int)
        }

    def roster_ids(self) -> list[str]:
        """Deduplicated union of arm roster IDs across every cluster, in first-seen order."""
        seen: set[str] = set()
        ordered: list[str] = []
        for cluster in self.clusters.values():
            for arm in cluster.get("arms") or []:
                arm_id = str(arm)
                if arm_id not in seen:
                    seen.add(arm_id)
                    ordered.append(arm_id)
        return ordered

    async def _evaluate(
        self, payload: dict[str, Any], *, allow_empty_candidates: bool
    ) -> tuple[list[Candidate], Any, Any, RouteTimings]:
        started = time.perf_counter()
        candidates = [
            Candidate.model_validate(value) for value in payload.get("candidates") or []
        ]
        if not candidates and not allow_empty_candidates:
            raise ValueError("route request has no candidates")
        by_roster = {candidate.roster_id: candidate for candidate in candidates}
        if len(by_roster) != len(candidates):
            raise ValueError("candidate roster_id values must be unique")

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
        return candidates, readout, classification, timings

    async def preview(self, payload: dict[str, Any]) -> RoutePreviewResult:
        candidates, readout, classification, _ = await self._evaluate(
            payload, allow_empty_candidates=True
        )
        selected_group, eligible_arms, ranked_fallback = select_roster_group(
            probabilities=classification.probabilities,
            classes=tuple(self.classifier.classes),
            clusters=self.clusters,
            available_roster_ids={candidate.roster_id for candidate in candidates},
        )
        return RoutePreviewResult(
            route_id=str(payload.get("route_id") or ""),
            policy_artifact_id=self.artifacts.manifest.model_id,
            policy_artifact_sha256=self.artifacts.package_sha256,
            roster_sha256=self.roster_version,
            hmm_state_id=readout.state,
            hmm_state_path=tuple(readout.state_path),
            hmm_state_probabilities=tuple(float(value) for value in readout.gamma[-1]),
            class_order=tuple(self.classifier.classes),
            class_probabilities=classification.probabilities,
            ranked_fallback=ranked_fallback,
            selected_group=selected_group,
            eligible_roster_ids=eligible_arms,
        )

    async def route(self, payload: dict[str, Any]) -> RouteResult:
        result, _ = await self.route_with_timings(payload)
        return result

    async def route_with_timings(
        self, payload: dict[str, Any]
    ) -> tuple[RouteResult, RouteTimings]:
        """Same as :meth:`route`, plus the per-stage cost of producing it.

        Split out rather than folded into :class:`RouteResult` so the response
        the router validates stays byte-identical — timings are an operational
        signal, not part of the policy contract.
        """
        candidates, readout, classification, timings = await self._evaluate(
            payload, allow_empty_candidates=False
        )
        by_roster = {candidate.roster_id: candidate for candidate in candidates}
        _, _, ranked_fallback = select_roster_group(
            probabilities=classification.probabilities,
            classes=tuple(self.classifier.classes),
            clusters=self.clusters,
            available_roster_ids=set(by_roster),
        )
        # The reported group is the classifier's own top class, not the first
        # group that happens to hold an eligible arm: eligibility is the
        # router's business now, and ranked_fallback already carries it.
        classified_label = ranked_fallback[0].group
        card = self.state_cards.get(readout.state, {})
        state_label = str(card.get("name") or f"state_{readout.state}")
        candidate_scores = {
            candidate.roster_id: max(
                (
                    classification.probabilities.get(label, 0.0)
                    for label, cluster in self.clusters.items()
                    if candidate.roster_id in (cluster.get("arms") or [])
                ),
                default=0.0,
            )
            for candidate in candidates
        }
        route_id = str(payload.get("route_id") or "")
        score = float(classification.probabilities[classified_label])
        margin = selected_margin(classification.probabilities, classified_label)
        reason = (
            f"classifier group {classified_label!r} "
            f"(p={score:.3f}, margin={margin:.3f}, "
            f"raw_top={classification.label!r})"
        )
        result = RouteResult(
            route_id=route_id,
            score=score,
            candidate_scores=candidate_scores,
            reason=reason,
            state_label=state_label,
            policy_group=classified_label,
            policy_label=classified_label,
            policy_route_key=f"hmm:{readout.state}:{classified_label}",
            confidence=score,
            margin=margin,
            propensity=1.0,
            policy_artifact_id=self.artifacts.manifest.model_id,
            policy_artifact_sha256=self.artifacts.package_sha256,
            roster_version=self.roster_version,
            ranked_fallback=ranked_fallback,
            predicted_label=classification.label,
            class_probabilities=classification.probabilities,
            debug={
                "hmm_state_id": readout.state,
                "hmm_state_path": list(readout.state_path),
                "hmm_posterior": readout.confidence,
                "hmm_posterior_margin": readout.margin,
                "classifier_probs": classification.probabilities,
                "frozen_policy": True,
            },
        )
        return result, timings
