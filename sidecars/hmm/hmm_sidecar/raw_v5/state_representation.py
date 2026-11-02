"""Immutable state-encoder contracts for HMM conversation sequences."""

from __future__ import annotations

import json
from dataclasses import dataclass
from enum import Enum
from typing import Any, Mapping, Sequence

from hmm_sidecar.raw_v5.raw_v5_contract import (
    RAW_V5_VOYAGE_CODE_4_ELEMENT_INPUT_CONTRACT_SHA256,
    RAW_V5_VOYAGE_CODE_4_ELEMENT_MAX_INPUT_TOKENS,
    RAW_V5_VOYAGE_CODE_4_ELEMENT_SERIALIZER_VERSION,
    RAW_V5_VOYAGE_CODE_4_ELEMENT_STATE_DEFINITION,
    RAW_V5_VOYAGE_CODE_4_EMBEDDING_MODEL,
    RAW_V5_GEMINI_ELEMENT_INPUT_CONTRACT_SHA256,
    RAW_V5_GEMINI_ELEMENT_SERIALIZER_VERSION,
    RAW_V5_GEMINI_ELEMENT_STATE_DEFINITION,
    RAW_V5_GEMINI_EMBEDDING_2_DIM,
    RAW_V5_GEMINI_EMBEDDING_2_MAX_INPUT_TOKENS,
    RAW_V5_GEMINI_EMBEDDING_2_MODEL,
    RAW_V5_GEMINI_USER_AGENT_ONLY_INPUT_CONTRACT_SHA256,
    RAW_V5_GEMINI_USER_AGENT_ONLY_LAST_STATE_INPUT_CONTRACT_SHA256,
    RAW_V5_GEMINI_USER_AGENT_ONLY_LAST_STATE_SERIALIZER_VERSION,
    RAW_V5_GEMINI_USER_AGENT_ONLY_LAST_STATE_STATE_DEFINITION,
    RAW_V5_GEMINI_USER_AGENT_ONLY_SERIALIZER_VERSION,
    RAW_V5_GEMINI_USER_AGENT_ONLY_STATE_DEFINITION,
    RAW_V5_GEMINI_USER_AGENT_ONLY_TEN_TURN_INPUT_CONTRACT_SHA256,
    RAW_V5_GEMINI_USER_AGENT_ONLY_TEN_TURN_SERIALIZER_VERSION,
    RAW_V5_GEMINI_USER_AGENT_ONLY_TEN_TURN_STATE_DEFINITION,
    RAW_V5_GEMINI_USER_AGENT_ONLY_TWO_TURN_INPUT_CONTRACT_SHA256,
    RAW_V5_GEMINI_USER_AGENT_ONLY_TWO_TURN_SERIALIZER_VERSION,
    RAW_V5_GEMINI_USER_AGENT_ONLY_TWO_TURN_STATE_DEFINITION,
    RAW_V5_GEMINI_ONE_TURN_INPUT_CONTRACT_SHA256,
    RAW_V5_GEMINI_ONE_TURN_SERIALIZER_VERSION,
    RAW_V5_GEMINI_ONE_TURN_STATE_DEFINITION,
    RAW_V5_GEMINI_TEN_TURN_INPUT_CONTRACT_SHA256,
    RAW_V5_GEMINI_TEN_TURN_SERIALIZER_VERSION,
    RAW_V5_GEMINI_TEN_TURN_STATE_DEFINITION,
    RAW_V5_GEMINI_TWO_TURN_INPUT_CONTRACT_SHA256,
    RAW_V5_GEMINI_TWO_TURN_SERIALIZER_VERSION,
    RAW_V5_GEMINI_TWO_TURN_STATE_DEFINITION,
    RAW_V5_VOYAGE_ELEMENT_INPUT_CONTRACT_SHA256,
    RAW_V5_VOYAGE_ELEMENT_SERIALIZER_VERSION,
    RAW_V5_VOYAGE_ELEMENT_STATE_DEFINITION,
    RAW_V5_ROUTER_EMBEDDING_DIM,
    RAW_V5_ROUTER_EMBEDDING_MODEL,
    RAW_V5_ROUTER_MAX_INPUT_TOKENS,
    RAW_V5_VOYAGE_ONE_TURN_INPUT_CONTRACT_SHA256,
    RAW_V5_VOYAGE_ONE_TURN_MAX_TURNS,
    RAW_V5_VOYAGE_ONE_TURN_SERIALIZER_VERSION,
    RAW_V5_VOYAGE_ONE_TURN_STATE_DEFINITION,
    RAW_V5_VOYAGE_TEN_TURN_INPUT_CONTRACT_SHA256,
    RAW_V5_VOYAGE_TEN_TURN_MAX_TURNS,
    RAW_V5_VOYAGE_TEN_TURN_SERIALIZER_VERSION,
    RAW_V5_VOYAGE_TEN_TURN_STATE_DEFINITION,
    RAW_V5_VOYAGE_TWO_TURN_INPUT_CONTRACT_SHA256,
    RAW_V5_VOYAGE_TWO_TURN_MAX_TURNS,
    RAW_V5_VOYAGE_TWO_TURN_SERIALIZER_VERSION,
    RAW_V5_VOYAGE_TWO_TURN_STATE_DEFINITION,
    raw_v5_voyage_user_agent_only_model_input_contract,
)
from hmm_sidecar.raw_v5.core import canonical_json, sha256_text

STATE_ENCODER_CONTRACT_VERSION = "hmm_router_state_encoder_contract_v1"
# Provider-embedding corpus rows that pin the distinct adjacent-turn input
# contract.  Keep this separate from v3, whose representation-aware payload
# also carries a Gemma/Qwen Modal service contract.
# Provider-embedding corpus rows that pin one embedding document containing
# the current partial turn plus the immediately preceding completed turn.
# Provider-embedding corpus rows whose single document holds every completed
# prior turn that fits the provider token budget, oldest dropped first.
# Canonical provider embeddings need both identities: the vector model and the
# canonical document serializer.  V10 makes those two independently verifiable
# for fresh canonical Voyage runs without rewriting the readable v1 Gemini
# corpus history.

LEGACY_STATE_DEFINITION = "hmm-router-conversation-sequence-v1"
GEMMA_CANONICAL_STATE_DEFINITION = LEGACY_STATE_DEFINITION
GEMINI_LAST_STATE_STATE_DEFINITION = "hmm-router-gemini-embedding-2-last-state-v1"
GEMINI_TWO_TURN_STATE_DEFINITION = "hmm-router-gemini-embedding-2-two-turn-v1"
GEMINI_TEN_TURN_STATE_DEFINITION = "hmm-router-gemini-embedding-2-10-turn-v1"
ROUTING_GROUP_STATE_DEFINITION = "hmm-router-causal-raw-previous-round-current-user-v1"
ROUTING_GROUP_COMPONENT_ORDER = (
    "previous_user",
    "previous_tool_compaction",
    "previous_agent_response",
    "current_user",
)
ROUTING_GROUP_COMPONENT_DIM = 3072
ROUTING_GROUP_HIDDEN_DIM = (
    len(ROUTING_GROUP_COMPONENT_ORDER) * ROUTING_GROUP_COMPONENT_DIM
)
GEMINI_FULL_CONTEXT_STATE_DEFINITION = "hmm-router-gemini-embedding-2-full-context-v1"
GEMMA_STATE_DEFINITION = "hmm-router-gemma4-raw-tool-trace-v1"
LEGACY_SERIALIZER_VERSION = "hmm_router_conversation_sequence_contract_v1"
GEMINI_LAST_STATE_SERIALIZER_VERSION = (
    "hmm_router_gemini_embedding_2_last_state_serializer_v1"
)
GEMINI_TWO_TURN_SERIALIZER_VERSION = (
    "hmm_router_gemini_embedding_2_two_turn_serializer_v1"
)
GEMINI_TEN_TURN_SERIALIZER_VERSION = (
    "hmm_router_gemini_embedding_2_ten_turn_serializer_v1"
)
ROUTING_GROUP_SERIALIZER_VERSION = "hmm_router_causal_raw_routing_group_serializer_v1"
GEMINI_FULL_CONTEXT_SERIALIZER_VERSION = (
    "hmm_router_gemini_embedding_2_full_context_serializer_v1"
)
# gemini-embedding-2 rejects inputs above 8192 tokens. Budget one token of
# headroom so a boundary-exact document can never be truncated by the provider
# itself -- this serializer drops whole oldest turns instead, which keeps the
# document valid JSON and always retains the current turn.
GEMINI_MAX_INPUT_TOKENS = 8192
GEMINI_FULL_CONTEXT_MAX_WINDOW_TOKENS = GEMINI_MAX_INPUT_TOKENS - 1
GEMMA_CANONICAL_SERIALIZER_VERSION = (
    "hmm_router_gemma4_canonical_sequence_serializer_v2"
)
GEMMA_SERIALIZER_VERSION = "hmm_router_gemma4_raw_tool_trace_serializer_v2"
QWEN35_CANONICAL_SERIALIZER_VERSION = (
    "hmm_router_qwen35_canonical_sequence_serializer_v1"
)

GEMMA_MODEL_ID = "google/gemma-4-26B-A4B-it"
GEMMA_MODEL_REVISION = "47b6801b24d15ff9bcd8c96dfaea0be9ed3a0301"
GEMMA_HIDDEN_DIM = 2816
GEMMA_MAX_POSITION_EMBEDDINGS = 262_144
# Preserve the already-pinned rolling-window geometry while changing only the
# execution service. Pure Transformers does not need vLLM's former reserved
# pooling slot, but changing this cap would create a different encoder domain.
GEMMA_MAX_WINDOW_TOKENS = GEMMA_MAX_POSITION_EMBEDDINGS - 1

# Gemma 4 12B is a separate dense checkpoint and encoder domain. It shares the
# canonical U/A serializer and rolling-window semantics with the 26B variants,
# but its model geometry and final hidden-state width are different.
GEMMA_12B_MODEL_ID = "google/gemma-4-12B-it"
GEMMA_12B_MODEL_REVISION = "12ace6d648d72bd41519e140f1185f34d38c7e3d"
GEMMA_12B_HIDDEN_DIM = 3840
GEMMA_12B_MAX_POSITION_EMBEDDINGS = 262_144
GEMMA_12B_MAX_WINDOW_TOKENS = GEMMA_12B_MAX_POSITION_EMBEDDINGS - 1

# Qwen 3.5 9B is a separate long-context encoder domain. Its hybrid text
# backbone uses 24 Gated DeltaNet linear-attention blocks and eight full
# attention blocks. The checkpoint supports 262K positions, but this HMM
# encoder deliberately retains only the newest 50K tokens to bound each state
# forward while still exposing the final normalized text state.
QWEN35_9B_MODEL_ID = "Qwen/Qwen3.5-9B"
QWEN35_9B_MODEL_REVISION = "21eca8a083a2121a92fba681f4a7c72cf20ff1a7"
QWEN35_9B_HIDDEN_DIM = 4096
QWEN35_9B_MAX_POSITION_EMBEDDINGS = 262_144
QWEN35_9B_MAX_WINDOW_TOKENS = 50_000

# Dev-only Modal serving geometry.  This is intentionally independent of the
# production models/v2 registry: the local HMM pipeline and the ephemeral Modal
# app share this small immutable identity directly.
# PyTorch 2.11's default BF16/256-head-dim Triton kernel exceeds Hopper's
# per-block resource ceiling. These documented FlexAttention controls keep the
# fused causal forward below that ceiling without changing sequence geometry.

