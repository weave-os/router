"""The raw-V5 runtime contract a sidecar package declares for serving.

A raw-V5 package is only servable if the process that loads it can rebuild the
exact model input the candidate was trained on. Before this contract, that was
implied by the package schema version, which pinned exactly one configuration
(Voyage, compacted tool trace, two-turn window). Every other combination
``wv router pipeline`` can train -- Gemini, tool-free projections, the
previous-element projection, one/two/ten-turn windows, any state count, any PCA
dimension -- had no way to be served.

The contract published here makes that configuration data instead: the
publisher declares it, the sidecar parses it through this one closed boundary
model, and both sides resolve the same
:class:`~ml_dev.hmm_router.raw_v5_serving_profile.RawV5ServingProfile` from it.
Anything unknown, incomplete, mutated, or internally inconsistent fails closed
here rather than serving a different geometry than the one that was trained.
"""

from __future__ import annotations

from enum import StrEnum
from typing import Literal

from pydantic import BaseModel, ConfigDict, ValidationError

from hmm_sidecar.raw_v5.complexity_classifier import (
    raw_v5_no_tool_context_feature_layout,
)
from hmm_sidecar.raw_v5.raw_v5_contract import RawV5HMMTrainingContractVersion
from hmm_sidecar.raw_v5.raw_v5_serving_profile import (
    RawV5DocumentWindow,
    RawV5EmbeddingProvider,
    RawV5InteractionStateGeometry,
    RawV5ServingConfig,
    RawV5ServingProfile,
    raw_v5_configured_hmm_training_contract_sha256,
    raw_v5_configured_serving_input_contract_sha256,
    raw_v5_interaction_state_serving_input_contract_sha256,
    raw_v5_serving_profile_for_runtime_contract,
)
from hmm_sidecar.raw_v5.core import canonical_json, sha256_text

RAW_V5_RUNTIME_CONTRACT_VERSION = "hmm_router_raw_v5_runtime_contract_v1"
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


def raw_v5_feature_layout_sha256(layout: object) -> str:
    """Hash a classifier feature layout as its own identity."""

    return sha256_text(canonical_json(layout))


class RawV5RuntimeContract(BaseModel):
    """The complete serving contract carried in a raw-V5 package manifest.

    Closed and frozen on purpose: a raw-V5 package that declares an unknown
    key, or omits one, is rejected instead of being served under a guessed
    contract.
    """

    model_config = ConfigDict(frozen=True, extra="forbid")

    contract_version: Literal[RAW_V5_RUNTIME_CONTRACT_VERSION]
    input_source: RawV5InputSource
    serving_config: RawV5ServingConfig
    sequence_representation: str
    embedding_provider: RawV5EmbeddingProvider
    embedding_model: str
    embedding_dim: int
    document_serializer_version: str
    document_serializer_sha256: str
    state_encoder_contract_sha256: str
    serving_input_sha256: str
    hmm_training_contract_version: RawV5HMMTrainingContractVersion
    hmm_training_contract_sha256: str
    hmm_states: int
    pca_dim: int
    classes: tuple[str, ...]
    classifier_feature_layout_sha256: str
    classifier_feature_dim: int
    # A raw-V5 package must never be served through the canonical document
    # lane, and must never silently substitute another representation.
    no_canonical_fallback: Literal[True]

    def resolved_profile(self) -> RawV5ServingProfile:
        """Resolve the declared configuration and prove the rest agrees.

        The registry resolves configuration plus the declared HMM lineage to
        exactly one supported serving identity. Every remaining declared field
        is then checked against that identity, so a package cannot claim a
        supported configuration while carrying another provider's model,
        another window's serializer, another row width, or a mutated contract
        hash.
        """

        try:
            profile = raw_v5_serving_profile_for_runtime_contract(
                self.serving_config,
                hmm_training_contract_version=self.hmm_training_contract_version,
                state_encoder_contract_sha256=self.state_encoder_contract_sha256,
            )
        except ValueError as error:
            raise RawV5RuntimeContractError(str(error)) from error
        self._require_chain_document_window(profile)
        declared = {
            "sequence_representation": self.sequence_representation,
            "embedding_provider": self.embedding_provider.value,
            "embedding_model": self.embedding_model,
            "embedding_dim": self.embedding_dim,
            "document_serializer_version": self.document_serializer_version,
            "document_serializer_sha256": self.document_serializer_sha256,
            "state_encoder_contract_sha256": self.state_encoder_contract_sha256,
            "serving_input_sha256": self.serving_input_sha256,
            "hmm_training_contract_version": self.hmm_training_contract_version,
            "hmm_training_contract_sha256": self.hmm_training_contract_sha256,
        }
        expected = {
            **_profile_identity(profile),
            "serving_input_sha256": self._serving_input_sha256(profile),
        }
        mismatched = sorted(
            key for key, value in declared.items() if expected[key] != value
        )
        if mismatched:
            raise RawV5RuntimeContractError(
                "raw V5 runtime contract disagrees with the serving profile it "
                f"declares: fields={mismatched} "
                f"representation={profile.representation.value}"
            )
        if self.hmm_states <= 0 or self.pca_dim <= 0:
            raise RawV5RuntimeContractError(
                "raw V5 runtime contract must pin positive HMM state and PCA "
                f"dimensions: states={self.hmm_states} pca_dim={self.pca_dim}"
            )
        if self.classifier_feature_dim <= 0:
            raise RawV5RuntimeContractError(
                "raw V5 runtime contract must pin a positive classifier "
                f"feature width: {self.classifier_feature_dim}"
            )
        if len(set(self.classes)) != len(self.classes) or not self.classes:
            raise RawV5RuntimeContractError(
                "raw V5 runtime contract must pin a unique class taxonomy: "
                f"{list(self.classes)}"
            )
        if (
            self.classifier_feature_layout_sha256,
            self.classifier_feature_dim,
        ) not in self._feature_layout_identities(profile):
            raise RawV5RuntimeContractError(
                "raw V5 runtime contract's classifier feature layout does not "
                "match the declared configuration's geometry: "
                f"states={self.hmm_states} embedding_dim={profile.embedding_dim} "
                f"feature_dim={self.classifier_feature_dim}"
            )
        return profile

    def serving_input_contract(self) -> dict[str, object]:
        raise NotImplementedError("not on the classifier serving path")

    def _require_chain_document_window(self, profile: RawV5ServingProfile) -> None:
        raise NotImplementedError("not on the classifier serving path")

    def _serving_input_sha256(self, profile: RawV5ServingProfile) -> str:
        raise NotImplementedError("not on the classifier serving path")

    def _feature_layout_identities(
        self, profile: RawV5ServingProfile
    ) -> set[tuple[str, int]]:
        raise NotImplementedError("not on the classifier serving path")


