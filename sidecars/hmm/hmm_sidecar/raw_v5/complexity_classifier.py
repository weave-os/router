"""Serving loader for the trained complexity classifier bundle.

Loads the immutable artifact dir written by ``train_complexity_classifier``
and refuses to serve anything whose integrity or contract does not match the
``complexity_clf.weavemodel.json`` sidecar: required files re-hashed against
the pinned ``bundle_sha256``, class order checked against the pinned
``COMPLEXITY_CLASSES``, and feature width validated per prediction. The
confidence gate lives with the caller (route selector): a prediction below
threshold falls back to the deterministic policy.
"""

from __future__ import annotations

import hashlib
import json
from dataclasses import dataclass
from pathlib import Path
from typing import Any, NamedTuple

import numpy as np

from hmm_sidecar.raw_v5.complexity_labels import (
    COMPLEXITY_CLASSES,
    LEGACY_FOUR_CLASS_COMPLEXITY_CLASSES,
    LEGACY_FIVE_CLASS_COMPLEXITY_CLASSES,
)
from hmm_sidecar.raw_v5.cd_privacy import (
    assert_training_privacy_attestation,
)
from hmm_sidecar.raw_v5.core import sha256_file
from hmm_sidecar.raw_v5.state_representation import (
    CONTEXTUAL_CANONICAL_REPRESENTATIONS,
    GEMINI_EMBEDDING_REPRESENTATIONS,
    RAW_V5_ROUTER_REPRESENTATIONS,
    SequenceRepresentation,
    VOYAGE_4_LARGE_RAW_ROUTER_V5_TWO_TURN,
    resolve_sequence_representation,
    validate_gemma_state_service_contract,
    validate_state_encoder_contract,
)
from hmm_sidecar.raw_v5.raw_v5_contract import (
    RAW_V5_ROUTER_EMBEDDING_DIM,
    RAW_V5_ROUTER_SEQUENCE_CONTRACT_SHA256,
    RAW_V5_ROUTER_SEQUENCE_LAYOUT,
    raw_v5_router_sequence_contract,
)
from hmm_sidecar.raw_v5.raw_v5_serving import (
    raw_v5_voyage_two_turn_hmm_training_contract,
    raw_v5_voyage_two_turn_hmm_training_contract_sha256,
)
from hmm_sidecar.raw_v5.raw_v5_serving_profile import (
    RawV5DocumentWindow,
    RawV5ServingProfile,
    raw_v5_configured_hmm_training_contract,
    raw_v5_configured_hmm_training_contract_sha256,
)
from hmm_sidecar.raw_v5.hmm_feature_arms import (
    ELEMENT_LOOKBACK_BLOCK_NAME,
    ElementLookback,
    resolve_element_lookback,
)
from hmm_sidecar.raw_v5.serving_features import (
    SCALAR_FEATURE_NAMES,
    belief_dynamics_layout_block,
)
from hmm_sidecar.raw_v5.weave_model import WeaveModel
from hmm_sidecar.raw_v5.frozen_raw_v5 import frozen_raw_v5_encoder

REQUIRED_BUNDLE_FILES = ("model.json", "classes.json", "metadata.json")
WEAVEMODEL_SIDECAR_FILENAME = "complexity_clf.weavemodel.json"
MODEL_BUNDLE_SCHEMA_VERSION = "hmm_router_complexity_clf_bundle.v4"
PREDICTION_SCHEMA_VERSION = "hmm_router_complexity_prediction_v4"
PREVIOUS_MODEL_BUNDLE_SCHEMA_VERSION = "hmm_router_complexity_clf_bundle.v3"
PREVIOUS_PREDICTION_SCHEMA_VERSION = "hmm_router_complexity_prediction_v3"
LEGACY_MODEL_BUNDLE_SCHEMA_VERSION = "hmm_router_complexity_clf_bundle.v2"
LEGACY_PREDICTION_SCHEMA_VERSION = "hmm_router_complexity_prediction_v2"
MODEL_FAMILY = "hmm_router_complexity_classifier"
TASK_FAMILY = "hmm_router_routing"


