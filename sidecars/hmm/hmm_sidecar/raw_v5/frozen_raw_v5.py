"""Frozen raw-V5 serving codecs from 31602cb06bcb7e312680be4863f670b6b90dde82.

Only known historical encoder identities select these builders. New training uses
the current token-budgeted builders; accepted old packages retain their original
8,000-byte Gemini and 20,000-character Voyage projections.
"""

from __future__ import annotations

from collections.abc import Mapping
from dataclasses import dataclass, replace
from typing import Any

from hmm_sidecar.raw_v5.state_representation import (
    SequenceRepresentation,
    StateEncoderContract,
    get_state_encoder_contract,
)

RAW_V5_MODEL_INPUT_MAX_CHARS = 20_000
RAW_V5_GEMINI_EMBEDDING_2_MAX_SERIALIZED_BYTES = 8_000


@dataclass(frozen=True)
class _FrozenEncoderIdentity:
    sha256: str
    serializer_version: str
    serializer_sha256: str
    window_policy: str
    max_window_tokens: int
    state_definition: str | None = None


_FROZEN_ENCODERS = {
    SequenceRepresentation.VOYAGE_4_LARGE_RAW_ROUTER_V5: _FrozenEncoderIdentity(
        sha256="ce504aff0383b19a8a17f23cd885772c018f957cd1422cde8257368c8dab4d44",
        state_definition="hmm-router-raw-agent-flow-v5-user-agent-structural-overflow-v1",
        serializer_version="hmm_router_raw_v5_model_input_structural_overflow_serializer_v1",
        serializer_sha256="ab92ed775ca768ac00f1a5feedf72cefcef823419924481fd669a6949439d672",
        window_policy="deterministic_structural_projection_max_20000_chars_v1",
        max_window_tokens=32000,
    ),
    SequenceRepresentation.VOYAGE_4_LARGE_RAW_ROUTER_V5_ONE_TURN: _FrozenEncoderIdentity(
        sha256="b2b0e6b09cd529b69ff4f5e89212d7e40203a1183e7220c9062c115239c9adee",
        serializer_version="hmm_router_raw_v5_voyage_one_turn_serializer_v1",
        serializer_sha256="e24bc3d0b53e94440e69f7a0391ca4e88de36c6811fccb060746d0eccd870786",
        window_policy="causal_raw_v5_completed_turn_window_max_1_with_current_partial_round_uniform_structural_normalization_v1",
        max_window_tokens=32000,
    ),
    SequenceRepresentation.VOYAGE_4_LARGE_RAW_ROUTER_V5_TWO_TURN: _FrozenEncoderIdentity(
        sha256="2740aa0a25cef463ea630ddcd081c6af0074ea21e3f0cc7a19b7d3218eba3a08",
        serializer_version="hmm_router_raw_v5_voyage_two_turn_serializer_v2",
        serializer_sha256="068dda873348614d9389b67fc2286feaada53ebd196db98394b59d776a2a44e7",
        window_policy="causal_raw_v5_completed_turn_window_max_2_with_current_partial_round_uniform_structural_normalization_v1",
        max_window_tokens=32000,
    ),
    SequenceRepresentation.VOYAGE_4_LARGE_RAW_ROUTER_V5_TEN_TURN: _FrozenEncoderIdentity(
        sha256="5b41718ae7c649da7e7283a3504d34104660f454de4f38db48ffe0e60fdd2fb7",
        serializer_version="hmm_router_raw_v5_voyage_ten_turn_serializer_v2",
        serializer_sha256="103d3aaa1821ad3e534d70e9f55ff1922930a61d9e2f546d9894c847df8879d8",
        window_policy="causal_raw_v5_completed_turn_window_max_10_with_current_partial_round_uniform_structural_normalization_v1",
        max_window_tokens=32000,
    ),
    SequenceRepresentation.GEMINI_EMBEDDING_2_RAW_ROUTER_V5_ONE_TURN: _FrozenEncoderIdentity(
        sha256="ba378f64ce913ff9868c7dcc6081d37033b480bee02c9e2ce9b6b6fb0018fa41",
        serializer_version="hmm_router_raw_v5_gemini_embedding_2_one_turn_serializer_v2",
        serializer_sha256="26a2f5eed52563f479e8f52067a1cf7bd19b164882d5b0f8a7bab94b65d70de3",
        window_policy="causal_raw_v5_completed_turn_window_max_1_with_current_partial_round_gemini_byte_safe_oldest_round_drop_then_current_prefix_v2",
        max_window_tokens=8192,
    ),
    SequenceRepresentation.GEMINI_EMBEDDING_2_RAW_ROUTER_V5_TWO_TURN: _FrozenEncoderIdentity(
        sha256="f3be40f18a1f4720f2eb9dc1ff2312056b274fef8a2dba47e7d1aa1b17590d94",
        serializer_version="hmm_router_raw_v5_gemini_embedding_2_two_turn_serializer_v2",
        serializer_sha256="11c47a59ad019a6e1484f03cbd65088bab94e73a22046deb958776fa8590ebab",
        window_policy="causal_raw_v5_completed_turn_window_max_2_with_current_partial_round_gemini_byte_safe_oldest_round_drop_then_current_prefix_v2",
        max_window_tokens=8192,
    ),
    SequenceRepresentation.GEMINI_EMBEDDING_2_RAW_ROUTER_V5_TEN_TURN: _FrozenEncoderIdentity(
        sha256="8dc8743803318dc317a31819550aa3638989a54ae4e82cc6fabf419980d415c7",
        serializer_version="hmm_router_raw_v5_gemini_embedding_2_ten_turn_serializer_v2",
        serializer_sha256="c628be166d4939cd16dca7422750c3539e58f3980abb0b9c80b5172d32d70709",
        window_policy="causal_raw_v5_completed_turn_window_max_10_with_current_partial_round_gemini_byte_safe_oldest_round_drop_then_current_prefix_v2",
        max_window_tokens=8192,
    ),
    SequenceRepresentation.GEMINI_EMBEDDING_2_RAW_ROUTER_V5_USER_AGENT_ONLY: _FrozenEncoderIdentity(
        sha256="0d0ab31f2040e90c8881bb6e0f71f7e41b2a67ee3fc8264dd594cf07372f4768",
        serializer_version="hmm_router_raw_v5_gemini_embedding_2_user_agent_only_serializer_v1",
        serializer_sha256="442b0968a2457bd10463f3da9223f4ed5e06df8542f67243ea6242665eca5b01",
        window_policy="causal_raw_v5_current_user_agent_response_only_pair_gemini_byte_safe_current_prefix_v1",
        max_window_tokens=8192,
    ),
    SequenceRepresentation.GEMINI_EMBEDDING_2_RAW_ROUTER_V5_USER_AGENT_ONLY_TWO_TURN: _FrozenEncoderIdentity(
        sha256="0e1c0c675a726600013380ee26c6df42852622b944e6664200905736bbf81149",
        serializer_version="hmm_router_raw_v5_gemini_embedding_2_user_agent_only_two_turn_serializer_v1",
        serializer_sha256="ce98e72003d09bd79ca775fd7ad17713781b8f31570d31b92bdfdd349654c604",
        window_policy="causal_raw_v5_user_agent_response_only_completed_turn_window_max_2_with_current_partial_round_gemini_byte_safe_oldest_round_drop_then_current_prefix_v1",
        max_window_tokens=8192,
    ),
    SequenceRepresentation.GEMINI_EMBEDDING_2_RAW_ROUTER_V5_USER_AGENT_ONLY_TEN_TURN: _FrozenEncoderIdentity(
        sha256="71a3c2ce1b7ad10b4f2477eb6b44170fec7145c722ebb4f9a6425a1c5b6c6039",
        serializer_version="hmm_router_raw_v5_gemini_embedding_2_user_agent_only_ten_turn_serializer_v1",
        serializer_sha256="57f3f4f2b101cd5e4aebb20c44d529245dbd92a78384b0fb7cd1cd2499e6b11d",
        window_policy="causal_raw_v5_user_agent_response_only_completed_turn_window_max_10_with_current_partial_round_gemini_byte_safe_oldest_round_drop_then_current_prefix_v1",
        max_window_tokens=8192,
    ),
    SequenceRepresentation.GEMINI_EMBEDDING_2_RAW_ROUTER_V5_USER_AGENT_ONLY_LAST_STATE: _FrozenEncoderIdentity(
        sha256="cb5f0031f73f51759d9aa409bf1b273a3ddc725c7c256133d0dff68db680f6b2",
        serializer_version="hmm_router_raw_v5_gemini_embedding_2_user_agent_only_last_state_serializer_v1",
        serializer_sha256="a407c6ec6fa64a3af4ddbabab9222b29e7c34dabe68a36135547ad35e88985cf",
        window_policy="causal_raw_v5_previous_element_plus_current_user_agent_response_only_gemini_byte_safe_previous_drop_then_current_prefix_v1",
        max_window_tokens=8192,
    ),
}


