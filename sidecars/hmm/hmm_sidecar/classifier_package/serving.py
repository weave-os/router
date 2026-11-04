"""Classifier-only ``policy_router_v4`` serving for configured raw-V5 packages."""

from __future__ import annotations

import asyncio
import hashlib
import math
import os
import re
from dataclasses import dataclass
from pathlib import Path
from typing import Any

import numpy as np

from hmm_sidecar.artifacts import is_classifier_package_manifest
from hmm_sidecar.classifier_package.agent_flow_sidecar import (
    AgentFlowHMMReadout,
    AgentFlowHMMSidecar,
)
from hmm_sidecar.classifier_package.complexity_classifier import ComplexityClassifier
from hmm_sidecar.classifier_package import openai_compatible_embeddings
from hmm_sidecar.classifier_package.gemini_embeddings import (
    EmbeddingError,
    resolve_gemini_transport,
)
from hmm_sidecar.classifier_package.hmm_feature_arms import ElementLookbackSource
from hmm_sidecar.classifier_package.live_sequence import (
    DEFAULT_LIVE_FLOW_MAX_ELEMENTS,
    clean_text,
    live_sequence_turns,
    route_message_role,
    route_message_text,
    tool_context_metadata,
)
from hmm_sidecar.classifier_package.runtime_contract import (
    InputSource,
    RuntimeContractError,
    parse_runtime_contract,
)
from hmm_sidecar.classifier_package.serving_profile import (
    EmbeddingProvider,
    validate_configured_hmm_bundle,
)
from hmm_sidecar.classifier_package.route_selector import ComplexityRouteSelector
from hmm_sidecar.classifier_package.schemas import (
    CognitiveAction,
    CognitiveStatePacket,
    ContextSignals,
    RouterHarness,
)
from hmm_sidecar.classifier_package.serving_features import current_text_has_tool_intent
from hmm_sidecar.inference_deadline import LastReadoutStore, infer_within_deadline

EMBEDDING_BATCH_SIZE = 8
EMBEDDING_PROBE_TEXT = "Weave Router embedding probe"
REFERENCE_PROBE_MIN_COSINE = 0.999
OPENAI_COMPATIBLE_PROVIDER = openai_compatible_embeddings.PROVIDER
# Unset keeps the pre-existing Gemini deployments valid.
GEMINI_PROVIDER_SETTINGS = ("", "google", "gemini")


@dataclass(frozen=True)
class ServingOptions:
    """Deployment settings that change latency and failure handling, never outputs."""

    vertex_project: str | None = None
    vertex_location: str | None = None
    embedding_request_concurrency: int | None = None
    infer_deadline_seconds: float | None = None


class PackageError(ValueError):
    pass


@dataclass(frozen=True)
class Readout:
    label: str
    probabilities: dict[str, float]
    confidence: float
    margin: float
    state_id: int
    state_path: tuple[int, ...]
    state_probabilities: tuple[float, ...]
    turns: int
    debug: dict[str, Any]
    degraded: bool = False
    prior_age_seconds: float | None = None


def _live_flow_max_elements() -> int:
    return int(os.environ.get("HMM_LIVE_FLOW_MAX_ELEMENTS", "0")) or int(
        DEFAULT_LIVE_FLOW_MAX_ELEMENTS
    )


def _recent_user_turns(messages: Any) -> tuple[str, ...]:
    if not isinstance(messages, list):
        return ()
    return tuple(
        clean_text(route_message_text(message))
        for message in messages
        if route_message_role(message) == "user" and route_message_text(message)
    )


def _tool_names(payload: dict[str, Any], field: str) -> tuple[str, ...]:
    names = (str(raw).strip() for raw in payload.get(field) or ())
    return tuple(dict.fromkeys(name for name in names if name))


def _harness(payload: dict[str, Any]) -> RouterHarness:
    try:
        return RouterHarness(str(payload.get("harness") or ""))
    except ValueError:
        return RouterHarness.UNKNOWN


# About 128 MB of retained prefix rows; a 48-element Gemini prefix is ~147k.
LAST_READOUT_MAX_EMBEDDING_VALUES = 16_000_000


