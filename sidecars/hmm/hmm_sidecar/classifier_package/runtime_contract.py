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

from hmm_sidecar.classifier_package.complexity_labels import COMPLEXITY_CLASSES
from hmm_sidecar.classifier_package.complexity_classifier import (
    no_tool_context_feature_layout,
)
from hmm_sidecar.classifier_package.serving_profile import (
    DocumentWindow,
    EmbeddingProvider,
    InteractionStateGeometry,
    ServingConfig,
    ServingProfile,
    ToolEvidenceMode,
    legacy_gemini_user_agent_turn_window_profile,
    serving_profile,
)
from hmm_sidecar.classifier_package.core import canonical_json, sha256_text
from hmm_sidecar.classifier_package.manifest import (
    CONFIGURED_INPUT_MODE as CONFIGURED_INPUT_MODE,
)

#: v2 adds the round-level HMM and lookback geometry of a per-message package.
#: v1 stays byte-identical so every published package keeps resolving.
INTERACTION_STATE_RUNTIME_CONTRACT_VERSION = "hmm_router_raw_v5_runtime_contract_v2"
LEGACY_TURN_WINDOW_RUNTIME_CONTRACT_VERSION = "hmm_router_raw_v5_runtime_contract_v1"
LEGACY_TURN_WINDOW_SERVING_INPUT_SHA256 = (
    "9767d22e1c1935b99faf93273454ab63c9f0fecfbf8bba0453dbb51ad99f7b49"
)
LEGACY_TURN_WINDOW_HMM_TRAINING_CONTRACT_SHA256 = (
    "4e154288afacd0b6c4709a99dffea1ef0f71a3c70afb9970715cf72fc7fe0d71"
)
LEGACY_TURN_WINDOW_HMM_TRAINING_CONTRACT_VERSION = (
    "hmm_router_raw_v5_gemini_embedding_2_hmm_training_contract_v1"
)
LEGACY_TURN_WINDOW_FEATURE_LAYOUT_SHA256 = (
    "0547e614ed62755ab8ce39ccd59057fbcc7c8987fdb7ec5c1e58ef7a1d8cec0e"
)
LEGACY_TURN_WINDOW_FEATURE_DIM = 3318
LEGACY_TURN_WINDOW_HMM_STATES = 80
LEGACY_TURN_WINDOW_PCA_DIM = 64


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


class LegacyGeminiTurnWindowRuntimeContract(BaseModel):
    """The one frozen raw-V5 v1 lineage still accepted for migration bootstrap."""

    model_config = ConfigDict(frozen=True, extra="forbid")

    contract_version: Literal[LEGACY_TURN_WINDOW_RUNTIME_CONTRACT_VERSION]
    input_source: InputSource
    serving_config: ServingConfig
    sequence_representation: str
    embedding_provider: EmbeddingProvider
    embedding_model: str
    embedding_dim: int
    document_serializer_version: str
    document_serializer_sha256: str
    state_encoder_contract_sha256: str
    serving_input_sha256: str
    hmm_training_contract_version: str
    hmm_training_contract_sha256: str
    hmm_states: int
    pca_dim: int
    classes: tuple[str, ...]
    classifier_feature_layout_sha256: str
    classifier_feature_dim: int
    no_canonical_fallback: Literal[True]

    @property
    def reference_probes(self) -> tuple[ReferenceProbe, ...]:
        return ()

    @property
    def interaction_state(self) -> None:
        return None

    def resolved_profile(self) -> ServingProfile:
        profile = legacy_gemini_user_agent_turn_window_profile()
        if (
            self.input_source is not InputSource.LIVE_CONVERSATION
            or self.serving_config.provider is not EmbeddingProvider.GEMINI
            or self.serving_config.tool_evidence is not ToolEvidenceMode.USER_AGENT_ONLY
            or self.serving_config.document_window is not DocumentWindow.TURN_WINDOW
            or self.serving_config.lookback_rounds != 1
        ):
            raise RuntimeContractError(
                "raw-V5 v1 support is limited to the frozen Gemini user-agent one-turn geometry"
            )
        expected = {
            "sequence_representation": profile.representation.value,
            "embedding_provider": EmbeddingProvider.GEMINI.value,
            "embedding_model": profile.embedding_model,
            "embedding_dim": profile.embedding_dim,
            "document_serializer_version": profile.document_serializer_version,
            "document_serializer_sha256": profile.document_serializer_sha256,
            "state_encoder_contract_sha256": profile.state_encoder_contract.sha256,
            "serving_input_sha256": LEGACY_TURN_WINDOW_SERVING_INPUT_SHA256,
            "hmm_training_contract_version": LEGACY_TURN_WINDOW_HMM_TRAINING_CONTRACT_VERSION,
            "hmm_training_contract_sha256": LEGACY_TURN_WINDOW_HMM_TRAINING_CONTRACT_SHA256,
            "hmm_states": LEGACY_TURN_WINDOW_HMM_STATES,
            "pca_dim": LEGACY_TURN_WINDOW_PCA_DIM,
            "classes": COMPLEXITY_CLASSES,
            "classifier_feature_layout_sha256": LEGACY_TURN_WINDOW_FEATURE_LAYOUT_SHA256,
            "classifier_feature_dim": LEGACY_TURN_WINDOW_FEATURE_DIM,
            "no_canonical_fallback": True,
        }
        declared = self.model_dump(mode="python")
        mismatched = sorted(
            key
            for key, expected_value in expected.items()
            if declared[key] != expected_value
        )
        if mismatched:
            raise RuntimeContractError(
                "raw-V5 v1 contract differs from the frozen one-turn artifact: "
                f"fields={mismatched}"
            )
        return profile


def parse_runtime_contract(
    value: object,
) -> RuntimeContract | LegacyGeminiTurnWindowRuntimeContract:
    """Parse and self-check a package's declared raw-V5 runtime contract."""

    if not isinstance(value, dict):
        raise RuntimeContractError(
            "raw V5 package does not declare a runtime contract object"
        )
    try:
        contract_model = (
            LegacyGeminiTurnWindowRuntimeContract
            if value.get("contract_version")
            == LEGACY_TURN_WINDOW_RUNTIME_CONTRACT_VERSION
            else RuntimeContract
        )
        contract = contract_model.model_validate(value)
    except ValidationError as error:
        raise RuntimeContractError(
            f"raw V5 runtime contract is not servable as declared: {error}"
        ) from error
    contract.resolved_profile()
    return contract
