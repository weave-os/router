from __future__ import annotations

import math
from pathlib import Path
from typing import Any

from pydantic import BaseModel, ConfigDict, Field, model_validator

from . import PACKAGE_SCHEMA_VERSION


class FrozenModel(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)


class EmbeddingContract(FrozenModel):
    model: str = Field(min_length=1)
    dimensions: int = Field(gt=0)
    task_type: str | None = None
    probe_text: str = Field(min_length=1)
    probe_vector_file: str = Field(min_length=1)
    minimum_cosine_similarity: float = Field(gt=0.0, le=1.0)


class ComponentFiles(FrozenModel):
    path: str = Field(min_length=1)


class ClassifierFiles(ComponentFiles):
    classes: tuple[str, ...] = Field(min_length=2)


class HMMFiles(ComponentFiles):
    states: int = Field(gt=1)


class FrozenPackageManifest(FrozenModel):
    schema_version: str
    model_id: str = Field(min_length=1)
    package_sha256: str | None = None
    embedding_contract: EmbeddingContract
    classifier: ClassifierFiles
    hmm: HMMFiles
    roster: ComponentFiles
    state_cards: ComponentFiles
    files: dict[str, str]

    @model_validator(mode="after")
    def validate_contract(self) -> "FrozenPackageManifest":
        if self.schema_version != PACKAGE_SCHEMA_VERSION:
            raise ValueError(
                f"unsupported package schema {self.schema_version!r}; "
                f"expected {PACKAGE_SCHEMA_VERSION!r}"
            )
        for relative, digest in self.files.items():
            path = Path(relative)
            if path.is_absolute() or ".." in path.parts:
                raise ValueError(f"unsafe manifest file path: {relative!r}")
            if len(digest) != 64 or any(
                char not in "0123456789abcdef" for char in digest
            ):
                raise ValueError(f"invalid sha256 for {relative!r}")
        return self


# Selection authority belongs to the Go router; a v4 classifier rejects any
# request that tries to hand it candidates, roster data or selection knobs.
SELECTION_AUTHORITY_FIELDS = frozenset(
    {
        "candidate_models",
        "candidate_providers",
        "candidates",
        "eligible_arms",
        "eligible_roster_ids",
        "fallback_model",
        "group_clamps",
        "model",
        "preferred_models",
        "provider",
        "quality_bias",
        "ranked_fallback",
        "routing_intent",
        "routing_knobs",
        "router_mode",
        "selected_arm_id",
        "selected_provider",
        "selected_roster_id",
    }
)


class ClassificationResult(FrozenModel):
    """``policy_router_v4`` /route and /preview body: classification facts only."""

    schema_version: str
    route_id: str
    classifier_artifact_id: str = Field(min_length=1)
    classifier_artifact_sha256: str = Field(min_length=64, max_length=64)
    predicted_label: str = Field(min_length=1)
    class_order: tuple[str, ...] = Field(min_length=1)
    class_probabilities: dict[str, float]
    classifier_confidence: float
    classifier_margin: float
    hmm_state_id: int = Field(ge=0)
    hmm_state_path: tuple[int, ...] = Field(min_length=1)
    hmm_state_probabilities: tuple[float, ...] = Field(min_length=1)
    debug: dict[str, Any] | None = None

    @model_validator(mode="after")
    def validate_probabilities(self) -> "ClassificationResult":
        if self.hmm_state_id >= len(self.hmm_state_probabilities):
            raise ValueError("hmm_state_id is outside hmm_state_probabilities")
        if any(
            not math.isfinite(value) or value < 0.0 or value > 1.0
            for value in self.hmm_state_probabilities
        ):
            raise ValueError("HMM state probabilities must be in [0,1]")
        if abs(sum(self.hmm_state_probabilities) - 1.0) > 1e-6:
            raise ValueError("HMM state probabilities must sum to one")
        if any(
            value < 0 or value >= len(self.hmm_state_probabilities)
            for value in self.hmm_state_path
        ):
            raise ValueError("HMM state path is outside the posterior vector")
        if set(self.class_probabilities) != set(self.class_order):
            raise ValueError("classifier probabilities must match class_order")
        if self.predicted_label not in self.class_probabilities:
            raise ValueError("predicted_label must be one of class_order")
        if any(
            not math.isfinite(value) or value < 0.0 or value > 1.0
            for value in self.class_probabilities.values()
        ):
            raise ValueError("classifier probabilities must be in [0,1]")
        if abs(sum(self.class_probabilities.values()) - 1.0) > 1e-6:
            raise ValueError("classifier probabilities must sum to one")
        return self
