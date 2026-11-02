"""Exact live-input adapter for the raw-V5 Voyage HMM serving lane.

The raw-V5 classifier was trained on ``RawAgentFlowTurnEvidence`` rather than
the canonical route summary.  This module is deliberately narrow: it accepts
only that stored evidence shape, produces the already-pinned two-turn Voyage
documents, and rejects any current-turn agent evidence.  It never derives raw
evidence from a lossy route-context snapshot and never falls back to canonical
documents.
"""

from __future__ import annotations

import hashlib
from enum import StrEnum


from hmm_sidecar.raw_v5.cd_raw_agent_flow_evidence import (
    RAW_AGENT_FLOW_TURN_EVIDENCE_SCHEMA_VERSION,
    RawAgentFlowTurnEvidence,
)
from hmm_sidecar.raw_v5.conversation_sequence import (
    SequenceElement,
)
from hmm_sidecar.raw_v5.raw_v5_contract import (
    RAW_V5_ROUTER_EMBEDDING_DIM,
    RAW_V5_ROUTER_EMBEDDING_MODEL,
    RAW_V5_ROUTER_SEQUENCE_CONTRACT_SHA256,
    RAW_V5_ROUTER_SEQUENCE_LAYOUT,
    RAW_V5_VOYAGE_TWO_TURN_MAX_TURNS,
    RawV5HMMTrainingContractVersion,
)
from hmm_sidecar.raw_v5.frozen_raw_v5 import (
    frozen_raw_v5_encoder,
)
from hmm_sidecar.raw_v5.state_representation import (
    VOYAGE_4_LARGE_RAW_ROUTER_V5_TWO_TURN,
)
from hmm_sidecar.raw_v5.serving_features import TOOL_CONTEXT_FEATURE_NAMES
from hmm_sidecar.raw_v5.core import canonical_json


class RawV5VoyageServingInputSchemaVersion(StrEnum):
    DIRECT_EVIDENCE_V1 = "hmm_router_raw_v5_voyage_serving_input_v1"
    LIVE_SEQUENCE_V1 = "hmm_router_raw_v5_voyage_live_sequence_serving_input_v1"


RAW_V5_VOYAGE_SERVING_INPUT_SCHEMA_VERSION = (
    RawV5VoyageServingInputSchemaVersion.DIRECT_EVIDENCE_V1
)
RAW_V5_VOYAGE_LIVE_SEQUENCE_SERVING_INPUT_SCHEMA_VERSION = (
    RawV5VoyageServingInputSchemaVersion.LIVE_SEQUENCE_V1
)
RAW_V5_TOOL_CONTEXT_SCHEMA_VERSION = "hmm_router_raw_v5_tool_context_v1"


class RawV5VoyageInputMode(StrEnum):
    DIRECT_EVIDENCE = "raw_v5_voyage_two_turn"
    LIVE_SEQUENCE = "raw_v5_voyage_two_turn_live_sequence"


class RawV5DocumentKind(StrEnum):
    USER = "raw_user_text"
    AGENT_WITH_LIVE_TOOL_TRACE = (
        "raw_agent_text_with_existing_live_structural_tool_trace"
    )


RAW_V5_VOYAGE_TWO_TURN_INPUT_MODE = RawV5VoyageInputMode.DIRECT_EVIDENCE
RAW_V5_VOYAGE_LIVE_SEQUENCE_INPUT_MODE = RawV5VoyageInputMode.LIVE_SEQUENCE
RAW_V5_CAUSAL_BOUNDARY = "current_raw_user_without_current_agent_evidence"

# These values are persisted by the trainer in the raw-V5 HMM bundle. Keep
# their exact stored semantics in this lightweight serving module instead of
# importing the full training entry point (and its native ML dependencies) at
# sidecar startup.
RAW_V5_ROUTER_HMM_AUTHORIZATION_FLAG = "--raw-v5-router-hmm"
RAW_V5_VOYAGE_HMM_TRAINING_CONTRACT_VERSION = RawV5HMMTrainingContractVersion.VOYAGE_V2


class RawV5ServingInputError(ValueError):
    """Raised when a live request cannot satisfy the raw-V5 model input."""


def raw_v5_voyage_two_turn_hmm_training_contract() -> dict[str, object]:
    raise NotImplementedError("not on the classifier serving path")


