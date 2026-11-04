"""Raw-V5 embeddings from a customer-hosted OpenAI-compatible ``/embeddings`` endpoint.

``HMM_EMBEDDING_BASE_URL`` (for example ``http://tei/v1``) and the optional
``HMM_EMBEDDING_API_KEY`` locate the endpoint. The request always names the
model the package was trained on; the startup check has already proven the
operator configured that same model.
"""

from __future__ import annotations

import asyncio
import os
import random
from typing import Any

import httpx

from hmm_sidecar.classifier_package.gemini_embeddings import (
    RETRY_BASE_DELAY_SECONDS,
    EmbeddingError,
    EmbeddingRunInfo,
    EmbeddingVector,
)

PROVIDER = "openai-compatible"
# TEI's default max_client_batch_size; most servers accept at least this many.
REQUEST_BATCH_SIZE = 32
DEFAULT_REQUEST_TIMEOUT_SECONDS = 30.0


class EmbeddingStatusError(EmbeddingError):
    def __init__(self, status_code: int) -> None:
        super().__init__(
            f"OpenAI-compatible embedding request failed with status {status_code}"
        )
        self.status_code = status_code


def embedding_endpoint() -> tuple[str, str]:
    base_url = os.environ.get("HMM_EMBEDDING_BASE_URL", "").strip().rstrip("/")
    if not base_url:
        raise EmbeddingError("HMM_EMBEDDING_BASE_URL is required")
    return base_url, os.environ.get("HMM_EMBEDDING_API_KEY", "").strip()


async def request_embeddings(
    client: httpx.AsyncClient,
    *,
    base_url: str,
    api_key: str,
    model: str,
    texts: list[str],
) -> list[list[float]]:
    headers = {"content-type": "application/json"}
    if api_key:
        headers["authorization"] = f"Bearer {api_key}"
    response = await client.post(
        f"{base_url}/embeddings",
        headers=headers,
        json={"model": model, "input": texts},
    )
    if response.is_error:
        raise EmbeddingStatusError(response.status_code)
    payload = response.json()
    data = payload.get("data") if isinstance(payload, dict) else None
    if not isinstance(data, list):
        raise EmbeddingError("OpenAI-compatible response lacks data")
    ordered = sorted(
        (item for item in data if isinstance(item, dict)),
        key=lambda item: int(item.get("index") or 0),
    )
    return [list(item.get("embedding") or []) for item in ordered]


def _retryable(exc: BaseException) -> bool:
    if isinstance(exc, EmbeddingStatusError):
        return exc.status_code == 429 or exc.status_code >= 500
    return isinstance(exc, httpx.TransportError)


async def _request_with_retries(
    client: httpx.AsyncClient,
    *,
    base_url: str,
    api_key: str,
    model: str,
    texts: list[str],
    max_retries: int,
) -> list[list[float]]:
    attempt = 0
    while True:
        try:
            return await request_embeddings(
                client, base_url=base_url, api_key=api_key, model=model, texts=texts
            )
        except Exception as exc:
            attempt += 1
            if attempt > max_retries or not _retryable(exc):
                if isinstance(exc, EmbeddingError):
                    raise
                raise EmbeddingError(
                    f"OpenAI-compatible embedding failed after {attempt} attempts: "
                    f"{type(exc).__name__}"
                ) from exc
            delay = RETRY_BASE_DELAY_SECONDS * (2 ** (attempt - 1))
            await asyncio.sleep(random.uniform(0, delay))


async def embed_texts_uncached(
    texts: list[str],
    *,
    model: str,
    max_retries: int = 6,
    vertex_request_timeout: float | None = None,
    return_info: bool = False,
    **_unused_provider_options: Any,
) -> dict[str, EmbeddingVector] | tuple[dict[str, EmbeddingVector], EmbeddingRunInfo]:
    base_url, api_key = embedding_endpoint()
    unique_texts = list(dict.fromkeys(texts))
    vectors: dict[str, EmbeddingVector] = {}
    async with httpx.AsyncClient(
        timeout=vertex_request_timeout or DEFAULT_REQUEST_TIMEOUT_SECONDS
    ) as client:
        for start in range(0, len(unique_texts), REQUEST_BATCH_SIZE):
            chunk = unique_texts[start : start + REQUEST_BATCH_SIZE]
            results = await _request_with_retries(
                client,
                base_url=base_url,
                api_key=api_key,
                model=model,
                texts=chunk,
                max_retries=max_retries,
            )
            if len(results) != len(chunk):
                raise EmbeddingError(
                    f"OpenAI-compatible endpoint returned {len(results)} vectors"
                    f" for {len(chunk)} inputs"
                )
            vectors.update(zip(chunk, results, strict=True))
    if not return_info:
        return vectors
    info = EmbeddingRunInfo(
        provider_requested=PROVIDER,
        provider_used=PROVIDER,
        fallback_used=False,
        requested_model=model,
        cache_model=f"{PROVIDER}:{model}",
        resolved_model=model,
    )
    return vectors, info
