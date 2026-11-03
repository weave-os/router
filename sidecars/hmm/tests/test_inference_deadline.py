"""The sidecar's own inference deadline, and what it is allowed to fall back to."""

from __future__ import annotations

import asyncio
import time
from collections.abc import Iterator

import pytest

from hmm_sidecar.inference_deadline import (
    LastReadoutStore,
    OVERRUN_COUNTS,
    infer_deadline_seconds,
    infer_within_deadline,
    overrun_stats,
)


@pytest.fixture(autouse=True)
def _reset_overrun_counts() -> Iterator[None]:
    """OVERRUN_COUNTS is a process-global dict `_keep_warming` mutates in
    place, so one test's overrun would otherwise still be sitting in it when
    the next test starts -- corrupting exactly the "returns to zero"
    assertions these tests exist to make. Reset before and after so a test
    that fails partway through does not poison every test that follows it.
    """
    OVERRUN_COUNTS["live"] = 0
    OVERRUN_COUNTS["total"] = 0
    yield
    OVERRUN_COUNTS["live"] = 0
    OVERRUN_COUNTS["total"] = 0


def _infer(readout: str, *, delay: float = 0.0):
    async def infer() -> tuple[str, str]:
        if delay:
            await asyncio.sleep(delay)
        return readout, f"timings:{readout}"

    return infer


def _failing_infer(*, delay: float, error: Exception):
    async def infer() -> tuple[str, str]:
        await asyncio.sleep(delay)
        raise error

    return infer


def test_inference_inside_the_deadline_is_returned_untouched() -> None:
    async def scenario() -> None:
        store = LastReadoutStore()
        bounded = await infer_within_deadline(
            infer=_infer("fresh"),
            session_id="s1",
            store=store,
            deadline_seconds=1.0,
        )

        assert bounded.readout == "fresh"
        assert bounded.timings == "timings:fresh"
        assert bounded.degraded is False

    asyncio.run(scenario())


def test_a_slow_inference_degrades_to_the_session_prior_state() -> None:
    """The 503 this exists to prevent: without a fallback the router gets no
    decision at all, so a one-turn-stale state beats no answer."""

    async def scenario() -> None:
        store = LastReadoutStore()
        await infer_within_deadline(
            infer=_infer("turn-1"),
            session_id="s1",
            store=store,
            deadline_seconds=1.0,
        )

        bounded = await infer_within_deadline(
            infer=_infer("turn-2", delay=5.0),
            session_id="s1",
            store=store,
            deadline_seconds=0.01,
        )

        assert bounded.readout == "turn-1"
        assert bounded.degraded is True
        # Timings describe the run that produced the state, and this state came
        # from an earlier turn; reporting the previous turn's numbers as this
        # turn's would misattribute the latency the caller is measuring.
        assert bounded.timings is None
        assert bounded.prior_age_seconds is not None

    asyncio.run(scenario())


def test_a_slow_inference_with_no_prior_state_fails_closed() -> None:
    """Nothing valid to route on, so the router must see the timeout and use
    its own fallback rather than the sidecar inventing a state."""

    async def scenario() -> None:
        with pytest.raises(asyncio.TimeoutError):
            await infer_within_deadline(
                infer=_infer("slow", delay=5.0),
                session_id="s1",
                store=LastReadoutStore(),
                deadline_seconds=0.01,
            )

    asyncio.run(scenario())


def test_a_prior_state_from_another_session_is_never_borrowed() -> None:
    """State is per conversation. Routing one session on another's state would
    be a wrong decision, not a degraded one."""

    async def scenario() -> None:
        store = LastReadoutStore()
        await infer_within_deadline(
            infer=_infer("session-a"),
            session_id="a",
            store=store,
            deadline_seconds=1.0,
        )

        with pytest.raises(asyncio.TimeoutError):
            await infer_within_deadline(
                infer=_infer("session-b", delay=5.0),
                session_id="b",
                store=store,
                deadline_seconds=0.01,
            )

    asyncio.run(scenario())


