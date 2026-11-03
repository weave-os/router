"""Keep native inference off the HTTP loop and close resources after it finishes."""

from __future__ import annotations

import asyncio
import logging
from collections.abc import Callable
from typing import Any, TypeVar

logger = logging.getLogger(__name__)
InferenceResult = TypeVar("InferenceResult")

# Keep shielded work alive even after its HTTP caller has returned/cancelled.
_retained_tasks: set[asyncio.Future[Any]] = set()


def retain_inference_task(task: asyncio.Future[Any]) -> None:
    _retained_tasks.add(task)

    def finished(completed: asyncio.Future[Any]) -> None:
        _retained_tasks.discard(completed)
        # Cancellation can detach the caller before it retrieves an exception.
        if not completed.cancelled():
            error: BaseException | None = completed.exception()
            if error is not None:
                logger.warning("Retained inference failed", exc_info=error)

    task.add_done_callback(finished)


async def drain_retained_inference(
    timeout_seconds: float, on_drained: Callable[[], None]
) -> None:
    """Run ``on_drained`` once shielded inference that outlived its caller ends.

    Waits at most ``timeout_seconds``; work still running after that runs
    ``on_drained`` from its completion instead of blocking the caller.
    """
    pending: set[asyncio.Future[Any]] = set(_retained_tasks)
    if pending:
        _completed, pending = await asyncio.wait(pending, timeout=timeout_seconds)
    if not pending:
        on_drained()
        return
    logger.warning(
        "retained inference still running after drain budget count=%s", len(pending)
    )

    def finished(completed: asyncio.Future[Any]) -> None:
        pending.discard(completed)
        if not pending:
            on_drained()

    for task in list(pending):
        task.add_done_callback(finished)


async def run_blocking_inference(
    call: Callable[..., InferenceResult],
    *args: Any,
    **kwargs: Any,
) -> InferenceResult:
    task: asyncio.Task[InferenceResult] = asyncio.create_task(
        asyncio.to_thread(call, *args, **kwargs)
    )
    retain_inference_task(task)
    return await asyncio.shield(task)
