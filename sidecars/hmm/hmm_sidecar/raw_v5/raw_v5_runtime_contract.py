"""The raw-V5 runtime contract a sidecar package declares for serving.

A raw-V5 package is only servable if the process that loads it can rebuild the
exact model input the candidate was trained on. This contract makes that
configuration data: the
publisher declares it, the sidecar parses it through this one closed boundary
model, and both sides resolve the same
:class:`~ml_dev.hmm_router.raw_v5_serving_profile.RawV5ServingProfile` from it.
Anything unknown, incomplete, mutated, or internally inconsistent fails closed
here rather than serving a different geometry than the one that was trained.
"""

from __future__ import annotations

from enum import StrEnum
from typing import Literal

from pydantic import BaseModel, ConfigDict, Field, ValidationError

from hmm_sidecar.raw_v5.complexity_classifier import (
    raw_v5_no_tool_context_feature_layout,
)
from hmm_sidecar.raw_v5.raw_v5_serving_profile import (
    RawV5InteractionStateGeometry,
    RawV5ServingConfig,
    RawV5ServingProfile,
    raw_v5_serving_profile,
)
from hmm_sidecar.raw_v5.core import canonical_json, sha256_text

#: v2 adds the round-level HMM and lookback geometry of a per-message package.
#: v1 stays byte-identical so every published package keeps resolving.
RAW_V5_INTERACTION_STATE_RUNTIME_CONTRACT_VERSION = (
    "hmm_router_raw_v5_runtime_contract_v2"
)

#: The ``runtime_contract.input_mode`` value a configuration-driven raw-V5
#: package declares. The two fixed legacy modes stay exactly as published so
#: the deployed packages remain explicit rollback targets.
RAW_V5_CONFIGURED_INPUT_MODE = "raw_v5_configured_serving_v1"


class RawV5RuntimeContractError(ValueError):
    """A package's declared runtime contract cannot be served as declared."""


class RawV5InputSource(StrEnum):
    """Where the routed request's raw conversation elements come from.

    This is orthogonal to the trained document geometry: the same profile can
    be served from persisted turn evidence supplied by the caller or rebuilt
    from the live conversation snapshot the router already sends.
    """

    DIRECT_EVIDENCE = "raw_v5_direct_turn_evidence"
    LIVE_CONVERSATION = "raw_v5_live_conversation_messages"


class RawV5RuntimeContract(BaseModel):
    """The runtime contract a raw-V5 package manifest declares.

    Serving supports one configuration, so the checks here are the ones that
    tie this code to the trained model: the document serializer that builds
    embedding inputs and the classifier feature layout. The package sha256 pin
    and the parity test cover everything else.
    """

    model_config = ConfigDict(frozen=True, extra="ignore")

    contract_version: Literal[RAW_V5_INTERACTION_STATE_RUNTIME_CONTRACT_VERSION]
    input_source: RawV5InputSource
    serving_config: RawV5ServingConfig
    interaction_state: RawV5InteractionStateGeometry
    sequence_representation: str
    embedding_model: str
    embedding_dim: int
    document_serializer_version: str
    document_serializer_sha256: str
    hmm_states: int = Field(gt=0)
    classes: tuple[str, ...] = Field(min_length=1)
    classifier_feature_layout_sha256: str
    classifier_feature_dim: int

    def resolved_profile(self) -> RawV5ServingProfile:
        """Byte identity is the package sha256 pin plus the parity test; this only
        proves the code builds the serializer and feature layout the classifier
        was trained on."""

        try:
            profile = raw_v5_serving_profile(self.serving_config)
        except ValueError as error:
            raise RawV5RuntimeContractError(str(error)) from error
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
            raise RawV5RuntimeContractError(
                "raw V5 runtime contract disagrees with the serving profile: "
                f"fields={mismatched}"
            )
        if len(set(self.classes)) != len(self.classes):
            raise RawV5RuntimeContractError(
                f"raw V5 runtime contract repeats a class: {list(self.classes)}"
            )
        # The round-level filter always emits belief dynamics, then the
        # declared lookback.
        layout = raw_v5_no_tool_context_feature_layout(
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
            raise RawV5RuntimeContractError(
                "raw V5 runtime contract's classifier feature layout does not "
                "match its geometry: "
                f"states={self.hmm_states} feature_dim={self.classifier_feature_dim}"
            )
        return profile


def parse_raw_v5_runtime_contract(value: object) -> RawV5RuntimeContract:
    """Parse and self-check a package's declared raw-V5 runtime contract."""

    if not isinstance(value, dict):
        raise RawV5RuntimeContractError(
            "raw V5 package does not declare a runtime contract object"
        )
    try:
        contract = RawV5RuntimeContract.model_validate(value)
    except ValidationError as error:
        raise RawV5RuntimeContractError(
            f"raw V5 runtime contract is not servable as declared: {error}"
        ) from error
    contract.resolved_profile()
    return contract