# The dense 12B checkpoint requires the newer unified Gemma 4 implementation.
# Keep its image, service identity, and batch scheduler isolated from both 26B
# services so upgrading it cannot invalidate existing artifacts or caches.
# Bucket the padded forward length so FlexAttention sees a finite set of input
# shapes. Without this, TorchDynamo eventually hits its recompile limit and
# falls back to eager ``create_block_mask``, which materializes a quadratic
# dense mask for long contexts. Right-padding does not affect any gathered
# causal token because every pad is strictly to its right.

# Full-context 12B encoder: ONE KV-cached chunked-prefill pass per
# conversation instead of one exact suffix forward per boundary. Gemma 4's
# hybrid attention keeps the full-window cache tiny (40/48 sliding layers at
# window 1,024; the 8 full-attention layers use a single unified KV head of
# width 512), so the complete 262,143-token window fits one H100 with stock
# SDPA — measured 39.5 GiB peak allocated and bit-exact parity with the
# one-shot forward (RESULTS_hmm_gemma4_12b_kv_chunked_prefill.md). Boundary
# states use absolute positions from the conversation origin; only when total
# tokens exceed the window does the pass re-anchor to the newest suffix. This
# is a distinct encoder domain: its states, caches, and artifacts never mix
# with the per-boundary suffix representation.
# Per-container LRU of live conversation caches so a warm turn only prefills
# its NEW tokens. Reuse is gated on an exact prefix-token SHA match, and a
# miss falls back to the cold chunked re-prefill, so cached and cold states
# are identical. ~16.4 KiB/token of global KV on the 12B → 1M tokens ≈ 16 GiB
# alongside ~24 GiB of weights on the H100.
# When a conversation outgrows the 262,143-token window the pass re-anchors to
# ``total - (max_window - slack)`` and re-prefills once. The slack buys
# headroom so subsequent boundaries keep appending to the same cache instead of
# re-prefilling per boundary: overflow boundaries see between
# ``max_window - slack`` and ``max_window`` tokens of context (232K-262K), and
# a conversation pays one re-prefill per ``slack`` tokens past the cap.

# Full-context Qwen encoder: same one-KV-cached-pass design as the Gemma
# full-context service. Qwen's hybrid backbone is even friendlier to it —
# 24/32 layers are Gated DeltaNet linear attention whose recurrent state is
# fixed-size regardless of context, so the full 262,143-token pass measured
# 36.0 GiB peak at 4,518 tok/s on one H100 with parity cosine >= 0.99989 vs
# the one-shot forward (RESULTS_hmm_qwen35_9b_kv_chunked_prefill.md). This
# removes the 50K cap; the canonical 50K representation stays untouched.
QWEN35_9B_FULL_CONTEXT_MAX_WINDOW_TOKENS = QWEN35_9B_MAX_POSITION_EMBEDDINGS - 1

# Keep Qwen in its own image, app, volume, and contract. It shares the local
# request/cache protocol with the Gemma encoders, but no vectors or artifacts
# are interchangeable across their model and serializer identities.
# The 50K encoder cap lets all four flows fit within one 200K padded-token
# batch. A CUDA-OOM multirow batch is still split into exact singletons without
# changing vectors.

# The quantized encoder is a separate representation and Modal class. Keep the
# existing BF16 service identity immutable so its cache and trained artifacts
# remain usable while the FP8 path is evaluated independently.
# Transformers 5.5.3 declares kernels>=0.12,<0.13. Older kernels releases
# interpret Transformers' integer Hub-kernel API version as a packaging
# specifier and fail on the first quantized forward before loading a kernel.
GEMMA_STATE_FP8_WEIGHT_DTYPE = "float8_e4m3fn"
# 64 divides the 2,112-wide shared MLP, 704-wide experts, and all attention
# projections. Larger 128-row blocks would leave the shared MLP unquantizable.
GEMMA_STATE_FP8_WEIGHT_BLOCK_SIZE = (64, 64)
GEMMA_STATE_FP8_ACTIVATION_SCHEME = "dynamic"

TOOL_COMPACTION_SCHEMA_VERSION = "tool_action_trace_v1"
TOOL_COMPACTION_MODEL = "deepseek/deepseek-v4-flash"
TOOL_COMPACTION_CHUNK_CHARS = 250_000
TOOL_COMPACTION_MAX_ACTION_GROUPS = 12

# These are SHA-256 digests of the exact prompt strings in
# agent_flow/extraction/conversation_dataset/prompts.py. Keeping the values in
# this lightweight module lets the Modal image share the contract without
# importing the Stage 2 processing stack.
TOOL_COMPACTION_PROMPT_SHA256 = (
    "4b13e33d232a277a1605be832f71bfca54ebef465dcdff722125711fde6af530"
)
TOOL_COMPACTION_USER_TEMPLATE_SHA256 = (
    "83a2f0c8d090a3ab52e67d4448ae1573128b3f351041ffc1074b0aafb13f8b5d"
)
TOOL_COMPACTION_SYNTHESIS_PROMPT_SHA256 = (
    "5dcd2f2ccd5ddb3d9d996ebbb60c6e9e263b86cdd5563a45e9eb8eb15eb21028"
)
TOOL_COMPACTION_SYNTHESIS_USER_TEMPLATE_SHA256 = (
    "5e92da01aef7e88562345ecdb5a3095bdc748602381e8ce13fdc91bc3a41a80a"
)
TOOL_COMPACTION_FORMATTER_VERSION = "format_agent_action_trace_v1"
TOOL_COMPACTION_FORMATTER_SOURCE_SHA256 = (
    "e24772476ee4dd7b6c1d93a67118a594b4e867ad321de9f4525ef0a4573ae197"
)
TOOL_COMPACTION_STRUCTURED_INPUT_VERSION = "tool_action_trace_structured_input_v1"
TOOL_COMPACTION_STRUCTURED_INPUT_SOURCE_SHA256 = (
    "ab88a1bed7cae9debb81a86a2ea534c04c784d54ab1eaadb75a00ef94f0d6edf"
)

_LEGACY_SERIALIZER_SPEC = """\
hmm_router_conversation_sequence_contract_v1
canonical user and agent documents from conversation_sequence.py
one independent provider embedding per document
"""

_GEMINI_LAST_STATE_SERIALIZER_SPEC = """\
hmm_router_gemini_embedding_2_last_state_serializer_v1
canonical user and agent documents from conversation_sequence.py
one provider embedding per canonical element
each provider document contains the immediately preceding canonical element,
then the current canonical element, with explicit Previous/Current labels
the first element uses the literal [no previous canonical element] sentinel
the previous element is reset at every conversation boundary
"""

_GEMINI_TWO_TURN_SERIALIZER_SPEC = """\
hmm_router_gemini_embedding_2_two_turn_serializer_v1
canonical user and agent documents from conversation_sequence.py
one provider embedding per canonical U/A boundary
each provider document is deterministic compact JSON with explicit turn_marker,
user_input, and agent_output fields
at user boundary U_t the document contains U_(t-1), A_(t-1), U_t
at agent boundary A_t the document contains U_(t-1), A_(t-1), U_t, A_t
the first turn contains only the available current partial turn
the completed-turn window is reset at every conversation boundary
"""

_GEMINI_TEN_TURN_SERIALIZER_SPEC = """\
hmm_router_gemini_embedding_2_ten_turn_serializer_v1
canonical user and agent documents from conversation_sequence.py
one provider embedding per canonical U/A boundary
each provider document is deterministic compact JSON with explicit turn_offset,
turn_marker, user_input, and agent_output fields
at user boundary U_t the document contains the newest nine completed U/A turns
followed by the current U_t
at agent boundary A_t the document also contains the current A_t
startup uses every available completed prior turn up to the fixed nine-turn cap
the completed-turn window is reset at every conversation boundary
"""

_ROUTING_GROUP_SERIALIZER_SPEC = """\
hmm_router_causal_raw_routing_group_serializer_v1
one target-aligned routing group X_k per labeled raw user turn U_k
X_0 contains previous_round null and exact current raw U_0
X_k contains exact raw U_(k-1), exact bound T_(k-1), every raw response block
A_(k-1), and exact current raw U_k
the predecessor is numeric k-1 and gaps are never bridged
T_k, A_k, and every later turn are forbidden from X_k
semantic records use deterministic compact canonical JSON
embedding input is four separately tagged component documents in fixed order:
previous_user, previous_tool_compaction, previous_agent_response, current_user
each component is embedded independently with google/gemini-embedding-2 default task
the four 3072-dimensional float vectors are concatenated on the feature axis
no previous round, no tools, no response, and unavailable response telemetry
are distinct typed component states and never ambiguous empty-string fallbacks
"""

_GEMINI_FULL_CONTEXT_SERIALIZER_SPEC = """\
hmm_router_gemini_embedding_2_full_context_serializer_v1
canonical user and agent documents from conversation_sequence.py
one provider embedding per canonical U/A boundary
each provider document is deterministic compact JSON with explicit turn_offset,
turn_marker, user_input, and agent_output fields
at user boundary U_t the document contains every completed prior U/A turn
followed by the current U_t
at agent boundary A_t the document also contains the current A_t
no fixed turn cap: the window is bounded only by the provider token limit
oldest completed turns are dropped first when the document exceeds
8191 tokens, so the current turn is always retained
the completed-turn window is reset at every conversation boundary
"""

_GEMMA_CANONICAL_SERIALIZER_SPEC = """\
hmm_router_gemma4_canonical_sequence_serializer_v2
exact ordered canonical user and agent documents from conversation_sequence.py
each document is one Gemma chat-template message with its matching user or assistant role
each document ends at the pinned Gemma chat-template end-of-turn token
no assistant generation prompt after a user boundary
newest 262143 token suffix ending at each boundary
suffix-local zero-based positions
"""

_GEMMA_SERIALIZER_SPEC = """\
hmm_router_gemma4_raw_tool_trace_serializer_v2
raw visible user and assistant response text only
assistant tool calls represented by exact tool_action_trace_v1 structured input
each role record ends at the pinned Gemma chat-template end-of-turn token
no assistant generation prompt after a user boundary
newest 262143 token suffix ending at each boundary
suffix-local zero-based positions
"""

_QWEN35_CANONICAL_SERIALIZER_SPEC = """\
hmm_router_qwen35_canonical_sequence_serializer_v1
exact ordered canonical user and agent documents from conversation_sequence.py
each document is one Qwen 3.5 chat-template message with its matching user or assistant role
each document ends at the pinned Qwen <|im_end|> end-of-turn token
no assistant generation prompt after a user boundary
newest 50000 token suffix ending at each boundary
suffix-local zero-based text positions expanded through the checkpoint's native multimodal RoPE
"""

