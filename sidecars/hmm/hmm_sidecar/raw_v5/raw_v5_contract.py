"""Immutable contracts shared by raw V5 router sequence consumers."""

from __future__ import annotations

import hashlib
from collections.abc import Mapping
from enum import StrEnum
from typing import Literal
from hmm_sidecar.raw_v5.core import canonical_json, sha256_text
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

    VOYAGE_V2 = "hmm_router_raw_v5_voyage_hmm_training_contract_v2"
    GEMINI_V1 = "hmm_router_raw_v5_gemini_embedding_2_hmm_training_contract_v1"
    GEMINI_V2 = "hmm_router_raw_v5_gemini_embedding_2_hmm_training_contract_v2"


class RawFeatureMatrix(StrEnum):
    """Raw HMM row layout a bundle was fit on.

    ``v2`` dropped the prefix-normalized position column, so a ``v1`` bundle's
    rows are one column wider and must not be reinterpreted under ``v2``.
    """

    VOYAGE_ELEMENT_KIND_SLOTS_V2 = "voyage_embedding_plus_element_kind_slots_v2"
    GEMINI_EMBEDDING_2_ELEMENT_KIND_SLOTS_RELATIVE_POSITION_V1 = (
        "gemini_embedding_2_plus_element_kind_slots_plus_relative_position_v1"
    )
    GEMINI_EMBEDDING_2_ELEMENT_KIND_SLOTS_V2 = (
        "gemini_embedding_2_plus_element_kind_slots_v2"
    )


RAW_V5_ROUTER_SEQUENCE_LAYOUT = "raw_agent_flow_v5_user_agent_v1"
RAW_V5_ROUTER_STATE_DEFINITION = "hmm-router-raw-agent-flow-v5-user-agent-v1"
RAW_V5_ROUTER_SERIALIZER_VERSION = "hmm_router_raw_agent_flow_v5_user_agent_serializer_v1"
RAW_V5_AGENT_RESPONSE_CHRONOLOGY_CONTRACT_VERSION: Literal[
    "raw_v5_agent_response_chronology_v1"
] = "raw_v5_agent_response_chronology_v1"
RAW_V5_ROUTER_EMBEDDING_MODEL = "voyageai/voyage-4-large"
RAW_V5_ROUTER_EMBEDDING_DIM = 1024
# Gemini uses the same exact raw-V5 source and causal turn-window document
# serializer as Voyage.  Its encoder identity is intentionally independent:
# vectors must never be allowed to reuse a Voyage cache or artifact merely
# because the input document bytes are the same.
RAW_V5_GEMINI_EMBEDDING_2_MODEL = "google/gemini-embedding-2"
RAW_V5_GEMINI_EMBEDDING_2_DIM = 3072
RAW_V5_ROUTER_MAX_INPUT_TOKENS = 32_000
RAW_V5_ROUTER_CLASSIFICATION_SCHEMA_VERSION = "hmm_router_stage2_turn_classification_v5"
RAW_V5_MODEL_INPUT_PROJECTION_SCHEMA_VERSION = (
    "hmm_router_raw_v5_structural_overflow_projection_v1"
)
RAW_V5_MODEL_INPUT_STATE_DEFINITION = (
    "hmm-router-raw-agent-flow-v5-user-agent-structural-overflow-v1"
)
RAW_V5_MODEL_INPUT_SERIALIZER_VERSION = (
    "hmm_router_raw_v5_model_input_structural_overflow_serializer_v1"
)
RAW_V5_MODEL_INPUT_MAX_CHARS = 20_000
RAW_V5_GEMINI_EMBEDDING_2_MAX_INPUT_TOKENS = GEMINI_EMBEDDING_MAX_INPUT_TOKENS
RAW_V5_GEMINI_EMBEDDING_2_MAX_INPUT_BYTES = GEMINI_EMBEDDING_MAX_INPUT_BYTES
RAW_V5_MODEL_INPUT_STRUCTURED_LITERAL_MIN_CHARS = 512
RAW_V5_MODEL_INPUT_CODE_EXCERPT_CHARS = 4_000
RAW_V5_MODEL_INPUT_STRING_SAMPLE_CHARS = 512
RAW_V5_MODEL_INPUT_SAMPLE_MAX_DEPTH = 8
RAW_V5_MODEL_INPUT_SCHEMA_MAX_DEPTH = 1
RAW_V5_MODEL_INPUT_SCHEMA_MAX_FIELDS = 16
RAW_V5_MODEL_INPUT_SCHEMA_MAX_ITEMS = 2_048
# The existing raw-V5 Voyage representation embeds one raw U/A element at a
# time.  These are deliberately separate contracts for bounded causal round
# windows: changing a provider document's history changes its vector geometry,
# so it must never reuse the one-element cache or artifact lineage.
RAW_V5_VOYAGE_TURN_WINDOW_DOCUMENT_SCHEMA_VERSION = (
    "hmm_router_raw_v5_voyage_turn_window_document_v1"
)
RAW_V5_VOYAGE_ONE_TURN_MAX_TURNS = 1
RAW_V5_VOYAGE_TWO_TURN_MAX_TURNS = 2
RAW_V5_VOYAGE_TEN_TURN_MAX_TURNS = 10
RAW_V5_VOYAGE_ONE_TURN_STATE_DEFINITION = (
    "hmm-router-raw-v5-voyage-4-large-one-turn-v1"
)
RAW_V5_VOYAGE_TWO_TURN_STATE_DEFINITION = (
    "hmm-router-raw-v5-voyage-4-large-two-turn-v1"
)
RAW_V5_VOYAGE_TEN_TURN_STATE_DEFINITION = (
    "hmm-router-raw-v5-voyage-4-large-10-turn-v1"
)
RAW_V5_VOYAGE_ONE_TURN_SERIALIZER_VERSION = (
    "hmm_router_raw_v5_voyage_one_turn_serializer_v2"
)
RAW_V5_VOYAGE_TWO_TURN_SERIALIZER_VERSION = (
    "hmm_router_raw_v5_voyage_two_turn_serializer_v3"
)
RAW_V5_VOYAGE_TEN_TURN_SERIALIZER_VERSION = (
    "hmm_router_raw_v5_voyage_ten_turn_serializer_v3"
)
RAW_V5_GEMINI_ONE_TURN_STATE_DEFINITION = (
    "hmm-router-raw-v5-gemini-embedding-2-one-turn-v1"
)
RAW_V5_GEMINI_TWO_TURN_STATE_DEFINITION = (
    "hmm-router-raw-v5-gemini-embedding-2-two-turn-v1"
)
RAW_V5_GEMINI_TEN_TURN_STATE_DEFINITION = (
    "hmm-router-raw-v5-gemini-embedding-2-10-turn-v1"
)
RAW_V5_GEMINI_ONE_TURN_SERIALIZER_VERSION = (
    "hmm_router_raw_v5_gemini_embedding_2_one_turn_serializer_v5"
)
RAW_V5_GEMINI_TWO_TURN_SERIALIZER_VERSION = (
    "hmm_router_raw_v5_gemini_embedding_2_two_turn_serializer_v5"
)
RAW_V5_GEMINI_TEN_TURN_SERIALIZER_VERSION = (
    "hmm_router_raw_v5_gemini_embedding_2_ten_turn_serializer_v5"
)
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
# The same per-message store over voyage-code-4 (32,000-token context). Its
# documents are plain message text; counting uses the pinned voyage-4-large
# tokenizer, so a 2,000-token margin covers voyage-code-4's unpublished one.
RAW_V5_VOYAGE_CODE_4_EMBEDDING_MODEL = "voyageai/voyage-code-4"
RAW_V5_VOYAGE_CODE_4_ELEMENT_MAX_INPUT_TOKENS = 30_000
RAW_V5_VOYAGE_CODE_4_ELEMENT_STATE_DEFINITION = (
    "hmm-router-raw-v5-voyage-code-4-element-v1"
)
RAW_V5_VOYAGE_CODE_4_ELEMENT_SERIALIZER_VERSION = (
    "hmm_router_raw_v5_voyage_code_4_element_serializer_v1"
)
RAW_V5_GEMINI_USER_AGENT_ONLY_TWO_TURN_STATE_DEFINITION = (
    "hmm-router-raw-v5-gemini-embedding-2-user-agent-only-two-turn-v1"
)
RAW_V5_GEMINI_USER_AGENT_ONLY_TEN_TURN_STATE_DEFINITION = (
    "hmm-router-raw-v5-gemini-embedding-2-user-agent-only-10-turn-v1"
)
RAW_V5_GEMINI_USER_AGENT_ONLY_TWO_TURN_SERIALIZER_VERSION = (
    "hmm_router_raw_v5_gemini_embedding_2_user_agent_only_two_turn_serializer_v4"
)
RAW_V5_GEMINI_USER_AGENT_ONLY_TEN_TURN_SERIALIZER_VERSION = (
    "hmm_router_raw_v5_gemini_embedding_2_user_agent_only_ten_turn_serializer_v4"
)
# The tool-free projection carrying the immediately preceding element, which
# is the timeframe prod's Gemini Last State classifier was trained on. A
# document's history is part of its vector geometry, so this carries its own
# identity instead of reusing the current-element-only or round-windowed
# caches and artifacts.
RAW_V5_GEMINI_USER_AGENT_ONLY_LAST_STATE_STATE_DEFINITION = (
    "hmm-router-raw-v5-gemini-embedding-2-user-agent-only-last-state-v1"
)
RAW_V5_GEMINI_USER_AGENT_ONLY_LAST_STATE_SERIALIZER_VERSION = (
    "hmm_router_raw_v5_gemini_embedding_2_user_agent_only_last_state_serializer_v4"
)
RAW_V5_PREVIOUS_ELEMENT_DOCUMENT_SCHEMA_VERSION = (
    "hmm_router_raw_v5_previous_element_document_v1"
)
QWEN_RAW_V5_TEXT_REPRESENTATION = "raw-v5-text-v3"
QWEN_RAW_V5_TEXT_LEGACY_REPRESENTATION = "raw-v5-text-v1"
QWEN_RAW_V5_TEXT_INPUT_SLUG = "qwen3.5-9b-raw-v5-text-v3"
QWEN_RAW_V5_TEXT_INPUT_SCHEMA_VERSION = (
    "hmm_router_qwen_raw_v5_text_input_contract_v3"
)
QWEN_RAW_V5_TEXT_CORPUS_SCHEMA_VERSION = (
    "hmm_router_raw_v5_text_sequence_corpus_v3"
)
QWEN_RAW_V5_TEXT_TARGETS_SCHEMA_VERSION = (
    "hmm_router_complexity_raw_v5_text_sequence_targets_v3"
)
QWEN_RAW_V5_TEXT_DATASET_METADATA_SCHEMA_VERSION = (
    "hmm_router_complexity_raw_v5_text_dataset_v3"
)
QWEN_RAW_V5_TEXT_DATASET_PIPELINE_MODE = "router_qwen_9b_raw_v5_text_v3"
RAW_V5_FEEDBACK_SEQUENCE_SERIALIZER_VERSION = (
    "hmm_router_raw_v5_feedback_sequence_serializer_v3"
)
RAW_V5_GEMINI_FEEDBACK_SEQUENCE_SERIALIZER_VERSION = (
    "hmm_router_raw_v5_gemini_embedding_2_feedback_sequence_serializer_v5"
)
RAW_V5_FEEDBACK_GOLD_PRODUCER = "ml_dev.hmm_router.raw_v5_feedback_gold"
RAW_V5_FEEDBACK_FEATURE_BUILD_MODE = (
    "raw_v5_feedback_direct_embedding_structural_overflow_v1"
)
RAW_V5_FEEDBACK_SEQUENCE_GOLD_FILENAME = "raw_v5_feedback_sequence_gold.npz"


