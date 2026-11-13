"""Bounded authenticated classification; no prompt logging or model selection."""

from __future__ import annotations

import asyncio
import hmac
import json
import time
from collections.abc import Sequence
from typing import Final, Literal, Protocol

from fastapi import FastAPI, HTTPException, Request
from pydantic import BaseModel, ConfigDict, ValidationError
from starlette.concurrency import run_in_threadpool

from contract import BUDGET_HEADER, MAX_BUDGET_MILLISECONDS, MAX_INPUT_BYTES, MAX_INPUT_TOKENS, OUTPUT, PROJECTION, SCHEMA

# Callers that predate the budget header get the router's three-second budget less its commit reserve.
DEFAULT_BUDGET_SECONDS: Final = 2.9
# Requests admitted at once; the engine batches them continuously and queues beyond its own
# sequence limit. Validated on an L4 with 96 simultaneous requests finishing within 1.5s.
MAX_IN_FLIGHT: Final = 256


class TokenLimitExceeded(Exception):
    pass


class Predictor(Protocol):
    def encode(self, text: str) -> list[int]: ...

    async def classify(self, tokens: Sequence[int]) -> str: ...


class ClassificationRequest(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True)
    schema_version: Literal["task_domain_classifier_v1"]
    release_sha256: str
    projection_version: Literal["initial_logical_user_turn_v1"]
    user_text: str


async def warm_up(predictor: Predictor, texts: Sequence[str]) -> None:
    """Runs each input alone, then all together, twice; only the second, verified pass gates serving."""
    encoded: list[list[int]] = [predictor.encode(text) for text in texts]

    async def run_pass() -> list[str]:
        alone: list[str] = [await predictor.classify(tokens) for tokens in encoded]
        return alone + list(await asyncio.gather(*(predictor.classify(tokens) for tokens in encoded)))

    await run_pass()
    for index, output in enumerate(await run_pass()):
        if not OUTPUT.fullmatch(output):
            raise RuntimeError(f"task classifier warmup produced invalid output for warmup input {index}")


def request_deadline(budget_header: str | None) -> float:
    if budget_header is None:
        return time.monotonic() + DEFAULT_BUDGET_SECONDS
    # The length check keeps int() from rejecting very long digit strings with a ValueError.
    if (not (budget_header.isascii() and budget_header.isdecimal()) or len(budget_header) > len(str(MAX_BUDGET_MILLISECONDS))
            or not 1 <= int(budget_header) <= MAX_BUDGET_MILLISECONDS):
        raise HTTPException(400, "invalid classification budget")
    return time.monotonic() + int(budget_header) / 1000


async def cancel_on_disconnect(request: Request, classification_task: asyncio.Task[tuple[list[int], str]]) -> None:
    """Cancels in-flight work once the caller hangs up, which aborts it in the engine."""
    while (await request.receive())["type"] != "http.disconnect":
        pass
    classification_task.cancel()


async def tokenize_and_classify(predictor: Predictor, text: str) -> tuple[list[int], str]:
    try:
        tokens: list[int] = await run_in_threadpool(predictor.encode, text)
    except ValueError:
        raise TokenLimitExceeded() from None
    return tokens, await predictor.classify(tokens)


def require_strong_bearer(bearer: str) -> None:
    if len(bearer) < 32 or "\n" in bearer or "\r" in bearer:
        raise ValueError("task classifier requires a strong bearer secret")


def create_app(predictor: Predictor, release_sha256: str, bearer: str) -> FastAPI:
    require_strong_bearer(bearer)
    app: FastAPI = FastAPI()
    in_flight: int = 0

    @app.post("/classify")
    async def classify(request: Request) -> dict[str, str | int]:
        nonlocal in_flight
        if not hmac.compare_digest(request.headers.get("authorization", "").encode(), ("Bearer " + bearer).encode()):
            raise HTTPException(401, "unauthorized")
        deadline: float = request_deadline(request.headers.get(BUDGET_HEADER))
        body: bytearray = bytearray()
        async for chunk in request.stream():
            body.extend(chunk)
            if len(body) > MAX_INPUT_BYTES * 6 + 1024:
                raise HTTPException(413, "input too large")
        try:
            classification: ClassificationRequest = ClassificationRequest.model_validate(json.loads(body))
        except (ValueError, ValidationError):
            raise HTTPException(400, "invalid classification request") from None
        if classification.release_sha256 != release_sha256 or classification.projection_version != PROJECTION:
            raise HTTPException(409, "release mismatch")
        if not classification.user_text or len(classification.user_text.encode()) > MAX_INPUT_BYTES:
            raise HTTPException(413, "input too large")
        # Admission precedes tokenization so rejected requests cost no CPU.
        if in_flight >= MAX_IN_FLIGHT:
            raise HTTPException(503, "classifier busy")
        in_flight += 1
        try:
            # The deadline and disconnect watcher cover tokenization too, so stale work frees its slot.
            classification_task: asyncio.Task[tuple[list[int], str]] = asyncio.create_task(tokenize_and_classify(predictor, classification.user_text))
            disconnect_watcher: asyncio.Task[None] = asyncio.create_task(cancel_on_disconnect(request, classification_task))
            try:
                tokens, output = await asyncio.wait_for(classification_task, timeout=deadline - time.monotonic())
            except TokenLimitExceeded:
                raise HTTPException(413, "input token limit exceeded") from None
            except TimeoutError:
                raise HTTPException(503, "classification deadline exceeded") from None
            except asyncio.CancelledError:
                if not disconnect_watcher.done():
                    raise
                raise HTTPException(503, "caller disconnected") from None
            except Exception:
                raise HTTPException(503, "classifier unavailable") from None
            finally:
                disconnect_watcher.cancel()
        finally:
            in_flight -= 1
        if not OUTPUT.fullmatch(output) or not 1 <= len(tokens) <= MAX_INPUT_TOKENS:
            raise HTTPException(503, "invalid classifier output")
        return {"schema_version": SCHEMA, "release_sha256": release_sha256, "output": output, "input_tokens": len(tokens)}

    return app
