"""Classifier + within-cluster route selection.

``ComplexityRouteSelector.select`` maps serving features to a complexity label,
then scores only the caller-eligible models in that cluster. The classifier
still owns cluster assignment; arm selection can use the pinned roster order,
a trained contextual selector, or a full-vector agent-environment critic.
"""

from __future__ import annotations


import logging
from collections.abc import Sequence
from dataclasses import dataclass
from typing import Any

import numpy as np

from hmm_sidecar.classifier_package.complexity_classifier import ComplexityClassifier
from hmm_sidecar.classifier_package.conversation_sequence import (
    AGENT_ELEMENT,
    CONVERSATION_SEQUENCE_STATE_DEFINITION,
    USER_ELEMENT,
)
from hmm_sidecar.classifier_package.hmm_feature_arms import (
    ELEMENT_LOOKBACK_BLOCK_NAME,
    ElementLookback,
    ElementLookbackSource,
    HMMChain,
    resolve_element_lookback,
)
from hmm_sidecar.classifier_package.interaction_state import (
    element_lookback_block,
    hmm_chain_of,
    raw_round_vectors,
    round_observations,
)
from hmm_sidecar.classifier_package.serving_profile import InteractionStateGeometry
from hmm_sidecar.classifier_package.serving_features import (
    BELIEF_DYNAMICS_BLOCK_NAME,
    BELIEF_DYNAMICS_DIM,
    _feature_row,
    _raw_prefix_matrix,
    raw_relative_position_dim,
)
from hmm_sidecar.classifier_package.serving_features import HARNESS_BLOCK_NAME
from hmm_sidecar.classifier_package.serving_features import harness_features_from_packet
from hmm_sidecar.classifier_package.serving_features import (
    TOOL_CONTEXT_FEATURE_NAMES,
    tool_context_features_from_packet,
)
from hmm_sidecar.classifier_package.schemas import (
    CognitiveStatePacket,
)

log = logging.getLogger(__name__)


ATTRIBUTION_SCHEMA_VERSION = "hmm_router_route_attribution_v1"
ATTRIBUTION_TOP_FACTORS = 5
# Human-facing names for aggregated feature blocks.
_BLOCK_FACTOR_NAMES = {
    "embedding": "current message content",
    "hmm_gamma": "conversation state posterior",
    "one_hot_current_state": "current HMM state",
    "one_hot_previous_state": "previous HMM state",
    "projected_embedding": "projected state context",
    "seq_ae_latent": "conversation history (sequence latent)",
    "tool_context": "tool-use context",
    HARNESS_BLOCK_NAME: "sending harness",
    BELIEF_DYNAMICS_BLOCK_NAME: "conversation state dynamics",
}


class RouteSelectionError(RuntimeError):
    """Classifier route selection could not make a valid model decision."""


@dataclass(frozen=True)
class ComplexityClassification:
    """Classifier facts that do not depend on roster or arm selection."""

    label: str
    probs: dict[str, float]
    confidence: float
    margin: float
    attribution: dict[str, Any] | None


def attribution_summary(
    contributions: np.ndarray,
    feature_layout: list[dict[str, Any]],
    *,
    chosen_label: str,
) -> dict[str, Any]:
    """Aggregate a TreeSHAP row into named factors for the chosen class.

    Per-dimension contributions are summed per feature block (a 3072-d
    embedding block becomes one 'current message content' factor); scalar
    features keep their individual names; gamma additionally surfaces its
    single strongest state so 'which state pushed this' is visible.
    """
    contributions = np.asarray(contributions, dtype=float).reshape(-1)
    factors: list[dict[str, Any]] = []
    offset = 0
    for block in feature_layout:
        dim = int(block["dim"])
        name = str(block["block"])
        segment = contributions[offset : offset + dim]
        if name == "scalars":
            for scalar_name, value in zip(block.get("names") or [], segment):
                factors.append(
                    {"factor": scalar_name, "contribution": round(float(value), 5)}
                )
        else:
            factors.append(
                {
                    "factor": _BLOCK_FACTOR_NAMES.get(name, name),
                    "contribution": round(float(segment.sum()), 5),
                }
            )
            if name == "hmm_gamma" and dim > 0:
                strongest = int(np.argmax(np.abs(segment)))
                factors.append(
                    {
                        "factor": f"state_{strongest} posterior",
                        "contribution": round(float(segment[strongest]), 5),
                    }
                )
        offset += dim
    top = sorted(factors, key=lambda item: -abs(item["contribution"]))
    return {
        "schema_version": ATTRIBUTION_SCHEMA_VERSION,
        "chosen_label": chosen_label,
        "bias": round(float(contributions[-1]), 5),
        "top_factors": top[:ATTRIBUTION_TOP_FACTORS],
    }