def frozen_raw_v5_encoder(
    representation: SequenceRepresentation,
) -> StateEncoderContract:
    """Reconstruct only known historical encoders and verify the full identity."""

    identity = _FROZEN_ENCODERS.get(representation)
    if identity is None:
        raise ValueError(f"No frozen raw-V5 encoder for {representation.value}")
    encoder = replace(
        get_state_encoder_contract(representation),
        serializer_version=identity.serializer_version,
        serializer_sha256=identity.serializer_sha256,
        window_policy=identity.window_policy,
        max_window_tokens=identity.max_window_tokens,
        state_definition=(
            identity.state_definition
            or get_state_encoder_contract(representation).state_definition
        ),
    )
    if encoder.sha256 != identity.sha256:
        raise ValueError(
            f"Frozen raw-V5 encoder identity drift: {representation.value}"
        )
    return encoder


def validate_raw_v5_serving_encoder(
    payload: Mapping[str, Any],
    *,
    expected_representation: SequenceRepresentation,
) -> StateEncoderContract:
    """Accept a complete current or explicitly pinned historical serving encoder."""

    current = get_state_encoder_contract(expected_representation)
    if dict(payload) == current.to_dict():
        return current
    frozen = frozen_raw_v5_encoder(expected_representation)
    if dict(payload) != frozen.to_dict():
        raise ValueError(
            "Raw-V5 serving encoder does not match the current or frozen registry: "
            f"{expected_representation.value}"
        )
    return frozen


def frozen_raw_v5_model_input_contract(
    current: dict[str, object],
    encoder: StateEncoderContract,
) -> dict[str, object]:
    raise NotImplementedError("not on the classifier serving path")
