from __future__ import annotations

import json

import pytest
import respx
from httpx import Response

from hmm_sidecar.classifier_package import openai_compatible_embeddings
from hmm_sidecar.classifier_package.complexity_classifier import (
    no_tool_context_feature_layout,
)
from hmm_sidecar.classifier_package.contract import (
    GEMINI_ELEMENT_INPUT_CONTRACT_SHA256,
    GEMINI_ELEMENT_SERIALIZER_VERSION,
)
from hmm_sidecar.classifier_package.core import canonical_json, sha256_text
from hmm_sidecar.classifier_package.gemini_embeddings import EmbeddingError
from hmm_sidecar.classifier_package.runtime_contract import (
    INTERACTION_STATE_RUNTIME_CONTRACT_VERSION,
    RuntimeContractError,
    parse_runtime_contract,
)
from hmm_sidecar.classifier_package.serving_profile import InteractionStateGeometry
from hmm_sidecar.classifier_package.state_representation import (
    SequenceRepresentation,
)

BASE_URL = "https://embed.customer.example/v1"
MODEL = "acme/embed-small"
DIM = 4
STATES = 3


def _runtime_contract(**overrides: object) -> dict[str, object]:
    geometry = {
        "hmm_chain": "interaction_state",
        "user_pca_dim": 2,
        "agent_pca_dim": 2,
        "element_lookback_rounds": 3,
        "element_lookback_source": "pca_projection",
    }
    layout = no_tool_context_feature_layout(
        STATES,
        embedding_dim=DIM,
        belief_dynamics=True,
        element_lookback=InteractionStateGeometry.model_validate(
            geometry
        ).element_lookback(embedding_dim=DIM),
    )
    contract: dict[str, object] = {
        "contract_version": INTERACTION_STATE_RUNTIME_CONTRACT_VERSION,
        "input_source": "raw_v5_live_conversation_messages",
        "serving_config": {
            "provider": "openai_compatible",
            "tool_evidence": "user_agent_only",
            "document_window": "per_message",
            "lookback_rounds": 1,
        },
        "interaction_state": geometry,
        "sequence_representation": (
            SequenceRepresentation.OPENAI_COMPATIBLE_RAW_ROUTER_V5_ELEMENT.value
        ),
        "embedding_model": MODEL,
        "embedding_dim": DIM,
        "document_serializer_version": GEMINI_ELEMENT_SERIALIZER_VERSION,
        "document_serializer_sha256": GEMINI_ELEMENT_INPUT_CONTRACT_SHA256,
        "hmm_states": STATES,
        "classes": ["low", "medium", "high", "maximum"],
        "classifier_feature_layout_sha256": sha256_text(canonical_json(layout)),
        "classifier_feature_dim": sum(int(block["dim"]) for block in layout),
        "reference_probes": [{"text": "probe", "vector": [1.0, 0.0, 0.0, 0.0]}],
    }
    contract.update(overrides)
    return contract


def test_customer_model_package_resolves_its_declared_encoder() -> None:
    profile = parse_runtime_contract(_runtime_contract()).resolved_profile()

    assert profile.embedding_model == MODEL
    assert profile.embedding_dim == DIM
    assert profile.state_encoder_contract.model_id == MODEL
    assert profile.state_encoder_contract.hidden_dim == DIM
    assert (
        profile.state_encoder_contract.representation
        is SequenceRepresentation.OPENAI_COMPATIBLE_RAW_ROUTER_V5_ELEMENT
    )


@pytest.mark.parametrize(
    ("overrides", "expected_error"),
    [
        ({"reference_probes": []}, "must declare reference_probes"),
        (
            {"reference_probes": [{"text": "probe", "vector": [1.0, 0.0]}]},
            "not 4 wide",
        ),
        ({"embedding_model": ""}, "must declare its embedding_model"),
    ],
)
def test_customer_model_package_without_a_servable_declaration_fails_closed(
    overrides: dict[str, object], expected_error: str
) -> None:
    with pytest.raises(RuntimeContractError, match=expected_error):
        parse_runtime_contract(_runtime_contract(**overrides))