# Stable semantic fixture identity. This hashes the exact input/specification,
# not an unexecuted success claim. A later authorized H100 conformance run must
# publish pass evidence and output hashes against this fixture before promotion.
GEMMA_EXTRACTION_PARITY_FIXTURE_SPEC: Mapping[str, object] = {
    "schema_version": "hmm_gemma_extraction_parity_fixture_v1",
    "model_id": GEMMA_MODEL_ID,
    "model_revision": GEMMA_MODEL_REVISION,
    "token_ids": (2, 105, 2364, 107, 106, 9259, 107),
    "position_ids": (0, 1, 2, 3, 4, 5, 6),
    "boundary_token_offset": 6,
    "reference_tap": ("block_29_post_final_rmsnorm_pre_lm_head_last_boundary_token"),
    "compute_dtype": "bfloat16",
    "comparison_dtype": "float32",
    "maximum_absolute_error": 0.03125,
    "minimum_cosine_similarity": 0.999,
}


GEMMA_EXTRACTION_PARITY_FIXTURE_SHA256 = sha256_text(
    json.dumps(
        GEMMA_EXTRACTION_PARITY_FIXTURE_SPEC,
        sort_keys=True,
        separators=(",", ":"),
    )
)

GEMMA_FP8_EXTRACTION_PARITY_FIXTURE_SPEC: Mapping[str, object] = {
    "schema_version": "hmm_gemma_fp8_extraction_parity_fixture_v1",
    "model_id": GEMMA_MODEL_ID,
    "model_revision": GEMMA_MODEL_REVISION,
    "token_ids": (2, 105, 2364, 107, 106, 9259, 107),
    "position_ids": (0, 1, 2, 3, 4, 5, 6),
    "boundary_token_offset": 6,
    "reference_tap": ("block_29_post_final_rmsnorm_pre_lm_head_last_boundary_token"),
    "weight_dtype": GEMMA_STATE_FP8_WEIGHT_DTYPE,
    "weight_block_size": GEMMA_STATE_FP8_WEIGHT_BLOCK_SIZE,
    "activation_scheme": GEMMA_STATE_FP8_ACTIVATION_SCHEME,
    "residual_dtype": "bfloat16",
    "comparison_dtype": "float32",
    # This pins the input and extraction target without pretending that a paid
    # GPU parity run has already succeeded.
    "validation_status": "requires_authorized_h200_conformance",
}
GEMMA_FP8_EXTRACTION_PARITY_FIXTURE_SHA256 = sha256_text(
    json.dumps(
        GEMMA_FP8_EXTRACTION_PARITY_FIXTURE_SPEC,
        sort_keys=True,
        separators=(",", ":"),
    )
)

GEMMA_12B_EXTRACTION_PARITY_FIXTURE_SPEC: Mapping[str, object] = {
    "schema_version": "hmm_gemma_12b_extraction_parity_fixture_v1",
    "model_id": GEMMA_12B_MODEL_ID,
    "model_revision": GEMMA_12B_MODEL_REVISION,
    "token_ids": (2, 105, 2364, 107, 106, 9259, 107),
    "position_ids": (0, 1, 2, 3, 4, 5, 6),
    "boundary_token_offset": 6,
    "reference_tap": ("block_47_post_final_rmsnorm_pre_lm_head_last_boundary_token"),
    "compute_dtype": "bfloat16",
    "comparison_dtype": "float32",
    "validation_status": "requires_authorized_h200_conformance",
}
GEMMA_12B_EXTRACTION_PARITY_FIXTURE_SHA256 = sha256_text(
    json.dumps(
        GEMMA_12B_EXTRACTION_PARITY_FIXTURE_SPEC,
        sort_keys=True,
        separators=(",", ":"),
    )
)

GEMMA_12B_FULL_CONTEXT_EXTRACTION_PARITY_FIXTURE_SPEC: Mapping[str, object] = {
    "schema_version": "hmm_gemma_12b_full_context_extraction_parity_fixture_v1",
    "model_id": GEMMA_12B_MODEL_ID,
    "model_revision": GEMMA_12B_MODEL_REVISION,
    "token_ids": (2, 105, 2364, 107, 106, 9259, 107),
    "position_ids": (0, 1, 2, 3, 4, 5, 6),
    "boundary_token_offset": 6,
    "reference_tap": ("block_47_post_final_rmsnorm_pre_lm_head_last_boundary_token"),
    "positions": "absolute_from_window_start_zero_based",
    "kv_cache": "transformers_dynamic_cache_config_hybrid_v1",
    "compute_dtype": "bfloat16",
    "comparison_dtype": "float32",
    "validation_status": ("chunked_prefill_bit_exact_vs_one_shot_sdpa_h100_20260717"),
}
GEMMA_12B_FULL_CONTEXT_EXTRACTION_PARITY_FIXTURE_SHA256 = sha256_text(
    json.dumps(
        GEMMA_12B_FULL_CONTEXT_EXTRACTION_PARITY_FIXTURE_SPEC,
        sort_keys=True,
        separators=(",", ":"),
    )
)

QWEN35_9B_EXTRACTION_PARITY_FIXTURE_SPEC: Mapping[str, object] = {
    "schema_version": "hmm_qwen35_9b_extraction_parity_fixture_v1",
    "model_id": QWEN35_9B_MODEL_ID,
    "model_revision": QWEN35_9B_MODEL_REVISION,
    "canonical_boundaries": (
        ("user", 0, "Verify the pinned Qwen state encoder."),
        ("agent", 0, "The encoder preflight is running."),
        ("user", 1, "Confirm the current routing state."),
    ),
    "boundary_index": 2,
    "reference_tap": ("block_31_post_final_rmsnorm_pre_lm_head_last_boundary_token"),
    "compute_dtype": "bfloat16",
    "comparison_dtype": "float32",
    "validation_status": "requires_authorized_h200_conformance",
}
QWEN35_9B_EXTRACTION_PARITY_FIXTURE_SHA256 = sha256_text(
    json.dumps(
        QWEN35_9B_EXTRACTION_PARITY_FIXTURE_SPEC,
        sort_keys=True,
        separators=(",", ":"),
    )
)

QWEN35_9B_FULL_CONTEXT_EXTRACTION_PARITY_FIXTURE_SPEC: Mapping[str, object] = {
    "schema_version": ("hmm_qwen35_9b_full_context_extraction_parity_fixture_v1"),
    "model_id": QWEN35_9B_MODEL_ID,
    "model_revision": QWEN35_9B_MODEL_REVISION,
    "canonical_boundaries": (
        ("user", 0, "Verify the pinned Qwen state encoder."),
        ("agent", 0, "The encoder preflight is running."),
        ("user", 1, "Confirm the current routing state."),
    ),
    "boundary_index": 2,
    "reference_tap": ("block_31_post_final_rmsnorm_pre_lm_head_last_boundary_token"),
    "positions": "absolute_from_window_start_zero_based",
    "kv_cache": "transformers_hybrid_linear_attention_cache_v1",
    "compute_dtype": "bfloat16",
    "comparison_dtype": "float32",
    # The fused Gated DeltaNet chunked scan accumulates in a different order
    # across chunk sizes, so parity is near-exact rather than bit-exact.
    "validation_status": (
        "chunked_prefill_cosine_ge_0p9998_vs_one_shot_sdpa_h100_20260717"
    ),
}
QWEN35_9B_FULL_CONTEXT_EXTRACTION_PARITY_FIXTURE_SHA256 = sha256_text(
    json.dumps(
        QWEN35_9B_FULL_CONTEXT_EXTRACTION_PARITY_FIXTURE_SPEC,
        sort_keys=True,
        separators=(",", ":"),
    )
)


class SequenceRepresentation(str, Enum):
    """Stable command-line and artifact identifiers for sequence geometry."""

    GEMINI_CANONICAL_V1 = "gemini-embedding-2-canonical-v1"
    GEMINI_EMBEDDING_2_LAST_STATE_V1 = "gemini-embedding-2-last-state-v1"
    GEMINI_EMBEDDING_2_TWO_TURN_V1 = "gemini-embedding-2-two-turn-v1"
    GEMINI_EMBEDDING_2_TEN_TURN_V1 = "gemini-embedding-2-10-turn-v1"
    GEMINI_EMBEDDING_2_ROUTING_GROUP_V1 = "gemini-embedding-2-routing-group-v1"
    # Source compatibility for code written while the public alias was being
    # introduced. The persisted value remains the established routing-group ID.
    GEMINI_EMBEDDING_2_NON_CANONICAL_V1 = GEMINI_EMBEDDING_2_ROUTING_GROUP_V1
    GEMINI_EMBEDDING_2_FULL_CONTEXT_V1 = "gemini-embedding-2-full-context-v1"
    GEMMA4_CANONICAL_V1 = "gemma4-26b-canonical-v1"
    GEMMA4_CANONICAL_FP8_V1 = "gemma4-26b-canonical-fp8-v1"
    GEMMA4_12B_CANONICAL_V1 = "gemma4-12b-canonical-v1"
    GEMMA4_12B_FULL_CONTEXT_V1 = "gemma4-12b-full-context-v1"
    QWEN35_9B_CANONICAL_V1 = "qwen3.5-9b-canonical-v1"
    QWEN35_9B_FULL_CONTEXT_V1 = "qwen3.5-9b-full-context-v1"
    GEMMA4_RAW_TOOL_TRACE_V1 = "gemma4-26b-raw-tool-trace-v1"
    # A fresh provider-vector representation over the current canonical U/A
    # document schema.  This is intentionally not an alias for the historical
    # Gemini canonical corpus: its model-space and V10 provenance are distinct.
    VOYAGE_4_LARGE_CANONICAL_V1 = "voyage-4-large-canonical-v1"
    VOYAGE_4_LARGE_RAW_ROUTER_V5 = "voyage-4-large-raw-router-v5"
    # This is intentionally distinct from the legacy one-event representation:
    # every source field is structurally normalized before it is embedded.
    VOYAGE_4_LARGE_RAW_ROUTER_V5_ONE_TURN = "voyage-4-large-raw-router-v5-one-turn"
    VOYAGE_4_LARGE_RAW_ROUTER_V5_TWO_TURN = "voyage-4-large-raw-router-v5-two-turn"
    VOYAGE_4_LARGE_RAW_ROUTER_V5_TEN_TURN = "voyage-4-large-raw-router-v5-10-turn"
    VOYAGE_4_LARGE_RAW_ROUTER_V5_USER_AGENT_ONLY = (
        "voyage-4-large-raw-router-v5-user-agent-only"
    )
    VOYAGE_4_LARGE_RAW_ROUTER_V5_USER_AGENT_ONLY_TWO_TURN = (
        "voyage-4-large-raw-router-v5-user-agent-only-two-turn"
    )
    VOYAGE_4_LARGE_RAW_ROUTER_V5_USER_AGENT_ONLY_TEN_TURN = (
        "voyage-4-large-raw-router-v5-user-agent-only-10-turn"
    )
    # Fresh Gemini vectors over the exact same causal raw-V5 documents as the
    # normalized Voyage windows.  They are distinct representations because
    # both provider geometry and cache/artifact lineage differ.
    GEMINI_EMBEDDING_2_RAW_ROUTER_V5_ONE_TURN = (
        "gemini-embedding-2-raw-router-v5-one-turn"
    )
    GEMINI_EMBEDDING_2_RAW_ROUTER_V5_TWO_TURN = (
        "gemini-embedding-2-raw-router-v5-two-turn"
    )
    GEMINI_EMBEDDING_2_RAW_ROUTER_V5_TEN_TURN = (
        "gemini-embedding-2-raw-router-v5-10-turn"
    )
    # A separate current-turn projection which deliberately excludes tool
    # traces and the tool-compaction sidecar from both source access and model
    # input.  It is not a lookback alias of the tool-aware raw V5 documents.
    GEMINI_EMBEDDING_2_RAW_ROUTER_V5_USER_AGENT_ONLY = (
        "gemini-embedding-2-raw-router-v5-user-agent-only"
    )
    GEMINI_EMBEDDING_2_RAW_ROUTER_V5_USER_AGENT_ONLY_TWO_TURN = (
        "gemini-embedding-2-raw-router-v5-user-agent-only-two-turn"
    )
    GEMINI_EMBEDDING_2_RAW_ROUTER_V5_USER_AGENT_ONLY_TEN_TURN = (
        "gemini-embedding-2-raw-router-v5-user-agent-only-10-turn"
    )
    # The same tool-free projection carrying the immediately preceding
    # element rather than a completed round, reproducing the lookback of the
    # canonical Last State recipe over raw documents.  Its documents and
    # cache lineage are distinct from every round-windowed representation
    # above.
    GEMINI_EMBEDDING_2_RAW_ROUTER_V5_USER_AGENT_ONLY_LAST_STATE = (
        "gemini-embedding-2-raw-router-v5-user-agent-only-last-state"
    )
    # Tool-free per-message store: each user message and each agent reply is
    # embedded once and alone; lookbacks are assembled from the stored vectors.
    GEMINI_EMBEDDING_2_RAW_ROUTER_V5_ELEMENT = (
        "gemini-embedding-2-raw-router-v5-element-v1"
    )
    # The same per-message store over voyage-code-4's 32,000-token context.
    VOYAGE_CODE_4_RAW_ROUTER_V5_ELEMENT = "voyage-code-4-raw-router-v5-element-v1"


