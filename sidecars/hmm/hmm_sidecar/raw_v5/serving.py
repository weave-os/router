"""Classifier-only ``policy_router_v4`` serving for configured raw-V5 packages."""

from __future__ import annotations

import asyncio
import math
import os
import re
from dataclasses import dataclass
from pathlib import Path
from typing import Any


from hmm_sidecar.artifacts import is_raw_v5_package_manifest
from hmm_sidecar.raw_v5.agent_flow_sidecar import AgentFlowHMMSidecar
from hmm_sidecar.raw_v5.complexity_classifier import ComplexityClassifier
from hmm_sidecar.raw_v5.live_sequence import (
    DEFAULT_LIVE_FLOW_MAX_ELEMENTS,
    clean_text,
    raw_v5_live_sequence_turns,
    route_message_role,
    route_message_text,
    tool_context_metadata,
)
from hmm_sidecar.raw_v5.raw_v5_runtime_contract import (
    RawV5InputSource,
    RawV5InteractionStateRuntimeContract,
    RawV5RuntimeContractError,
    parse_raw_v5_runtime_contract,
)
from hmm_sidecar.raw_v5.raw_v5_serving_profile import (
    RawV5EmbeddingProvider,
    validate_raw_v5_configured_hmm_bundle,
)
from hmm_sidecar.raw_v5.route_selector import ComplexityRouteSelector
from hmm_sidecar.raw_v5.schemas import (
    CognitiveAction,
    CognitiveStatePacket,
    ContextSignals,
    RouterHarness,
)
from hmm_sidecar.raw_v5.serving_features import current_text_has_tool_intent

EMBEDDING_BATCH_SIZE = 8


class RawV5PackageError(ValueError):
    pass


@dataclass(frozen=True)
class RawV5Readout:
    label: str
    probabilities: dict[str, float]
    confidence: float
    margin: float
    state_id: int
    state_path: tuple[int, ...]
    state_probabilities: tuple[float, ...]
    turns: int
    debug: dict[str, Any]


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


class RawV5Classifier:
    """Raw-V5 live sequence -> Gemini embeddings -> HMM -> complexity classifier."""

    def __init__(self, root: Path, manifest: dict[str, Any]) -> None:
        if not is_raw_v5_package_manifest(manifest):
            raise RawV5PackageError("package is not a configured raw-V5 package")
        try:
            contract = parse_raw_v5_runtime_contract(
                manifest["runtime_contract"].get("raw_v5_runtime_contract")
            )
        except RawV5RuntimeContractError as error:
            raise RawV5PackageError(f"raw-V5 package is not servable: {error}")
        if contract.input_source is not RawV5InputSource.LIVE_CONVERSATION:
            raise RawV5PackageError(
                "raw-V5 packages serve only the live conversation snapshot"
            )
        profile = contract.resolved_profile()
        if profile.config.provider is not RawV5EmbeddingProvider.GEMINI:
            raise RawV5PackageError("only Gemini raw-V5 packages are servable")
        self.profile = profile
        self.interaction_state = (
            contract.interaction_state
            if isinstance(contract, RawV5InteractionStateRuntimeContract)
            else None
        )
        self.classifier = ComplexityClassifier.load(
            root / "classifier",
            allow_raw_v5_serving_release=False,
            raw_v5_configured_profile=profile,
        )
        if sorted(self.classifier.classes) != sorted(contract.classes):
            raise RawV5PackageError(
                "raw-V5 classifier taxonomy does not match its declared classes"
            )
        self.hmm = AgentFlowHMMSidecar(
            workweave_dir=root,
            bundle_dir=root / "hmm_bundle",
            use_vertex=True,
            embedding_batch_size=EMBEDDING_BATCH_SIZE,
            embedding_request_concurrency=math.ceil(
                _live_flow_max_elements() / EMBEDDING_BATCH_SIZE
            ),
            embedding_persist_vertex_client=True,
            offload_posterior=True,
        )
        validate_raw_v5_configured_hmm_bundle(self.hmm.bundle, profile)
        if self.hmm.sequence_representation is not profile.representation:
            raise RawV5PackageError(
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

    async def classify(self, payload: dict[str, Any]) -> tuple[RawV5Readout, Any]:
        messages = payload.get("conversation_messages")
        latest_user_text = str(payload.get("latest_user_text") or "").strip()
        interaction_lane = self.interaction_state is not None
        omitted_user_turns = (
            _non_negative_int(payload.get("omitted_user_turn_count"))
            if interaction_lane
            else 0
        )
        turns, history_debug = raw_v5_live_sequence_turns(
            messages,
            fallback_text=latest_user_text,
            profile=self.profile,
            max_elements=0 if interaction_lane else _live_flow_max_elements(),
            first_turn_index=omitted_user_turns,
        )
        readout, infer_timings = await self.hmm.ainfer_turns_with_timings(turns)
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
        prefix_length = int(readout.prefix_length) + 2 * omitted_user_turns
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
            RawV5Readout(
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
                },
            ),
            infer_timings,
        )
