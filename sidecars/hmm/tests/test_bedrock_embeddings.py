from __future__ import annotations

import io
import json

import pytest

from hmm_sidecar.classifier_package import bedrock_embeddings
from hmm_sidecar.classifier_package.gemini_embeddings import EmbeddingError

TITAN = "amazon.titan-embed-text-v2:0"
COHERE = "cohere.embed-english-v3"


class _FakeBedrock:
    def __init__(self, reply) -> None:
        self.reply = reply
        self.requests: list[dict[str, object]] = []

    def invoke_model(self, *, modelId, body, contentType, accept):
        request = {"modelId": modelId, **json.loads(body)}
        self.requests.append(request)
        return {"body": io.BytesIO(json.dumps(self.reply(request)).encode())}


@pytest.fixture
def bedrock(monkeypatch: pytest.MonkeyPatch):
    def install(reply):
        fake = _FakeBedrock(reply)
        monkeypatch.setattr(bedrock_embeddings, "_client", lambda: fake)
        return fake

    return install


async def test_titan_embeds_each_text_at_the_package_width(bedrock) -> None:
    fake = bedrock(
        lambda request: {"embedding": [float(len(request["inputText"])), 1.0]}
    )

    vectors, info = await bedrock_embeddings.embed_texts_uncached(
        ["ab", "abc", "ab"], model=TITAN, expected_dim=2, return_info=True
    )

    assert vectors == {"ab": [2.0, 1.0], "abc": [3.0, 1.0]}
    assert [request["dimensions"] for request in fake.requests] == [2, 2]
    assert {request["normalize"] for request in fake.requests} == {True}
    assert {request["modelId"] for request in fake.requests} == {TITAN}
    assert info.provider_used == "bedrock"


async def test_cohere_batches_texts_as_documents(bedrock) -> None:
    fake = bedrock(
        lambda request: {
            "embeddings": {"float": [[1.0, 0.0] for _ in request["texts"]]}
        }
    )

    vectors = await bedrock_embeddings.embed_texts_uncached(
        [f"t{index}" for index in range(100)], model=COHERE, expected_dim=2
    )

    assert len(vectors) == 100
    assert [len(request["texts"]) for request in fake.requests] == [96, 4]
    assert {request["input_type"] for request in fake.requests} == {"search_document"}


@pytest.mark.parametrize(
    ("reply", "expected_error"),
    [
        ({"embedding": [1.0]}, "1-dimension vector; expected 2"),
        ({"embedding": [1.0, float("nan")]}, "non-finite"),
        ({"embedding": []}, "0-dimension vector"),
    ],
)
async def test_rejects_an_unusable_vector_before_it_can_be_cached(
    bedrock, reply: dict[str, object], expected_error: str
) -> None:
    bedrock(lambda request: reply)

    with pytest.raises(EmbeddingError, match=expected_error):
        await bedrock_embeddings.embed_texts_uncached(
            ["a"], model=TITAN, expected_dim=2
        )


async def test_wraps_an_aws_failure_and_rejects_unknown_models(bedrock) -> None:
    def deny(request):
        raise RuntimeError("AccessDeniedException")

    bedrock(deny)
    with pytest.raises(EmbeddingError, match="AccessDeniedException"):
        await bedrock_embeddings.embed_texts_uncached(["a"], model=TITAN)
    with pytest.raises(EmbeddingError, match="unsupported Bedrock embedding model"):
        await bedrock_embeddings.embed_texts_uncached(["a"], model="meta.llama")


def test_client_reads_aws_region_and_fails_clearly_without_one(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.delenv("AWS_DEFAULT_REGION", raising=False)
    monkeypatch.setenv("AWS_CONFIG_FILE", "/nonexistent")
    monkeypatch.setenv("AWS_REGION", "us-west-2")
    assert bedrock_embeddings._client().meta.region_name == "us-west-2"

    monkeypatch.delenv("AWS_REGION")
    with pytest.raises(EmbeddingError, match="need AWS_REGION"):
        bedrock_embeddings._client()