def validate_raw_v5_agent_response_chronology_contract(
    metadata: Mapping[str, object], *, context: str
) -> None:
    raise NotImplementedError("not on the classifier serving path")


def raw_v5_router_sequence_contract() -> dict[str, object]:
    """Return the immutable, label-free raw V5 source sequence contract."""

    return {
        "schema_version": RAW_V5_ROUTER_SERIALIZER_VERSION,
        "source_classification_schema": RAW_V5_ROUTER_CLASSIFICATION_SCHEMA_VERSION,
        "sequence_layout": RAW_V5_ROUTER_SEQUENCE_LAYOUT,
        "state_definition": RAW_V5_ROUTER_STATE_DEFINITION,
        "embedding_model": RAW_V5_ROUTER_EMBEDDING_MODEL,
        "embedding_dimension": RAW_V5_ROUTER_EMBEDDING_DIM,
        "max_input_tokens": RAW_V5_ROUTER_MAX_INPUT_TOKENS,
        "projection": {
            "user_element": "exact_raw_agent_flow_turn_evidence.user_prompt",
            "agent_element": (
                "exact_raw_agent_flow_turn_evidence.agent_response_blocks_plus_"
                "persisted_tool_action_trace.trace"
            ),
            "tool_provenance_fields": "excluded_from_embedding_text",
            "label_fields": "excluded_from_embedding_text",
        },
        "causal_boundary": (
            "target_label_attaches_to_user_element_and_prefix_ends_at_user_element"
        ),
        "current_agent_response_policy": "strictly_after_its_user_routing_boundary",
        "no_canonical_fallback": True,
        "no_truncation": True,
    }


RAW_V5_ROUTER_SEQUENCE_CONTRACT_SHA256 = hashlib.sha256(
    canonical_json(raw_v5_router_sequence_contract()).encode("utf-8")
).hexdigest()