@dataclass(frozen=True)
class _SessionReadout:
    """A session's HMM readout with the omitted-turn offset it was computed at.

    A deadline fallback reuses the readout on a later request whose
    ``omitted_user_turn_count`` may differ, so the offset must travel with it.
    """

    hmm: AgentFlowHMMReadout
    omitted_user_turns: int


def _non_negative_int(value: Any) -> int:
    try:
        return max(int(value), 0)
    except (TypeError, ValueError):
        return 0


def _state_display_label(summary: Any) -> str | None:
    description = str(getattr(summary, "state_description", "") or "").strip()
    if description:
        return description
    label = str(getattr(summary, "state_label", "") or "").strip()
    if not label or re.fullmatch(r"state[_\s-]*\d+", label, flags=re.IGNORECASE):
        return None
    return label


def _posterior_diagnostics(readout: Any) -> dict[str, Any]:
    gamma = [float(value) for value in readout.gamma_row]
    positive = [value for value in gamma if value > 0.0]
    entropy = -sum(value * math.log(value) for value in positive)
    return {
        "hmm_gamma_entropy": float(entropy) if gamma else None,
        "hmm_gamma_effective_states": float(math.exp(entropy)) if gamma else None,
        "hmm_prev_state": readout.prev_state,
        "hmm_prefix_length": readout.prefix_length,
    }