def raw_v5_voyage_two_turn_feature_layout() -> list[dict[str, Any]]:
    raise NotImplementedError("not on the classifier serving path")


def raw_v5_voyage_two_turn_feature_dim() -> int:
    raise NotImplementedError("not on the classifier serving path")


def raw_v5_no_tool_context_feature_layout(
    n_states: int,
    *,
    embedding_dim: int = RAW_V5_ROUTER_EMBEDDING_DIM,
    belief_dynamics: bool = False,
    element_lookback: ElementLookback | None = None,
) -> list[dict[str, Any]]:
    """Return the raw-V5 classifier columns for one HMM state count.

    Unlike the v6 rollback contract above, this layout has no supplemental
    numeric tool-context block. Tool use remains part of the embedded raw
    agent document; it is not appended as a second classifier input.

    ``embedding_dim`` is the declared provider's vector width, so the same
    structural layout describes a Voyage and a Gemini candidate without either
    one being able to validate against the other's geometry.

    ``belief_dynamics`` selects the layout of classifiers retrained with the
    trailing causal belief-dynamics block. It is an additive suffix, so a
    bundle trained before the block keeps validating unchanged. An
    interaction-state classifier may append ``element_lookback`` after it.
    """

    if isinstance(n_states, bool) or not isinstance(n_states, int) or n_states <= 0:
        raise ComplexityClassifierError(
            "raw V5 no-tool-context classifier state count must be a positive "
            f"integer, got {n_states!r}"
        )
    if element_lookback is not None and not belief_dynamics:
        raise ComplexityClassifierError(
            f"{ELEMENT_LOOKBACK_BLOCK_NAME} follows the belief-dynamics block"
        )
    layout = [
        {"block": "embedding", "dim": embedding_dim},
        {"block": "hmm_gamma", "dim": n_states},
        {"block": "one_hot_current_state", "dim": n_states},
        {"block": "one_hot_previous_state", "dim": n_states},
        {"block": "scalars", "dim": 6, "names": list(SCALAR_FEATURE_NAMES)},
    ]
    if belief_dynamics:
        layout.append(belief_dynamics_layout_block())
    if element_lookback is not None:
        layout.append(element_lookback.layout_block())
    return layout


def raw_v5_no_tool_context_feature_dim(
    n_states: int,
    *,
    embedding_dim: int = RAW_V5_ROUTER_EMBEDDING_DIM,
    belief_dynamics: bool = False,
    element_lookback: ElementLookback | None = None,
) -> int:
    return sum(
        int(block["dim"])
        for block in raw_v5_no_tool_context_feature_layout(
            n_states,
            embedding_dim=embedding_dim,
            belief_dynamics=belief_dynamics,
            element_lookback=element_lookback,
        )
    )


class RawV5ClassifierLayout(NamedTuple):
    """The optional blocks a no-tool-context raw-V5 classifier declares."""

    n_states: int
    belief_dynamics: bool
    element_lookback: ElementLookback | None


def raw_v5_voyage_two_turn_no_tool_context_feature_layout(
    n_states: int,
    *,
    belief_dynamics: bool = False,
) -> list[dict[str, Any]]:
    raise NotImplementedError("not on the classifier serving path")


def raw_v5_voyage_two_turn_no_tool_context_feature_dim(
    n_states: int,
    *,
    belief_dynamics: bool = False,
) -> int:
    raise NotImplementedError("not on the classifier serving path")