def raw_v5_model_input_projection_contract() -> dict[str, object]:
    """Return the shared deterministic overflow-only model-input contract."""

    return {
        "schema_version": RAW_V5_MODEL_INPUT_PROJECTION_SCHEMA_VERSION,
        "base_raw_v5_router_sequence_contract_sha256": (
            RAW_V5_ROUTER_SEQUENCE_CONTRACT_SHA256
        ),
        "activation": {
            "scope": "individual_raw_v5_sequence_element",
            "source_chars_greater_than": RAW_V5_MODEL_INPUT_MAX_CHARS,
        },
        "maximum_projected_chars": RAW_V5_MODEL_INPUT_MAX_CHARS,
        "structured_literal_reducer": {
            "syntaxes": ["json", "python_literal"],
            "minimum_source_chars": RAW_V5_MODEL_INPUT_STRUCTURED_LITERAL_MIN_CHARS,
            "lossless_first_representation": (
                "reversible_minified_json_with_uniform_object_array_tables"
            ),
            "still_oversized_representation": (
                "recursive_schema_counts_plus_exact_first_representative_item"
            ),
            "source_span_sha256_recorded": True,
            "schema_max_depth": RAW_V5_MODEL_INPUT_SCHEMA_MAX_DEPTH,
            "schema_max_fields": RAW_V5_MODEL_INPUT_SCHEMA_MAX_FIELDS,
            "schema_max_items_scanned": RAW_V5_MODEL_INPUT_SCHEMA_MAX_ITEMS,
            "long_string_sample_chars": RAW_V5_MODEL_INPUT_STRING_SAMPLE_CHARS,
            "representative_sample_max_depth": RAW_V5_MODEL_INPUT_SAMPLE_MAX_DEPTH,
        },
        "fenced_code_overflow_reducer": {
            "representation": "exact_head_tail_excerpt_with_source_hash",
            "maximum_excerpt_chars": RAW_V5_MODEL_INPUT_CODE_EXCERPT_CHARS,
            "ast_body_deletion": False,
            "paraphrase": False,
        },
        "final_unstructured_overflow_reducer": (
            "exact_head_tail_excerpt_with_source_and_pre_cap_hashes"
        ),
        "projected_element_source_chars_and_sha256_recorded": True,
        "prose_outside_reduced_spans": "unchanged_until_final_hard_cap",
        "parenthetical_text_policy": "never_targeted",
        "learned_model": False,
        "neural_compression": False,
        "canonicalization": False,
        "raw_v5_storage_mutated": False,
    }


RAW_V5_MODEL_INPUT_PROJECTION_CONTRACT_SHA256 = hashlib.sha256(
    canonical_json(raw_v5_model_input_projection_contract()).encode("utf-8")
).hexdigest()


RAW_V5_VOYAGE_ELEMENT_SERIALIZER_VERSION = (
    "hmm_router_raw_v5_voyage_element_token_serializer_v1"
)
RAW_V5_VOYAGE_ELEMENT_STATE_DEFINITION = (
    "hmm-router-raw-v5-voyage-element-token-budget-v1"
)


def raw_v5_voyage_element_model_input_contract() -> dict[str, object]:
    """Separate Voyage's token-limited input from Qwen's frozen character cap."""

    from hmm_sidecar.raw_v5.voyage_tokens import (
        VOYAGE_MAX_INPUT_TOKENS,
        VOYAGE_TOKENIZER_REPOSITORY,
        VOYAGE_TOKENIZER_REVISION,
        VOYAGE_TOKENIZER_SHA256,
    )

    return {
        "schema_version": RAW_V5_VOYAGE_ELEMENT_SERIALIZER_VERSION,
        "state_definition": RAW_V5_VOYAGE_ELEMENT_STATE_DEFINITION,
        "embedding_model": RAW_V5_ROUTER_EMBEDDING_MODEL,
        "maximum_input_tokens": VOYAGE_MAX_INPUT_TOKENS,
        "tokenizer": {
            "repository": VOYAGE_TOKENIZER_REPOSITORY,
            "revision": VOYAGE_TOKENIZER_REVISION,
            "sha256": VOYAGE_TOKENIZER_SHA256,
            "add_special_tokens": True,
        },
        "input_within_budget": "preserve_exact_source_text",
        "overflow": "exact_source_head_tail_with_explicit_marker_verified_by_tokenizer",
        "projection_scope": "individual_raw_v5_sequence_element",
        "raw_v5_storage_mutated": False,
    }


RAW_V5_VOYAGE_ELEMENT_INPUT_CONTRACT_SHA256 = sha256_text(
    canonical_json(raw_v5_voyage_element_model_input_contract())
)


def _raw_v5_voyage_turn_window_constants(
    max_turns: int,
) -> tuple[str, str]:
    """Return the separately pinned state and serializer identity for a window."""

    if isinstance(max_turns, bool) or not isinstance(max_turns, int):
        raise ValueError(
            "raw V5 Voyage turn-window max_turns must be an integer; "
            f"got {max_turns!r}"
        )
    if max_turns == RAW_V5_VOYAGE_ONE_TURN_MAX_TURNS:
        return (
            RAW_V5_VOYAGE_ONE_TURN_STATE_DEFINITION,
            RAW_V5_VOYAGE_ONE_TURN_SERIALIZER_VERSION,
        )
    if max_turns == RAW_V5_VOYAGE_TWO_TURN_MAX_TURNS:
        return (
            RAW_V5_VOYAGE_TWO_TURN_STATE_DEFINITION,
            RAW_V5_VOYAGE_TWO_TURN_SERIALIZER_VERSION,
        )
    if max_turns == RAW_V5_VOYAGE_TEN_TURN_MAX_TURNS:
        return (
            RAW_V5_VOYAGE_TEN_TURN_STATE_DEFINITION,
            RAW_V5_VOYAGE_TEN_TURN_SERIALIZER_VERSION,
        )
    raise ValueError(
        "raw V5 Voyage turn-window max_turns must be one of "
        f"{RAW_V5_VOYAGE_ONE_TURN_MAX_TURNS}, "
        f"{RAW_V5_VOYAGE_TWO_TURN_MAX_TURNS}, "
        f"{RAW_V5_VOYAGE_TEN_TURN_MAX_TURNS}; got {max_turns!r}"
    )


def _raw_v5_gemini_turn_window_constants(
    max_turns: int,
) -> tuple[str, str]:
    """Return the Gemini-specific identity for one shared raw-V5 window."""

    if isinstance(max_turns, bool) or not isinstance(max_turns, int):
        raise ValueError(
            "raw V5 Gemini turn-window max_turns must be an integer; "
            f"got {max_turns!r}"
        )
    if max_turns == RAW_V5_VOYAGE_ONE_TURN_MAX_TURNS:
        return (
            RAW_V5_GEMINI_ONE_TURN_STATE_DEFINITION,
            RAW_V5_GEMINI_ONE_TURN_SERIALIZER_VERSION,
        )
    if max_turns == RAW_V5_VOYAGE_TWO_TURN_MAX_TURNS:
        return (
            RAW_V5_GEMINI_TWO_TURN_STATE_DEFINITION,
            RAW_V5_GEMINI_TWO_TURN_SERIALIZER_VERSION,
        )
    if max_turns == RAW_V5_VOYAGE_TEN_TURN_MAX_TURNS:
        return (
            RAW_V5_GEMINI_TEN_TURN_STATE_DEFINITION,
            RAW_V5_GEMINI_TEN_TURN_SERIALIZER_VERSION,
        )
    raise ValueError(
        "raw V5 Gemini turn-window max_turns must be one of "
        f"{RAW_V5_VOYAGE_ONE_TURN_MAX_TURNS}, "
        f"{RAW_V5_VOYAGE_TWO_TURN_MAX_TURNS}, "
        f"{RAW_V5_VOYAGE_TEN_TURN_MAX_TURNS}; got {max_turns!r}"
    )


