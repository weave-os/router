from __future__ import annotations

from dataclasses import dataclass
from typing import Any


@dataclass(frozen=True)
class CachedLatentModel:
    """Cached PCA projector and fitted latent sequence model."""

    cache_key: str
    baseline: str
    projector: Any
    model: Any
    model_info: dict[str, Any]