def _raw_v5_no_tool_context_layout(
    source: dict[str, Any],
    *,
    source_name: str,
    embedding_dim: int = RAW_V5_ROUTER_EMBEDDING_DIM,
) -> RawV5ClassifierLayout:
    """Return the layout's HMM state count and its optional trailing blocks:
    belief dynamics, then (interaction-state classifiers only) element_lookback."""

    layout = source.get("feature_layout")
    if not isinstance(layout, list) or len(layout) not in (5, 6, 7):
        raise ComplexityClassifierError(
            "raw V5 no-tool-context classifier has an invalid "
            f"{source_name} feature layout"
        )
    belief_dynamics = len(layout) >= 6
    element_lookback = None
    if len(layout) == 7:
        try:
            element_lookback = resolve_element_lookback(layout[6], embedding_dim=embedding_dim)
        except ValueError as error:
            raise ComplexityClassifierError(
                "raw V5 no-tool-context classifier has an invalid "
                f"{source_name} {ELEMENT_LOOKBACK_BLOCK_NAME} block: {error}"
            ) from error
    state_dims: list[int] = []
    for index in (1, 2, 3):
        dim = layout[index].get("dim") if isinstance(layout[index], dict) else None
        if isinstance(dim, bool) or not isinstance(dim, int) or dim <= 0:
            raise ComplexityClassifierError(
                "raw V5 no-tool-context classifier has an invalid "
                f"{source_name} HMM state dimension"
            )
        state_dims.append(dim)
    if len(set(state_dims)) != 1:
        raise ComplexityClassifierError(
            "raw V5 no-tool-context classifier has unequal HMM feature block "
            f"dimensions in {source_name}"
        )
    n_states = state_dims[0]
    expected_layout = raw_v5_no_tool_context_feature_layout(
        n_states,
        embedding_dim=embedding_dim,
        belief_dynamics=belief_dynamics,
        element_lookback=element_lookback,
    )
    expected_dim = raw_v5_no_tool_context_feature_dim(
        n_states,
        embedding_dim=embedding_dim,
        belief_dynamics=belief_dynamics,
        element_lookback=element_lookback,
    )
    if layout != expected_layout or source.get("feature_dim") != expected_dim:
        raise ComplexityClassifierError(
            "raw V5 no-tool-context classifier has an invalid "
            f"{source_name} feature layout"
        )
    return RawV5ClassifierLayout(n_states, belief_dynamics, element_lookback)


def validate_raw_v5_no_tool_context_feature_contract(
    *,
    metadata: dict[str, Any],
    input_contract: dict[str, Any],
    embedding_dim: int = RAW_V5_ROUTER_EMBEDDING_DIM,
) -> RawV5ClassifierLayout:
    """Validate the structural layout and return its state count and blocks."""

    metadata_layout = _raw_v5_no_tool_context_layout(
        metadata,
        source_name="metadata",
        embedding_dim=embedding_dim,
    )
    contract_layout = _raw_v5_no_tool_context_layout(
        input_contract,
        source_name="WeaveModel input contract",
        embedding_dim=embedding_dim,
    )
    if metadata_layout != contract_layout:
        raise ComplexityClassifierError(
            "raw V5 no-tool-context classifier metadata and WeaveModel HMM "
            "state counts or feature blocks disagree"
        )
    return metadata_layout


def validate_raw_v5_voyage_two_turn_no_tool_context_feature_contract(
    *, metadata: dict[str, Any], input_contract: dict[str, Any]
) -> int:
    raise NotImplementedError("not on the classifier serving path")


def _validate_raw_v5_voyage_two_turn_feature_contract(
    *, metadata: dict[str, Any], input_contract: dict[str, Any]
) -> None:
    raise NotImplementedError("not on the classifier serving path")