@pytest.fixture
def endpoint(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("HMM_EMBEDDING_BASE_URL", BASE_URL + "/")
    monkeypatch.setenv("HMM_EMBEDDING_API_KEY", "customer-key")


@respx.mock
async def test_embeds_in_bounded_batches_with_the_package_model(endpoint) -> None:
    requests: list[dict[str, object]] = []

    def reply(request):
        body = json.loads(request.content)
        requests.append({"auth": request.headers["authorization"], **body})
        data = [
            {"index": index, "embedding": [float(len(text)), 0.0]}
            for index, text in reversed(list(enumerate(body["input"])))
        ]
        return Response(200, json={"data": data})

    respx.post(f"{BASE_URL}/embeddings").mock(side_effect=reply)
    texts = [f"t{index}" for index in range(40)] + ["t0"]

    vectors, info = await openai_compatible_embeddings.embed_texts_uncached(
        texts, model=MODEL, return_info=True
    )

    assert [len(request["input"]) for request in requests] == [32, 8]
    assert {request["model"] for request in requests} == {MODEL}
    assert {request["auth"] for request in requests} == {"Bearer customer-key"}
    assert vectors["t12"] == [3.0, 0.0]
    assert len(vectors) == 40
    assert info.provider_used == "openai-compatible"
    assert info.fallback_used is False


@respx.mock
async def test_retries_a_server_error_but_not_a_client_error(endpoint) -> None:
    route = respx.post(f"{BASE_URL}/embeddings").mock(
        side_effect=[
            Response(503),
            Response(200, json={"data": [{"index": 0, "embedding": [1.0]}]}),
        ]
    )
    vectors = await openai_compatible_embeddings.embed_texts_uncached(
        ["a"], model=MODEL, max_retries=1
    )
    assert vectors == {"a": [1.0]}
    assert route.call_count == 2

    route.side_effect = [Response(404)]
    with pytest.raises(EmbeddingError, match="status 404"):
        await openai_compatible_embeddings.embed_texts_uncached(
            ["b"], model=MODEL, max_retries=3
        )


@respx.mock
async def test_rejects_a_response_missing_vectors(endpoint) -> None:
    respx.post(f"{BASE_URL}/embeddings").mock(
        return_value=Response(200, json={"data": [{"index": 0, "embedding": [1.0]}]})
    )
    with pytest.raises(EmbeddingError, match="1 vectors for 2 inputs"):
        await openai_compatible_embeddings.embed_texts_uncached(["a", "b"], model=MODEL)


async def test_requires_a_base_url(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.delenv("HMM_EMBEDDING_BASE_URL", raising=False)
    with pytest.raises(EmbeddingError, match="HMM_EMBEDDING_BASE_URL"):
        await openai_compatible_embeddings.embed_texts_uncached(["a"], model=MODEL)


@pytest.mark.parametrize(
    "vector", [[0.0, 0.0, 0.0, 0.0], [float("nan"), 1.0, 0.0, 0.0]]
)
def test_customer_model_package_rejects_an_unusable_reference_vector(
    vector: list[float],
) -> None:
    with pytest.raises(RuntimeContractError, match="finite and non-zero"):
        parse_runtime_contract(
            _runtime_contract(reference_probes=[{"text": "probe", "vector": vector}])
        )


@pytest.mark.parametrize(
    ("data", "expected_error"),
    [
        (
            [{"index": 0, "embedding": [1.0]}, {"index": 0, "embedding": [2.0]}],
            "invalid index",
        ),
        (
            [{"index": 0, "embedding": [1.0]}, {"index": 2, "embedding": [2.0]}],
            "2 vectors for 2 inputs",
        ),
        (
            [{"index": "0", "embedding": [1.0]}, {"index": 1, "embedding": [2.0]}],
            "invalid index",
        ),
    ],
)
@respx.mock
async def test_rejects_response_indices_that_do_not_cover_each_input_once(
    endpoint, data: list[dict[str, object]], expected_error: str
) -> None:
    respx.post(f"{BASE_URL}/embeddings").mock(
        return_value=Response(200, json={"data": data})
    )
    with pytest.raises(EmbeddingError, match=expected_error):
        await openai_compatible_embeddings.embed_texts_uncached(["a", "b"], model=MODEL)


@pytest.mark.parametrize(
    ("embedding", "expected_error"),
    [
        ([1.0, 0.0, 0.0], "3-dimension vector; expected 4"),
        ([], "0-dimension vector"),
        ([1.0, float("inf"), 0.0, 0.0], "non-finite"),
    ],
)
@respx.mock
async def test_rejects_an_unusable_vector_before_it_can_be_cached(
    endpoint, embedding: list[float], expected_error: str
) -> None:
    respx.post(f"{BASE_URL}/embeddings").mock(
        return_value=Response(
            200, content=json.dumps({"data": [{"index": 0, "embedding": embedding}]})
        )
    )
    with pytest.raises(EmbeddingError, match=expected_error):
        await openai_compatible_embeddings.embed_texts_uncached(
            ["a"], model=MODEL, expected_dim=DIM
        )