def _raw_v5_provider_turn_window_model_input_contract(
    *,
    max_turns: int,
    state_definition: str,
    serializer_version: str,
    embedding_model: str,
    embedding_dimension: int,
) -> dict[str, object]:
    """Build a provider-specific contract over the pinned shared documents."""

    return {
        "schema_version": serializer_version,
        # The document serialization is intentionally shared across providers;
        # only vector geometry and provider provenance differ.
        "document_schema_version": RAW_V5_VOYAGE_TURN_WINDOW_DOCUMENT_SCHEMA_VERSION,
        "state_definition": state_definition,
        "embedding_model": embedding_model,
        "embedding_dimension": embedding_dimension,
        "base_raw_v5_router_sequence_contract_sha256": (
            RAW_V5_ROUTER_SEQUENCE_CONTRACT_SHA256
        ),
        "window": {
            "maximum_turns": max_turns,
            "maximum_completed_prior_turns": max_turns - 1,
            "completed_turn": "paired_raw_user_and_raw_agent_elements",
            "ordering": "oldest_completed_turn_to_current_partial_turn",
            "document_format": "deterministic_compact_json",
            "turn_fields": [
                "turn_offset",
                "turn_marker",
                "user_input",
                "agent_output",
            ],
        },
        "document_size_policy": {
            "maximum_serialized_chars": RAW_V5_MODEL_INPUT_MAX_CHARS,
            "source_element_projection": (
                "always_apply_lossless_structural_json_python_literal_normalization_"
                "to_each_selected_raw_user_or_agent_element"
            ),
            "window_overflow": (
                "uniformly_lower_the_per_element_structural_projection_budget_"
                "for_every_selected_element"
            ),
            "selected_trailing_rounds": "never_dropped_for_window_budget",
            "residual_fixed_window_overflow": (
                "fail_closed_after_uniform_per_element_structural_projection"
            ),
        },
        "causal_boundary": {
            "user_element": (
                "up_to_maximum_completed_prior_raw_user_agent_turns_plus_current_raw_user"
            ),
            "agent_element": (
                "same_causal_window_plus_current_raw_agent_after_its_user_boundary"
            ),
            "future_agent_response": "forbidden",
            "window_reset": "every_conversation",
        },
        "model_input_projection": raw_v5_model_input_projection_contract(),
        "model_input_projection_sha256": (
            RAW_V5_MODEL_INPUT_PROJECTION_CONTRACT_SHA256
        ),
        "model_input_projection_scope": (
            "always_normalize_every_selected_raw_source_element_then_uniformly_"
            "tighten_each_selected_element_if_the_fixed_window_is_oversized"
        ),
        "no_canonical_fallback": True,
        "raw_v5_storage_mutated": False,
    }


def raw_v5_voyage_turn_window_model_input_contract(
    max_turns: int,
) -> dict[str, object]:
    """Return the causal provider-document contract for one Voyage window.

    The raw V5 source sequence remains unchanged.  This contract only defines
    the document supplied to Voyage for each source U/A element, so a model
    trained with a history window cannot be confused with the one-event raw
    V5 representation.
    """

    from hmm_sidecar.raw_v5.voyage_tokens import (
        VOYAGE_MAX_INPUT_TOKENS,
        VOYAGE_TOKENIZER_REPOSITORY,
        VOYAGE_TOKENIZER_REVISION,
        VOYAGE_TOKENIZER_SHA256,
    )

    state_definition, serializer_version = _raw_v5_voyage_turn_window_constants(
        max_turns
    )
    contract = _raw_v5_provider_turn_window_model_input_contract(
        max_turns=max_turns,
        state_definition=state_definition,
        serializer_version=serializer_version,
        embedding_model=RAW_V5_ROUTER_EMBEDDING_MODEL,
        embedding_dimension=RAW_V5_ROUTER_EMBEDDING_DIM,
    )
    policy = {
        "maximum_serialized_tokens": VOYAGE_MAX_INPUT_TOKENS,
        "tokenizer": {
            "repository": VOYAGE_TOKENIZER_REPOSITORY,
            "revision": VOYAGE_TOKENIZER_REVISION,
            "sha256": VOYAGE_TOKENIZER_SHA256,
            "add_special_tokens": True,
        },
        "source_element_projection": "preserve_exact_source_fields_when_window_fits",
        "window_overflow": "uniform_source_prefix_allocation_with_complete_json_token_count",
        "selected_trailing_rounds": "never_dropped_for_window_budget",
        "truncation_marker": "explicit_inside_each_clipped_field_and_counted",
        "residual_fixed_window_overflow": "fail_closed",
    }
    projection = {
        "schema_version": "hmm_router_raw_v5_voyage_window_token_projection_v1",
        **policy,
    }
    contract["document_size_policy"] = policy
    contract["model_input_projection"] = projection
    contract["model_input_projection_sha256"] = sha256_text(canonical_json(projection))
    contract["model_input_projection_scope"] = "voyage_raw_v5_window_token_budget_only"
    return contract


def raw_v5_gemini_turn_window_model_input_contract(
    max_turns: int,
) -> dict[str, object]:
    """Return Gemini's independent vector contract over raw-V5 window docs."""

    state_definition, serializer_version = _raw_v5_gemini_turn_window_constants(
        max_turns
    )
    contract = _raw_v5_provider_turn_window_model_input_contract(
        max_turns=max_turns,
        state_definition=state_definition,
        serializer_version=serializer_version,
        embedding_model=RAW_V5_GEMINI_EMBEDDING_2_MODEL,
        embedding_dimension=RAW_V5_GEMINI_EMBEDDING_2_DIM,
    )
    contract["document_size_policy"] = {
        "maximum_serialized_bytes": RAW_V5_GEMINI_EMBEDDING_2_MAX_INPUT_BYTES,
        "assumed_token_budget": RAW_V5_GEMINI_EMBEDDING_2_MAX_INPUT_TOKENS,
        "provider_maximum_input_tokens": 8_192,
        "scope": "raw_v5_gemini_embedding_2_turn_window_documents_only",
        "window_overflow": "drop_oldest_completed_turns_before_truncating_current_turn",
        "current_turn_overflow": (
            "retain_utf8_prefixes_of_current_user_and_available_agent_fields"
        ),
        "truncation_marker": "deterministic_and_included_inside_the_json_field",
        "future_agent_response": "forbidden",
        "residual_overflow": "fail_closed",
    }
    contract["model_input_projection_scope"] = (
        "gemini_embedding_2_raw_v5_window_only_8000_byte_prefix_projection_v5"
    )
    return contract


