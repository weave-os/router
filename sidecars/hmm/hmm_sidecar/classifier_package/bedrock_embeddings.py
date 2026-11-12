"""Raw-V5 embeddings from an Amazon Bedrock embedding model via ``InvokeModel``.

Credentials and region come from the standard AWS chain (``AWS_REGION``, an
instance or task role, ``AWS_PROFILE`` or static keys). The request always
names the model the package was trained on and asks for its declared width.
"""

from __future__ import annotations

import asyncio
import json
import math
import os
from typing import Any

from hmm_sidecar.classifier_package.gemini_embeddings import (
    EmbeddingError,
    EmbeddingRunInfo,
    EmbeddingVector,
)

PROVIDER = "bedrock"
TITAN_V2_PREFIX = "amazon.titan-embed-text-v2"
COHERE_PREFIX = "cohere.embed-"
# Only Embed v4 accepts a configurable width; v3 is fixed at 1024.
COHERE_V4_PREFIX = "cohere.embed-v4"
# Cohere's InvokeModel accepts at most 96 texts per call; Titan takes one.
COHERE_BATCH_SIZE = 96

# Clients are shared across requests so connections and adaptive-retry state
# are reused; keyed by (region, max_attempts) like gemini_embeddings._CLIENTS.
_CLIENTS: dict[tuple[str | None, int | None], Any] = {}


def _region() -> str | None:
    # botocore itself only reads AWS_DEFAULT_REGION; honor AWS_REGION first.
    return os.environ.get("AWS_REGION", "").strip() or None


def _client(max_attempts: int | None = None) -> Any:
    import boto3
    from botocore.config import Config
    from botocore.exceptions import NoRegionError

    retries: dict[str, Any] = {"mode": "adaptive"}
    if max_attempts is not None:
        retries["max_attempts"] = max_attempts
    try:
        return boto3.client(
            "bedrock-runtime",
            region_name=_region(),
            config=Config(retries=retries),
        )
    except NoRegionError as error:
        raise EmbeddingError(
            "Bedrock embeddings need AWS_REGION (or AWS_DEFAULT_REGION)"
        ) from error


def _shared_client(max_attempts: int | None = None) -> Any:
    key = (_region(), max_attempts)
    client = _CLIENTS.get(key)
    if client is None:
        client = _client(max_attempts)
        _CLIENTS[key] = client
    return client


def _request_body(
    model: str, texts: list[str], expected_dim: int | None
) -> dict[str, Any]:
    if model.startswith(TITAN_V2_PREFIX):
        body: dict[str, Any] = {"inputText": texts[0], "normalize": True}
        if expected_dim is not None:
            body["dimensions"] = expected_dim
        return body
    if model.startswith(COHERE_PREFIX):
        body = {
            "texts": texts,
            "input_type": "search_document",
            "embedding_types": ["float"],
        }
        if expected_dim is not None and model.startswith(COHERE_V4_PREFIX):
            body["output_dimension"] = expected_dim
        return body
    raise EmbeddingError(f"unsupported Bedrock embedding model {model!r}")


def _vectors(model: str, payload: dict[str, Any]) -> list[list[float]]:
    if model.startswith(TITAN_V2_PREFIX):
        return [list(payload.get("embedding") or [])]
    embeddings = payload.get("embeddings")
    if isinstance(embeddings, dict):
        embeddings = embeddings.get("float")
    if not isinstance(embeddings, list):
        raise EmbeddingError("Bedrock response lacks embeddings")
    return [list(vector or []) for vector in embeddings]


def _invoke(
    client: Any, model: str, texts: list[str], expected_dim: int | None
) -> list[list[float]]:
    try:
        response = client.invoke_model(
            modelId=model,
            body=json.dumps(_request_body(model, texts, expected_dim)),
            contentType="application/json",
            accept="application/json",
        )
        payload = json.loads(response["body"].read())
    except EmbeddingError:
        raise
    except Exception as exc:
        raise EmbeddingError(
            f"Bedrock embedding failed: {type(exc).__name__}: {exc}"
        ) from exc
    vectors = _vectors(model, payload)
    if len(vectors) != len(texts):
        raise EmbeddingError(
            f"Bedrock returned {len(vectors)} vectors for {len(texts)} inputs"
        )
    for vector in vectors:
        if not vector or (expected_dim is not None and len(vector) != expected_dim):
            raise EmbeddingError(
                f"Bedrock returned a {len(vector)}-dimension vector; expected {expected_dim}"
            )
        if not all(math.isfinite(value) for value in vector):
            raise EmbeddingError("Bedrock returned a non-finite vector")
    return vectors


def _batches(model: str, texts: list[str]) -> list[list[str]]:
    size = COHERE_BATCH_SIZE if model.startswith(COHERE_PREFIX) else 1
    return [texts[start : start + size] for start in range(0, len(texts), size)]


async def embed_texts_uncached(
    texts: list[str],
    *,
    model: str,
    max_retries: int = 6,
    vertex_request_concurrency: int = 1,
    return_info: bool = False,
    expected_dim: int | None = None,
    **_unused_provider_options: Any,
) -> dict[str, EmbeddingVector] | tuple[dict[str, EmbeddingVector], EmbeddingRunInfo]:
    unique_texts = list(dict.fromkeys(texts))
    # Building the boto3 client can hit the network for role credentials.
    client = await asyncio.to_thread(_shared_client, max_retries + 1)
    batches = _batches(model, unique_texts)
    vectors: dict[str, EmbeddingVector] = {}
    concurrency = max(vertex_request_concurrency, 1)
    for start in range(0, len(batches), concurrency):
        group = batches[start : start + concurrency]
        results = await asyncio.gather(
            *(
                asyncio.to_thread(_invoke, client, model, batch, expected_dim)
                for batch in group
            )
        )
        for batch, batch_vectors in zip(group, results, strict=True):
            vectors.update(zip(batch, batch_vectors, strict=True))
    if not return_info:
        return vectors
    info = EmbeddingRunInfo(
        provider_requested=PROVIDER,
        provider_used=PROVIDER,
        fallback_used=False,
        requested_model=model,
        cache_model=f"{PROVIDER}:{model}:dim={expected_dim}",
        resolved_model=model,
        output_dimensionality=expected_dim,
    )
    return vectors, info