def raw_v5_voyage_two_turn_hmm_training_contract_sha256() -> str:
    raise NotImplementedError("not on the classifier serving path")


def raw_v5_voyage_two_turn_serving_input_contract() -> dict[str, object]:
    """Return the exact data/input contract for the raw Voyage serving lane.

    This records only model-consumed input semantics: the evidence object that
    supplies raw user/agent/tool values and the fixed provider document that is
    embedded.  Transport, collection, and unrelated infrastructure details are
    intentionally absent.
    """

    return {
        "schema_version": RAW_V5_VOYAGE_SERVING_INPUT_SCHEMA_VERSION,
        "source_evidence_schema_version": RAW_AGENT_FLOW_TURN_EVIDENCE_SCHEMA_VERSION,
        "sequence_layout": RAW_V5_ROUTER_SEQUENCE_LAYOUT,
        "sequence_representation": VOYAGE_4_LARGE_RAW_ROUTER_V5_TWO_TURN.value,
        "embedding_model": RAW_V5_ROUTER_EMBEDDING_MODEL,
        "embedding_dim": RAW_V5_ROUTER_EMBEDDING_DIM,
        "state_encoder_contract_sha256": frozen_raw_v5_encoder(
            VOYAGE_4_LARGE_RAW_ROUTER_V5_TWO_TURN
        ).sha256,
        "raw_v5_router_sequence_contract_sha256": (
            RAW_V5_ROUTER_SEQUENCE_CONTRACT_SHA256
        ),
        "document_serializer_version": frozen_raw_v5_encoder(
            VOYAGE_4_LARGE_RAW_ROUTER_V5_TWO_TURN
        ).serializer_version,
        "document_serializer_sha256": frozen_raw_v5_encoder(
            VOYAGE_4_LARGE_RAW_ROUTER_V5_TWO_TURN
        ).serializer_sha256,
        "turn_window_size": RAW_V5_VOYAGE_TWO_TURN_MAX_TURNS,
        "causal_boundary": RAW_V5_CAUSAL_BOUNDARY,
        "no_canonical_fallback": True,
        "tool_context": raw_v5_tool_context_input_contract(),
    }


def validate_raw_v5_voyage_two_turn_serving_input_contract(value: object) -> None:
    raise NotImplementedError("not on the classifier serving path")


def raw_v5_voyage_live_sequence_serving_input_contract() -> dict[str, object]:
    """Return v7's exact model-consumed raw document contract.

    The request continues to use the deployed conversation-message interface.
    This contract therefore pins only what reaches the model: raw user and
    agent documents, with the already-deployed live structural tool summary
    embedded in the agent document. It intentionally carries no
    request-transport, collection, or supplemental numeric tool-context
    contract.
    """

    return {
        "schema_version": RAW_V5_VOYAGE_LIVE_SEQUENCE_SERVING_INPUT_SCHEMA_VERSION,
        "sequence_layout": RAW_V5_ROUTER_SEQUENCE_LAYOUT,
        "sequence_representation": VOYAGE_4_LARGE_RAW_ROUTER_V5_TWO_TURN.value,
        "embedding_model": RAW_V5_ROUTER_EMBEDDING_MODEL,
        "embedding_dim": RAW_V5_ROUTER_EMBEDDING_DIM,
        "state_encoder_contract_sha256": frozen_raw_v5_encoder(
            VOYAGE_4_LARGE_RAW_ROUTER_V5_TWO_TURN
        ).sha256,
        "document_serializer_version": frozen_raw_v5_encoder(
            VOYAGE_4_LARGE_RAW_ROUTER_V5_TWO_TURN
        ).serializer_version,
        "document_serializer_sha256": frozen_raw_v5_encoder(
            VOYAGE_4_LARGE_RAW_ROUTER_V5_TWO_TURN
        ).serializer_sha256,
        "turn_window_size": RAW_V5_VOYAGE_TWO_TURN_MAX_TURNS,
        "model_consumed_documents": {
            "user_element": RawV5DocumentKind.USER.value,
            "agent_element": RawV5DocumentKind.AGENT_WITH_LIVE_TOOL_TRACE.value,
        },
        "causal_boundary": RAW_V5_CAUSAL_BOUNDARY,
        "no_canonical_fallback": True,
    }


RAW_V5_VOYAGE_LIVE_SEQUENCE_SERVING_INPUT_CONTRACT_SHA256 = hashlib.sha256(
    canonical_json(raw_v5_voyage_live_sequence_serving_input_contract()).encode("utf-8")
).hexdigest()


