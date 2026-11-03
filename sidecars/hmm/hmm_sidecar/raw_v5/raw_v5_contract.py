"""Immutable contracts shared by raw V5 router sequence consumers."""

from __future__ import annotations

import hashlib
from enum import StrEnum
from hmm_sidecar.raw_v5.core import canonical_json
from hmm_sidecar.raw_v5.gemini_tokens import (
    GEMINI_EMBEDDING_MAX_INPUT_TOKENS,
    GEMINI_EMBEDDING_MAX_INPUT_BYTES,
)


class RawV5HMMTrainingContractVersion(StrEnum):
    """Lineage stamped into a raw-V5 HMM bundle by the trainer.

    ``GEMINI_V1`` is the frozen lineage of Gemini bundles fit before the raw
    matrix dropped its position column; it exists so those immutable artifacts
    can be validated against their own contract bytes, never re-stamped on new
    training, which always writes the current ``_V2`` versions.
    """

    GEMINI_V1 = "hmm_router_raw_v5_gemini_embedding_2_hmm_training_contract_v1"
    GEMINI_V2 = "hmm_router_raw_v5_gemini_embedding_2_hmm_training_contract_v2"


class RawFeatureMatrix(StrEnum):
    """Raw HMM row layout a bundle was fit on.

    ``v2`` dropped the prefix-normalized position column, so a ``v1`` bundle's
    rows are one column wider and must not be reinterpreted under ``v2``.
    """

    GEMINI_EMBEDDING_2_ELEMENT_KIND_SLOTS_RELATIVE_POSITION_V1 = (
        "gemini_embedding_2_plus_element_kind_slots_plus_relative_position_v1"
    )
    GEMINI_EMBEDDING_2_ELEMENT_KIND_SLOTS_V2 = (
        "gemini_embedding_2_plus_element_kind_slots_v2"
    )


RAW_V5_GEMINI_EMBEDDING_2_MODEL = "google/gemini-embedding-2"
RAW_V5_GEMINI_EMBEDDING_2_DIM = 3072
RAW_V5_ROUTER_CLASSIFICATION_SCHEMA_VERSION = "hmm_router_stage2_turn_classification_v5"
RAW_V5_GEMINI_EMBEDDING_2_MAX_INPUT_TOKENS = GEMINI_EMBEDDING_MAX_INPUT_TOKENS
RAW_V5_GEMINI_EMBEDDING_2_MAX_INPUT_BYTES = GEMINI_EMBEDDING_MAX_INPUT_BYTES
# Model-input bytes: every document embeds this schema name, so it cannot change
# without retraining.
RAW_V5_TURN_WINDOW_DOCUMENT_SCHEMA_VERSION = (
    "hmm_router_raw_v5_voyage_turn_window_document_v1"
)
RAW_V5_ONE_TURN_MAX_TURNS = 1
# This is deliberately separate from the tool-aware raw V5 documents above.
# Its source projection contains only user text and persisted response blocks;
# the distinct 1/2/10-turn identities prevent both tool-aware and differently
# windowed Gemini vectors or artifacts from being reused.
RAW_V5_GEMINI_USER_AGENT_ONLY_STATE_DEFINITION = (
    "hmm-router-raw-v5-gemini-embedding-2-user-agent-only-v1"
)
RAW_V5_GEMINI_USER_AGENT_ONLY_SERIALIZER_VERSION = (
    "hmm_router_raw_v5_gemini_embedding_2_user_agent_only_serializer_v4"
)
# Per-message store: each user message and each agent reply is embedded once,
# alone. Lookbacks are assembled from the stored vectors, so this single
# identity replaces one embedding run per window length.
RAW_V5_GEMINI_ELEMENT_STATE_DEFINITION = (
    "hmm-router-raw-v5-gemini-embedding-2-element-v1"
)
RAW_V5_GEMINI_ELEMENT_SERIALIZER_VERSION = (
    "hmm_router_raw_v5_gemini_embedding_2_element_serializer_v1"
)