def _validate_raw_v5_configured_contract(
    *,
    metadata: dict[str, Any],
    input_contract: dict[str, Any],
    profile: RawV5ServingProfile,
) -> None:
    """Validate a raw-V5 classifier against the configuration it declares.

    This is the generic form of the two fixed Voyage checks: every expectation
    is derived from the declared profile, so a Gemini or user/agent-only
    candidate is validated as strictly as the deployed Voyage one and neither
    can pass under the other's geometry.
    """

    raw_encoder_contract = metadata.get("state_encoder_contract")
    if not isinstance(raw_encoder_contract, dict):
        raise ComplexityClassifierError(
            "raw V5 classifier lacks state_encoder_contract"
        )
    try:
        encoder_contract = profile.state_encoder_contract
        if raw_encoder_contract != encoder_contract.to_dict():
            raise ValueError("state encoder does not match the declared serving profile")
    except ValueError as exc:
        raise ComplexityClassifierError(
            f"raw V5 classifier has an invalid state encoder contract: {exc}"
        ) from exc
    expected_values: dict[str, Any] = {
        "sequence_representation": profile.representation.value,
        "sequence_layout": RAW_V5_ROUTER_SEQUENCE_LAYOUT,
        "raw_v5_router_sequence_contract": raw_v5_router_sequence_contract(),
        "raw_v5_router_sequence_contract_sha256": (
            RAW_V5_ROUTER_SEQUENCE_CONTRACT_SHA256
        ),
        "raw_v5_router_hmm_training_contract": (
            raw_v5_configured_hmm_training_contract(profile)
        ),
        "raw_v5_router_hmm_training_contract_sha256": (
            raw_v5_configured_hmm_training_contract_sha256(profile)
        ),
        "state_encoder_contract_sha256": encoder_contract.sha256,
        "training_only": True,
        "serving_loader_available": False,
    }
    for field, expected in expected_values.items():
        if metadata.get(field) != expected or input_contract.get(field) != expected:
            raise ComplexityClassifierError(
                f"raw V5 classifier has an invalid {field} for the "
                f"{profile.representation.value} configuration it declares"
            )
    layout = validate_raw_v5_no_tool_context_feature_contract(
        metadata=metadata,
        input_contract=input_contract,
        embedding_dim=profile.embedding_dim,
    )
    if (
        layout.element_lookback is not None
        and profile.config.document_window is not RawV5DocumentWindow.PER_MESSAGE
    ):
        raise ComplexityClassifierError(
            f"an {ELEMENT_LOOKBACK_BLOCK_NAME} block is served only from per-message "
            f"documents, not {profile.representation.value}"
        )


class ComplexityClassifierError(RuntimeError):
    """Raised when the classifier bundle is missing, corrupt, or mismatched."""


def _validate_training_only_serving_release(
    *,
    training_only: bool,
    representation: SequenceRepresentation | None,
    allow_raw_v5_serving_release: bool,
    allow_raw_v5_no_tool_context_serving_release: bool = False,
    raw_v5_configured_profile: RawV5ServingProfile | None = None,
) -> None:
    selected = (
        int(allow_raw_v5_serving_release)
        + int(allow_raw_v5_no_tool_context_serving_release)
        + int(raw_v5_configured_profile is not None)
    )
    if selected > 1:
        raise ComplexityClassifierError(
            "raw V5 serving release must select exactly one package feature contract"
        )
    if not training_only:
        return
    if selected == 0:
        raise ComplexityClassifierError(
            "classifier is marked training-only and cannot be loaded for serving"
        )
    if raw_v5_configured_profile is not None:
        if representation is not raw_v5_configured_profile.representation:
            raise ComplexityClassifierError(
                "training-only classifier does not carry the representation its "
                "declared raw V5 configuration serves: "
                f"{representation!s} != "
                f"{raw_v5_configured_profile.representation.value}"
            )
        return
    if representation is not VOYAGE_4_LARGE_RAW_ROUTER_V5_TWO_TURN:
        raise ComplexityClassifierError(
            "training-only classifier is only releasable for the pinned raw V5 "
            "Voyage two-turn representation"
        )


@dataclass(frozen=True)
class ComplexityPrediction:
    label: str
    label_index: int
    probs: dict[str, float]
    confidence: float
    margin: float


@dataclass(frozen=True)
class ValidatedClassifierBundle:
    """Classifier metadata proven safe to package or load for serving."""

    weave_model: WeaveModel
    metadata: dict[str, Any]
    file_sha256: dict[str, str]


