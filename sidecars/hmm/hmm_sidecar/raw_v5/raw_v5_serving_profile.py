"""Configuration-driven serving profiles for raw-V5 HMM router packages.

``wv router pipeline`` can train a raw-V5 HMM candidate for any supported
combination of embedding provider, tool-evidence projection, document window,
and lookback rounds. Serving previously hardcoded one of those combinations,
so a valid candidate could be unservable for reasons unrelated to its quality.

This module is the single place that maps a declared training configuration to
the exact serving identity it implies: the sequence representation, provider
geometry, document serializer, and model-input contract already pinned in
:mod:`ml_dev.hmm_router.raw_v5_contract`. It adds no new document semantics and
never widens an existing contract — an unsupported combination raises instead
of resolving to a neighbouring profile, so a package either declares a
combination this registry can serve or it fails closed.

The registry is intentionally explicit rather than derived: every servable
combination is one table row whose serializer version and contract hash are
the values the corpus, trainer, and embedding caches already use.
"""

from __future__ import annotations

from collections.abc import Callable, Mapping
from dataclasses import dataclass
from enum import StrEnum
from typing import Literal, Self

from pydantic import BaseModel, ConfigDict, model_validator

from hmm_sidecar.raw_v5.hmm_feature_arms import (
    ElementLookback,
    ElementLookbackSource,
    HMMChain,
    hmm_chain_of,
)
from hmm_sidecar.raw_v5.serving_features import (
    LEGACY_RELATIVE_POSITION_DIM,
    RELATIVE_POSITION_DIM,
    raw_relative_position_dim,
)
from hmm_sidecar.raw_v5.raw_v5_contract import (
    RAW_V5_GEMINI_ELEMENT_INPUT_CONTRACT_SHA256,
    RAW_V5_GEMINI_ELEMENT_SERIALIZER_VERSION,
    RAW_V5_GEMINI_EMBEDDING_2_DIM,
    RAW_V5_GEMINI_EMBEDDING_2_MODEL,
    RAW_V5_VOYAGE_ONE_TURN_MAX_TURNS,
    RawFeatureMatrix,
    RawV5HMMTrainingContractVersion,
    raw_v5_gemini_element_model_input_contract,
)
from hmm_sidecar.raw_v5.raw_v5_serving import (
    RawV5DocumentKind,
    RawV5ServingInputError,
)
from hmm_sidecar.raw_v5.state_representation import (
    GEMINI_EMBEDDING_2_RAW_ROUTER_V5_ELEMENT,
    SequenceRepresentation,
    StateEncoderContract,
    get_state_encoder_contract,
)


class RawV5EmbeddingProvider(StrEnum):
    """The embedding provider a raw-V5 candidate was trained against."""

    VOYAGE = "voyage"
    GEMINI = "gemini"


class RawV5ToolEvidenceMode(StrEnum):
    """Whether agent documents carry the compacted structural tool trace."""

    COMPACTED_TOOL_TRACE = "compacted_tool_trace"
    USER_AGENT_ONLY = "user_agent_only"


class RawV5AgentDocumentKind(StrEnum):
    """What an embedded agent document is allowed to contain.

    The compacted-trace value is the identity the deployed v7 Voyage contract
    already pins, so a package trained under that lane keeps its exact bytes.
    """

    WITH_COMPACTED_TOOL_TRACE = RawV5DocumentKind.AGENT_WITH_LIVE_TOOL_TRACE.value
    WITHOUT_TOOL_TRACE = "raw_agent_text_without_tool_trace"


class RawV5DocumentWindow(StrEnum):
    """How much conversation history each embedded document carries."""

    #: ``lookback_rounds`` completed user/agent rounds plus the current turn.
    TURN_WINDOW = "turn_window"
    #: The current element plus the single element preceding it.
    PREVIOUS_ELEMENT = "previous_element"
    #: Each user message and each agent reply embedded alone; history reaches
    #: the model only through the HMM and the classifier's lookback block.
    PER_MESSAGE = "per_message"


