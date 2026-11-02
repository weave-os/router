"""Persistent event-loop runner for sync wrappers in long-lived services."""

from __future__ import annotations

import threading
from collections.abc import Coroutine
from concurrent.futures import Future
from typing import Any, TypeVar

_T = TypeVar("_T")


class PersistentAsyncRunner:
    """Run coroutines on one process-lifetime loop from synchronous code."""

    def __init__(self, *, name: str) -> None:
        raise NotImplementedError("not on the classifier serving path")

    def _run(self) -> None:
        raise NotImplementedError("not on the classifier serving path")

    def run(self, coroutine: Coroutine[Any, Any, _T]) -> _T:
        raise NotImplementedError("not on the classifier serving path")

    def submit(self, coroutine: Coroutine[Any, Any, _T]) -> Future[_T]:
        raise NotImplementedError("not on the classifier serving path")

    def close(self) -> None:
        raise NotImplementedError("not on the classifier serving path")


_runner: PersistentAsyncRunner | None = None
_runner_guard = threading.Lock()


def _get_runner() -> PersistentAsyncRunner:
    raise NotImplementedError("not on the classifier serving path")


def run_on_persistent_loop(coroutine: Coroutine[Any, Any, _T]) -> _T:
    raise NotImplementedError("not on the classifier serving path")


def submit_on_persistent_loop(
    coroutine: Coroutine[Any, Any, _T],
) -> Future[_T]:
    raise NotImplementedError("not on the classifier serving path")