GEMINI_EMBEDDING_2_NON_CANONICAL_ALIAS = "gemini-embedding-2-non-canonical-v1"
GEMINI_CANONICAL_V1 = SequenceRepresentation.GEMINI_CANONICAL_V1
GEMINI_EMBEDDING_2_LAST_STATE_V1 = (
    SequenceRepresentation.GEMINI_EMBEDDING_2_LAST_STATE_V1
)
GEMINI_EMBEDDING_2_TWO_TURN_V1 = SequenceRepresentation.GEMINI_EMBEDDING_2_TWO_TURN_V1
GEMINI_EMBEDDING_2_TEN_TURN_V1 = SequenceRepresentation.GEMINI_EMBEDDING_2_TEN_TURN_V1
GEMINI_EMBEDDING_2_ROUTING_GROUP_V1 = (
    SequenceRepresentation.GEMINI_EMBEDDING_2_ROUTING_GROUP_V1
)
GEMINI_EMBEDDING_2_NON_CANONICAL_V1 = GEMINI_EMBEDDING_2_ROUTING_GROUP_V1
GEMINI_EMBEDDING_2_FULL_CONTEXT_V1 = (
    SequenceRepresentation.GEMINI_EMBEDDING_2_FULL_CONTEXT_V1
)
GEMMA4_CANONICAL_V1 = SequenceRepresentation.GEMMA4_CANONICAL_V1
GEMMA4_CANONICAL_FP8_V1 = SequenceRepresentation.GEMMA4_CANONICAL_FP8_V1
GEMMA4_12B_CANONICAL_V1 = SequenceRepresentation.GEMMA4_12B_CANONICAL_V1
GEMMA4_12B_FULL_CONTEXT_V1 = SequenceRepresentation.GEMMA4_12B_FULL_CONTEXT_V1
QWEN35_9B_CANONICAL_V1 = SequenceRepresentation.QWEN35_9B_CANONICAL_V1
QWEN35_9B_FULL_CONTEXT_V1 = SequenceRepresentation.QWEN35_9B_FULL_CONTEXT_V1
GEMMA4_RAW_TOOL_TRACE_V1 = SequenceRepresentation.GEMMA4_RAW_TOOL_TRACE_V1
VOYAGE_4_LARGE_CANONICAL_V1 = SequenceRepresentation.VOYAGE_4_LARGE_CANONICAL_V1
VOYAGE_4_LARGE_RAW_ROUTER_V5 = SequenceRepresentation.VOYAGE_4_LARGE_RAW_ROUTER_V5
VOYAGE_4_LARGE_RAW_ROUTER_V5_ONE_TURN = (
    SequenceRepresentation.VOYAGE_4_LARGE_RAW_ROUTER_V5_ONE_TURN
)
VOYAGE_4_LARGE_RAW_ROUTER_V5_TWO_TURN = (
    SequenceRepresentation.VOYAGE_4_LARGE_RAW_ROUTER_V5_TWO_TURN
)
VOYAGE_4_LARGE_RAW_ROUTER_V5_TEN_TURN = (
    SequenceRepresentation.VOYAGE_4_LARGE_RAW_ROUTER_V5_TEN_TURN
)
VOYAGE_4_LARGE_RAW_ROUTER_V5_USER_AGENT_ONLY = (
    SequenceRepresentation.VOYAGE_4_LARGE_RAW_ROUTER_V5_USER_AGENT_ONLY
)
VOYAGE_4_LARGE_RAW_ROUTER_V5_USER_AGENT_ONLY_TWO_TURN = (
    SequenceRepresentation.VOYAGE_4_LARGE_RAW_ROUTER_V5_USER_AGENT_ONLY_TWO_TURN
)
VOYAGE_4_LARGE_RAW_ROUTER_V5_USER_AGENT_ONLY_TEN_TURN = (
    SequenceRepresentation.VOYAGE_4_LARGE_RAW_ROUTER_V5_USER_AGENT_ONLY_TEN_TURN
)
GEMINI_EMBEDDING_2_RAW_ROUTER_V5_ONE_TURN = (
    SequenceRepresentation.GEMINI_EMBEDDING_2_RAW_ROUTER_V5_ONE_TURN
)
GEMINI_EMBEDDING_2_RAW_ROUTER_V5_TWO_TURN = (
    SequenceRepresentation.GEMINI_EMBEDDING_2_RAW_ROUTER_V5_TWO_TURN
)
GEMINI_EMBEDDING_2_RAW_ROUTER_V5_TEN_TURN = (
    SequenceRepresentation.GEMINI_EMBEDDING_2_RAW_ROUTER_V5_TEN_TURN
)
GEMINI_EMBEDDING_2_RAW_ROUTER_V5_USER_AGENT_ONLY = (
    SequenceRepresentation.GEMINI_EMBEDDING_2_RAW_ROUTER_V5_USER_AGENT_ONLY
)
GEMINI_EMBEDDING_2_RAW_ROUTER_V5_USER_AGENT_ONLY_TWO_TURN = (
    SequenceRepresentation.GEMINI_EMBEDDING_2_RAW_ROUTER_V5_USER_AGENT_ONLY_TWO_TURN
)
GEMINI_EMBEDDING_2_RAW_ROUTER_V5_USER_AGENT_ONLY_TEN_TURN = (
    SequenceRepresentation.GEMINI_EMBEDDING_2_RAW_ROUTER_V5_USER_AGENT_ONLY_TEN_TURN
)
GEMINI_EMBEDDING_2_RAW_ROUTER_V5_USER_AGENT_ONLY_LAST_STATE = (
    SequenceRepresentation.GEMINI_EMBEDDING_2_RAW_ROUTER_V5_USER_AGENT_ONLY_LAST_STATE
)
GEMINI_EMBEDDING_2_RAW_ROUTER_V5_ELEMENT = (
    SequenceRepresentation.GEMINI_EMBEDDING_2_RAW_ROUTER_V5_ELEMENT
)
VOYAGE_CODE_4_RAW_ROUTER_V5_ELEMENT = (
    SequenceRepresentation.VOYAGE_CODE_4_RAW_ROUTER_V5_ELEMENT
)
# Per-message stores: one embedding per user or agent message; lookback is
# assembled from stored vectors downstream.
GEMINI_EMBEDDING_REPRESENTATIONS = frozenset(
    (
        GEMINI_CANONICAL_V1,
        GEMINI_EMBEDDING_2_LAST_STATE_V1,
        GEMINI_EMBEDDING_2_TWO_TURN_V1,
        GEMINI_EMBEDDING_2_TEN_TURN_V1,
        GEMINI_EMBEDDING_2_NON_CANONICAL_V1,
        GEMINI_EMBEDDING_2_FULL_CONTEXT_V1,
    )
)
GEMMA4_CANONICAL_REPRESENTATIONS = frozenset(
    (
        GEMMA4_CANONICAL_V1,
        GEMMA4_CANONICAL_FP8_V1,
        GEMMA4_12B_CANONICAL_V1,
        GEMMA4_12B_FULL_CONTEXT_V1,
    )
)
CONTEXTUAL_CANONICAL_REPRESENTATIONS = frozenset(
    (
        *GEMMA4_CANONICAL_REPRESENTATIONS,
        QWEN35_9B_CANONICAL_V1,
        QWEN35_9B_FULL_CONTEXT_V1,
    )
)
VOYAGE_RAW_ROUTER_USER_AGENT_ONLY_MAX_TURNS: Mapping[SequenceRepresentation, int] = {
    VOYAGE_4_LARGE_RAW_ROUTER_V5_USER_AGENT_ONLY: RAW_V5_VOYAGE_ONE_TURN_MAX_TURNS,
    VOYAGE_4_LARGE_RAW_ROUTER_V5_USER_AGENT_ONLY_TWO_TURN: RAW_V5_VOYAGE_TWO_TURN_MAX_TURNS,
    VOYAGE_4_LARGE_RAW_ROUTER_V5_USER_AGENT_ONLY_TEN_TURN: RAW_V5_VOYAGE_TEN_TURN_MAX_TURNS,
    VOYAGE_CODE_4_RAW_ROUTER_V5_ELEMENT: RAW_V5_VOYAGE_ONE_TURN_MAX_TURNS,
}
VOYAGE_RAW_ROUTER_USER_AGENT_ONLY_REPRESENTATIONS = frozenset(
    VOYAGE_RAW_ROUTER_USER_AGENT_ONLY_MAX_TURNS
)
VOYAGE_RAW_ROUTER_REPRESENTATIONS = frozenset(
    (
        VOYAGE_4_LARGE_RAW_ROUTER_V5,
        VOYAGE_4_LARGE_RAW_ROUTER_V5_ONE_TURN,
        VOYAGE_4_LARGE_RAW_ROUTER_V5_TWO_TURN,
        VOYAGE_4_LARGE_RAW_ROUTER_V5_TEN_TURN,
        *VOYAGE_RAW_ROUTER_USER_AGENT_ONLY_REPRESENTATIONS,
    )
)
# The menu names these bounded contexts by total rounds including the current
# partial round.  The legacy one-event raw-V5 representation remains readable,
# while this new one-round entry uses the uniform structural normalizer for
# every selected source element just like the multi-round entries.
GEMINI_RAW_ROUTER_REPRESENTATIONS = frozenset(
    (
        GEMINI_EMBEDDING_2_RAW_ROUTER_V5_ONE_TURN,
        GEMINI_EMBEDDING_2_RAW_ROUTER_V5_TWO_TURN,
        GEMINI_EMBEDDING_2_RAW_ROUTER_V5_TEN_TURN,
        GEMINI_EMBEDDING_2_RAW_ROUTER_V5_USER_AGENT_ONLY,
        GEMINI_EMBEDDING_2_RAW_ROUTER_V5_USER_AGENT_ONLY_TWO_TURN,
        GEMINI_EMBEDDING_2_RAW_ROUTER_V5_USER_AGENT_ONLY_TEN_TURN,
        GEMINI_EMBEDDING_2_RAW_ROUTER_V5_USER_AGENT_ONLY_LAST_STATE,
        GEMINI_EMBEDDING_2_RAW_ROUTER_V5_ELEMENT,
    )
)
RAW_V5_ROUTER_REPRESENTATIONS = frozenset(
    (*VOYAGE_RAW_ROUTER_REPRESENTATIONS, *GEMINI_RAW_ROUTER_REPRESENTATIONS)
)