def raw_v5_gemini_user_agent_only_model_input_contract() -> dict[str, object]:
    """Return Gemini's tool-free U/A-pair turn-window input contract.

    This is an additive training representation, not an alternate rendering
    of the existing raw V5 tool-aware sequence. It reads only the persisted
    raw user prompt and agent response blocks. Tool traces and the separate
    compaction artifact are neither required nor consulted.
    """

    return {
        "schema_version": RAW_V5_GEMINI_USER_AGENT_ONLY_SERIALIZER_VERSION,
        "document_schema_version": RAW_V5_TURN_WINDOW_DOCUMENT_SCHEMA_VERSION,
        "state_definition": RAW_V5_GEMINI_USER_AGENT_ONLY_STATE_DEFINITION,
        "embedding_model": RAW_V5_GEMINI_EMBEDDING_2_MODEL,
        "embedding_dimension": RAW_V5_GEMINI_EMBEDDING_2_DIM,
        "source_classification_schema": RAW_V5_ROUTER_CLASSIFICATION_SCHEMA_VERSION,
        "source_turn_evidence_schema": "agent_flow_raw_turn_evidence_v1",
        "projection": {
            "user_element": "exact_raw_agent_flow_turn_evidence.user_prompt",
            "agent_element": (
                "exact_raw_agent_flow_turn_evidence.agent_response_blocks_only"
            ),
            "tool_action_trace": "not_read_and_excluded_from_embedding_text",
            "tool_compaction": "not_read_and_not_required",
            "label_fields": "excluded_from_embedding_text",
        },
        "window": {
            "maximum_turns": RAW_V5_ONE_TURN_MAX_TURNS,
            "maximum_completed_prior_turns": 0,
            "current_turn": "raw_user_plus_raw_agent_response_blocks_after_boundary",
            "document_format": "deterministic_compact_json",
            "turn_fields": [
                "turn_offset",
                "turn_marker",
                "user_input",
                "agent_output",
            ],
        },
        "document_size_policy": {
            "maximum_serialized_bytes": RAW_V5_GEMINI_EMBEDDING_2_MAX_INPUT_BYTES,
            "assumed_token_budget": RAW_V5_GEMINI_EMBEDDING_2_MAX_INPUT_TOKENS,
            "provider_maximum_input_tokens": 8_192,
            "scope": "raw_v5_gemini_embedding_2_user_agent_only_documents",
            "window_overflow": "not_applicable_no_completed_history",
            "current_turn_overflow": (
                "retain_utf8_prefixes_of_current_user_and_available_agent_fields"
            ),
            "truncation_marker": "deterministic_and_included_inside_the_json_field",
            "residual_overflow": "fail_closed",
        },
        "causal_boundary": {
            "user_element": "current_raw_user_only",
            "agent_element": "same_current_raw_user_plus_agent_response_blocks",
            "future_agent_response": "forbidden",
            "window_reset": "every_conversation",
        },
        "no_canonical_fallback": True,
        "raw_v5_storage_mutated": False,
    }


def raw_v5_gemini_element_model_input_contract() -> dict[str, object]:
    """Return the per-message contract: every U and A document embedded alone.

    The user document is byte-identical to the one-round routing document. The
    agent document carries only the response blocks, unlike the one-round
    agent element, which embeds the user turn and the reply together.
    """

    contract = raw_v5_gemini_user_agent_only_model_input_contract()
    contract.update(
        schema_version=RAW_V5_GEMINI_ELEMENT_SERIALIZER_VERSION,
        state_definition=RAW_V5_GEMINI_ELEMENT_STATE_DEFINITION,
        window={
            "maximum_turns": RAW_V5_ONE_TURN_MAX_TURNS,
            "maximum_completed_prior_turns": 0,
            "user_document": "one_round_routing_document_current_raw_user_only",
            "agent_document": "raw_agent_reply_response_blocks_text_only",
            "lookback": "assembled_downstream_from_stored_per_message_vectors",
        },
        causal_boundary={
            "user_element": "current_raw_user_only",
            "agent_element": "current_raw_agent_response_blocks_only",
            "future_agent_response": "forbidden",
            "window_reset": "every_conversation",
        },
    )
    contract["document_size_policy"] = {
        **contract["document_size_policy"],
        "current_turn_overflow": "retain_marked_utf8_prefix_of_each_document_alone",
    }
    return contract


RAW_V5_GEMINI_ELEMENT_INPUT_CONTRACT_SHA256 = hashlib.sha256(
    canonical_json(raw_v5_gemini_element_model_input_contract()).encode("utf-8")
).hexdigest()