def _routing_prefix_kinds(length: int) -> list[str]:
    """Element kinds of an Action prefix, strictly alternating from U.

    Both the dataset builder and the live sequence builder emit strict
    alternation starting at a user element. Offline labeled prefixes and v1
    live prefixes end at U; v2 agent-step Actions may end at A. In either
    case the kinds are fully determined by position.
    """
    return [
        USER_ELEMENT if index % 2 == 0 else AGENT_ELEMENT for index in range(length)
    ]


def _served_element_lookback(
    *,
    classifier: ComplexityClassifier,
    latent_model: Any,
    interaction_state: InteractionStateGeometry | None,
) -> ElementLookback | None:
    """The lookback this selector appends; only a declared package serves the round-level chain."""
    chain = hmm_chain_of(getattr(latent_model, "model_info", None))
    if interaction_state is None:
        if chain is HMMChain.INTERACTION_STATE:
            raise ValueError(
                "an interaction-state HMM bundle is served only by a package that "
                "declares the interaction-state runtime contract"
            )
        return None
    if chain is not HMMChain.INTERACTION_STATE:
        raise ValueError(
            "the interaction-state runtime contract requires an interaction-state HMM bundle"
        )
    projector = latent_model.projector
    if (projector.user_dim, projector.agent_dim) != (
        interaction_state.user_pca_dim,
        interaction_state.agent_pca_dim,
    ):
        raise ValueError(
            "loaded per-role projection widths differ from the package's declared geometry"
        )
    layout = classifier.metadata["feature_layout"]
    embedding_dim = int(layout[0]["dim"])
    served = (
        resolve_element_lookback(layout[-1], embedding_dim=embedding_dim)
        if layout[-1]["block"] == ELEMENT_LOOKBACK_BLOCK_NAME
        else None
    )
    if served != interaction_state.element_lookback(embedding_dim=embedding_dim):
        raise ValueError(
            "classifier element_lookback differs from the package's declared geometry"
        )
    return served