def validate_raw_v5_voyage_live_sequence_serving_input_contract(
    value: object,
) -> None:
    raise NotImplementedError("not on the classifier serving path")


def validate_raw_v5_voyage_two_turn_hmm_bundle(bundle: object) -> None:
    raise NotImplementedError("not on the classifier serving path")


def raw_v5_tool_context_input_contract() -> dict[str, object]:
    """Describe the classifier's explicit raw tool-context feature input.

    The candidate was trained with this vector from the Stage-1 raw trace.
    The persisted turn-evidence document retains the agent/tool text used by
    Voyage, but not every scalar used by the XGBoost feature block (notably
    individual tool result/error counts).  Rebuilding it from clipped live
    messages would mix source contracts, so v6 accepts this exact feature
    vector only when the upstream producer supplies it.
    """

    return {
        "schema_version": RAW_V5_TOOL_CONTEXT_SCHEMA_VERSION,
        "feature_names": list(TOOL_CONTEXT_FEATURE_NAMES),
        "feature_dim": len(TOOL_CONTEXT_FEATURE_NAMES),
    }


RAW_V5_VOYAGE_SERVING_INPUT_CONTRACT_SHA256 = hashlib.sha256(
    canonical_json(raw_v5_voyage_two_turn_serving_input_contract()).encode("utf-8")
).hexdigest()


def raw_v5_tool_context_features(raw: object) -> tuple[float, ...]:
    raise NotImplementedError("not on the classifier serving path")


def _raw_v5_voyage_two_turn_serving_evidence(
    raw_turn_evidence: object,
) -> list[RawAgentFlowTurnEvidence]:
    raise NotImplementedError("not on the classifier serving path")


def _raw_v5_voyage_two_turn_sequence_elements(
    ordered: list[RawAgentFlowTurnEvidence],
) -> list[SequenceElement]:
    raise NotImplementedError("not on the classifier serving path")


def raw_v5_voyage_two_turn_serving_evidence_and_turns(
    raw_turn_evidence: object,
) -> tuple[RawAgentFlowTurnEvidence, list[SequenceElement]]:
    raise NotImplementedError("not on the classifier serving path")


def raw_v5_voyage_two_turn_serving_turns(
    raw_turn_evidence: object,
) -> list[SequenceElement]:
    raise NotImplementedError("not on the classifier serving path")


__all__ = [
    "RAW_V5_CAUSAL_BOUNDARY",
    "RAW_V5_ROUTER_HMM_AUTHORIZATION_FLAG",
    "RAW_V5_VOYAGE_HMM_TRAINING_CONTRACT_VERSION",
    "RAW_V5_VOYAGE_LIVE_SEQUENCE_INPUT_MODE",
    "RAW_V5_VOYAGE_LIVE_SEQUENCE_SERVING_INPUT_CONTRACT_SHA256",
    "RAW_V5_VOYAGE_LIVE_SEQUENCE_SERVING_INPUT_SCHEMA_VERSION",
    "RAW_V5_VOYAGE_SERVING_INPUT_CONTRACT_SHA256",
    "RAW_V5_VOYAGE_SERVING_INPUT_SCHEMA_VERSION",
    "RAW_V5_VOYAGE_TWO_TURN_INPUT_MODE",
    "RAW_V5_TOOL_CONTEXT_SCHEMA_VERSION",
    "RawV5DocumentKind",
    "RawV5VoyageInputMode",
    "RawV5VoyageServingInputSchemaVersion",
    "RawV5ServingInputError",
    "raw_v5_tool_context_features",
    "raw_v5_tool_context_input_contract",
    "raw_v5_voyage_two_turn_hmm_training_contract",
    "raw_v5_voyage_two_turn_hmm_training_contract_sha256",
    "raw_v5_voyage_live_sequence_serving_input_contract",
    "raw_v5_voyage_two_turn_serving_evidence_and_turns",
    "raw_v5_voyage_two_turn_serving_input_contract",
    "raw_v5_voyage_two_turn_serving_turns",
    "validate_raw_v5_voyage_two_turn_hmm_bundle",
    "validate_raw_v5_voyage_live_sequence_serving_input_contract",
    "validate_raw_v5_voyage_two_turn_serving_input_contract",
]
