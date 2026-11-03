"""Error raised when a live request cannot be turned into the raw-V5 model input."""

from __future__ import annotations


class RawV5ServingInputError(ValueError):
    """Raised when a live request cannot satisfy the raw-V5 model input."""