class RawV5InteractionStateRuntimeContract(RawV5RuntimeContract):
    """v2: a per-message package served by the round-level HMM and its lookback.

    The geometry is part of the serving-input contract, so its hash identifies
    the chain, the per-role PCA widths, and the lookback the classifier reads.
    """

    contract_version: Literal[RAW_V5_INTERACTION_STATE_RUNTIME_CONTRACT_VERSION]
    interaction_state: RawV5InteractionStateGeometry

    def serving_input_contract(self) -> dict[str, object]:
        raise NotImplementedError("not on the classifier serving path")

    def _require_chain_document_window(self, profile: RawV5ServingProfile) -> None:
        if profile.config.document_window is not RawV5DocumentWindow.PER_MESSAGE:
            raise RawV5RuntimeContractError(
                "the interaction-state runtime contract serves only per-message "
                f"documents, not {profile.representation.value}"
            )

    def _serving_input_sha256(self, profile: RawV5ServingProfile) -> str:
        return raw_v5_interaction_state_serving_input_contract_sha256(
            profile, self.interaction_state
        )

    def _feature_layout_identities(
        self, profile: RawV5ServingProfile
    ) -> set[tuple[str, int]]:
        # The round-level filter always emits belief dynamics, so the layout
        # is exactly one shape: belief dynamics, then the declared lookback.
        layout = raw_v5_no_tool_context_feature_layout(
            self.hmm_states,
            embedding_dim=profile.embedding_dim,
            belief_dynamics=True,
            element_lookback=self.interaction_state.element_lookback(
                embedding_dim=profile.embedding_dim
            ),
        )
        return {
            (
                raw_v5_feature_layout_sha256(layout),
                sum(int(block["dim"]) for block in layout),
            )
        }


def _configured_feature_layout_identities(
    *,
    embedding_dim: int,
    hmm_states: int,
) -> set[tuple[str, int]]:
    raise NotImplementedError("not on the classifier serving path")


def _profile_identity(profile: RawV5ServingProfile) -> dict[str, object]:
    return {
        "sequence_representation": profile.representation.value,
        "embedding_provider": profile.config.provider.value,
        "embedding_model": profile.embedding_model,
        "embedding_dim": profile.embedding_dim,
        "document_serializer_version": profile.document_serializer_version,
        "document_serializer_sha256": profile.document_serializer_sha256,
        "state_encoder_contract_sha256": profile.state_encoder_contract.sha256,
        "serving_input_sha256": (
            raw_v5_configured_serving_input_contract_sha256(profile)
        ),
        "hmm_training_contract_version": (profile.hmm_training_contract_version.value),
        "hmm_training_contract_sha256": (
            raw_v5_configured_hmm_training_contract_sha256(profile)
        ),
    }


def build_raw_v5_runtime_contract(
    profile: RawV5ServingProfile,
    *,
    input_source: RawV5InputSource,
    hmm_states: int,
    pca_dim: int,
    classes: tuple[str, ...],
    classifier_feature_layout: object,
    classifier_feature_dim: int,
    interaction_state: RawV5InteractionStateGeometry | None = None,
) -> dict[str, object]:
    raise NotImplementedError("not on the classifier serving path")


def parse_raw_v5_runtime_contract(value: object) -> RawV5RuntimeContract:
    """Parse and self-check a package's declared raw-V5 runtime contract."""

    if not isinstance(value, dict):
        raise RawV5RuntimeContractError(
            "raw V5 package does not declare a runtime contract object"
        )
    contract_model = (
        RawV5InteractionStateRuntimeContract
        if value.get("contract_version")
        == RAW_V5_INTERACTION_STATE_RUNTIME_CONTRACT_VERSION
        else RawV5RuntimeContract
    )
    try:
        contract = contract_model.model_validate(value)
    except ValidationError as error:
        raise RawV5RuntimeContractError(
            f"raw V5 runtime contract is not servable as declared: {error}"
        ) from error
    contract.resolved_profile()
    return contract