class RawV5ServingConfig(BaseModel):
    """The pipeline configuration a raw-V5 package declares for serving.

    This is the boundary model parsed out of a package manifest, so it is
    closed and frozen: an unknown key or a mutated field must fail rather than
    silently serve a different geometry than the one that was trained.
    """

    model_config = ConfigDict(frozen=True, extra="forbid")

    provider: RawV5EmbeddingProvider
    tool_evidence: RawV5ToolEvidenceMode
    document_window: RawV5DocumentWindow
    lookback_rounds: int


class RawV5InteractionStateGeometry(BaseModel):
    """The round-level HMM and classifier lookback an interaction-state package serves.

    ``element_lookback_rounds`` counts the current round (1 = no lookback
    block), so a lookback source is declared exactly when earlier rounds are.
    """

    model_config = ConfigDict(frozen=True, extra="forbid")

    hmm_chain: Literal[HMMChain.INTERACTION_STATE]
    user_pca_dim: int
    agent_pca_dim: int
    element_lookback_rounds: int
    element_lookback_source: ElementLookbackSource | None

    @model_validator(mode="after")
    def _consistent_lookback(self) -> Self:
        if (
            self.user_pca_dim <= 0
            or self.agent_pca_dim <= 0
            or self.element_lookback_rounds < 1
        ):
            raise ValueError(
                "interaction-state geometry needs positive per-role PCA widths and lookback rounds"
            )
        if (self.element_lookback_source is None) != (
            self.element_lookback_rounds == 1
        ):
            raise ValueError(
                "an element_lookback source is declared exactly when the lookback spans earlier rounds"
            )
        return self

    def element_lookback(self, *, embedding_dim: int) -> ElementLookback | None:
        if self.element_lookback_source is None:
            return None
        if self.element_lookback_source is ElementLookbackSource.STORED_VECTORS:
            return ElementLookback(
                rounds=self.element_lookback_rounds,
                source=self.element_lookback_source,
                user_dim=embedding_dim,
                agent_dim=embedding_dim,
            )
        return ElementLookback(
            rounds=self.element_lookback_rounds,
            source=self.element_lookback_source,
            user_dim=self.user_pca_dim,
            agent_dim=self.agent_pca_dim,
        )


@dataclass(frozen=True, slots=True)
class InteractionStateObservationSpace:
    """Round emissions ``[P_user(U_r) | P_agent(A_r)]``, one PCA basis per role.

    The bundle's element-row ``feature_dim`` describes a matrix this HMM never
    reads, so serving takes the emission width from here instead.
    """

    user_pca_dim: int
    agent_pca_dim: int


@dataclass(frozen=True, slots=True)
class RawV5ServingProfile:
    """The exact serving identity implied by one supported configuration."""

    config: RawV5ServingConfig
    representation: SequenceRepresentation
    embedding_model: str
    embedding_dim: int
    document_serializer_version: str
    document_serializer_sha256: str
    raw_feature_matrix: RawFeatureMatrix
    hmm_training_contract_version: RawV5HMMTrainingContractVersion
    _model_input_contract: Callable[[], dict[str, object]]
    frozen_encoder: StateEncoderContract | None = None

    @property
    def state_encoder_contract(self) -> StateEncoderContract:
        return self.frozen_encoder or get_state_encoder_contract(self.representation)

    @property
    def relative_position_dim(self) -> int:
        """Trailing position-column width of the raw HMM rows this profile serves.

        The sidecar rebuilds rows at the width the bundle's feature schema
        declares, so this is what binds the declared matrix to that width.
        """

        return _RAW_FEATURE_MATRIX_RELATIVE_POSITION_DIM[self.raw_feature_matrix]


