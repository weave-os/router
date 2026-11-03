"""Immutable state-encoder contracts for HMM conversation sequences."""

from __future__ import annotations

import json
from dataclasses import dataclass
from enum import Enum
from typing import Any, Mapping, Sequence

from hmm_sidecar.raw_v5.raw_v5_contract import (
    RAW_V5_GEMINI_ELEMENT_INPUT_CONTRACT_SHA256,
    RAW_V5_GEMINI_ELEMENT_SERIALIZER_VERSION,
    RAW_V5_GEMINI_ELEMENT_STATE_DEFINITION,
    RAW_V5_GEMINI_EMBEDDING_2_DIM,
    RAW_V5_GEMINI_EMBEDDING_2_MAX_INPUT_TOKENS,
    RAW_V5_GEMINI_EMBEDDING_2_MODEL,
    RAW_V5_VOYAGE_ONE_TURN_MAX_TURNS,
    RAW_V5_VOYAGE_TEN_TURN_MAX_TURNS,
    RAW_V5_VOYAGE_TWO_TURN_MAX_TURNS,
)
from hmm_sidecar.raw_v5.core import sha256_text

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

# gemini-embedding-2 rejects inputs above 8192 tokens. Budget one token of
# headroom so a boundary-exact document can never be truncated by the provider
# itself -- this serializer drops whole oldest turns instead, which keeps the
# document valid JSON and always retains the current turn.

# Preserve the already-pinned rolling-window geometry while changing only the
# execution service. Pure Transformers does not need vLLM's former reserved
# pooling slot, but changing this cap would create a different encoder domain.

# Gemma 4 12B is a separate dense checkpoint and encoder domain. It shares the
# canonical U/A serializer and rolling-window semantics with the 26B variants,
# but its model geometry and final hidden-state width are different.

# Qwen 3.5 9B is a separate long-context encoder domain. Its hybrid text
# backbone uses 24 Gated DeltaNet linear-attention blocks and eight full
# attention blocks. The checkpoint supports 262K positions, but this HMM
# encoder deliberately retains only the newest 50K tokens to bound each state
# forward while still exposing the final normalized text state.

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
# 64 divides the 2,112-wide shared MLP, 704-wide experts, and all attention
# projections. Larger 128-row blocks would leave the shared MLP unquantizable.


# These are SHA-256 digests of the exact prompt strings in
# agent_flow/extraction/conversation_dataset/prompts.py. Keeping the values in
# this lightweight module lets the Modal image share the contract without
# importing the Stage 2 processing stack.


# Stable semantic fixture identity. This hashes the exact input/specification,
# not an unexecuted success claim. A later authorized H100 conformance run must
# publish pass evidence and output hashes against this fixture before promotion.


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
    raise NotImplementedError(
        "unsupported by the served Gemini per-message raw-V5 profile"
    )


def gemini_two_turn_documents(
    texts: Sequence[str],
    kinds: Sequence[str] | None = None,
) -> tuple[str, ...]:
    raise NotImplementedError(
        "unsupported by the served Gemini per-message raw-V5 profile"
    )


def gemini_ten_turn_documents(
    texts: Sequence[str],
    kinds: Sequence[str] | None = None,
) -> tuple[str, ...]:
    raise NotImplementedError(
        "unsupported by the served Gemini per-message raw-V5 profile"
    )


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


# Append-only representation registry. Once a representation ID has produced a
# training artifact, its contract payload must remain immutable. A new input or
# relative-state format gets a new ``SequenceRepresentation`` value instead of
# replacing an existing entry and invalidating historical bundles.


# Serving supports one representation; see raw_v5_serving_profile._PROFILES.
_CONTRACTS: dict[SequenceRepresentation, StateEncoderContract] = {
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
