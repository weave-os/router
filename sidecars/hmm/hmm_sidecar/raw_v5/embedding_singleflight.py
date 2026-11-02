"""Collapse concurrent provider embedding calls for the same document."""

from __future__ import annotations

import asyncio
import time
from collections.abc import Awaitable, Callable, Sequence
from dataclasses import dataclass
from typing import Any

from hmm_sidecar.raw_v5.af_embeddings import EmbeddingVector

FetchMissing = Callable[[list[str]], Awaitable[tuple[dict[str, EmbeddingVector], Any]]]


@dataclass(frozen=True)
class SingleFlightResult:
    """Embeddings keyed by document, and how this call obtained them.

    ``fetched`` counts documents this call's own provider request produced;
    ``joined`` counts documents whose provider request was already in flight
    and were awaited to completion instead.
    """

    embeddings: dict[str, EmbeddingVector]
    info: Any
    fetched: int
    joined: int


@dataclass(frozen=True)
class _InFlightFetch:
    future: asyncio.Future[EmbeddingVector]
    started_at: float


class EmbeddingSingleFlight:
    """One provider call per in-flight document, shared by every waiter.

    A document only enters the process-local embedding cache once its provider
    call has *returned*. Requests carrying the same document while that call is
    outstanding — a router retry, a shadow evaluation, a client resend — each
    used to pay for their own multi-second round trip, and each of those calls
    added load that made the provider slower still. Waiters now attach to the
    outstanding call instead.

    ``max_inflight_age_seconds`` bounds how long an outstanding call stays
    joinable. A provider call that has already outlived the serving deadline
    is, to every later request, a stalled one: joining it can only end in the
    same deadline miss, and documents shared across sessions (a client's
    system prompt or tool list) would turn one stalled call into a fleet-wide
    outage lasting as long as that call and its retries. Past the bound a new
    request fetches the document itself and becomes the in-flight owner; the
    old call keeps running and still populates the cache when it returns.
    ``None`` keeps every outstanding call joinable indefinitely.
    """

    def __init__(self, *, max_inflight_age_seconds: float | None = None) -> None:
        if max_inflight_age_seconds is not None and max_inflight_age_seconds <= 0:
            raise ValueError("max_inflight_age_seconds must be positive or None")
        self._max_inflight_age_seconds = max_inflight_age_seconds
        self._inflight: dict[str, _InFlightFetch] = {}

    def _joinable(
        self,
        pending: _InFlightFetch | None,
        loop: asyncio.AbstractEventLoop,
    ) -> bool:
        # A future created on another loop cannot be awaited here, so that
        # document is simply fetched again on this one.
        if pending is None or pending.future.get_loop() is not loop:
            return False
        if self._max_inflight_age_seconds is None:
            return True
        return time.monotonic() - pending.started_at < self._max_inflight_age_seconds

    async def resolve(
        self,
        documents: Sequence[str],
        fetch: FetchMissing,
    ) -> SingleFlightResult:
        """Embed ``documents``, fetching only those not already in flight."""
        loop = asyncio.get_running_loop()
        joined: dict[str, asyncio.Future[EmbeddingVector]] = {}
        owned: dict[str, asyncio.Future[EmbeddingVector]] = {}
        started_at = time.monotonic()
        for document in dict.fromkeys(documents):
            pending = self._inflight.get(document)
            if self._joinable(pending, loop):
                joined[document] = pending.future
                continue
            future: asyncio.Future[EmbeddingVector] = loop.create_future()
            owned[document] = future
            self._inflight[document] = _InFlightFetch(future, started_at)

        if not owned:
            return SingleFlightResult(
                embeddings={
                    document: await pending
                    for document, pending in joined.items()
                },
                info=None,
                fetched=0,
                joined=len(joined),
            )

        try:
            embeddings, info = await fetch(list(owned))
        except BaseException as error:
            self._settle(owned, error=error)
            raise
        self._settle(owned, embeddings=embeddings)

        for document, pending in joined.items():
            embeddings[document] = await pending
        return SingleFlightResult(
            embeddings=embeddings,
            info=info,
            fetched=len(owned),
            joined=len(joined),
        )

    def _settle(
        self,
        owned: dict[str, asyncio.Future[EmbeddingVector]],
        *,
        embeddings: dict[str, EmbeddingVector] | None = None,
        error: BaseException | None = None,
    ) -> None:
        for document, pending in owned.items():
            # A stale fetch may have been superseded by a newer owner for the
            # same document; only the current owner's entry is released.
            current = self._inflight.get(document)
            if current is not None and current.future is pending:
                del self._inflight[document]
            if pending.done():
                continue
            if isinstance(error, asyncio.CancelledError):
                pending.cancel()
                continue
            if embeddings is not None and document in embeddings:
                pending.set_result(embeddings[document])
                continue
            pending.set_exception(
                error
                if error is not None
                else KeyError("provider returned no embedding for document")
            )
            # Marks the exception retrieved: a fetch can fail with no waiter
            # attached, and an unretrieved one is reported as an asyncio error.
            pending.exception()