_RAW_FEATURE_MATRIX_RELATIVE_POSITION_DIM: Mapping[RawFeatureMatrix, int] = {
    RawFeatureMatrix.VOYAGE_ELEMENT_KIND_SLOTS_V2: RELATIVE_POSITION_DIM,
    RawFeatureMatrix.GEMINI_EMBEDDING_2_ELEMENT_KIND_SLOTS_RELATIVE_POSITION_V1: (
        LEGACY_RELATIVE_POSITION_DIM
    ),
    RawFeatureMatrix.GEMINI_EMBEDDING_2_ELEMENT_KIND_SLOTS_V2: RELATIVE_POSITION_DIM,
}


_GEMINI_ELEMENT_PROFILE = RawV5ServingProfile(
    config=RawV5ServingConfig(
        provider=RawV5EmbeddingProvider.GEMINI,
        tool_evidence=RawV5ToolEvidenceMode.USER_AGENT_ONLY,
        document_window=RawV5DocumentWindow.PER_MESSAGE,
        lookback_rounds=RAW_V5_VOYAGE_ONE_TURN_MAX_TURNS,
    ),
    representation=GEMINI_EMBEDDING_2_RAW_ROUTER_V5_ELEMENT,
    embedding_model=RAW_V5_GEMINI_EMBEDDING_2_MODEL,
    embedding_dim=RAW_V5_GEMINI_EMBEDDING_2_DIM,
    document_serializer_version=RAW_V5_GEMINI_ELEMENT_SERIALIZER_VERSION,
    document_serializer_sha256=RAW_V5_GEMINI_ELEMENT_INPUT_CONTRACT_SHA256,
    raw_feature_matrix=RawFeatureMatrix.GEMINI_EMBEDDING_2_ELEMENT_KIND_SLOTS_V2,
    hmm_training_contract_version=RawV5HMMTrainingContractVersion.GEMINI_V2,
    _model_input_contract=raw_v5_gemini_element_model_input_contract,
)


# The only configuration serving supports: Gemini per-message documents with
# user/agent-only tool evidence and one lookback round. Packages declaring any
# other combination are rejected at load.
_PROFILES: tuple[RawV5ServingProfile, ...] = (_GEMINI_ELEMENT_PROFILE,)


# The Gemini user/agent-only candidates fit before the raw HMM matrix dropped
# its relative-position column (#12323) share every document and encoder
# contract with the current profile but carry one extra row column. They are
# immutable artifacts, so serving validates them against their own frozen
# lineage instead of relabelling them as v2. This profile is deliberately kept
# out of the configuration registry new training resolves through.


_PROFILES_BY_CONFIG: Mapping[RawV5ServingConfig, RawV5ServingProfile] = {
    profile.config: profile for profile in _PROFILES
}


def _supported_configs_message() -> str:
    return ", ".join(
        f"{p.config.provider!s}/{p.config.tool_evidence!s}/"
        f"{p.config.document_window!s}/{p.config.lookback_rounds}"
        for p in _PROFILES
    )


def raw_v5_serving_profile(config: RawV5ServingConfig) -> RawV5ServingProfile:
    """Resolve one servable configuration, or fail closed.

    An unsupported combination has no trained representation, corpus, or
    embedding cache, so it can only be rejected — never mapped onto the
    nearest configuration that happens to exist.
    """

    profile = _PROFILES_BY_CONFIG.get(config)
    if profile is None:
        raise RawV5ServingInputError(
            "raw V5 serving configuration is not servable: "
            f"provider={config.provider!s} tool_evidence={config.tool_evidence!s} "
            f"document_window={config.document_window!s} "
            f"lookback_rounds={config.lookback_rounds!r}; supported="
            f"{_supported_configs_message()}"
        )
    return profile