def _raw_v5_gemini_user_agent_only_constants(
    max_turns: int,
) -> tuple[str, str]:
    """Return the separately pinned identity for a no-tool U/A window."""

    if isinstance(max_turns, bool) or not isinstance(max_turns, int):
        raise ValueError(
            "raw V5 Gemini user-agent-only max_turns must be an integer; "
            f"got {max_turns!r}"
        )
    if max_turns == RAW_V5_VOYAGE_ONE_TURN_MAX_TURNS:
        return (
            RAW_V5_GEMINI_USER_AGENT_ONLY_STATE_DEFINITION,
            RAW_V5_GEMINI_USER_AGENT_ONLY_SERIALIZER_VERSION,
        )
    if max_turns == RAW_V5_VOYAGE_TWO_TURN_MAX_TURNS:
        return (
            RAW_V5_GEMINI_USER_AGENT_ONLY_TWO_TURN_STATE_DEFINITION,
            RAW_V5_GEMINI_USER_AGENT_ONLY_TWO_TURN_SERIALIZER_VERSION,
        )
    if max_turns == RAW_V5_VOYAGE_TEN_TURN_MAX_TURNS:
        return (
            RAW_V5_GEMINI_USER_AGENT_ONLY_TEN_TURN_STATE_DEFINITION,
            RAW_V5_GEMINI_USER_AGENT_ONLY_TEN_TURN_SERIALIZER_VERSION,
        )
    raise ValueError(
        "raw V5 Gemini user-agent-only max_turns must be one of "
        f"{RAW_V5_VOYAGE_ONE_TURN_MAX_TURNS}, "
        f"{RAW_V5_VOYAGE_TWO_TURN_MAX_TURNS}, "
        f"{RAW_V5_VOYAGE_TEN_TURN_MAX_TURNS}; got {max_turns!r}"
    )


