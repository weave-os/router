from __future__ import annotations

import hashlib
import json
from pathlib import Path
from types import SimpleNamespace

import numpy as np
import pytest

from hmm_sidecar.classifier_package.complexity_labels import COMPLEXITY_CLASSES
from hmm_sidecar.classifier_package.complexity_classifier import (
    no_tool_context_feature_layout,
)
from hmm_sidecar.classifier_package.core import canonical_json
from hmm_sidecar.classifier_package.live_sequence import live_sequence_turns
from hmm_sidecar.classifier_package.route_selector import ComplexityRouteSelector
from hmm_sidecar.classifier_package.runtime_contract import (
    LEGACY_TURN_WINDOW_FEATURE_DIM,
    LEGACY_TURN_WINDOW_FEATURE_LAYOUT_SHA256,
    LEGACY_TURN_WINDOW_HMM_STATES,
    LEGACY_TURN_WINDOW_PCA_DIM,
    LEGACY_TURN_WINDOW_RUNTIME_CONTRACT_VERSION,
    LEGACY_TURN_WINDOW_SERVING_INPUT_SHA256,
    LEGACY_TURN_WINDOW_HMM_TRAINING_CONTRACT_SHA256,
    LEGACY_TURN_WINDOW_HMM_TRAINING_CONTRACT_VERSION,
    parse_runtime_contract,
)
from hmm_sidecar.classifier_package.serving_profile import (
    legacy_gemini_user_agent_turn_window_profile,
)

PARITY_FIXTURE = (
    Path(__file__).parent
    / "fixtures/classifier_package/legacy_v1_turn_window_parity.json"
)


def _v1_runtime_contract() -> dict[str, object]:
    profile = legacy_gemini_user_agent_turn_window_profile()
    return {
        "contract_version": LEGACY_TURN_WINDOW_RUNTIME_CONTRACT_VERSION,
        "input_source": "raw_v5_live_conversation_messages",
        "serving_config": {
            "provider": "gemini",
            "tool_evidence": "user_agent_only",
            "document_window": "turn_window",
            "lookback_rounds": 1,
        },
        "sequence_representation": profile.representation.value,
        "embedding_provider": "gemini",
        "embedding_model": profile.embedding_model,
        "embedding_dim": profile.embedding_dim,
        "document_serializer_version": profile.document_serializer_version,
        "document_serializer_sha256": profile.document_serializer_sha256,
        "state_encoder_contract_sha256": profile.state_encoder_contract.sha256,
        "serving_input_sha256": LEGACY_TURN_WINDOW_SERVING_INPUT_SHA256,
        "hmm_training_contract_version": LEGACY_TURN_WINDOW_HMM_TRAINING_CONTRACT_VERSION,
        "hmm_training_contract_sha256": LEGACY_TURN_WINDOW_HMM_TRAINING_CONTRACT_SHA256,
        "hmm_states": LEGACY_TURN_WINDOW_HMM_STATES,
        "pca_dim": LEGACY_TURN_WINDOW_PCA_DIM,
        "classes": list(COMPLEXITY_CLASSES),
        "classifier_feature_layout_sha256": LEGACY_TURN_WINDOW_FEATURE_LAYOUT_SHA256,
        "classifier_feature_dim": LEGACY_TURN_WINDOW_FEATURE_DIM,
        "no_canonical_fallback": True,
    }


def _parity_messages() -> list[dict[str, object]]:
    messages: list[dict[str, object]] = []
    for turn_index in range(6):
        user_text = (
            f"User request {turn_index}: parse the input and preserve the result. "
        )
        agent_text = f"Agent answer {turn_index}: completed the requested step."
        if turn_index == 4:
            user_text += "x" * 4000
            agent_text += "a" * 2500
        messages.extend(
            (
                {"role": "user", "text": user_text},
                {
                    "role": "assistant",
                    "text": agent_text,
                    "tool_calls": [
                        {"name": "shell", "input_keys": ["private-tool-marker"]}
                    ],
                    "tool_results": [{"text": "private-tool-marker"}],
                },
            )
        )
    messages.append(
        {"role": "user", "text": "Final request stays at the causal boundary."}
    )
    return messages


def test_v1_turn_window_contract_is_an_exact_supported_artifact_geometry() -> None:
    runtime_contract = parse_runtime_contract(_v1_runtime_contract())

    assert (
        runtime_contract.resolved_profile()
        == legacy_gemini_user_agent_turn_window_profile()
    )
    assert runtime_contract.reference_probes == ()
    assert runtime_contract.interaction_state is None

    for unsupported_change in (
        {
            "serving_config": {
                "provider": "gemini",
                "tool_evidence": "user_agent_only",
                "document_window": "per_message",
                "lookback_rounds": 1,
            }
        },
        {"hmm_training_contract_sha256": "0" * 64},
    ):
        invalid_contract = _v1_runtime_contract()
        invalid_contract.update(unsupported_change)
        with pytest.raises(ValueError):
            parse_runtime_contract(invalid_contract)


def test_turn_window_documents_match_workweave_v1_reference_fixture() -> None:
    fixture = json.loads(PARITY_FIXTURE.read_text())
    turns, debug = live_sequence_turns(
        _parity_messages(),
        fallback_text="Final request stays at the causal boundary.",
        profile=legacy_gemini_user_agent_turn_window_profile(),
        max_elements=fixture["max_elements"],
    )

    observed_documents = [
        {
            "sha256": hashlib.sha256(turn.turn_text.encode()).hexdigest(),
            "byte_count": len(turn.turn_text.encode()),
        }
        for turn in turns
    ]
    assert observed_documents == fixture["expected_documents"]
    assert [list(turn.categorical) for turn in turns] == fixture["expected_categories"]
    assert len(turns) == fixture["expected_turn_count"]
    assert debug["hmm_sequence_truncated_elements"] == 8
    assert all("private-tool-marker" not in turn.turn_text for turn in turns)


def test_v1_feature_construction_matches_workweave_reference_fixture() -> None:
    fixture = json.loads(PARITY_FIXTURE.read_text())
    runtime_contract = parse_runtime_contract(_v1_runtime_contract())
    profile = runtime_contract.resolved_profile()
    layout = no_tool_context_feature_layout(
        runtime_contract.hmm_states,
        embedding_dim=profile.embedding_dim,
        belief_dynamics=False,
    )
    classifier = SimpleNamespace(
        classes=COMPLEXITY_CLASSES,
        metadata={"feature_layout": layout, "state_repr": "embedding"},
        sequence_autoencoder_identity=None,
    )
    selector = ComplexityRouteSelector(
        classifier=classifier,
        confidence_threshold=0.0,
    )
    features = selector.serving_features(
        embedding=[float(index % 7) / 7 for index in range(profile.embedding_dim)],
        gamma_row=[1.0 / runtime_contract.hmm_states] * runtime_contract.hmm_states,
        current_state=7,
        previous_state=6,
        position=4,
        prefix_length=5,
    )

    assert features.shape == (fixture["feature_dim"],)
    assert hashlib.sha256(features.astype(np.float64).tobytes()).hexdigest() == (
        fixture["feature_vector_sha256"]
    )
    assert hashlib.sha256(canonical_json(layout).encode()).hexdigest() == (
        fixture["feature_layout_sha256"]
    )