class ComplexityRouteSelector:
    def __init__(
        self,
        *,
        classifier: ComplexityClassifier,
        confidence_threshold: float | None = None,
        latent_model: Any = None,
        sequence_encoder: Any = None,
        interaction_state: InteractionStateGeometry | None = None,
    ) -> None:
        classifier_classes = tuple(classifier.classes)
        effective_confidence_threshold = (
            1.0 / len(classifier_classes)
            if confidence_threshold is None
            else float(confidence_threshold)
        )
        if not 0.0 <= effective_confidence_threshold <= 1.0:
            raise ValueError("confidence_threshold must be in [0, 1]")
        expected_identity = classifier.sequence_autoencoder_identity
        actual_identity = (
            getattr(sequence_encoder, "identity", None)
            if sequence_encoder is not None
            else None
        )
        if expected_identity != actual_identity:
            raise ValueError(
                "classifier/sequence-encoder identity mismatch: the classifier "
                f"was trained with {expected_identity!r} but serving has "
                f"{actual_identity!r}; load the matching encoder artifact (or "
                "none) before serving"
            )
        self.classifier = classifier
        self.confidence_threshold = effective_confidence_threshold
        self._element_lookback = _served_element_lookback(
            classifier=classifier,
            latent_model=latent_model,
            interaction_state=interaction_state,
        )
        self.latent_model = latent_model
        self.sequence_encoder = sequence_encoder
        self._requires_projected_embedding = _classifier_uses_feature_block(
            classifier,
            "projected_embedding",
        )
        self._uses_tool_context = _classifier_uses_feature_block(
            classifier,
            "tool_context",
        )
        # Layout-driven, exactly like tool_context: a bundle trained before the
        # harness block simply doesn't declare it, so nothing is emitted and the
        # served width is unchanged. That makes this safe to ship ahead of a
        # retrained bundle — no flag day, no coordinated pin move.
        self._uses_harness = _classifier_uses_feature_block(
            classifier,
            HARNESS_BLOCK_NAME,
        )
        # Same layout-driven gate. The block needs values only the HMM readout
        # can produce (the filtered belief history, each turn's predictive log
        # likelihood, the transition diagonal), so a classifier that declares it
        # cannot be served from a readout that does not carry them.
        self._uses_belief_dynamics = _classifier_uses_feature_block(
            classifier,
            BELIEF_DYNAMICS_BLOCK_NAME,
        )
        # State-compressor classifiers (--state-repr seqae) have no raw
        # embedding block: the seq-AE latent over the embedding prefix IS the
        # state, alongside the HMM cluster features. Legacy bundles predate
        # state_repr and always carry the embedding block. A wrong value here
        # cannot serve silently: predict() rejects mismatched feature_dim.
        metadata = classifier.metadata
        state_repr = metadata.get("state_repr", "embedding")
        self._includes_raw_embedding = state_repr != "seqae"
        self._conversation_sequence_contract = (
            metadata.get("state_definition") == CONVERSATION_SEQUENCE_STATE_DEFINITION
        )
        # An HMM-free classifier (--no-hmm-features) has no hmm_gamma /
        # one_hot_state blocks in its feature_layout: derive it from the
        # classifier metadata so serving passes n_states=0 even though the
        # live HMM sidecar readout always reports the gamma shape of the
        # currently-loaded bundle, which would otherwise drive a feature
        # width that predict() rejects. Only an EXPLICIT feature_layout that
        # omits hmm_gamma counts as HMM-free -- a legacy classifier whose
        # metadata predates feature_layout entirely must default to
        # HMM-present (the only layout that existed before this field), not
        # be silently misclassified as HMM-free because the block lookup
        # can't find a layout to search.
        self._classifier_hmm_free = _classifier_has_feature_layout(
            classifier
        ) and not _classifier_uses_feature_block(classifier, "hmm_gamma")
        if (
            self._requires_projected_embedding
            and getattr(
                self.latent_model,
                "projector",
                None,
            )
            is None
        ):
            raise ValueError(
                "classifier was trained with projected_embedding features, "
                "but serving has no HMM latent projector; load the matching "
                "bundle latent model before serving"
            )

    # ------------------------------------------------------------------
    # Serving features (single implementation shared with training)
    # ------------------------------------------------------------------

    def serving_features(
        self,
        *,
        embedding: list[float],
        gamma_row: list[float],
        current_state: int,
        previous_state: int,
        position: int,
        prefix_length: int,
        embedding_prefix: list[list[float]] | None = None,
        packet: CognitiveStatePacket | None = None,
        tool_context_features: np.ndarray | None = None,
        belief_dynamics: Sequence[float] | None = None,
    ) -> np.ndarray:
        gamma = np.asarray(gamma_row, dtype=float)
        # A readout carries belief dynamics whenever its HMM can filter, so a
        # value an older classifier does not consume is ignored rather than
        # rejected: like the embedding prefix, it is always-available readout
        # state, not a caller-supplied override.
        belief_dynamics_values = None
        if self._uses_belief_dynamics:
            if belief_dynamics is None:
                raise ValueError(
                    "belief-dynamics classifier requires the HMM readout's "
                    "causal belief-dynamics features"
                )
            belief_dynamics_values = np.asarray(belief_dynamics, dtype=float)
            if belief_dynamics_values.shape != (BELIEF_DYNAMICS_DIM,) or not np.all(
                np.isfinite(belief_dynamics_values)
            ):
                raise ValueError(
                    "belief-dynamics feature vector does not match the "
                    f"classifier's {BELIEF_DYNAMICS_DIM}-wide block"
                )
        projected_row = None
        if self._requires_projected_embedding:
            if not embedding_prefix:
                raise ValueError(
                    "projected-embedding classifier requires the embedding prefix"
                )
            # Conversation-sequence classifiers were trained with element-kind
            # one-hots in the raw matrix; the kinds of a routing prefix are
            # positional ([U, A, ..., U]).
            prefix_kinds = (
                _routing_prefix_kinds(len(embedding_prefix))
                if self._conversation_sequence_contract
                else None
            )
            projected = self.latent_model.projector.transform(
                _raw_prefix_matrix(
                    embedding_prefix,
                    prefix_kinds,
                    relative_position_dim=raw_relative_position_dim(
                        getattr(self.latent_model, "model_info", None)
                    ),
                )
            )
            projected_row = np.asarray(projected, dtype=float)[-1]
        seqae_latent = None
        if self.sequence_encoder is not None:
            if not embedding_prefix:
                raise ValueError(
                    "sequence-encoder classifier requires the embedding prefix"
                )
            seqae_latent = self.sequence_encoder.encode(embedding_prefix)
        if tool_context_features is not None:
            if not self._uses_tool_context:
                raise ValueError(
                    "tool-context feature override was supplied to a classifier "
                    "that does not consume that feature block"
                )
            tool_context_features = np.asarray(tool_context_features, dtype=float)
            if (
                tool_context_features.ndim != 1
                or len(tool_context_features) != len(TOOL_CONTEXT_FEATURE_NAMES)
                or not np.all(np.isfinite(tool_context_features))
            ):
                raise ValueError(
                    "tool-context feature override does not match the supported "
                    "raw V5 feature vector"
                )
        row = _feature_row(
            embedding=embedding,
            gamma_row=gamma,
            current_state=int(current_state),
            previous_state=int(previous_state),
            position=int(position),
            prefix_length=int(prefix_length),
            is_last=0.0,  # serving never knows a turn is final
            n_states=0 if self._classifier_hmm_free else int(gamma.shape[0]),
            projected_row=projected_row,
            seqae_latent=seqae_latent,
            include_embedding=self._includes_raw_embedding,
            tool_context_features=(
                tool_context_features
                if tool_context_features is not None
                else (
                    tool_context_features_from_packet(packet)
                    if self._uses_tool_context
                    else None
                )
            ),
            harness_features=(
                harness_features_from_packet(packet) if self._uses_harness else None
            ),
            belief_dynamics=belief_dynamics_values,
        )
        element_lookback = self._element_lookback
        if element_lookback is None:
            return row
        if not embedding_prefix:
            raise ValueError(
                "element_lookback classifier requires the embedding prefix"
            )
        return np.concatenate(
            [row, self._element_lookback_features(element_lookback, embedding_prefix)]
        )

    def _element_lookback_features(
        self,
        element_lookback: ElementLookback,
        embedding_prefix: list[list[float]],
    ) -> np.ndarray:
        """Earlier rounds' vectors from the declared source, as the dataset builder fills them."""
        kinds = _routing_prefix_kinds(len(embedding_prefix))
        rounds = (
            raw_round_vectors(embedding_prefix, kinds)
            if element_lookback.source is ElementLookbackSource.STORED_VECTORS
            else round_observations(
                self.latent_model.projector, embedding_prefix, kinds
            )
        )
        return element_lookback_block(
            rounds,
            round_index=rounds.shape[0] - 1,
            lookback_rounds=element_lookback.rounds,
        )

    # ------------------------------------------------------------------
    # Selection
    # ------------------------------------------------------------------

    def classify(self, features: np.ndarray | None) -> ComplexityClassification:
        """Return classifier facts without consulting the serving roster."""
        if features is None:
            raise RouteSelectionError(
                "no serving features; HMM sidecar or sequence feature "
                "construction is unavailable"
            )
        try:
            prediction = self.classifier.predict(features)
        except Exception as exc:  # noqa: BLE001 - fail closed loudly
            raise RouteSelectionError(
                f"classifier prediction failed: {type(exc).__name__}: {exc}"
            ) from exc
        attribution = None
        try:
            contributions = self.classifier.contributions(features)
            attribution_index = tuple(prediction.probs).index(prediction.label)
            attribution = attribution_summary(
                contributions[attribution_index],
                self.classifier.metadata.get("feature_layout") or [],
                chosen_label=prediction.label,
            )
        except Exception:  # noqa: BLE001 - explanation must never break classification
            log.exception("route attribution failed")
        return ComplexityClassification(
            label=prediction.label,
            probs={
                label: float(probability)
                for label, probability in prediction.probs.items()
            },
            confidence=float(prediction.confidence),
            margin=float(prediction.margin),
            attribution=attribution,
        )


def _classifier_uses_feature_block(
    classifier: ComplexityClassifier,
    block_name: str,
) -> bool:
    feature_layout = classifier.metadata.get("feature_layout")
    if not isinstance(feature_layout, list):
        return False
    return any(
        isinstance(block, dict) and block.get("block") == block_name
        for block in feature_layout
    )


def _classifier_has_feature_layout(classifier: ComplexityClassifier) -> bool:
    """True only when metadata carries an explicit, non-empty feature_layout.

    Distinguishes "no hmm_gamma block because the layout was searched and it
    genuinely isn't there" from "no hmm_gamma block because there is no
    layout to search" -- the latter is a legacy classifier that predates this
    field, not an HMM-free one.
    """
    feature_layout = classifier.metadata.get("feature_layout")
    return isinstance(feature_layout, list) and len(feature_layout) > 0
