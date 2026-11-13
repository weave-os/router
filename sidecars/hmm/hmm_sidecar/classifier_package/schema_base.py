"""Shared Pydantic primitives for Weave schema objects."""

from __future__ import annotations

from typing import Annotated

from pydantic import BaseModel, ConfigDict, Field

NonEmptyString = Annotated[str, Field(min_length=1)]
Sha256Hex = Annotated[str, Field(pattern=r"^[A-Fa-f0-9]{64}$")]


class FrozenOpenModel(BaseModel):
    """Frozen model that tolerates fields from newer data producers."""

    model_config = ConfigDict(
        extra="allow",
        frozen=True,
        protected_namespaces=(),
    )


class FrozenClosedModel(BaseModel):
    """Frozen model that rejects unknown fields so schema typos fail loudly."""

    model_config = ConfigDict(
        extra="forbid",
        frozen=True,
        protected_namespaces=(),
    )