def raw_v5_gemini_user_agent_only_model_input_contract(
    max_turns: int = RAW_V5_VOYAGE_ONE_TURN_MAX_TURNS,
) -> dict[str, object]:
    """Return Gemini's tool-free U/A-pair turn-window input contract.

    This is an additive training representation, not an alternate rendering
    of the existing raw V5 tool-aware sequence. It reads only the persisted
    raw user prompt and agent response blocks. Tool traces and the separate
    compaction artifact are neither required nor consulted.
    """

    state_definition, serializer_version = (
        _raw_v5_gemini_user_agent_only_constants(max_turns)
    )
    has_completed_history = max_turns > RAW_V5_VOYAGE_ONE_TURN_MAX_TURNS
    return {
        "schema_version": serializer_version,
        "document_schema_version": RAW_V5_VOYAGE_TURN_WINDOW_DOCUMENT_SCHEMA_VERSION,
        "state_definition": state_definition,
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
            "maximum_turns": max_turns,
            "maximum_completed_prior_turns": max_turns - 1,
            **(
                {
                    "completed_turn": (
                        "paired_raw_user_and_raw_agent_response_blocks"
                    ),
                    "ordering": "oldest_completed_turn_to_current_partial_turn",
                }
                if has_completed_history
                else {}
            ),
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
            "window_overflow": (
                "drop_oldest_completed_turns_before_truncating_current_turn"
                if has_completed_history
                else "not_applicable_no_completed_history"
            ),
            "current_turn_overflow": (
                "retain_utf8_prefixes_of_current_user_and_available_agent_fields"
            ),
            "truncation_marker": "deterministic_and_included_inside_the_json_field",
            "residual_overflow": "fail_closed",
        },
        "causal_boundary": {
            "user_element": (
                "up_to_maximum_completed_prior_raw_user_agent_response_only_"
                "turns_plus_current_raw_user"
                if has_completed_history
                else "current_raw_user_only"
            ),
            "agent_element": (
                "same_causal_window_plus_current_raw_agent_response_blocks"
                if has_completed_history
                else "same_current_raw_user_plus_agent_response_blocks"
            ),
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

    contract = raw_v5_gemini_user_agent_only_model_input_contract(
        RAW_V5_VOYAGE_ONE_TURN_MAX_TURNS
    )
    contract.update(
        schema_version=RAW_V5_GEMINI_ELEMENT_SERIALIZER_VERSION,
        state_definition=RAW_V5_GEMINI_ELEMENT_STATE_DEFINITION,
        window={
            "maximum_turns": RAW_V5_VOYAGE_ONE_TURN_MAX_TURNS,
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


def raw_v5_voyage_code_4_element_model_input_contract() -> dict[str, object]:
    """Return the voyage-code-4 per-message contract: plain U and A text, each alone."""

    contract = raw_v5_gemini_element_model_input_contract()
    contract.update(
        schema_version=RAW_V5_VOYAGE_CODE_4_ELEMENT_SERIALIZER_VERSION,
        state_definition=RAW_V5_VOYAGE_CODE_4_ELEMENT_STATE_DEFINITION,
        embedding_model=RAW_V5_VOYAGE_CODE_4_EMBEDDING_MODEL,
        embedding_dimension=RAW_V5_ROUTER_EMBEDDING_DIM,
        window={
            "maximum_turns": RAW_V5_VOYAGE_ONE_TURN_MAX_TURNS,
            "maximum_completed_prior_turns": 0,
            "user_document": "plain_raw_user_message_text_only",
            "agent_document": "raw_agent_reply_response_blocks_text_only",
            "lookback": "assembled_downstream_from_stored_per_message_vectors",
        },
        document_size_policy={
            "maximum_input_tokens": RAW_V5_VOYAGE_CODE_4_ELEMENT_MAX_INPUT_TOKENS,
            "provider_maximum_input_tokens": 32_000,
            "token_counter": "pinned_voyage_4_large_tokenizer",
            "overflow": "retain_measured_source_head_and_tail_with_marker",
            "residual_overflow": "fail_closed",
        },
    )
    return contract


def raw_v5_voyage_user_agent_only_model_input_contract(
    max_turns: int = RAW_V5_VOYAGE_ONE_TURN_MAX_TURNS,
) -> dict[str, object]:
    """Use the no-tool U/A source projection with Voyage's token budget."""

    contract = raw_v5_gemini_user_agent_only_model_input_contract(max_turns)
    voyage_window = raw_v5_voyage_turn_window_model_input_contract(max_turns)
    contract.update(
        schema_version="raw_v5_voyage_user_agent_only_token_window_v1",
        state_definition=f"raw_v5_voyage_user_agent_only_{max_turns}_turn",
        embedding_model=RAW_V5_ROUTER_EMBEDDING_MODEL,
        embedding_dimension=RAW_V5_ROUTER_EMBEDDING_DIM,
        document_size_policy=voyage_window["document_size_policy"],
    )
    return contract


def raw_v5_gemini_user_agent_only_last_state_model_input_contract() -> (
    dict[str, object]
):
    """Return Gemini's tool-free previous-element input contract.

    This reproduces the timeframe prod's Gemini Last State classifier was
    trained on -- the immediately preceding sequence element plus the current
    one -- over raw rather than canonical documents. The element before a
    routed user turn is that turn's predecessor agent response, so a user
    document carries one agent reply and never a future one. It is strictly
    less context than the two-round window, whose previous round also carries
    the previous user prompt.
    """

    return {
        "schema_version": RAW_V5_GEMINI_USER_AGENT_ONLY_LAST_STATE_SERIALIZER_VERSION,
        "document_schema_version": RAW_V5_PREVIOUS_ELEMENT_DOCUMENT_SCHEMA_VERSION,
        "state_definition": RAW_V5_GEMINI_USER_AGENT_ONLY_LAST_STATE_STATE_DEFINITION,
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
            "maximum_elements": 2,
            "maximum_previous_elements": 1,
            "previous_element": "immediately_preceding_raw_user_or_agent_element",
            "document_format": "deterministic_compact_json",
            "element_fields": [
                "element_offset",
                "element_marker",
                "element_kind",
                "element_text",
            ],
        },
        "document_size_policy": {
            "maximum_serialized_bytes": RAW_V5_GEMINI_EMBEDDING_2_MAX_INPUT_BYTES,
            "assumed_token_budget": RAW_V5_GEMINI_EMBEDDING_2_MAX_INPUT_TOKENS,
            "provider_maximum_input_tokens": 8_192,
            "scope": (
                "raw_v5_gemini_embedding_2_user_agent_only_last_state_documents"
            ),
            "window_overflow": "drop_previous_element_before_truncating_current",
            "current_turn_overflow": (
                "retain_utf8_prefixes_of_current_and_available_previous_fields"
            ),
            "truncation_marker": "deterministic_and_included_inside_the_json_field",
            "residual_overflow": "fail_closed",
        },
        "causal_boundary": {
            "user_element": "previous_raw_agent_element_plus_current_raw_user",
            "agent_element": "current_raw_user_plus_its_raw_response_blocks",
            "future_agent_response": "forbidden",
            "window_reset": "every_conversation",
        },
        "no_canonical_fallback": True,
        "raw_v5_storage_mutated": False,
    }


def raw_v5_voyage_two_turn_model_input_contract() -> dict[str, object]:
    """Return the fixed two-round raw-V5 Voyage provider-input contract."""

    return raw_v5_voyage_turn_window_model_input_contract(
        RAW_V5_VOYAGE_TWO_TURN_MAX_TURNS
    )


def raw_v5_voyage_one_turn_model_input_contract() -> dict[str, object]:
    """Return the uniformly normalized current-round Voyage input contract."""

    return raw_v5_voyage_turn_window_model_input_contract(
        RAW_V5_VOYAGE_ONE_TURN_MAX_TURNS
    )


def raw_v5_voyage_ten_turn_model_input_contract() -> dict[str, object]:
    """Return the fixed ten-round raw-V5 Voyage provider-input contract."""

    return raw_v5_voyage_turn_window_model_input_contract(
        RAW_V5_VOYAGE_TEN_TURN_MAX_TURNS
    )


RAW_V5_VOYAGE_ONE_TURN_INPUT_CONTRACT_SHA256 = hashlib.sha256(
    canonical_json(raw_v5_voyage_one_turn_model_input_contract()).encode("utf-8")
).hexdigest()
RAW_V5_VOYAGE_TWO_TURN_INPUT_CONTRACT_SHA256 = hashlib.sha256(
    canonical_json(raw_v5_voyage_two_turn_model_input_contract()).encode("utf-8")
).hexdigest()
RAW_V5_VOYAGE_TEN_TURN_INPUT_CONTRACT_SHA256 = hashlib.sha256(
    canonical_json(raw_v5_voyage_ten_turn_model_input_contract()).encode("utf-8")
).hexdigest()
RAW_V5_GEMINI_ONE_TURN_INPUT_CONTRACT_SHA256 = hashlib.sha256(
    canonical_json(
        raw_v5_gemini_turn_window_model_input_contract(
            RAW_V5_VOYAGE_ONE_TURN_MAX_TURNS
        )
    ).encode("utf-8")
).hexdigest()
RAW_V5_GEMINI_TWO_TURN_INPUT_CONTRACT_SHA256 = hashlib.sha256(
    canonical_json(
        raw_v5_gemini_turn_window_model_input_contract(
            RAW_V5_VOYAGE_TWO_TURN_MAX_TURNS
        )
    ).encode("utf-8")
).hexdigest()
RAW_V5_GEMINI_TEN_TURN_INPUT_CONTRACT_SHA256 = hashlib.sha256(
    canonical_json(
        raw_v5_gemini_turn_window_model_input_contract(
            RAW_V5_VOYAGE_TEN_TURN_MAX_TURNS
        )
    ).encode("utf-8")
).hexdigest()
RAW_V5_GEMINI_USER_AGENT_ONLY_INPUT_CONTRACT_SHA256 = hashlib.sha256(
    canonical_json(raw_v5_gemini_user_agent_only_model_input_contract()).encode("utf-8")
).hexdigest()
RAW_V5_GEMINI_ELEMENT_INPUT_CONTRACT_SHA256 = hashlib.sha256(
    canonical_json(raw_v5_gemini_element_model_input_contract()).encode("utf-8")
).hexdigest()
RAW_V5_VOYAGE_CODE_4_ELEMENT_INPUT_CONTRACT_SHA256 = hashlib.sha256(
    canonical_json(raw_v5_voyage_code_4_element_model_input_contract()).encode("utf-8")
).hexdigest()
RAW_V5_GEMINI_USER_AGENT_ONLY_TWO_TURN_INPUT_CONTRACT_SHA256 = hashlib.sha256(
    canonical_json(
        raw_v5_gemini_user_agent_only_model_input_contract(
            RAW_V5_VOYAGE_TWO_TURN_MAX_TURNS
        )
    ).encode("utf-8")
).hexdigest()
RAW_V5_GEMINI_USER_AGENT_ONLY_TEN_TURN_INPUT_CONTRACT_SHA256 = hashlib.sha256(
    canonical_json(
        raw_v5_gemini_user_agent_only_model_input_contract(
            RAW_V5_VOYAGE_TEN_TURN_MAX_TURNS
        )
    ).encode("utf-8")
).hexdigest()
RAW_V5_GEMINI_USER_AGENT_ONLY_LAST_STATE_INPUT_CONTRACT_SHA256 = hashlib.sha256(
    canonical_json(
        raw_v5_gemini_user_agent_only_last_state_model_input_contract()
    ).encode("utf-8")
).hexdigest()


def qwen_raw_v5_text_input_contract() -> dict[str, object]:
    """Return Qwen's raw-V5 text and label-alignment contract."""

    return {
        "schema_version": QWEN_RAW_V5_TEXT_INPUT_SCHEMA_VERSION,
        "corpus_schema_version": QWEN_RAW_V5_TEXT_CORPUS_SCHEMA_VERSION,
        "targets_schema_version": QWEN_RAW_V5_TEXT_TARGETS_SCHEMA_VERSION,
        "input": "exact_raw_v5_causal_user_agent_tool_evidence_text",
        "sequence_layout": RAW_V5_ROUTER_SEQUENCE_LAYOUT,
        "base_raw_v5_router_sequence_contract_sha256": (
            RAW_V5_ROUTER_SEQUENCE_CONTRACT_SHA256
        ),
        "corpus_text_policy": "untruncated_source_text",
        "tokenizer": "selected_qwen_model_at_pinned_revision",
        "context_window": "exact_causal_token_suffix_at_configured_qwen_max_length",
        "prediction_boundary": "end_of_labeled_raw_user_turn",
        "current_agent_response_policy": "strictly_after_its_user_routing_boundary",
        "no_canonical_fallback": True,
        "no_embedding_stage": True,
        "no_neural_compression": True,
    }


QWEN_RAW_V5_TEXT_INPUT_CONTRACT_SHA256 = hashlib.sha256(
    canonical_json(qwen_raw_v5_text_input_contract()).encode("utf-8")
).hexdigest()


def raw_v5_feedback_sequence_contract() -> dict[str, object]:
    """Return the separate raw-runtime-feedback projection contract.

    Runtime thumbs are offline human complexity labels, but their evidence is
    captured by the router's current route-context producer rather than Agent
    Flow Stage 2.  This deliberately remains a distinct contract: it shares
    V5's raw user/agent layout and Voyage vector space without pretending that
    a bounded serving snapshot is an Agent Flow V5 classification record.
    """

    return {
        "schema_version": RAW_V5_FEEDBACK_SEQUENCE_SERIALIZER_VERSION,
        "source_context_schema": "hmm_router_route_context_v6",
        "sequence_layout": RAW_V5_ROUTER_SEQUENCE_LAYOUT,
        "embedding_model": RAW_V5_ROUTER_EMBEDDING_MODEL,
        "embedding_dimension": RAW_V5_ROUTER_EMBEDDING_DIM,
        "base_raw_v5_router_sequence_contract_sha256": (
            RAW_V5_ROUTER_SEQUENCE_CONTRACT_SHA256
        ),
        "model_input_projection": raw_v5_voyage_element_model_input_contract(),
        "model_input_projection_sha256": (
            RAW_V5_VOYAGE_ELEMENT_INPUT_CONTRACT_SHA256
        ),
        "projection": {
            "user_element": "exact_raw_route_context_v6.conversation_messages user text",
            "agent_element": (
                "exact_raw_route_context_v6 assistant/tool evidence between "
                "adjacent user routing boundaries"
            ),
            "system_developer_messages": "excluded_to_match_v5_user_agent_layout",
            "label_fields": "excluded_from_embedding_text",
        },
        "causal_boundary": (
            "human_complexity_label_attaches_to_last_user_element_and_prefix_ends_at_user"
        ),
        "no_canonical_fallback": True,
        "no_builder_truncation_within_token_budget": True,
        "source_snapshot_policy": (
            "use_exact_captured_route_context_v6_snapshot; upstream serving "
            "capture_may_have_been_bounded"
        ),
    }


RAW_V5_FEEDBACK_SEQUENCE_CONTRACT_SHA256 = hashlib.sha256(
    canonical_json(raw_v5_feedback_sequence_contract()).encode("utf-8")
).hexdigest()


def raw_v5_gemini_feedback_sequence_contract() -> dict[str, object]:
    """Return the raw-runtime-feedback contract for Gemini Embedding 2.

    This preserves the raw route-context evidence and causal boundary while
    applying the same Gemini-only byte-bounded window projection used by Stage 2.
    """

    return {
        "schema_version": RAW_V5_GEMINI_FEEDBACK_SEQUENCE_SERIALIZER_VERSION,
        "source_context_schema": "hmm_router_route_context_v6",
        "sequence_layout": RAW_V5_ROUTER_SEQUENCE_LAYOUT,
        "embedding_model": RAW_V5_GEMINI_EMBEDDING_2_MODEL,
        "embedding_dimension": RAW_V5_GEMINI_EMBEDDING_2_DIM,
        "base_raw_v5_router_sequence_contract_sha256": (
            RAW_V5_ROUTER_SEQUENCE_CONTRACT_SHA256
        ),
        "model_input_projection": raw_v5_model_input_projection_contract(),
        "model_input_projection_sha256": (
            RAW_V5_MODEL_INPUT_PROJECTION_CONTRACT_SHA256
        ),
        "gemini_window_size_policy": {
            "maximum_serialized_bytes": RAW_V5_GEMINI_EMBEDDING_2_MAX_INPUT_BYTES,
            "assumed_token_budget": RAW_V5_GEMINI_EMBEDDING_2_MAX_INPUT_TOKENS,
            "window_overflow": "drop_oldest_completed_turns_first",
            "current_turn_overflow": "retain_marked_utf8_prefixes",
        },
        "projection": {
            "user_element": "exact_raw_route_context_v6.conversation_messages user text",
            "agent_element": (
                "exact_raw_route_context_v6 assistant/tool evidence between "
                "adjacent user routing boundaries"
            ),
            "system_developer_messages": "excluded_to_match_v5_user_agent_layout",
            "label_fields": "excluded_from_embedding_text",
        },
        "causal_boundary": (
            "human_complexity_label_attaches_to_last_user_element_and_prefix_ends_at_user"
        ),
        "no_canonical_fallback": True,
        "builder_truncation": "gemini_8000_byte_marked_prefix_projection_v5",
        "source_snapshot_policy": (
            "use_exact_captured_route_context_v6_snapshot; upstream serving "
            "capture_may_have_been_bounded"
        ),
    }


RAW_V5_GEMINI_FEEDBACK_SEQUENCE_CONTRACT_SHA256 = hashlib.sha256(
    canonical_json(raw_v5_gemini_feedback_sequence_contract()).encode("utf-8")
).hexdigest()


__all__ = [
    "QWEN_RAW_V5_TEXT_CORPUS_SCHEMA_VERSION",
    "QWEN_RAW_V5_TEXT_DATASET_METADATA_SCHEMA_VERSION",
    "QWEN_RAW_V5_TEXT_DATASET_PIPELINE_MODE",
    "QWEN_RAW_V5_TEXT_INPUT_CONTRACT_SHA256",
    "QWEN_RAW_V5_TEXT_INPUT_SCHEMA_VERSION",
    "QWEN_RAW_V5_TEXT_INPUT_SLUG",
    "QWEN_RAW_V5_TEXT_LEGACY_REPRESENTATION",
    "QWEN_RAW_V5_TEXT_REPRESENTATION",
    "QWEN_RAW_V5_TEXT_TARGETS_SCHEMA_VERSION",
    "RAW_V5_ROUTER_CLASSIFICATION_SCHEMA_VERSION",
    "RAW_V5_GEMINI_EMBEDDING_2_DIM",
    "RAW_V5_GEMINI_EMBEDDING_2_MAX_INPUT_TOKENS",
    "RAW_V5_GEMINI_EMBEDDING_2_MAX_INPUT_BYTES",
    "RAW_V5_GEMINI_EMBEDDING_2_MODEL",
    "RAW_V5_GEMINI_FEEDBACK_SEQUENCE_CONTRACT_SHA256",
    "RAW_V5_GEMINI_FEEDBACK_SEQUENCE_SERIALIZER_VERSION",
    "RAW_V5_GEMINI_ONE_TURN_INPUT_CONTRACT_SHA256",
    "RAW_V5_GEMINI_ONE_TURN_SERIALIZER_VERSION",
    "RAW_V5_GEMINI_ONE_TURN_STATE_DEFINITION",
    "RAW_V5_GEMINI_TEN_TURN_INPUT_CONTRACT_SHA256",
    "RAW_V5_GEMINI_TEN_TURN_SERIALIZER_VERSION",
    "RAW_V5_GEMINI_TEN_TURN_STATE_DEFINITION",
    "RAW_V5_GEMINI_TWO_TURN_INPUT_CONTRACT_SHA256",
    "RAW_V5_GEMINI_TWO_TURN_SERIALIZER_VERSION",
    "RAW_V5_GEMINI_TWO_TURN_STATE_DEFINITION",
    "RAW_V5_GEMINI_ELEMENT_INPUT_CONTRACT_SHA256",
    "RAW_V5_GEMINI_ELEMENT_SERIALIZER_VERSION",
    "RAW_V5_GEMINI_ELEMENT_STATE_DEFINITION",
    "RAW_V5_VOYAGE_CODE_4_ELEMENT_INPUT_CONTRACT_SHA256",
    "RAW_V5_VOYAGE_CODE_4_ELEMENT_MAX_INPUT_TOKENS",
    "RAW_V5_VOYAGE_CODE_4_ELEMENT_SERIALIZER_VERSION",
    "RAW_V5_VOYAGE_CODE_4_ELEMENT_STATE_DEFINITION",
    "RAW_V5_VOYAGE_CODE_4_EMBEDDING_MODEL",
    "RAW_V5_GEMINI_USER_AGENT_ONLY_INPUT_CONTRACT_SHA256",
    "RAW_V5_GEMINI_USER_AGENT_ONLY_LAST_STATE_INPUT_CONTRACT_SHA256",
    "RAW_V5_GEMINI_USER_AGENT_ONLY_LAST_STATE_SERIALIZER_VERSION",
    "RAW_V5_GEMINI_USER_AGENT_ONLY_LAST_STATE_STATE_DEFINITION",
    "RAW_V5_PREVIOUS_ELEMENT_DOCUMENT_SCHEMA_VERSION",
    "RAW_V5_GEMINI_USER_AGENT_ONLY_SERIALIZER_VERSION",
    "RAW_V5_GEMINI_USER_AGENT_ONLY_STATE_DEFINITION",
    "RAW_V5_GEMINI_USER_AGENT_ONLY_TEN_TURN_INPUT_CONTRACT_SHA256",
    "RAW_V5_GEMINI_USER_AGENT_ONLY_TEN_TURN_SERIALIZER_VERSION",
    "RAW_V5_GEMINI_USER_AGENT_ONLY_TEN_TURN_STATE_DEFINITION",
    "RAW_V5_GEMINI_USER_AGENT_ONLY_TWO_TURN_INPUT_CONTRACT_SHA256",
    "RAW_V5_GEMINI_USER_AGENT_ONLY_TWO_TURN_SERIALIZER_VERSION",
    "RAW_V5_GEMINI_USER_AGENT_ONLY_TWO_TURN_STATE_DEFINITION",
    "RAW_V5_MODEL_INPUT_CODE_EXCERPT_CHARS",
    "RAW_V5_MODEL_INPUT_MAX_CHARS",
    "RAW_V5_MODEL_INPUT_PROJECTION_CONTRACT_SHA256",
    "RAW_V5_VOYAGE_ELEMENT_INPUT_CONTRACT_SHA256",
    "RAW_V5_VOYAGE_ELEMENT_SERIALIZER_VERSION",
    "RAW_V5_VOYAGE_ELEMENT_STATE_DEFINITION",
    "RAW_V5_MODEL_INPUT_PROJECTION_SCHEMA_VERSION",
    "RAW_V5_MODEL_INPUT_SCHEMA_MAX_DEPTH",
    "RAW_V5_MODEL_INPUT_SCHEMA_MAX_FIELDS",
    "RAW_V5_MODEL_INPUT_SCHEMA_MAX_ITEMS",
    "RAW_V5_MODEL_INPUT_SERIALIZER_VERSION",
    "RAW_V5_MODEL_INPUT_SAMPLE_MAX_DEPTH",
    "RAW_V5_MODEL_INPUT_STATE_DEFINITION",
    "RAW_V5_MODEL_INPUT_STRING_SAMPLE_CHARS",
    "RAW_V5_MODEL_INPUT_STRUCTURED_LITERAL_MIN_CHARS",
    "RAW_V5_VOYAGE_TEN_TURN_INPUT_CONTRACT_SHA256",
    "RAW_V5_VOYAGE_TEN_TURN_MAX_TURNS",
    "RAW_V5_VOYAGE_TEN_TURN_SERIALIZER_VERSION",
    "RAW_V5_VOYAGE_TEN_TURN_STATE_DEFINITION",
    "RAW_V5_VOYAGE_TURN_WINDOW_DOCUMENT_SCHEMA_VERSION",
    "RAW_V5_VOYAGE_ONE_TURN_INPUT_CONTRACT_SHA256",
    "RAW_V5_VOYAGE_ONE_TURN_MAX_TURNS",
    "RAW_V5_VOYAGE_ONE_TURN_SERIALIZER_VERSION",
    "RAW_V5_VOYAGE_ONE_TURN_STATE_DEFINITION",
    "RAW_V5_VOYAGE_TWO_TURN_INPUT_CONTRACT_SHA256",
    "RAW_V5_VOYAGE_TWO_TURN_MAX_TURNS",
    "RAW_V5_VOYAGE_TWO_TURN_SERIALIZER_VERSION",
    "RAW_V5_VOYAGE_TWO_TURN_STATE_DEFINITION",
    "RAW_V5_FEEDBACK_SEQUENCE_CONTRACT_SHA256",
    "RAW_V5_FEEDBACK_FEATURE_BUILD_MODE",
    "RAW_V5_FEEDBACK_GOLD_PRODUCER",
    "RAW_V5_FEEDBACK_SEQUENCE_GOLD_FILENAME",
    "RAW_V5_FEEDBACK_SEQUENCE_SERIALIZER_VERSION",
    "RAW_V5_ROUTER_EMBEDDING_DIM",
    "RAW_V5_ROUTER_EMBEDDING_MODEL",
    "RAW_V5_ROUTER_MAX_INPUT_TOKENS",
    "RAW_V5_ROUTER_SEQUENCE_CONTRACT_SHA256",
    "RAW_V5_ROUTER_SEQUENCE_LAYOUT",
    "RAW_V5_ROUTER_SERIALIZER_VERSION",
    "RAW_V5_ROUTER_STATE_DEFINITION",
    "qwen_raw_v5_text_input_contract",
    "raw_v5_model_input_projection_contract",
    "raw_v5_voyage_element_model_input_contract",
    "raw_v5_feedback_sequence_contract",
    "raw_v5_gemini_feedback_sequence_contract",
    "raw_v5_gemini_turn_window_model_input_contract",
    "raw_v5_gemini_user_agent_only_model_input_contract",
    "raw_v5_voyage_user_agent_only_model_input_contract",
    "raw_v5_router_sequence_contract",
    "raw_v5_voyage_ten_turn_model_input_contract",
    "raw_v5_voyage_one_turn_model_input_contract",
    "raw_v5_voyage_turn_window_model_input_contract",
    "raw_v5_voyage_two_turn_model_input_contract",
]