def gemini_last_state_documents(texts: Sequence[str]) -> tuple[str, ...]:
    raise NotImplementedError("not on the classifier serving path")


def gemini_two_turn_documents(
    texts: Sequence[str],
    kinds: Sequence[str] | None = None,
) -> tuple[str, ...]:
    raise NotImplementedError("not on the classifier serving path")


def gemini_ten_turn_documents(
    texts: Sequence[str],
    kinds: Sequence[str] | None = None,
) -> tuple[str, ...]:
    raise NotImplementedError("not on the classifier serving path")


@dataclass(frozen=True)
class StateEncoderContract:
    """Everything that can change the state-vector geometry."""

    contract_version: str
    representation: SequenceRepresentation
    state_definition: str
    model_id: str
    model_revision: str | None
    tokenizer_revision: str | None
    serializer_version: str
    serializer_sha256: str
    hidden_dim: int
    extraction: str
    compute_dtype: str
    wire_dtype: str
    added_normalization: str
    max_window_tokens: int | None
    window_policy: str | None
    position_policy: str | None
    extraction_parity_fixture_sha256: str | None
    tool_compaction: Mapping[str, object] | None

    def to_dict(self) -> dict[str, Any]:
        """Return the complete JSON-serializable contract."""
        return {
            "contract_version": self.contract_version,
            "representation": self.representation.value,
            "state_definition": self.state_definition,
            "model_id": self.model_id,
            "model_revision": self.model_revision,
            "tokenizer_revision": self.tokenizer_revision,
            "serializer_version": self.serializer_version,
            "serializer_sha256": self.serializer_sha256,
            "hidden_dim": self.hidden_dim,
            "extraction": self.extraction,
            "compute_dtype": self.compute_dtype,
            "wire_dtype": self.wire_dtype,
            "added_normalization": self.added_normalization,
            "max_window_tokens": self.max_window_tokens,
            "window_policy": self.window_policy,
            "position_policy": self.position_policy,
            "extraction_parity_fixture_sha256": (self.extraction_parity_fixture_sha256),
            "tool_compaction": (
                dict(self.tool_compaction) if self.tool_compaction is not None else None
            ),
        }

    def canonical_json(self) -> str:
        """Return the unique compact representation used for identity."""
        return json.dumps(
            self.to_dict(),
            ensure_ascii=False,
            sort_keys=True,
            separators=(",", ":"),
        )

    @property
    def sha256(self) -> str:
        return sha256_text(self.canonical_json())


_TOOL_COMPACTION_CONTRACT: Mapping[str, object] = {
    "schema_version": TOOL_COMPACTION_SCHEMA_VERSION,
    "model": TOOL_COMPACTION_MODEL,
    "chunk_chars": TOOL_COMPACTION_CHUNK_CHARS,
    "max_action_groups": TOOL_COMPACTION_MAX_ACTION_GROUPS,
    "system_prompt_sha256": TOOL_COMPACTION_PROMPT_SHA256,
    "user_template_sha256": TOOL_COMPACTION_USER_TEMPLATE_SHA256,
    "synthesis_system_prompt_sha256": (TOOL_COMPACTION_SYNTHESIS_PROMPT_SHA256),
    "synthesis_user_template_sha256": (TOOL_COMPACTION_SYNTHESIS_USER_TEMPLATE_SHA256),
    "action_trace_formatter_version": TOOL_COMPACTION_FORMATTER_VERSION,
    "action_trace_formatter_source_sha256": (TOOL_COMPACTION_FORMATTER_SOURCE_SHA256),
    "structured_input_formatter_version": (TOOL_COMPACTION_STRUCTURED_INPUT_VERSION),
    "structured_input_formatter_source_sha256": (
        TOOL_COMPACTION_STRUCTURED_INPUT_SOURCE_SHA256
    ),
}


# Append-only representation registry. Once a representation ID has produced a
# training artifact, its contract payload must remain immutable. A new input or
# relative-state format gets a new ``SequenceRepresentation`` value instead of
# replacing an existing entry and invalidating historical bundles.
def _voyage_user_agent_only_encoder_contract(
    representation: SequenceRepresentation,
    max_turns: int,
) -> StateEncoderContract:
    input_contract = raw_v5_voyage_user_agent_only_model_input_contract(max_turns)
    return StateEncoderContract(
        contract_version=STATE_ENCODER_CONTRACT_VERSION,
        representation=representation,
        state_definition=str(input_contract["state_definition"]),
        model_id=RAW_V5_ROUTER_EMBEDDING_MODEL,
        model_revision=None,
        tokenizer_revision=None,
        serializer_version=str(input_contract["schema_version"]),
        serializer_sha256=sha256_text(canonical_json(input_contract)),
        hidden_dim=RAW_V5_ROUTER_EMBEDDING_DIM,
        extraction="provider_embedding",
        compute_dtype="provider_defined",
        wire_dtype="provider_float_sequence",
        added_normalization="provider_defined",
        max_window_tokens=RAW_V5_ROUTER_MAX_INPUT_TOKENS,
        window_policy=f"causal_raw_v5_user_agent_only_{max_turns}_turn_max_32000_tokens_v1",
        position_policy="raw_user_agent_event_order_causal_turn_window_v1",
        extraction_parity_fixture_sha256=None,
        tool_compaction=None,
    )


