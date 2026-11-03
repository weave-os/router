"""Bound HMM inference so a slow embedding cannot burn the router's budget.

The router gives one routing decision a few seconds in total. Embedding is a
provider round trip whose tail runs far past that under load, and when it does
the router gets nothing at all — no decision, a 503, and a request that never
reaches a model. This module puts the sidecar's own, shorter deadline in front
of that call so a slow embed degrades to the session's last known HMM state
instead of to no answer.

Degrading is only safe because the fallback state is a real readout the sidecar
itself produced for the same session moments earlier: the decision is one turn
stale, not fabricated. Everything that would make it *not* that — no session to
key on, no prior readout, a readout too old to describe the conversation —
fails closed and lets the router see the timeout.
"""

from __future__ import annotations

import asyncio
import logging
import os
import time
from collections import OrderedDict
from collections.abc import Awaitable, Callable
from dataclasses import dataclass
from typing import Any

from hmm_sidecar.inference_work import retain_inference_task

log = logging.getLogger(__name__)

DEFAULT_INFER_DEADLINE_SECONDS = 1.5
INFER_DEADLINE_ENV = "HMM_INFER_DEADLINE_SECONDS"
DEFAULT_MAX_PRIOR_AGE_SECONDS = 300.0
MAX_PRIOR_AGE_ENV = "HMM_DEGRADED_PRIOR_MAX_AGE_SECONDS"
LAST_READOUT_MAX_SESSIONS = 2048

# Process-local serving counters for inference that outran the deadline and
# was left running instead of cancelled (see `_keep_warming` below). ``live``
# is how many such overruns are in flight *right now* — the backpressure
# signal a request-slot count cannot see, precisely because by the time a
# task reaches this dict it has already been handed back to the caller as a
# degraded result; the slot is free, but the task is still holding an
# embedding call (and its retries) open underneath. ``total`` is cumulative
# since process start, for rate and trend. Mutated in place, never rebound,
# so nothing here needs `global`.
OVERRUN_COUNTS: dict[str, int] = {"live": 0, "total": 0}


def overrun_stats() -> dict[str, int]:
    """A snapshot of `OVERRUN_COUNTS`, safe for the caller to hold onto."""
    return dict(OVERRUN_COUNTS)


def _env_float(name: str, default: float) -> float:
    raw = os.environ.get(name)
    if raw is None:
        return default
    try:
        return float(raw)
    except ValueError:
        log.warning("ignoring non-numeric %s=%r", name, raw)
        return default


def infer_deadline_seconds() -> float | None:
    """Seconds to wait for inference, or ``None`` for no sidecar deadline.

    A non-positive value disables the bound, leaving the router's deadline the
    only one — the behaviour every deployment had before this existed.
    """
    deadline = _env_float(INFER_DEADLINE_ENV, DEFAULT_INFER_DEADLINE_SECONDS)
    return deadline if deadline > 0 else None


def max_prior_age_seconds() -> float:
    return _env_float(MAX_PRIOR_AGE_ENV, DEFAULT_MAX_PRIOR_AGE_SECONDS)


@dataclass(frozen=True)
class BoundedInference:
    """One inference result, and whether it describes the current turn."""

    readout: Any
    timings: Any
    degraded: bool
    prior_age_seconds: float | None = None


@dataclass(frozen=True)
class _PriorReadout:
    readout: Any
    # When the inference that produced this readout *started*, not when it
    # returned: an overrunning call describes the turn it was launched for, and
    # dating it at completion would make a seconds-late readout look fresh.
    observed_at: float


class LastReadoutStore:
    """Most recent completed readout per session, bounded and in-process."""

    def __init__(self, max_sessions: int = LAST_READOUT_MAX_SESSIONS) -> None:
        self._max_sessions = max_sessions
        self._entries: OrderedDict[str, _PriorReadout] = OrderedDict()

    def put(
        self,
        session_id: str | None,
        readout: Any,
        observed_at: float | None = None,
    ) -> None:
        """Record ``readout`` unless a newer turn's readout is already stored.

        An overrunning inference finishes after the turns that raced past it,
        so without the ordering check it would overwrite a newer state with an
        older one.
        """
        if not session_id or readout is None:
            return
        if observed_at is None:
            observed_at = time.monotonic()
        existing = self._entries.get(session_id)
        if existing is not None and existing.observed_at >= observed_at:
            return
        self._entries.pop(session_id, None)
        self._entries[session_id] = _PriorReadout(
            readout=readout,
            observed_at=observed_at,
        )
        while len(self._entries) > self._max_sessions:
            self._entries.popitem(last=False)

    def get(
        self, session_id: str | None, max_age_seconds: float
    ) -> _PriorReadout | None:
        if not session_id:
            return None
        entry = self._entries.get(session_id)
        if entry is None:
            return None
        if time.monotonic() - entry.observed_at > max_age_seconds:
            self._entries.pop(session_id, None)
            return None
        self._entries.move_to_end(session_id)
        return entry


async def infer_within_deadline(
    *,
    infer: Callable[[], Awaitable[tuple[Any, Any]]],
    session_id: str | None,
    store: LastReadoutStore,
    deadline_seconds: float | None,
) -> BoundedInference:
    """Run ``infer``; fall back to the session's last readout if it is too slow.

    The overrun inference is deliberately **not** cancelled. It is already
    paying for the provider call, and letting it finish warms the embedding
    cache and this store, so the retry that follows a degraded decision — or
    the next turn of the same session — is the fast path instead of a second
    cold miss.
    """
    started_at = time.monotonic()
    task = asyncio.ensure_future(infer())
    retain_inference_task(task)
    try:
        readout, timings = await asyncio.wait_for(
            asyncio.shield(task), deadline_seconds
        )
    except asyncio.TimeoutError:
        _keep_warming(task, session_id, store, started_at, overran=True)
        prior = store.get(session_id, max_prior_age_seconds())
        if prior is None:
            raise
        return BoundedInference(
            readout=prior.readout,
            timings=None,
            degraded=True,
            prior_age_seconds=time.monotonic() - prior.observed_at,
        )
    except asyncio.CancelledError:
        # A disconnected caller can leave native computation running.
        # Keep its resources alive and preserve prior ordering,
        # but a cancellation before the deadline is not an overrun.
        _keep_warming(task, session_id, store, started_at, overran=False)
        raise
    store.put(session_id, readout, started_at)
    return BoundedInference(readout=readout, timings=timings, degraded=False)


def _keep_warming(
    task: "asyncio.Future[tuple[Any, Any]]",
    session_id: str | None,
    store: LastReadoutStore,
    started_at: float,
    *,
    overran: bool,
) -> None:
    # On the timeout path the task is known to have outrun the deadline — this
    # is the one place that fact is visible, since the caller already moved on
    # with a degraded result. Count it before handing it off to the event
    # loop.
    if overran:
        OVERRUN_COUNTS["live"] += 1
        OVERRUN_COUNTS["total"] += 1

    def _on_done(finished: "asyncio.Future[tuple[Any, Any]]") -> None:
        try:
            if finished.cancelled():
                return
            error = finished.exception()
            if error is not None:
                log.warning("overrunning hmm inference failed: %s", error)
                return
            readout, _timings = finished.result()
            store.put(session_id, readout, started_at)
        finally:
            # Must fire on every terminal path — cancelled, raised, or
            # succeeded — or the gauge leaks exactly under the Vertex-429
            # pressure it exists to measure.
            if overran:
                OVERRUN_COUNTS["live"] -= 1

    task.add_done_callback(_on_done)
