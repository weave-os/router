"""The raw-V5 runtime contract a sidecar package declares for serving.

A raw-V5 package is only servable if the process that loads it can rebuild the
exact model input the candidate was trained on. This contract makes that
configuration data: the
publisher declares it, the sidecar parses it through this one closed boundary
model, and both sides resolve the same
:class:`~hmm_sidecar.classifier_package.serving_profile.ServingProfile` from it.
Anything unknown, incomplete, mutated, or internally inconsistent fails closed
here rather than serving a different geometry than the one that was trained.
"""

from __future__ import annotations

import math
from enum import StrEnum
from typing import Literal

from pydantic import BaseModel, ConfigDict, Field, ValidationError, field_validator

from hmm_sidecar.classifier_package.complexity_classifier import (
    no_tool_context_feature_layout,
)
from hmm_sidecar.classifier_package.serving_profile import (
    EmbeddingProvider,
    InteractionStateGeometry,
    ServingConfig,
    ServingProfile,
    serving_profile,
)
from hmm_sidecar.classifier_package.core import canonical_json, sha256_text
from hmm_sidecar.classifier_package.manifest import CONFIGURED_INPUT_MODE

#: v2 adds the round-level HMM and lookback geometry of a per-message package.
#: v1 stays byte-identical so every published package keeps resolving.
INTERACTION_STATE_RUNTIME_CONTRACT_VERSION = "hmm_router_raw_v5_runtime_contract_v2"


class RuntimeContractError(ValueError):
    """A package's declared runtime contract cannot be served as declared."""


class InputSource(StrEnum):
    """Where the routed request's raw conversation elements come from.

    This is orthogonal to the trained document geometry: the same profile can
    be served from persisted turn evidence supplied by the caller or rebuilt
    from the live conversation snapshot the router already sends.
    """

    DIRECT_EVIDENCE = "raw_v5_direct_turn_evidence"
    LIVE_CONVERSATION = "raw_v5_live_conversation_messages"


class ReferenceProbe(BaseModel):
    """A probe text and the vector the training-time embedding model returned."""

    model_config = ConfigDict(frozen=True, extra="forbid")

    text: str = Field(min_length=1)
    vector: tuple[float, ...] = Field(min_length=1)

    @field_validator("vector")
    @classmethod
    def _finite_and_nonzero(cls, vector: tuple[float, ...]) -> tuple[float, ...]:
        if not all(math.isfinite(value) for value in vector) or not any(vector):
            raise ValueError("a reference probe vector must be finite and non-zero")
        return vector


class RuntimeContract(BaseModel):
    """The runtime contract a raw-V5 package manifest declares.

    Serving supports one configuration, so the checks here are the ones that
    tie this code to the trained model: the document serializer that builds
    embedding inputs and the classifier feature layout. The package sha256 pin
    and the parity test cover everything else.
    """

    model_config = ConfigDict(frozen=True, extra="ignore")

    contract_version: Literal[INTERACTION_STATE_RUNTIME_CONTRACT_VERSION]
    input_source: InputSource
    serving_config: ServingConfig
    interaction_state: InteractionStateGeometry
    sequence_representation: str
    embedding_model: str
    embedding_dim: int
    document_serializer_version: str
    document_serializer_sha256: str
    hmm_states: int = Field(gt=0)
    classes: tuple[str, ...] = Field(min_length=1)
    classifier_feature_layout_sha256: str
    classifier_feature_dim: int
    #: Required for customer-model packages: startup re-embeds each text and
    #: refuses to serve unless the live vectors match these.
    reference_probes: tuple[ReferenceProbe, ...] = ()

    def resolved_profile(self) -> ServingProfile:
        """Byte identity is the package sha256 pin plus the parity test; this only
        proves the code builds the serializer and feature layout the classifier
        was trained on."""

        try:
            profile = serving_profile(
                self.serving_config,
                embedding_model=self.embedding_model,
                embedding_dim=self.embedding_dim,
            )
        except ValueError as error:
            raise RuntimeContractError(str(error)) from error
        if (
            self.serving_config.provider is EmbeddingProvider.OPENAI_COMPATIBLE
            and not self.reference_probes
        ):
            raise RuntimeContractError(
                "an openai_compatible package must declare reference_probes"
            )
        if any(
            len(probe.vector) != self.embedding_dim for probe in self.reference_probes
        ):
            raise RuntimeContractError(
                f"a reference probe vector is not {self.embedding_dim} wide"
            )
        declared = {
            "sequence_representation": self.sequence_representation,
            "embedding_model": self.embedding_model,
            "embedding_dim": self.embedding_dim,
            "document_serializer_version": self.document_serializer_version,
            "document_serializer_sha256": self.document_serializer_sha256,
        }
        expected = {
            "sequence_representation": profile.representation.value,
            "embedding_model": profile.embedding_model,
            "embedding_dim": profile.embedding_dim,
            "document_serializer_version": profile.document_serializer_version,
            "document_serializer_sha256": profile.document_serializer_sha256,
        }
        mismatched = sorted(
            key for key, value in declared.items() if expected[key] != value
        )
        if mismatched:
            raise RuntimeContractError(
                "raw V5 runtime contract disagrees with the serving profile: "
                f"fields={mismatched}"
            )
        if len(set(self.classes)) != len(self.classes):
            raise RuntimeContractError(
                f"raw V5 runtime contract repeats a class: {list(self.classes)}"
            )
        # The round-level filter always emits belief dynamics, then the
        # declared lookback.
        layout = no_tool_context_feature_layout(
            self.hmm_states,
            embedding_dim=profile.embedding_dim,
            belief_dynamics=True,
            element_lookback=self.interaction_state.element_lookback(
                embedding_dim=profile.embedding_dim
            ),
        )
        expected_layout = (
            sha256_text(canonical_json(layout)),
            sum(int(block["dim"]) for block in layout),
        )
        if expected_layout != (
            self.classifier_feature_layout_sha256,
            self.classifier_feature_dim,
        ):
            raise RuntimeContractError(
                "raw V5 runtime contract's classifier feature layout does not "
                "match its geometry: "
                f"states={self.hmm_states} feature_dim={self.classifier_feature_dim}"
            )
        return profile


def parse_runtime_contract(value: object) -> RuntimeContract:
    """Parse and self-check a package's declared raw-V5 runtime contract."""

    if not isinstance(value, dict):
        raise RuntimeContractError(
            "raw V5 package does not declare a runtime contract object"
        )
    try:
        contract = RuntimeContract.model_validate(value)
    except ValidationError as error:
        raise RuntimeContractError(
            f"raw V5 runtime contract is not servable as declared: {error}"
        ) from error
    contract.resolved_profile()
    return contract
