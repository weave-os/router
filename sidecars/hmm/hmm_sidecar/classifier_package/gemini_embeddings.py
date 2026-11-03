"""Gemini embeddings for raw-V5 serving, over Vertex AI or a Gemini API key.

The call shape (one ``embed_content`` request per text, fixed output
dimensionality, no auto-truncation) matches the production Vertex path so the
same document always produces the same vector.
"""

from __future__ import annotations

import asyncio
import os
import random
from array import array
from dataclasses import dataclass
from enum import StrEnum
from typing import Any, TypeAlias

EmbeddingVector: TypeAlias = list[float] | array

DEFAULT_VERTEX_LOCATION = "global"
DEFAULT_VERTEX_OUTPUT_DIMENSIONALITY = 3072
RETRY_BASE_DELAY_SECONDS = 0.5


class EmbeddingError(RuntimeError):
    pass


class GeminiTransport(StrEnum):
    VERTEX = "vertex"
    API_KEY = "api_key"


@dataclass(frozen=True)
class EmbeddingRunInfo:
    provider_requested: str
    provider_used: str
    fallback_used: bool
    requested_model: str
    cache_model: str
    resolved_model: str | None = None
    output_dimensionality: int | None = None
    task_type: str | None = None
    vertex_project: str | None = None
    vertex_location: str | None = None


def _gemini_api_key() -> str:
    return (
        os.environ.get("GEMINI_API_KEY", "").strip()
        or os.environ.get("GOOGLE_API_KEY", "").strip()
    )


def resolve_gemini_transport() -> GeminiTransport:
    configured = os.environ.get("HMM_GEMINI_TRANSPORT", "").strip().lower()
    if configured:
        try:
            return GeminiTransport(configured)
        except ValueError as exc:
            raise EmbeddingError(
                "HMM_GEMINI_TRANSPORT must be 'vertex' or 'api_key'"
            ) from exc
    return GeminiTransport.API_KEY if _gemini_api_key() else GeminiTransport.VERTEX


def _vertex_project(explicit: str | None) -> str:
    project = (
        (explicit or "").strip()
        or os.environ.get("HMM_VERTEX_PROJECT", "").strip()
        or os.environ.get("GOOGLE_CLOUD_PROJECT", "").strip()
    )
    if not project:
        raise EmbeddingError(
            "Vertex embeddings need HMM_VERTEX_PROJECT or GOOGLE_CLOUD_PROJECT"
        )
    return project


_CLIENTS: dict[tuple[str, str, str], Any] = {}


def _client(transport: GeminiTransport, project: str, location: str) -> Any:
    from google import genai

    key = (transport.value, project, location)
    client = _CLIENTS.get(key)
    if client is not None:
        return client
    if transport is GeminiTransport.API_KEY:
        api_key = _gemini_api_key()
        if not api_key:
            raise EmbeddingError("GEMINI_API_KEY or GOOGLE_API_KEY is required")
        client = genai.Client(api_key=api_key)
    else:
        client = genai.Client(vertexai=True, project=project, location=location)
    _CLIENTS[key] = client
    return client


def _resolved_model(model: str, vertex_model: str | None) -> str:
    return (vertex_model or model).removeprefix("google/").removeprefix("models/")


def _retryable(exc: BaseException) -> bool:
    """Rate limits, server errors, timeouts and transport failures are retried;
    other client errors (bad request, auth, quota config) fail at once."""
    import httpx
    from google.genai import errors

    if isinstance(exc, errors.APIError):
        return exc.code == 429 or exc.code >= 500
    return isinstance(exc, (asyncio.TimeoutError, httpx.TransportError, OSError))


async def _embed_one(
    client: Any,
    *,
    model: str,
    text: str,
    config: Any,
    max_retries: int,
    timeout: float | None,
) -> list[float]:
    attempt = 0
    while True:
        try:
            call = client.aio.models.embed_content(
                model=model, contents=text, config=config
            )
            response = await (asyncio.wait_for(call, timeout) if timeout else call)
            embeddings = response.embeddings or []
            if len(embeddings) != 1 or not embeddings[0].values:
                raise EmbeddingError("Gemini embedding response lacks one vector")
            return [float(value) for value in embeddings[0].values]
        except EmbeddingError:
            raise
        except Exception as exc:
            attempt += 1
            if attempt > max_retries or not _retryable(exc):
                raise EmbeddingError(
                    f"Gemini embedding failed after {attempt} attempts: "
                    f"{type(exc).__name__}"
                ) from exc
            delay = RETRY_BASE_DELAY_SECONDS * (2 ** (attempt - 1))
            await asyncio.sleep(random.uniform(0, delay))


async def embed_texts_uncached(
    texts: list[str],
    *,
    model: str,
    max_retries: int = 6,
    use_vertex: bool = True,
    vertex_project: str | None = None,
    vertex_location: str | None = None,
    vertex_model: str | None = None,
    vertex_output_dimensionality: int | None = DEFAULT_VERTEX_OUTPUT_DIMENSIONALITY,
    vertex_task_type: str | None = None,
    vertex_request_timeout: float | None = None,
    vertex_request_concurrency: int = 1,
    return_info: bool = False,
    **_unused_provider_options: Any,
) -> dict[str, list[float]] | tuple[dict[str, list[float]], EmbeddingRunInfo]:
    from google.genai import types

    transport = resolve_gemini_transport()
    location = (
        vertex_location
        or os.environ.get("HMM_VERTEX_LOCATION", "").strip()
        or DEFAULT_VERTEX_LOCATION
    ).strip()
    project = (
        _vertex_project(vertex_project) if transport is GeminiTransport.VERTEX else ""
    )
    resolved_model = _resolved_model(model, vertex_model)
    config_args: dict[str, object] = {"auto_truncate": False}
    if vertex_output_dimensionality is not None:
        config_args["output_dimensionality"] = vertex_output_dimensionality
    if vertex_task_type:
        config_args["task_type"] = vertex_task_type
    config = types.EmbedContentConfig(**config_args)
    client = _client(transport, project, location)
    unique_texts = list(dict.fromkeys(texts))
    concurrency = vertex_request_concurrency
    vectors: dict[str, list[float]] = {}
    for start in range(0, len(unique_texts), concurrency):
        chunk = unique_texts[start : start + concurrency]
        results = await asyncio.gather(
            *(
                _embed_one(
                    client,
                    model=resolved_model,
                    text=text,
                    config=config,
                    max_retries=max_retries,
                    timeout=vertex_request_timeout,
                )
                for text in chunk
            )
        )
        vectors.update(zip(chunk, results, strict=True))
    if not return_info:
        return vectors
    task = vertex_task_type or "default"
    info = EmbeddingRunInfo(
        provider_requested=transport.value,
        provider_used=transport.value,
        fallback_used=False,
        requested_model=model,
        cache_model=(
            f"vertex:{resolved_model}:dim={vertex_output_dimensionality}:task={task}"
        ),
        resolved_model=resolved_model,
        output_dimensionality=vertex_output_dimensionality,
        task_type=vertex_task_type,
        vertex_project=project or None,
        vertex_location=location if transport is GeminiTransport.VERTEX else None,
    )
    return vectors, info


async def embed_texts(texts: list[str], **_kwargs: Any) -> Any:
    raise EmbeddingError("the public sidecar does not serve from a replay cache")