def validate_raw_v5_configured_hmm_bundle(
    bundle: object,
    profile: RawV5ServingProfile,
) -> InteractionStateObservationSpace | None:
    """Prove a bundle carries the declared configuration's representation.

    Used before package creation and again at sidecar startup so a release can
    never claim one configuration while loading another configuration's HMM.
    Returns the per-role emission geometry of an interaction-state bundle, and
    None for an element-chain bundle.
    """

    if not isinstance(bundle, dict):
        raise RawV5ServingInputError("raw V5 HMM bundle must be an object")
    sequence_contract = bundle.get("sequence_contract")
    if not isinstance(sequence_contract, dict):
        raise RawV5ServingInputError("raw V5 HMM bundle lacks sequence_contract")
    expected_encoder = profile.state_encoder_contract
    expected_values: dict[str, object] = {
        "sequence_representation": profile.representation.value,
        "embedding_model": expected_encoder.model_id,
    }
    sequence_expected_values = dict(expected_values)
    sequence_expected_values["representation"] = sequence_expected_values.pop(
        "sequence_representation"
    )
    for location, value, expected_by_field in (
        ("raw V5 HMM bundle", bundle, expected_values),
        ("raw V5 HMM sequence contract", sequence_contract, sequence_expected_values),
    ):
        for field, expected in expected_by_field.items():
            if value.get(field) != expected:
                raise RawV5ServingInputError(
                    f"{location} has an invalid {field} contract"
                )
    if sequence_contract.get("state_dimension") != expected_encoder.hidden_dim:
        raise RawV5ServingInputError(
            "raw V5 HMM sequence contract has an invalid state dimension"
        )
    # The serving row builder takes its position-column width from this schema,
    # so a v1 bundle that omitted it would be rebuilt one column narrower than
    # its PCA input, and a v2 bundle labelled v1 one column wider.
    feature_schema = sequence_contract.get("feature_schema")
    if not isinstance(feature_schema, dict):
        raise RawV5ServingInputError(
            "raw V5 HMM sequence contract lacks feature_schema"
        )
    try:
        declared_relative_position_dim = raw_relative_position_dim(feature_schema)
    except ValueError as error:
        raise RawV5ServingInputError(str(error)) from error
    if declared_relative_position_dim != profile.relative_position_dim:
        raise RawV5ServingInputError(
            "raw V5 HMM sequence contract feature_schema declares a "
            f"relative_position_dim of {declared_relative_position_dim}, but "
            f"{profile.raw_feature_matrix.value} rows carry "
            f"{profile.relative_position_dim}"
        )
    return _hmm_observation_space(bundle, profile)


def _hmm_observation_space(
    bundle: dict[str, object],
    profile: RawV5ServingProfile,
) -> InteractionStateObservationSpace | None:
    """Per-message profiles serve only the round-level chain; every other profile the element chain."""

    training = bundle.get("training")
    chain = hmm_chain_of(training if isinstance(training, dict) else None)
    if profile.config.document_window is not RawV5DocumentWindow.PER_MESSAGE:
        if chain is not HMMChain.ELEMENT:
            raise RawV5ServingInputError(
                f"{profile.representation.value} serves only element-chain HMM bundles"
            )
        return None
    if not isinstance(training, dict) or chain is not HMMChain.INTERACTION_STATE:
        raise RawV5ServingInputError(
            "per-message raw V5 documents are served only by an interaction-state HMM bundle"
        )
    # Only diagonal emissions let the unobserved current agent block marginalize exactly.
    if (
        training.get("covariance") != "diagonal"
        or training.get("baseline") != "full_bayes_sticky_hdp_hmm"
    ):
        raise RawV5ServingInputError(
            "interaction-state HMM bundle must be a diagonal Bayesian sticky HDP-HMM"
        )
    user_pca_dim = training.get("user_pca_dim")
    agent_pca_dim = training.get("agent_pca_dim")
    for name, value in (
        ("user_pca_dim", user_pca_dim),
        ("agent_pca_dim", agent_pca_dim),
    ):
        if isinstance(value, bool) or not isinstance(value, int) or value <= 0:
            raise RawV5ServingInputError(
                f"interaction-state HMM bundle must pin a positive training.{name}"
            )
    return InteractionStateObservationSpace(
        user_pca_dim=user_pca_dim, agent_pca_dim=agent_pca_dim
    )