def test_an_unidentified_session_cannot_degrade() -> None:
    """With no session key there is no way to prove the prior state belongs to
    this conversation, so the request fails closed."""

    async def scenario() -> None:
        store = LastReadoutStore()
        await infer_within_deadline(
            infer=_infer("turn-1"),
            session_id="s1",
            store=store,
            deadline_seconds=1.0,
        )

        with pytest.raises(asyncio.TimeoutError):
            await infer_within_deadline(
                infer=_infer("turn-2", delay=5.0),
                session_id=None,
                store=store,
                deadline_seconds=0.01,
            )

    asyncio.run(scenario())


def test_a_prior_state_older_than_the_age_bound_is_not_used(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """A stale state stops describing the conversation. Past the bound it is a
    guess, and the sidecar fails closed instead of guessing."""
    monkeypatch.setenv("HMM_DEGRADED_PRIOR_MAX_AGE_SECONDS", "0.01")

    async def scenario() -> None:
        store = LastReadoutStore()
        await infer_within_deadline(
            infer=_infer("ancient"),
            session_id="s1",
            store=store,
            deadline_seconds=1.0,
        )
        await asyncio.sleep(0.02)

        with pytest.raises(asyncio.TimeoutError):
            await infer_within_deadline(
                infer=_infer("turn-2", delay=5.0),
                session_id="s1",
                store=store,
                deadline_seconds=0.01,
            )

    asyncio.run(scenario())


def test_the_overrunning_inference_still_completes_and_arms_the_next_turn() -> None:
    """It has already paid for the provider call. Cancelling it would throw
    that away and leave the following turn to miss the cache all over again."""

    async def scenario() -> None:
        store = LastReadoutStore()
        await infer_within_deadline(
            infer=_infer("turn-1"),
            session_id="s1",
            store=store,
            deadline_seconds=1.0,
        )

        degraded = await infer_within_deadline(
            infer=_infer("turn-2", delay=0.05),
            session_id="s1",
            store=store,
            deadline_seconds=0.01,
        )
        assert degraded.readout == "turn-1"

        await asyncio.sleep(0.1)
        assert store.get("s1", 60.0).readout == "turn-2"

    asyncio.run(scenario())


def test_overrun_counters_advance_and_live_returns_to_zero_on_completion() -> None:
    """overruns_live is read as backpressure right now; overruns_total is read
    as a rate over time. This drives one overrun through to completion and
    checks both: total must record that the overrun happened at all, and live
    must drop back to zero once the task actually finishes -- otherwise every
    overrun this deployment survives would look, from the gauge alone,
    indistinguishable from one still stuck open under Vertex-429 pressure.
    """

    async def scenario() -> None:
        store = LastReadoutStore()
        await infer_within_deadline(
            infer=_infer("turn-1"),
            session_id="s1",
            store=store,
            deadline_seconds=1.0,
        )

        degraded = await infer_within_deadline(
            infer=_infer("turn-2", delay=0.05),
            session_id="s1",
            store=store,
            deadline_seconds=0.01,
        )
        assert degraded.degraded is True
        assert overrun_stats() == {"live": 1, "total": 1}

        await asyncio.sleep(0.1)

        assert overrun_stats() == {"live": 0, "total": 1}

    asyncio.run(scenario())


def test_overrun_live_returns_to_zero_when_the_overrunning_inference_raises() -> None:
    """The exact case this gauge exists to surface: Vertex-429 pressure shows
    up as the embed call erroring out, not hanging forever. `_on_done`'s error
    branch logs and returns without touching `live` itself -- the decrement
    only happens because it sits in a `finally` around that branch. If that
    `finally` were ever narrowed to the success path only, this is the path
    that would silently leak the gauge upward forever under exactly the load
    spike it is meant to report.
    """

    async def scenario() -> None:
        store = LastReadoutStore()
        await infer_within_deadline(
            infer=_infer("turn-1"),
            session_id="s1",
            store=store,
            deadline_seconds=1.0,
        )

        overrun_error = RuntimeError("vertex quota exceeded")
        degraded = await infer_within_deadline(
            infer=_failing_infer(delay=0.05, error=overrun_error),
            session_id="s1",
            store=store,
            deadline_seconds=0.01,
        )
        assert degraded.degraded is True
        assert overrun_stats()["live"] == 1

        await asyncio.sleep(0.1)

        assert overrun_stats()["live"] == 0

    asyncio.run(scenario())


def test_an_inference_within_the_deadline_registers_no_overrun() -> None:
    """The gauge must stay silent on the ordinary path. If a routine,
    on-time inference nudged these counters even briefly, every normal
    request would add noise indistinguishable from a real overrun, and the
    signal this exists to give ops would be worthless from minute one."""

    async def scenario() -> None:
        store = LastReadoutStore()
        await infer_within_deadline(
            infer=_infer("turn-1"),
            session_id="s1",
            store=store,
            deadline_seconds=1.0,
        )

        assert overrun_stats() == {"live": 0, "total": 0}

    asyncio.run(scenario())


def test_no_deadline_configured_leaves_inference_unbounded() -> None:
    """Deployments must be able to opt out and keep the router's deadline as
    the only one."""

    async def scenario() -> None:
        bounded = await infer_within_deadline(
            infer=_infer("slow", delay=0.05),
            session_id="s1",
            store=LastReadoutStore(),
            deadline_seconds=None,
        )

        assert bounded.readout == "slow"
        assert bounded.degraded is False

    asyncio.run(scenario())


@pytest.mark.parametrize(
    ("raw", "expected"),
    [("2.5", 2.5), ("0", None), ("-1", None), ("not-a-number", 1.5)],
)
def test_the_deadline_env_var_is_read_and_a_bad_value_falls_back(
    monkeypatch: pytest.MonkeyPatch,
    raw: str,
    expected: float | None,
) -> None:
    monkeypatch.setenv("HMM_INFER_DEADLINE_SECONDS", raw)

    assert infer_deadline_seconds() == expected


def test_the_prior_store_evicts_the_least_recently_used_session() -> None:
    """It holds live conversations, so it is bounded; unbounded growth in a
    long-lived serving process is a leak."""
    store = LastReadoutStore(max_sessions=2)
    store.put("a", "readout-a")
    store.put("b", "readout-b")
    store.get("a", 60.0)
    store.put("c", "readout-c")

    assert store.get("b", 60.0) is None
    assert store.get("a", 60.0).readout == "readout-a"
    assert store.get("c", 60.0).readout == "readout-c"


def test_a_late_overrun_never_replaces_a_newer_turns_state() -> None:
    """The embed tail outlives the turn that launched it.

    Turn 2 times out and keeps running; turn 3 completes normally while it is
    still in flight. When turn 2 finally lands it describes an older state, so
    storing it would both roll the session backwards and — dated at completion
    — make that stale state look freshly observed to the age gate.
    """

    async def scenario() -> None:
        store = LastReadoutStore()
        await infer_within_deadline(
            infer=_infer("turn-1"),
            session_id="s1",
            store=store,
            deadline_seconds=1.0,
        )
        degraded = await infer_within_deadline(
            infer=_infer("turn-2", delay=0.2),
            session_id="s1",
            store=store,
            deadline_seconds=0.01,
        )
        assert degraded.readout == "turn-1"

        await infer_within_deadline(
            infer=_infer("turn-3"),
            session_id="s1",
            store=store,
            deadline_seconds=1.0,
        )
        await asyncio.sleep(0.3)

        assert store.get("s1", 60.0).readout == "turn-3"

    asyncio.run(scenario())


def test_an_out_of_order_put_is_dropped_rather_than_redating_the_session() -> None:
    """Ordering is by when the inference started, not when it returned."""
    store = LastReadoutStore()
    newer = time.monotonic()
    store.put("s1", "newer", newer)
    store.put("s1", "older", newer - 10.0)

    assert store.get("s1", 60.0).readout == "newer"


def test_caller_cancellation_before_the_deadline_is_not_an_overrun() -> None:
    async def scenario() -> None:
        store = LastReadoutStore()
        caller = asyncio.ensure_future(
            infer_within_deadline(
                infer=_infer("finished", delay=0.05),
                session_id="s1",
                store=store,
                deadline_seconds=10.0,
            )
        )
        await asyncio.sleep(0.01)
        caller.cancel()
        with pytest.raises(asyncio.CancelledError):
            await caller
        assert OVERRUN_COUNTS == {"live": 0, "total": 0}
        await asyncio.sleep(0.1)
        assert store.get("s1", 60).readout == "finished"
        assert OVERRUN_COUNTS == {"live": 0, "total": 0}

    asyncio.run(scenario())