def validate_classifier_bundle(
    artifact_dir: Path | str,
    *,
    expected_hmm_bundle_sha256: str | None = None,
    allow_raw_v5_serving_release: bool = False,
    allow_raw_v5_no_tool_context_serving_release: bool = False,
    raw_v5_configured_profile: RawV5ServingProfile | None = None,
) -> ValidatedClassifierBundle:
    """Validate the immutable classifier contract without loading XGBoost.

    This is deliberately side-effect free so publishers can fail before they
    create an archive or write anything to WeaveRegistry.
    """

    artifact_dir = Path(artifact_dir)
    for name in (*REQUIRED_BUNDLE_FILES, WEAVEMODEL_SIDECAR_FILENAME):
        if not (artifact_dir / name).is_file():
            raise ComplexityClassifierError(
                f"classifier bundle missing {name}: {artifact_dir}"
            )
    weave_model = WeaveModel.model_validate_json(
        (artifact_dir / WEAVEMODEL_SIDECAR_FILENAME).read_text(encoding="utf-8")
    )
    bundle_schema = weave_model.artifact.bundle_schema_version
    schema_contracts = {
        MODEL_BUNDLE_SCHEMA_VERSION: (
            COMPLEXITY_CLASSES,
            PREDICTION_SCHEMA_VERSION,
        ),
        PREVIOUS_MODEL_BUNDLE_SCHEMA_VERSION: (
            LEGACY_FOUR_CLASS_COMPLEXITY_CLASSES,
            PREVIOUS_PREDICTION_SCHEMA_VERSION,
        ),
        LEGACY_MODEL_BUNDLE_SCHEMA_VERSION: (
            LEGACY_FIVE_CLASS_COMPLEXITY_CLASSES,
            LEGACY_PREDICTION_SCHEMA_VERSION,
        ),
    }
    if bundle_schema not in schema_contracts:
        raise ComplexityClassifierError(
            "classifier WeaveModel bundle schema is unsupported: "
            f"{weave_model.artifact.bundle_schema_version!r}"
        )
    expected_classes_tuple, expected_prediction_schema = schema_contracts[bundle_schema]
    if tuple(weave_model.artifact.required_files) != REQUIRED_BUNDLE_FILES:
        raise ComplexityClassifierError(
            "classifier WeaveModel required_files do not match the canonical "
            f"bundle: {list(weave_model.artifact.required_files)!r}"
        )
    file_sha256 = {
        name: sha256_file(artifact_dir / name) for name in REQUIRED_BUNDLE_FILES
    }
    actual_bundle_sha = hashlib.sha256(
        json.dumps(file_sha256, sort_keys=True).encode("utf-8")
    ).hexdigest()
    pinned = weave_model.artifact.bundle_sha256
    if pinned != actual_bundle_sha:
        raise ComplexityClassifierError(
            "classifier bundle files do not match the pinned WeaveModel "
            f"bundle_sha256 ({actual_bundle_sha[:12]} != {str(pinned)[:12]}): "
            f"{artifact_dir}"
        )
    metadata = json.loads((artifact_dir / "metadata.json").read_text(encoding="utf-8"))
    if not isinstance(metadata, dict):
        raise ComplexityClassifierError("classifier metadata must be a JSON object")
    training_only = (
        metadata.get("training_only") is True
        or metadata.get("serving_loader_available") is False
    )
    metadata_schema = metadata.get("schema_version")
    if metadata_schema != bundle_schema:
        raise ComplexityClassifierError(
            "classifier metadata/WeaveModel bundle schemas disagree: "
            f"{metadata.get('schema_version')!r}"
        )
    classes = json.loads((artifact_dir / "classes.json").read_text(encoding="utf-8"))
    expected_classes = list(expected_classes_tuple)
    if classes != expected_classes or metadata.get("classes") != expected_classes:
        raise ComplexityClassifierError(
            "classifier class order does not match pinned COMPLEXITY_CLASSES: "
            f"{classes!r}"
        )
    if weave_model.contracts.output_contract.get("classes") != expected_classes:
        raise ComplexityClassifierError(
            "classifier WeaveModel output contract class order does not match "
            "pinned COMPLEXITY_CLASSES"
        )
    if (
        weave_model.model_family != MODEL_FAMILY
        or weave_model.task_family != TASK_FAMILY
    ):
        raise ComplexityClassifierError(
            "classifier WeaveModel family/task contract mismatch: "
            f"{weave_model.model_family!r}/{weave_model.task_family!r}"
        )
    if weave_model.contracts.score_schema_version != expected_prediction_schema:
        raise ComplexityClassifierError(
            "classifier WeaveModel prediction schema mismatch: "
            f"{weave_model.contracts.score_schema_version!r}"
        )
    if (
        len(weave_model.output_data) != 1
        or weave_model.output_data[0].schema_version != expected_prediction_schema
        or weave_model.output_data[0].data_id is not None
    ):
        raise ComplexityClassifierError(
            "classifier WeaveModel output-data contract does not use the "
            f"abstract {expected_prediction_schema!r} schema"
        )
    expected_model_id = f"{MODEL_FAMILY}_{actual_bundle_sha[:16]}"
    if weave_model.model_id != expected_model_id:
        raise ComplexityClassifierError(
            "classifier WeaveModel model_id is not derived from its bundle "
            f"hash: expected {expected_model_id!r}, got {weave_model.model_id!r}"
        )
    if weave_model.artifact.artifact_version != actual_bundle_sha:
        raise ComplexityClassifierError(
            "classifier WeaveModel artifact_version does not pin its bundle hash"
        )
    input_contract = weave_model.contracts.input_contract
    for field in (
        "feature_dim",
        "hmm_bundle_json_sha256",
        "sequence_representation",
        "state_encoder_contract",
        "state_encoder_contract_sha256",
        "state_service_contract",
        "state_service_contract_sha256",
        "state_dimension",
    ):
        if input_contract.get(field) != metadata.get(field):
            raise ComplexityClassifierError(
                f"classifier metadata {field} does not match WeaveModel contract"
            )
    raw_representation = metadata.get("sequence_representation")
    representation: SequenceRepresentation | None = None
    if raw_representation is not None:
        try:
            representation = resolve_sequence_representation(str(raw_representation))
        except ValueError as exc:
            raise ComplexityClassifierError(
                f"classifier has an invalid sequence representation: {exc}"
            ) from exc
    _validate_training_only_serving_release(
        training_only=training_only,
        representation=representation,
        allow_raw_v5_serving_release=allow_raw_v5_serving_release,
        allow_raw_v5_no_tool_context_serving_release=(
            allow_raw_v5_no_tool_context_serving_release
        ),
        raw_v5_configured_profile=raw_v5_configured_profile,
    )
    if raw_v5_configured_profile is not None:
        if representation not in RAW_V5_ROUTER_REPRESENTATIONS:
            raise ComplexityClassifierError(
                "raw V5 configured serving release requires a raw V5 classifier "
                f"representation, got {representation!s}"
            )
        _validate_raw_v5_configured_contract(
            metadata=metadata,
            input_contract=input_contract,
            profile=raw_v5_configured_profile,
        )
    elif representation is not None:
        if representation in GEMINI_EMBEDDING_REPRESENTATIONS:
            raw_encoder_contract = metadata.get("state_encoder_contract")
            if not isinstance(raw_encoder_contract, dict):
                raise ComplexityClassifierError(
                    "Gemini classifier lacks state_encoder_contract"
                )
            try:
                validate_state_encoder_contract(
                    raw_encoder_contract,
                    expected_representation=representation,
                )
            except ValueError as exc:
                raise ComplexityClassifierError(
                    f"Gemini classifier has an invalid state encoder contract: {exc}"
                ) from exc
        elif representation in CONTEXTUAL_CANONICAL_REPRESENTATIONS:
            raw_service_contract = metadata.get("state_service_contract")
            if not isinstance(raw_service_contract, dict):
                raise ComplexityClassifierError(
                    "Gemma classifier lacks state_service_contract"
                )
            try:
                validate_gemma_state_service_contract(
                    raw_service_contract,
                    expected_representation=representation,
                    expected_sha256=metadata.get("state_service_contract_sha256"),
                )
            except ValueError as exc:
                raise ComplexityClassifierError(
                    f"Gemma classifier has an invalid state service contract: {exc}"
                ) from exc
        elif representation in RAW_V5_ROUTER_REPRESENTATIONS:
            if not (
                allow_raw_v5_serving_release
                or allow_raw_v5_no_tool_context_serving_release
            ):
                raise ComplexityClassifierError(
                    "raw V5 Voyage classifier requires an explicit raw serving "
                    "package release"
                )
            if representation is not VOYAGE_4_LARGE_RAW_ROUTER_V5_TWO_TURN:
                raise ComplexityClassifierError(
                    "raw V5 serving supports only the pinned Voyage two-turn "
                    "representation"
                )
            raw_encoder_contract = metadata.get("state_encoder_contract")
            if not isinstance(raw_encoder_contract, dict):
                raise ComplexityClassifierError(
                    "raw V5 Voyage classifier lacks state_encoder_contract"
                )
            try:
                encoder_contract = frozen_raw_v5_encoder(representation)
                if raw_encoder_contract != encoder_contract.to_dict():
                    raise ValueError("state encoder does not match the frozen Voyage profile")
            except ValueError as exc:
                raise ComplexityClassifierError(
                    "raw V5 Voyage classifier has an invalid state encoder "
                    f"contract: {exc}"
                ) from exc
            expected_values = {
                "sequence_layout": RAW_V5_ROUTER_SEQUENCE_LAYOUT,
                "raw_v5_router_sequence_contract": raw_v5_router_sequence_contract(),
                "raw_v5_router_sequence_contract_sha256": (
                    RAW_V5_ROUTER_SEQUENCE_CONTRACT_SHA256
                ),
                "raw_v5_router_hmm_training_contract": (
                    raw_v5_voyage_two_turn_hmm_training_contract()
                ),
                "raw_v5_router_hmm_training_contract_sha256": (
                    raw_v5_voyage_two_turn_hmm_training_contract_sha256()
                ),
                "training_only": True,
                "serving_loader_available": False,
            }
            for field, expected in expected_values.items():
                if metadata.get(field) != expected or input_contract.get(field) != expected:
                    raise ComplexityClassifierError(
                        "raw V5 Voyage classifier has an invalid " f"{field}"
                    )
            if allow_raw_v5_no_tool_context_serving_release:
                validate_raw_v5_voyage_two_turn_no_tool_context_feature_contract(
                    metadata=metadata,
                    input_contract=input_contract,
                )
            else:
                _validate_raw_v5_voyage_two_turn_feature_contract(
                    metadata=metadata,
                    input_contract=input_contract,
                )
            if (
                encoder_contract.sha256
                != metadata.get("state_encoder_contract_sha256")
                or encoder_contract.sha256
                != input_contract.get("state_encoder_contract_sha256")
            ):
                raise ComplexityClassifierError(
                    "raw V5 Voyage classifier state encoder contract hash is invalid"
                )
    hmm_sha = str(input_contract.get("hmm_bundle_json_sha256") or "")
    if expected_hmm_bundle_sha256 is not None and hmm_sha != expected_hmm_bundle_sha256:
        raise ComplexityClassifierError(
            "classifier was not trained against the HMM bundle being packaged: "
            f"classifier expects {hmm_sha!r}, package has "
            f"{expected_hmm_bundle_sha256!r}"
        )
    metadata_privacy = assert_training_privacy_attestation(
        metadata.get("training_privacy"),
        context=f"classifier metadata {artifact_dir}",
    )
    contract_privacy = assert_training_privacy_attestation(
        input_contract.get("training_privacy"),
        context=f"classifier WeaveModel {artifact_dir}",
    )
    if metadata_privacy != contract_privacy:
        raise ComplexityClassifierError(
            "classifier metadata privacy attestation does not match WeaveModel contract"
        )
    return ValidatedClassifierBundle(
        weave_model=weave_model,
        metadata=metadata,
        file_sha256=file_sha256,
    )