_CONTRACTS: dict[SequenceRepresentation, StateEncoderContract] = {
    **{
        representation: _voyage_user_agent_only_encoder_contract(
            representation, max_turns
        )
        for representation, max_turns in VOYAGE_RAW_ROUTER_USER_AGENT_ONLY_MAX_TURNS.items()
    },
    SequenceRepresentation.GEMINI_CANONICAL_V1: StateEncoderContract(
        contract_version=STATE_ENCODER_CONTRACT_VERSION,
        representation=SequenceRepresentation.GEMINI_CANONICAL_V1,
        state_definition=LEGACY_STATE_DEFINITION,
        model_id="google/gemini-embedding-2",
        model_revision=None,
        tokenizer_revision=None,
        serializer_version=LEGACY_SERIALIZER_VERSION,
        serializer_sha256=sha256_text(_LEGACY_SERIALIZER_SPEC),
        hidden_dim=3072,
        extraction="provider_embedding",
        compute_dtype="provider_defined",
        wire_dtype="provider_float_sequence",
        added_normalization="provider_defined",
        max_window_tokens=None,
        window_policy=None,
        position_policy=None,
        extraction_parity_fixture_sha256=None,
        tool_compaction=None,
    ),
    SequenceRepresentation.GEMINI_EMBEDDING_2_LAST_STATE_V1: StateEncoderContract(
        contract_version=STATE_ENCODER_CONTRACT_VERSION,
        representation=SequenceRepresentation.GEMINI_EMBEDDING_2_LAST_STATE_V1,
        state_definition=GEMINI_LAST_STATE_STATE_DEFINITION,
        model_id="google/gemini-embedding-2",
        model_revision=None,
        tokenizer_revision=None,
        serializer_version=GEMINI_LAST_STATE_SERIALIZER_VERSION,
        serializer_sha256=sha256_text(_GEMINI_LAST_STATE_SERIALIZER_SPEC),
        hidden_dim=3072,
        extraction="provider_embedding",
        compute_dtype="provider_defined",
        wire_dtype="provider_float_sequence",
        added_normalization="provider_defined",
        max_window_tokens=None,
        window_policy=None,
        position_policy=None,
        extraction_parity_fixture_sha256=None,
        tool_compaction=None,
    ),
    SequenceRepresentation.GEMINI_EMBEDDING_2_TWO_TURN_V1: StateEncoderContract(
        contract_version=STATE_ENCODER_CONTRACT_VERSION,
        representation=SequenceRepresentation.GEMINI_EMBEDDING_2_TWO_TURN_V1,
        state_definition=GEMINI_TWO_TURN_STATE_DEFINITION,
        model_id="google/gemini-embedding-2",
        model_revision=None,
        tokenizer_revision=None,
        serializer_version=GEMINI_TWO_TURN_SERIALIZER_VERSION,
        serializer_sha256=sha256_text(_GEMINI_TWO_TURN_SERIALIZER_SPEC),
        hidden_dim=3072,
        extraction="provider_embedding",
        compute_dtype="provider_defined",
        wire_dtype="provider_float_sequence",
        added_normalization="provider_defined",
        max_window_tokens=None,
        window_policy=None,
        position_policy=None,
        extraction_parity_fixture_sha256=None,
        tool_compaction=None,
    ),
    SequenceRepresentation.GEMINI_EMBEDDING_2_TEN_TURN_V1: StateEncoderContract(
        contract_version=STATE_ENCODER_CONTRACT_VERSION,
        representation=SequenceRepresentation.GEMINI_EMBEDDING_2_TEN_TURN_V1,
        state_definition=GEMINI_TEN_TURN_STATE_DEFINITION,
        model_id="google/gemini-embedding-2",
        model_revision=None,
        tokenizer_revision=None,
        serializer_version=GEMINI_TEN_TURN_SERIALIZER_VERSION,
        serializer_sha256=sha256_text(_GEMINI_TEN_TURN_SERIALIZER_SPEC),
        hidden_dim=3072,
        extraction="provider_embedding",
        compute_dtype="provider_defined",
        wire_dtype="provider_float_sequence",
        added_normalization="provider_defined",
        max_window_tokens=None,
        window_policy=None,
        position_policy=None,
        extraction_parity_fixture_sha256=None,
        tool_compaction=None,
    ),
    SequenceRepresentation.GEMINI_EMBEDDING_2_NON_CANONICAL_V1: StateEncoderContract(
        contract_version=STATE_ENCODER_CONTRACT_VERSION,
        representation=(SequenceRepresentation.GEMINI_EMBEDDING_2_NON_CANONICAL_V1),
        state_definition=ROUTING_GROUP_STATE_DEFINITION,
        model_id="google/gemini-embedding-2",
        model_revision=None,
        tokenizer_revision=None,
        serializer_version=ROUTING_GROUP_SERIALIZER_VERSION,
        serializer_sha256=sha256_text(_ROUTING_GROUP_SERIALIZER_SPEC),
        hidden_dim=ROUTING_GROUP_HIDDEN_DIM,
        extraction=(
            "concat_feature_axis(previous_user,previous_tool_compaction,"
            "previous_agent_response,current_user)"
        ),
        compute_dtype="provider_defined",
        wire_dtype="provider_float_sequence_concatenated",
        added_normalization="provider_defined_per_component",
        max_window_tokens=None,
        window_policy="all_prior_routing_groups_through_current_target_v1",
        position_policy="one_group_per_target_user_turn_zero_based_v1",
        extraction_parity_fixture_sha256=None,
        tool_compaction=_TOOL_COMPACTION_CONTRACT,
    ),
    SequenceRepresentation.GEMINI_EMBEDDING_2_FULL_CONTEXT_V1: StateEncoderContract(
        contract_version=STATE_ENCODER_CONTRACT_VERSION,
        representation=(SequenceRepresentation.GEMINI_EMBEDDING_2_FULL_CONTEXT_V1),
        state_definition=GEMINI_FULL_CONTEXT_STATE_DEFINITION,
        model_id="google/gemini-embedding-2",
        model_revision=None,
        tokenizer_revision=None,
        serializer_version=GEMINI_FULL_CONTEXT_SERIALIZER_VERSION,
        serializer_sha256=sha256_text(_GEMINI_FULL_CONTEXT_SERIALIZER_SPEC),
        hidden_dim=3072,
        extraction="provider_embedding",
        compute_dtype="provider_defined",
        wire_dtype="provider_float_sequence",
        added_normalization="provider_defined",
        max_window_tokens=GEMINI_FULL_CONTEXT_MAX_WINDOW_TOKENS,
        # Same newest-suffix rule the Gemma/Qwen contracts use, applied at whole
        # completed-turn granularity rather than raw tokens: the current turn is
        # never the thing dropped, so a boundary document is always about the
        # turn being routed.
        window_policy="newest_completed_turn_suffix_ending_at_boundary_v1",
        position_policy=None,
        extraction_parity_fixture_sha256=None,
        tool_compaction=None,
    ),
    SequenceRepresentation.GEMMA4_CANONICAL_V1: StateEncoderContract(
        contract_version=STATE_ENCODER_CONTRACT_VERSION,
        representation=SequenceRepresentation.GEMMA4_CANONICAL_V1,
        state_definition=GEMMA_CANONICAL_STATE_DEFINITION,
        model_id=GEMMA_MODEL_ID,
        model_revision=GEMMA_MODEL_REVISION,
        tokenizer_revision=GEMMA_MODEL_REVISION,
        # Reuse the exact canonical U/A documents used by the Gemini path,
        # while pinning the distinct causal serialization applied by Gemma.
        serializer_version=GEMMA_CANONICAL_SERIALIZER_VERSION,
        serializer_sha256=sha256_text(_GEMMA_CANONICAL_SERIALIZER_SPEC),
        hidden_dim=GEMMA_HIDDEN_DIM,
        extraction=("block_29_post_final_rmsnorm_pre_lm_head_last_boundary_token"),
        compute_dtype="bfloat16",
        wire_dtype="float32_le_base64",
        added_normalization="none",
        max_window_tokens=GEMMA_MAX_WINDOW_TOKENS,
        window_policy="newest_token_suffix_ending_at_boundary_v1",
        position_policy="suffix_local_positions_zero_based_v1",
        extraction_parity_fixture_sha256=(GEMMA_EXTRACTION_PARITY_FIXTURE_SHA256),
        tool_compaction=None,
    ),
    SequenceRepresentation.GEMMA4_CANONICAL_FP8_V1: StateEncoderContract(
        contract_version=STATE_ENCODER_CONTRACT_VERSION,
        representation=SequenceRepresentation.GEMMA4_CANONICAL_FP8_V1,
        state_definition=GEMMA_CANONICAL_STATE_DEFINITION,
        model_id=GEMMA_MODEL_ID,
        model_revision=GEMMA_MODEL_REVISION,
        tokenizer_revision=GEMMA_MODEL_REVISION,
        serializer_version=GEMMA_CANONICAL_SERIALIZER_VERSION,
        serializer_sha256=sha256_text(_GEMMA_CANONICAL_SERIALIZER_SPEC),
        hidden_dim=GEMMA_HIDDEN_DIM,
        extraction=("block_29_post_final_rmsnorm_pre_lm_head_last_boundary_token"),
        compute_dtype="fp8_e4m3fn_w8a8_with_bfloat16_residual",
        wire_dtype="float32_le_base64",
        added_normalization="none",
        max_window_tokens=GEMMA_MAX_WINDOW_TOKENS,
        window_policy="newest_token_suffix_ending_at_boundary_v1",
        position_policy="suffix_local_positions_zero_based_v1",
        extraction_parity_fixture_sha256=(GEMMA_FP8_EXTRACTION_PARITY_FIXTURE_SHA256),
        tool_compaction=None,
    ),
    SequenceRepresentation.GEMMA4_12B_CANONICAL_V1: StateEncoderContract(
        contract_version=STATE_ENCODER_CONTRACT_VERSION,
        representation=SequenceRepresentation.GEMMA4_12B_CANONICAL_V1,
        state_definition=GEMMA_CANONICAL_STATE_DEFINITION,
        model_id=GEMMA_12B_MODEL_ID,
        model_revision=GEMMA_12B_MODEL_REVISION,
        tokenizer_revision=GEMMA_12B_MODEL_REVISION,
        serializer_version=GEMMA_CANONICAL_SERIALIZER_VERSION,
        serializer_sha256=sha256_text(_GEMMA_CANONICAL_SERIALIZER_SPEC),
        hidden_dim=GEMMA_12B_HIDDEN_DIM,
        extraction=("block_47_post_final_rmsnorm_pre_lm_head_last_boundary_token"),
        compute_dtype="bfloat16",
        wire_dtype="float32_le_base64",
        added_normalization="none",
        max_window_tokens=GEMMA_12B_MAX_WINDOW_TOKENS,
        window_policy="newest_token_suffix_ending_at_boundary_v1",
        position_policy="suffix_local_positions_zero_based_v1",
        extraction_parity_fixture_sha256=(GEMMA_12B_EXTRACTION_PARITY_FIXTURE_SHA256),
        tool_compaction=None,
    ),
    SequenceRepresentation.GEMMA4_12B_FULL_CONTEXT_V1: StateEncoderContract(
        contract_version=STATE_ENCODER_CONTRACT_VERSION,
        representation=SequenceRepresentation.GEMMA4_12B_FULL_CONTEXT_V1,
        state_definition=GEMMA_CANONICAL_STATE_DEFINITION,
        model_id=GEMMA_12B_MODEL_ID,
        model_revision=GEMMA_12B_MODEL_REVISION,
        tokenizer_revision=GEMMA_12B_MODEL_REVISION,
        serializer_version=GEMMA_CANONICAL_SERIALIZER_VERSION,
        serializer_sha256=sha256_text(_GEMMA_CANONICAL_SERIALIZER_SPEC),
        hidden_dim=GEMMA_12B_HIDDEN_DIM,
        extraction=("block_47_post_final_rmsnorm_pre_lm_head_last_boundary_token"),
        compute_dtype="bfloat16",
        wire_dtype="float32_le_base64",
        added_normalization="none",
        max_window_tokens=GEMMA_12B_MAX_WINDOW_TOKENS,
        window_policy="kv_cached_full_context_reanchor_past_window_v1",
        position_policy="absolute_from_window_start_zero_based_v1",
        extraction_parity_fixture_sha256=(
            GEMMA_12B_FULL_CONTEXT_EXTRACTION_PARITY_FIXTURE_SHA256
        ),
        tool_compaction=None,
    ),
    SequenceRepresentation.QWEN35_9B_CANONICAL_V1: StateEncoderContract(
        contract_version=STATE_ENCODER_CONTRACT_VERSION,
        representation=SequenceRepresentation.QWEN35_9B_CANONICAL_V1,
        state_definition=GEMMA_CANONICAL_STATE_DEFINITION,
        model_id=QWEN35_9B_MODEL_ID,
        model_revision=QWEN35_9B_MODEL_REVISION,
        tokenizer_revision=QWEN35_9B_MODEL_REVISION,
        serializer_version=QWEN35_CANONICAL_SERIALIZER_VERSION,
        serializer_sha256=sha256_text(_QWEN35_CANONICAL_SERIALIZER_SPEC),
        hidden_dim=QWEN35_9B_HIDDEN_DIM,
        extraction=("block_31_post_final_rmsnorm_pre_lm_head_last_boundary_token"),
        compute_dtype="bfloat16",
        wire_dtype="float32_le_base64",
        added_normalization="none",
        max_window_tokens=QWEN35_9B_MAX_WINDOW_TOKENS,
        window_policy="newest_token_suffix_ending_at_boundary_v1",
        position_policy="suffix_local_positions_zero_based_mrope_v1",
        extraction_parity_fixture_sha256=(QWEN35_9B_EXTRACTION_PARITY_FIXTURE_SHA256),
        tool_compaction=None,
    ),
    SequenceRepresentation.QWEN35_9B_FULL_CONTEXT_V1: StateEncoderContract(
        contract_version=STATE_ENCODER_CONTRACT_VERSION,
        representation=SequenceRepresentation.QWEN35_9B_FULL_CONTEXT_V1,
        state_definition=GEMMA_CANONICAL_STATE_DEFINITION,
        model_id=QWEN35_9B_MODEL_ID,
        model_revision=QWEN35_9B_MODEL_REVISION,
        tokenizer_revision=QWEN35_9B_MODEL_REVISION,
        serializer_version=QWEN35_CANONICAL_SERIALIZER_VERSION,
        serializer_sha256=sha256_text(_QWEN35_CANONICAL_SERIALIZER_SPEC),
        hidden_dim=QWEN35_9B_HIDDEN_DIM,
        extraction=("block_31_post_final_rmsnorm_pre_lm_head_last_boundary_token"),
        compute_dtype="bfloat16",
        wire_dtype="float32_le_base64",
        added_normalization="none",
        max_window_tokens=QWEN35_9B_FULL_CONTEXT_MAX_WINDOW_TOKENS,
        window_policy="kv_cached_full_context_reanchor_past_window_v1",
        position_policy="absolute_from_window_start_zero_based_v1",
        extraction_parity_fixture_sha256=(
            QWEN35_9B_FULL_CONTEXT_EXTRACTION_PARITY_FIXTURE_SHA256
        ),
        tool_compaction=None,
    ),
    SequenceRepresentation.GEMMA4_RAW_TOOL_TRACE_V1: StateEncoderContract(
        contract_version=STATE_ENCODER_CONTRACT_VERSION,
        representation=SequenceRepresentation.GEMMA4_RAW_TOOL_TRACE_V1,
        state_definition=GEMMA_STATE_DEFINITION,
        model_id=GEMMA_MODEL_ID,
        model_revision=GEMMA_MODEL_REVISION,
        tokenizer_revision=GEMMA_MODEL_REVISION,
        serializer_version=GEMMA_SERIALIZER_VERSION,
        serializer_sha256=sha256_text(_GEMMA_SERIALIZER_SPEC),
        hidden_dim=GEMMA_HIDDEN_DIM,
        extraction=("block_29_post_final_rmsnorm_pre_lm_head_last_boundary_token"),
        compute_dtype="bfloat16",
        wire_dtype="float32_le_base64",
        added_normalization="none",
        max_window_tokens=GEMMA_MAX_WINDOW_TOKENS,
        window_policy="newest_token_suffix_ending_at_boundary_v1",
        position_policy="suffix_local_positions_zero_based_v1",
        extraction_parity_fixture_sha256=(GEMMA_EXTRACTION_PARITY_FIXTURE_SHA256),
        tool_compaction=_TOOL_COMPACTION_CONTRACT,
    ),
    SequenceRepresentation.VOYAGE_4_LARGE_CANONICAL_V1: StateEncoderContract(
        contract_version=STATE_ENCODER_CONTRACT_VERSION,
        representation=SequenceRepresentation.VOYAGE_4_LARGE_CANONICAL_V1,
        state_definition=LEGACY_STATE_DEFINITION,
        model_id=RAW_V5_ROUTER_EMBEDDING_MODEL,
        model_revision=None,
        tokenizer_revision=None,
        serializer_version=LEGACY_SERIALIZER_VERSION,
        serializer_sha256=sha256_text(_LEGACY_SERIALIZER_SPEC),
        hidden_dim=RAW_V5_ROUTER_EMBEDDING_DIM,
        extraction="provider_embedding",
        compute_dtype="provider_defined",
        wire_dtype="provider_float_sequence",
        added_normalization="provider_defined",
        max_window_tokens=None,
        window_policy=None,
        position_policy=None,
        extraction_parity_fixture_sha256=None,
        tool_compaction=None,
    ),
    SequenceRepresentation.VOYAGE_4_LARGE_RAW_ROUTER_V5: StateEncoderContract(
        contract_version=STATE_ENCODER_CONTRACT_VERSION,
        representation=SequenceRepresentation.VOYAGE_4_LARGE_RAW_ROUTER_V5,
        state_definition=RAW_V5_VOYAGE_ELEMENT_STATE_DEFINITION,
        model_id=RAW_V5_ROUTER_EMBEDDING_MODEL,
        model_revision=None,
        tokenizer_revision=None,
        serializer_version=RAW_V5_VOYAGE_ELEMENT_SERIALIZER_VERSION,
        serializer_sha256=RAW_V5_VOYAGE_ELEMENT_INPUT_CONTRACT_SHA256,
        hidden_dim=RAW_V5_ROUTER_EMBEDDING_DIM,
        extraction="provider_embedding",
        compute_dtype="provider_defined",
        wire_dtype="provider_float_sequence",
        added_normalization="provider_defined",
        max_window_tokens=RAW_V5_ROUTER_MAX_INPUT_TOKENS,
        window_policy="exact_source_head_tail_with_marker_max_32000_tokens_v1",
        position_policy="raw_user_agent_event_order_v1",
        extraction_parity_fixture_sha256=None,
        tool_compaction={
            "source": "raw_agent_flow_turn_evidence_v1",
            "serialized_after_current_user_boundary": True,
            "provenance_fields_excluded_from_embedding_text": True,
        },
    ),
    SequenceRepresentation.VOYAGE_4_LARGE_RAW_ROUTER_V5_ONE_TURN: StateEncoderContract(
        contract_version=STATE_ENCODER_CONTRACT_VERSION,
        representation=SequenceRepresentation.VOYAGE_4_LARGE_RAW_ROUTER_V5_ONE_TURN,
        state_definition=RAW_V5_VOYAGE_ONE_TURN_STATE_DEFINITION,
        model_id=RAW_V5_ROUTER_EMBEDDING_MODEL,
        model_revision=None,
        tokenizer_revision=None,
        serializer_version=RAW_V5_VOYAGE_ONE_TURN_SERIALIZER_VERSION,
        serializer_sha256=RAW_V5_VOYAGE_ONE_TURN_INPUT_CONTRACT_SHA256,
        hidden_dim=RAW_V5_ROUTER_EMBEDDING_DIM,
        extraction="provider_embedding",
        compute_dtype="provider_defined",
        wire_dtype="provider_float_sequence",
        added_normalization="provider_defined",
        max_window_tokens=RAW_V5_ROUTER_MAX_INPUT_TOKENS,
        window_policy=(
            "causal_raw_v5_completed_turn_window_max_1_with_current_partial_round_"
            "exact_source_token_budget_32000_v1"
        ),
        position_policy="raw_user_agent_event_order_causal_turn_window_v1",
        extraction_parity_fixture_sha256=None,
        tool_compaction={
            "source": "raw_agent_flow_turn_evidence_v1",
            "serialized_after_current_user_boundary": True,
            "provenance_fields_excluded_from_embedding_text": True,
        },
    ),
    SequenceRepresentation.VOYAGE_4_LARGE_RAW_ROUTER_V5_TWO_TURN: StateEncoderContract(
        contract_version=STATE_ENCODER_CONTRACT_VERSION,
        representation=SequenceRepresentation.VOYAGE_4_LARGE_RAW_ROUTER_V5_TWO_TURN,
        state_definition=RAW_V5_VOYAGE_TWO_TURN_STATE_DEFINITION,
        model_id=RAW_V5_ROUTER_EMBEDDING_MODEL,
        model_revision=None,
        tokenizer_revision=None,
        serializer_version=RAW_V5_VOYAGE_TWO_TURN_SERIALIZER_VERSION,
        serializer_sha256=RAW_V5_VOYAGE_TWO_TURN_INPUT_CONTRACT_SHA256,
        hidden_dim=RAW_V5_ROUTER_EMBEDDING_DIM,
        extraction="provider_embedding",
        compute_dtype="provider_defined",
        wire_dtype="provider_float_sequence",
        added_normalization="provider_defined",
        max_window_tokens=RAW_V5_ROUTER_MAX_INPUT_TOKENS,
        window_policy=(
            "causal_raw_v5_completed_turn_window_max_2_with_current_partial_round_"
            "exact_source_token_budget_32000_v1"
        ),
        position_policy="raw_user_agent_event_order_causal_turn_window_v1",
        extraction_parity_fixture_sha256=None,
        tool_compaction={
            "source": "raw_agent_flow_turn_evidence_v1",
            "serialized_after_current_user_boundary": True,
            "provenance_fields_excluded_from_embedding_text": True,
        },
    ),
    SequenceRepresentation.VOYAGE_4_LARGE_RAW_ROUTER_V5_TEN_TURN: StateEncoderContract(
        contract_version=STATE_ENCODER_CONTRACT_VERSION,
        representation=SequenceRepresentation.VOYAGE_4_LARGE_RAW_ROUTER_V5_TEN_TURN,
        state_definition=RAW_V5_VOYAGE_TEN_TURN_STATE_DEFINITION,
        model_id=RAW_V5_ROUTER_EMBEDDING_MODEL,
        model_revision=None,
        tokenizer_revision=None,
        serializer_version=RAW_V5_VOYAGE_TEN_TURN_SERIALIZER_VERSION,
        serializer_sha256=RAW_V5_VOYAGE_TEN_TURN_INPUT_CONTRACT_SHA256,
        hidden_dim=RAW_V5_ROUTER_EMBEDDING_DIM,
        extraction="provider_embedding",
        compute_dtype="provider_defined",
        wire_dtype="provider_float_sequence",
        added_normalization="provider_defined",
        max_window_tokens=RAW_V5_ROUTER_MAX_INPUT_TOKENS,
        window_policy=(
            "causal_raw_v5_completed_turn_window_max_10_with_current_partial_round_"
            "exact_source_token_budget_32000_v1"
        ),
        position_policy="raw_user_agent_event_order_causal_turn_window_v1",
        extraction_parity_fixture_sha256=None,
        tool_compaction={
            "source": "raw_agent_flow_turn_evidence_v1",
            "serialized_after_current_user_boundary": True,
            "provenance_fields_excluded_from_embedding_text": True,
        },
    ),
    SequenceRepresentation.GEMINI_EMBEDDING_2_RAW_ROUTER_V5_ONE_TURN: StateEncoderContract(
        contract_version=STATE_ENCODER_CONTRACT_VERSION,
        representation=SequenceRepresentation.GEMINI_EMBEDDING_2_RAW_ROUTER_V5_ONE_TURN,
        state_definition=RAW_V5_GEMINI_ONE_TURN_STATE_DEFINITION,
        model_id=RAW_V5_GEMINI_EMBEDDING_2_MODEL,
        model_revision=None,
        tokenizer_revision=None,
        serializer_version=RAW_V5_GEMINI_ONE_TURN_SERIALIZER_VERSION,
        serializer_sha256=RAW_V5_GEMINI_ONE_TURN_INPUT_CONTRACT_SHA256,
        hidden_dim=RAW_V5_GEMINI_EMBEDDING_2_DIM,
        extraction="provider_embedding",
        compute_dtype="provider_defined",
        wire_dtype="provider_float_sequence",
        added_normalization="provider_defined",
        max_window_tokens=RAW_V5_GEMINI_EMBEDDING_2_MAX_INPUT_TOKENS,
        window_policy=(
            "causal_raw_v5_completed_turn_window_max_1_with_current_partial_round_"
            "gemini_8000_byte_oldest_round_drop_then_current_prefix_v3"
        ),
        position_policy="raw_user_agent_event_order_causal_turn_window_v1",
        extraction_parity_fixture_sha256=None,
        tool_compaction={
            "source": "raw_agent_flow_turn_evidence_v1",
            "serialized_after_current_user_boundary": True,
            "provenance_fields_excluded_from_embedding_text": True,
        },
    ),
    SequenceRepresentation.GEMINI_EMBEDDING_2_RAW_ROUTER_V5_TWO_TURN: StateEncoderContract(
        contract_version=STATE_ENCODER_CONTRACT_VERSION,
        representation=SequenceRepresentation.GEMINI_EMBEDDING_2_RAW_ROUTER_V5_TWO_TURN,
        state_definition=RAW_V5_GEMINI_TWO_TURN_STATE_DEFINITION,
        model_id=RAW_V5_GEMINI_EMBEDDING_2_MODEL,
        model_revision=None,
        tokenizer_revision=None,
        serializer_version=RAW_V5_GEMINI_TWO_TURN_SERIALIZER_VERSION,
        serializer_sha256=RAW_V5_GEMINI_TWO_TURN_INPUT_CONTRACT_SHA256,
        hidden_dim=RAW_V5_GEMINI_EMBEDDING_2_DIM,
        extraction="provider_embedding",
        compute_dtype="provider_defined",
        wire_dtype="provider_float_sequence",
        added_normalization="provider_defined",
        max_window_tokens=RAW_V5_GEMINI_EMBEDDING_2_MAX_INPUT_TOKENS,
        window_policy=(
            "causal_raw_v5_completed_turn_window_max_2_with_current_partial_round_"
            "gemini_8000_byte_oldest_round_drop_then_current_prefix_v3"
        ),
        position_policy="raw_user_agent_event_order_causal_turn_window_v1",
        extraction_parity_fixture_sha256=None,
        tool_compaction={
            "source": "raw_agent_flow_turn_evidence_v1",
            "serialized_after_current_user_boundary": True,
            "provenance_fields_excluded_from_embedding_text": True,
        },
    ),
    SequenceRepresentation.GEMINI_EMBEDDING_2_RAW_ROUTER_V5_TEN_TURN: StateEncoderContract(
        contract_version=STATE_ENCODER_CONTRACT_VERSION,
        representation=SequenceRepresentation.GEMINI_EMBEDDING_2_RAW_ROUTER_V5_TEN_TURN,
        state_definition=RAW_V5_GEMINI_TEN_TURN_STATE_DEFINITION,
        model_id=RAW_V5_GEMINI_EMBEDDING_2_MODEL,
        model_revision=None,
        tokenizer_revision=None,
        serializer_version=RAW_V5_GEMINI_TEN_TURN_SERIALIZER_VERSION,
        serializer_sha256=RAW_V5_GEMINI_TEN_TURN_INPUT_CONTRACT_SHA256,
        hidden_dim=RAW_V5_GEMINI_EMBEDDING_2_DIM,
        extraction="provider_embedding",
        compute_dtype="provider_defined",
        wire_dtype="provider_float_sequence",
        added_normalization="provider_defined",
        max_window_tokens=RAW_V5_GEMINI_EMBEDDING_2_MAX_INPUT_TOKENS,
        window_policy=(
            "causal_raw_v5_completed_turn_window_max_10_with_current_partial_round_"
            "gemini_8000_byte_oldest_round_drop_then_current_prefix_v3"
        ),
        position_policy="raw_user_agent_event_order_causal_turn_window_v1",
        extraction_parity_fixture_sha256=None,
        tool_compaction={
            "source": "raw_agent_flow_turn_evidence_v1",
            "serialized_after_current_user_boundary": True,
            "provenance_fields_excluded_from_embedding_text": True,
        },
    ),
    SequenceRepresentation.GEMINI_EMBEDDING_2_RAW_ROUTER_V5_USER_AGENT_ONLY: StateEncoderContract(
        contract_version=STATE_ENCODER_CONTRACT_VERSION,
        representation=(
            SequenceRepresentation.GEMINI_EMBEDDING_2_RAW_ROUTER_V5_USER_AGENT_ONLY
        ),
        state_definition=RAW_V5_GEMINI_USER_AGENT_ONLY_STATE_DEFINITION,
        model_id=RAW_V5_GEMINI_EMBEDDING_2_MODEL,
        model_revision=None,
        tokenizer_revision=None,
        serializer_version=RAW_V5_GEMINI_USER_AGENT_ONLY_SERIALIZER_VERSION,
        serializer_sha256=RAW_V5_GEMINI_USER_AGENT_ONLY_INPUT_CONTRACT_SHA256,
        hidden_dim=RAW_V5_GEMINI_EMBEDDING_2_DIM,
        extraction="provider_embedding",
        compute_dtype="provider_defined",
        wire_dtype="provider_float_sequence",
        added_normalization="provider_defined",
        max_window_tokens=RAW_V5_GEMINI_EMBEDDING_2_MAX_INPUT_TOKENS,
        window_policy=(
            "causal_raw_v5_current_user_agent_response_only_pair_"
            "gemini_8000_byte_current_prefix_v2"
        ),
        position_policy="raw_user_agent_event_order_current_turn_only_v1",
        extraction_parity_fixture_sha256=None,
        tool_compaction=None,
    ),
    SequenceRepresentation.GEMINI_EMBEDDING_2_RAW_ROUTER_V5_USER_AGENT_ONLY_TWO_TURN: StateEncoderContract(
        contract_version=STATE_ENCODER_CONTRACT_VERSION,
        representation=(
            SequenceRepresentation.GEMINI_EMBEDDING_2_RAW_ROUTER_V5_USER_AGENT_ONLY_TWO_TURN
        ),
        state_definition=RAW_V5_GEMINI_USER_AGENT_ONLY_TWO_TURN_STATE_DEFINITION,
        model_id=RAW_V5_GEMINI_EMBEDDING_2_MODEL,
        model_revision=None,
        tokenizer_revision=None,
        serializer_version=(RAW_V5_GEMINI_USER_AGENT_ONLY_TWO_TURN_SERIALIZER_VERSION),
        serializer_sha256=(
            RAW_V5_GEMINI_USER_AGENT_ONLY_TWO_TURN_INPUT_CONTRACT_SHA256
        ),
        hidden_dim=RAW_V5_GEMINI_EMBEDDING_2_DIM,
        extraction="provider_embedding",
        compute_dtype="provider_defined",
        wire_dtype="provider_float_sequence",
        added_normalization="provider_defined",
        max_window_tokens=RAW_V5_GEMINI_EMBEDDING_2_MAX_INPUT_TOKENS,
        window_policy=(
            "causal_raw_v5_user_agent_response_only_completed_turn_window_max_2_"
            "with_current_partial_round_gemini_8000_byte_oldest_round_drop_then_"
            "current_prefix_v2"
        ),
        position_policy="raw_user_agent_event_order_causal_turn_window_v1",
        extraction_parity_fixture_sha256=None,
        tool_compaction=None,
    ),
    SequenceRepresentation.GEMINI_EMBEDDING_2_RAW_ROUTER_V5_USER_AGENT_ONLY_TEN_TURN: StateEncoderContract(
        contract_version=STATE_ENCODER_CONTRACT_VERSION,
        representation=(
            SequenceRepresentation.GEMINI_EMBEDDING_2_RAW_ROUTER_V5_USER_AGENT_ONLY_TEN_TURN
        ),
        state_definition=RAW_V5_GEMINI_USER_AGENT_ONLY_TEN_TURN_STATE_DEFINITION,
        model_id=RAW_V5_GEMINI_EMBEDDING_2_MODEL,
        model_revision=None,
        tokenizer_revision=None,
        serializer_version=(RAW_V5_GEMINI_USER_AGENT_ONLY_TEN_TURN_SERIALIZER_VERSION),
        serializer_sha256=(
            RAW_V5_GEMINI_USER_AGENT_ONLY_TEN_TURN_INPUT_CONTRACT_SHA256
        ),
        hidden_dim=RAW_V5_GEMINI_EMBEDDING_2_DIM,
        extraction="provider_embedding",
        compute_dtype="provider_defined",
        wire_dtype="provider_float_sequence",
        added_normalization="provider_defined",
        max_window_tokens=RAW_V5_GEMINI_EMBEDDING_2_MAX_INPUT_TOKENS,
        window_policy=(
            "causal_raw_v5_user_agent_response_only_completed_turn_window_max_10_"
            "with_current_partial_round_gemini_8000_byte_oldest_round_drop_then_"
            "current_prefix_v2"
        ),
        position_policy="raw_user_agent_event_order_causal_turn_window_v1",
        extraction_parity_fixture_sha256=None,
        tool_compaction=None,
    ),
    SequenceRepresentation.GEMINI_EMBEDDING_2_RAW_ROUTER_V5_USER_AGENT_ONLY_LAST_STATE: StateEncoderContract(
        contract_version=STATE_ENCODER_CONTRACT_VERSION,
        representation=(
            SequenceRepresentation.GEMINI_EMBEDDING_2_RAW_ROUTER_V5_USER_AGENT_ONLY_LAST_STATE
        ),
        state_definition=(RAW_V5_GEMINI_USER_AGENT_ONLY_LAST_STATE_STATE_DEFINITION),
        model_id=RAW_V5_GEMINI_EMBEDDING_2_MODEL,
        model_revision=None,
        tokenizer_revision=None,
        serializer_version=(
            RAW_V5_GEMINI_USER_AGENT_ONLY_LAST_STATE_SERIALIZER_VERSION
        ),
        serializer_sha256=(
            RAW_V5_GEMINI_USER_AGENT_ONLY_LAST_STATE_INPUT_CONTRACT_SHA256
        ),
        hidden_dim=RAW_V5_GEMINI_EMBEDDING_2_DIM,
        extraction="provider_embedding",
        compute_dtype="provider_defined",
        wire_dtype="provider_float_sequence",
        added_normalization="provider_defined",
        max_window_tokens=RAW_V5_GEMINI_EMBEDDING_2_MAX_INPUT_TOKENS,
        window_policy=(
            "causal_raw_v5_previous_element_plus_current_user_agent_response_only_"
            "gemini_8000_byte_previous_drop_then_current_prefix_v2"
        ),
        position_policy=("raw_user_agent_event_order_previous_element_lookback_v1"),
        extraction_parity_fixture_sha256=None,
        tool_compaction=None,
    ),
    SequenceRepresentation.VOYAGE_CODE_4_RAW_ROUTER_V5_ELEMENT: StateEncoderContract(
        contract_version=STATE_ENCODER_CONTRACT_VERSION,
        representation=SequenceRepresentation.VOYAGE_CODE_4_RAW_ROUTER_V5_ELEMENT,
        state_definition=RAW_V5_VOYAGE_CODE_4_ELEMENT_STATE_DEFINITION,
        model_id=RAW_V5_VOYAGE_CODE_4_EMBEDDING_MODEL,
        model_revision=None,
        tokenizer_revision=None,
        serializer_version=RAW_V5_VOYAGE_CODE_4_ELEMENT_SERIALIZER_VERSION,
        serializer_sha256=RAW_V5_VOYAGE_CODE_4_ELEMENT_INPUT_CONTRACT_SHA256,
        hidden_dim=RAW_V5_ROUTER_EMBEDDING_DIM,
        extraction="provider_embedding",
        compute_dtype="provider_defined",
        wire_dtype="provider_float_sequence",
        added_normalization="provider_defined",
        max_window_tokens=RAW_V5_VOYAGE_CODE_4_ELEMENT_MAX_INPUT_TOKENS,
        window_policy="per_message_user_or_agent_alone_voyage_30000_token_head_tail_clip_v1",
        position_policy="raw_user_agent_event_order_per_message_v1",
        extraction_parity_fixture_sha256=None,
        tool_compaction=None,
    ),
    SequenceRepresentation.GEMINI_EMBEDDING_2_RAW_ROUTER_V5_ELEMENT: StateEncoderContract(
        contract_version=STATE_ENCODER_CONTRACT_VERSION,
        representation=SequenceRepresentation.GEMINI_EMBEDDING_2_RAW_ROUTER_V5_ELEMENT,
        state_definition=RAW_V5_GEMINI_ELEMENT_STATE_DEFINITION,
        model_id=RAW_V5_GEMINI_EMBEDDING_2_MODEL,
        model_revision=None,
        tokenizer_revision=None,
        serializer_version=RAW_V5_GEMINI_ELEMENT_SERIALIZER_VERSION,
        serializer_sha256=RAW_V5_GEMINI_ELEMENT_INPUT_CONTRACT_SHA256,
        hidden_dim=RAW_V5_GEMINI_EMBEDDING_2_DIM,
        extraction="provider_embedding",
        compute_dtype="provider_defined",
        wire_dtype="provider_float_sequence",
        added_normalization="provider_defined",
        max_window_tokens=RAW_V5_GEMINI_EMBEDDING_2_MAX_INPUT_TOKENS,
        window_policy="per_message_user_or_agent_alone_gemini_8000_byte_marked_prefix_v1",
        position_policy="raw_user_agent_event_order_per_message_v1",
        extraction_parity_fixture_sha256=None,
        tool_compaction=None,
    ),
}


def resolve_sequence_representation(
    value: str | SequenceRepresentation,
) -> SequenceRepresentation:
    """Parse a stable representation ID plus its public pipeline alias."""
    if isinstance(value, SequenceRepresentation):
        return value
    normalized = str(value).strip().lower()
    if normalized == GEMINI_EMBEDDING_2_NON_CANONICAL_ALIAS:
        return GEMINI_EMBEDDING_2_ROUTING_GROUP_V1
    for representation in SequenceRepresentation:
        if representation.value.lower() == normalized:
            return representation
    choices = ", ".join(item.value for item in SequenceRepresentation)
    raise ValueError(
        f"unknown HMM sequence representation {value!r}; expected one of {choices}"
    )


def get_state_encoder_contract(
    value: str | SequenceRepresentation,
) -> StateEncoderContract:
    return _CONTRACTS[resolve_sequence_representation(value)]