class PackageClassifier:
    """Raw-V5 live sequence -> package's embedding model -> HMM -> complexity classifier."""

    def __init__(
        self,
        root: Path,
        manifest: dict[str, Any],
        options: ServingOptions = ServingOptions(),
    ) -> None:
        if not is_classifier_package_manifest(manifest):
            raise PackageError("package is not a configured raw-V5 package")
        try:
            contract = parse_runtime_contract(
                manifest["runtime_contract"].get("raw_v5_runtime_contract")
            )
        except RuntimeContractError as error:
            raise PackageError(f"raw-V5 package is not servable: {error}")
        if contract.input_source is not InputSource.LIVE_CONVERSATION:
            raise PackageError(
                "raw-V5 packages serve only the live conversation snapshot"
            )
        if (
            contract.interaction_state.element_lookback_source
            is ElementLookbackSource.STORED_VECTORS
        ):
            raise PackageError("raw-V5 stored-vector element lookback is not servable")
        profile = contract.resolved_profile()
        self.profile = profile
        self.interaction_state = contract.interaction_state
        self.classifier = ComplexityClassifier.load(
            root / "classifier", profile=profile
        )
        if sorted(self.classifier.classes) != sorted(contract.classes):
            raise PackageError(
                "raw-V5 classifier taxonomy does not match its declared classes"
            )
        self.hmm = AgentFlowHMMSidecar(
            workweave_dir=root,
            bundle_dir=root / "hmm_bundle",
            use_vertex=True,
            vertex_project=options.vertex_project,
            vertex_location=options.vertex_location,
            embedding_request_concurrency=options.embedding_request_concurrency
            or math.ceil(_live_flow_max_elements() / EMBEDDING_BATCH_SIZE),
            embedding_persist_vertex_client=True,
            # A call that outlived the deadline cannot finish any route that
            # joins it, so later requests fetch anew instead of inheriting it.
            embedding_inflight_max_age_seconds=options.infer_deadline_seconds,
            offload_posterior=True,
            embed_uncached=(
                openai_compatible_embeddings.embed_texts_uncached
                if profile.config.provider is EmbeddingProvider.OPENAI_COMPATIBLE
                else None
            ),
            state_encoder_contract=profile.state_encoder_contract,
        )
        self.reference_probes = contract.reference_probes
        self.infer_deadline_seconds = options.infer_deadline_seconds
        self.last_readouts = LastReadoutStore(
            weigh=lambda readout: sum(len(row) for row in readout.hmm.embedding_prefix),
            max_weight=LAST_READOUT_MAX_EMBEDDING_VALUES,
        )
        validate_configured_hmm_bundle(self.hmm.bundle, profile)
        hmm_bundle_sha256 = hashlib.sha256(
            (root / "hmm_bundle" / "bundle.json").read_bytes()
        ).hexdigest()
        if hmm_bundle_sha256 != self.classifier.metadata.get("hmm_bundle_json_sha256"):
            raise PackageError(
                "raw-V5 classifier was not trained on this package's HMM bundle"
            )
        if self.hmm.sequence_representation is not profile.representation:
            raise PackageError(
                f"raw-V5 HMM bundle loads {self.hmm.sequence_representation.value!r},"
                f" not the declared {profile.representation.value!r}"
            )
        self.selector = ComplexityRouteSelector(
            classifier=self.classifier,
            confidence_threshold=0.0,
            latent_model=self.hmm.latent_model,
            sequence_encoder=None,
            interaction_state=self.interaction_state,
        )

    @property
    def classes(self) -> tuple[str, ...]:
        return tuple(self.classifier.classes)

    async def verify_embedding_config(self) -> None:
        """Embed probes at boot so bad credentials, a different embedding model
        or a wrong endpoint fail readiness instead of every route."""
        model = self.profile.embedding_model
        embedding_dim = self.profile.embedding_dim
        endpoint = self._configured_embedding_endpoint()
        probe_texts = [probe.text for probe in self.reference_probes] or [
            EMBEDDING_PROBE_TEXT
        ]
        failure = f"embedding probe for package model {model!r} over {endpoint}"
        try:
            vectors = await self.hmm.probe_embeddings(probe_texts)
        except Exception as error:
            raise EmbeddingError(
                f"{failure} failed: {type(error).__name__}: {error}"
            ) from error
        for vector in vectors:
            probe_values = np.asarray(vector, dtype=np.float64)
            if probe_values.shape != (embedding_dim,):
                raise EmbeddingError(
                    f"{failure} returned {probe_values.size} dimensions; the"
                    f" package was trained on {embedding_dim}"
                )
            if not np.all(np.isfinite(probe_values)) or not np.any(probe_values):
                raise EmbeddingError(f"{failure} returned a zero or non-finite vector")
        for probe, vector in zip(self.reference_probes, vectors, strict=False):
            live = np.asarray(vector, dtype=np.float64)
            reference = np.asarray(probe.vector, dtype=np.float64)
            cosine = float(
                live @ reference / (np.linalg.norm(live) * np.linalg.norm(reference))
            )
            if cosine < REFERENCE_PROBE_MIN_COSINE:
                raise EmbeddingError(
                    f"{failure} does not reproduce the package's reference"
                    f" vectors (cosine {cosine:.4f} < {REFERENCE_PROBE_MIN_COSINE})"
                )

    def _configured_embedding_endpoint(self) -> str:
        """Name the configured endpoint, or fail if it is not the package's provider."""
        model = self.profile.embedding_model
        configured_provider = (
            os.environ.get("HMM_EMBEDDING_PROVIDER", "").strip().lower()
        )
        if self.profile.config.provider is EmbeddingProvider.GEMINI:
            if configured_provider not in GEMINI_PROVIDER_SETTINGS:
                raise EmbeddingError(
                    f"package model {model!r} is served by Gemini, but"
                    f" HMM_EMBEDDING_PROVIDER is {configured_provider!r}"
                )
            configured_transport = (
                os.environ.get("HMM_GEMINI_TRANSPORT", "").strip() or "auto"
            )
            try:
                return f"transport {resolve_gemini_transport().value!r}"
            except EmbeddingError as error:
                raise EmbeddingError(
                    f"embedding probe for package model {model!r} over transport"
                    f" {configured_transport!r} failed: {error}"
                ) from error
        if configured_provider != OPENAI_COMPATIBLE_PROVIDER:
            raise EmbeddingError(
                f"package model {model!r} needs HMM_EMBEDDING_PROVIDER="
                f"{OPENAI_COMPATIBLE_PROVIDER}, got {configured_provider or 'unset'!r}"
            )
        configured_model = os.environ.get("HMM_EMBEDDING_MODEL", "").strip()
        if configured_model != model:
            raise EmbeddingError(
                f"HMM_EMBEDDING_MODEL {configured_model or 'unset'!r} is not the"
                f" package's embedding model {model!r}"
            )
        return f"provider {OPENAI_COMPATIBLE_PROVIDER!r}"

    async def classify(
        self, payload: dict[str, Any], *, session_id: str | None = None
    ) -> tuple[Readout, Any]:
        """Classify one turn.

        Past ``infer_deadline_seconds`` the turn is classified on the session's
        last completed HMM readout instead (``Readout.degraded``); without a
        prior readout the deadline raises ``TimeoutError``.
        """
        messages = payload.get("conversation_messages")
        latest_user_text = str(payload.get("latest_user_text") or "").strip()
        interaction_lane = self.interaction_state is not None
        omitted_user_turns = (
            _non_negative_int(payload.get("omitted_user_turn_count"))
            if interaction_lane
            else 0
        )
        turns, history_debug = live_sequence_turns(
            messages,
            fallback_text=latest_user_text,
            profile=self.profile,
            max_elements=0 if interaction_lane else _live_flow_max_elements(),
            first_turn_index=omitted_user_turns,
        )

        async def infer():
            hmm_readout, timings = await self.hmm.ainfer_turns_with_timings(turns)
            return _SessionReadout(hmm_readout, omitted_user_turns), timings

        inference = await infer_within_deadline(
            infer=infer,
            session_id=session_id,
            store=self.last_readouts,
            deadline_seconds=self.infer_deadline_seconds,
        )
        readout = inference.readout.hmm
        infer_timings = inference.timings
        has_tools = bool(payload.get("has_tools"))
        packet = CognitiveStatePacket(
            request_id=str(payload.get("route_id") or ""),
            action=CognitiveAction.ANSWER,
            action_confidence=readout.summary.posterior,
            latest_user_request=latest_user_text or "[missing latest user request]",
            recent_user_turns=_recent_user_turns(messages),
            hmm=readout.summary,
            context=ContextSignals(
                harness=_harness(payload),
                input_token_estimate=int(payload.get("estimated_input_tokens") or 0),
                has_tools=has_tools,
                requires_tool_use=(
                    has_tools and current_text_has_tool_intent(latest_user_text)
                ),
            ),
            constraints=("Preserve tool-use affordances and user-visible intent.",),
            available_tools=_tool_names(payload, "available_tools"),
            metadata={
                "hmm_bundle_version": readout.bundle_version,
                **tool_context_metadata(messages),
                **history_debug,
            },
        )
        prefix_length = (
            int(readout.prefix_length) + 2 * inference.readout.omitted_user_turns
        )
        features = await asyncio.to_thread(
            self.selector.serving_features,
            embedding=list(readout.latest_embedding),
            gamma_row=list(readout.gamma_row),
            current_state=int(readout.summary.state_id or 0),
            previous_state=int(readout.prev_state),
            position=max(prefix_length - 1, 0),
            prefix_length=prefix_length,
            embedding_prefix=[list(row) for row in readout.embedding_prefix] or None,
            packet=packet,
            tool_context_features=None,
            belief_dynamics=readout.belief_dynamics,
        )
        classification = await asyncio.to_thread(self.selector.classify, features)
        state_path = tuple(int(value) for value in readout.summary.state_path or ())
        state_id = int(readout.summary.state_id or 0)
        return (
            Readout(
                label=classification.label,
                probabilities=dict(classification.probs),
                confidence=float(classification.confidence),
                margin=float(classification.margin),
                state_id=state_id,
                state_path=state_path or (state_id,),
                state_probabilities=tuple(float(v) for v in readout.gamma_row),
                turns=len(turns),
                debug={
                    "hmm_state_label": _state_display_label(readout.summary),
                    "hmm_posterior": float(readout.summary.posterior),
                    "attribution": classification.attribution,
                    **_posterior_diagnostics(readout),
                    **history_debug,
                },
                degraded=inference.degraded,
                prior_age_seconds=inference.prior_age_seconds,
            ),
            infer_timings,
        )