class ComplexityClassifier:
    def __init__(
        self,
        *,
        booster: Any,
        metadata: dict[str, Any],
        weave_model: WeaveModel,
        artifact_dir: Path,
    ) -> None:
        self.booster = booster
        self.metadata = metadata
        self.weave_model = weave_model
        self.artifact_dir = artifact_dir
        self.feature_dim = int(metadata["feature_dim"])
        self.classes = tuple(metadata["classes"])

    @classmethod
    def load(
        cls,
        artifact_dir: Path | str,
        *,
        allow_raw_v5_serving_release: bool = False,
        allow_raw_v5_no_tool_context_serving_release: bool = False,
        raw_v5_configured_profile: RawV5ServingProfile | None = None,
    ) -> "ComplexityClassifier":
        artifact_dir = Path(artifact_dir)
        validated = validate_classifier_bundle(
            artifact_dir,
            allow_raw_v5_serving_release=allow_raw_v5_serving_release,
            allow_raw_v5_no_tool_context_serving_release=(
                allow_raw_v5_no_tool_context_serving_release
            ),
            raw_v5_configured_profile=raw_v5_configured_profile,
        )
        metadata = validated.metadata
        try:
            import xgboost

            booster = xgboost.XGBClassifier()
            booster.load_model(artifact_dir / "model.json")
            config = json.loads(booster.get_booster().save_config())["learner"]
            objective = str(config["objective"]["name"])
            params = config["learner_model_param"]
        except Exception as exc:
            raise ComplexityClassifierError(
                f"classifier XGBoost model is invalid: {exc}"
            ) from exc
        if objective != "multi:softprob":
            raise ComplexityClassifierError(
                "classifier XGBoost objective must be 'multi:softprob', got "
                f"{objective!r}"
            )
        if int(params["num_class"]) != len(metadata["classes"]):
            raise ComplexityClassifierError(
                "classifier XGBoost output width does not match the pinned "
                "complexity classes"
            )
        if int(params["num_feature"]) != int(metadata["feature_dim"]):
            raise ComplexityClassifierError(
                "classifier XGBoost input width does not match metadata.feature_dim"
            )
        return cls(
            booster=booster,
            metadata=validated.metadata,
            weave_model=validated.weave_model,
            artifact_dir=artifact_dir,
        )

    @property
    def sequence_autoencoder_identity(self) -> str | None:
        value = self.metadata.get("sequence_autoencoder_identity")
        return str(value) if value else None

    @property
    def hmm_bundle_version(self) -> str:
        raise NotImplementedError("not on the classifier serving path")

    def contributions(self, features: np.ndarray) -> np.ndarray:
        """Exact per-feature attributions (TreeSHAP) for every class.

        Returns shape (n_classes, n_features + 1); the last column is the
        bias term. Row k decomposes class k's margin: why the trees scored
        this input the way they did — not an approximation.
        """
        import xgboost

        vector = np.asarray(features, dtype=np.float32).reshape(-1)
        if vector.shape[0] != self.feature_dim:
            raise ComplexityClassifierError(
                f"classifier expects feature_dim={self.feature_dim}, got "
                f"{vector.shape[0]}"
            )
        contribs = self.booster.get_booster().predict(
            xgboost.DMatrix(vector[None, :]), pred_contribs=True
        )
        return np.asarray(contribs, dtype=float)[0]

    def predict(self, features: np.ndarray) -> ComplexityPrediction:
        vector = np.asarray(features, dtype=np.float32).reshape(-1)
        if vector.shape[0] != self.feature_dim:
            raise ComplexityClassifierError(
                f"classifier expects feature_dim={self.feature_dim}, got "
                f"{vector.shape[0]} — the serving feature builder does not "
                "match the training layout"
            )
        probs = np.asarray(self.booster.predict_proba(vector[None, :])[0], dtype=float)
        order = np.argsort(probs)[::-1]
        top = int(order[0])
        margin = float(probs[order[0]] - probs[order[1]]) if len(order) > 1 else 1.0
        return ComplexityPrediction(
            label=self.classes[top],
            label_index=top,
            probs={
                self.classes[index]: float(value) for index, value in enumerate(probs)
            },
            confidence=float(probs[top]),
            margin=margin,
        )
