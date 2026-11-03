"""Error raised when a live request cannot be turned into the raw-V5 model input."""

from __future__ import annotations


class ServingInputError(ValueError):
    pass
