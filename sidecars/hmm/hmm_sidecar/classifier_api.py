"""Versioned classifier-only HTTP API with an injected immutable release resolver."""

from __future__ import annotations

import asyncio
from collections.abc import Mapping
from dataclasses import dataclass
from enum import StrEnum
from typing import Protocol

from fastapi import APIRouter, Header, HTTPException, Query
from fastapi.responses import JSONResponse
from pydantic import BaseModel, ConfigDict, Field, ValidationError, model_validator

from hmm_sidecar.classifier_release import (
    ClassifierTaxonomy,
    ClassifierVariantID,
    is_classifier_release_id,
)


class ClassifierAPIError(RuntimeError):
    """Base error raised by classifier release resolution or inference."""


class UnknownClassifierRelease(ClassifierAPIError):
    """The requested immutable release is unavailable."""


class UnknownClassifierVariant(ClassifierAPIError):
    """The requested classifier variant is absent from the pinned release."""


class ClassifierWarmingError(ClassifierAPIError):
    """The exact classifier variant is starting and can be retried shortly."""


class ClassifierVariantUnavailable(ClassifierAPIError):
    """The pinned variant cannot classify this request."""


class ClassifierErrorCode(StrEnum):
    """Stable error codes consumed by the Router's deadline/fallback policy."""

    WARMING = "classifier_warming"
    UNAVAILABLE = "classifier_unavailable"


CLASSIFIER_TIMEOUT_HEADER = "X-Weave-Classifier-Timeout-Ms"
MAX_CLASSIFIER_TIMEOUT_MS = 600_000


class ClassifierAPIVersion(StrEnum):
    """Fixed classifier API wire schemas."""

    V5 = "policy_router_v5"


class MessageRole(StrEnum):
    """Roles accepted in the classifier's bounded conversation evidence."""

    SYSTEM = "system"
    USER = "user"
    ASSISTANT = "assistant"
    TOOL = "tool"


class ClassifierToolCall(BaseModel):
    """Generic tool-call evidence; it carries no provider or model selection."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    name: str = ""
    input_keys: tuple[str, ...] = ()
    input_json: str = ""


class ClassifierToolResult(BaseModel):
    """Tool-result evidence: error flag, bounded text, and size counts."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    tool_use_id: str = ""
    is_error: bool = False
    text: str = ""
    result_present: bool = False
    char_count: int = Field(default=0, ge=0)
    byte_count: int = Field(default=0, ge=0)
    exit_category: str = ""


class ClassifierMessage(BaseModel):
    """Canonical bounded turn evidence shared by every API major."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    role: MessageRole
    text: str = ""
    tool_calls: tuple[ClassifierToolCall, ...] = ()
    tool_results: tuple[ClassifierToolResult, ...] = ()


class ClassifierTool(BaseModel):
    """A tool advertised or invoked in the current conversation."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    name: str = Field(min_length=1)
    type: str = ""
    server_executed: bool = False


class ClassifierRequestV5(BaseModel):
    """Strict classifier facts request; all policy and dispatch authority stays in Go."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    schema_version: ClassifierAPIVersion
    classifier_selection_id: str = Field(pattern=r"^[0-9a-f]{64}$")
    classifier_release_id: str = Field(pattern=r"^[0-9a-f]{64}$")
    classifier_variant_id: ClassifierVariantID
    request_id: str = Field(min_length=1)
    # Opaque authenticated client session identity used only for in-memory inference continuity.
    session_id: str | None = Field(default=None, max_length=128)
    harness: str = Field(min_length=1)
    prompt_text: str = ""
    latest_user_text: str = ""
    turn_index: int = Field(default=0, ge=0)
    is_subagent: bool = False
    visible_turn_index: int | None = Field(default=None, ge=0)
    session_turn_count: int | None = Field(default=None, ge=0)
    turn_type: str = ""
    cache_state: str = ""
    prior_output_tokens: int | None = Field(default=None, ge=0)
    session_ever_switched: bool | None = None
    history_truncated: bool | None = None
    conversation_messages: tuple[ClassifierMessage, ...] = ()
    omitted_user_turn_count: int | None = Field(default=None, ge=0)
    available_tools: tuple[str, ...] = ()
    invoked_tools: tuple[str, ...] = ()
    tools: tuple[ClassifierTool, ...] = ()
    estimated_input_tokens: int = Field(default=0, ge=0)
    has_tools: bool = False
    has_images: bool = False


class ClassifierFactsV5(BaseModel):
    """Validated classification facts without timings or routing decisions."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    predicted_label: str = Field(min_length=1)
    class_order: tuple[str, ...] = Field(min_length=1)
    class_probabilities: dict[str, float]
    classifier_confidence: float = Field(ge=0.0, le=1.0)
    classifier_margin: float = Field(ge=0.0, le=1.0)
    hmm_state_id: int | None = Field(default=None, ge=0)
    hmm_state_path: tuple[int, ...] | None = None
    hmm_state_probabilities: tuple[float, ...] | None = None

    @model_validator(mode="after")
    def validate_taxonomy(self) -> ClassifierFactsV5:
        if set(self.class_probabilities) != set(self.class_order):
            raise ValueError("class probabilities must match class_order")
        if self.predicted_label not in self.class_probabilities:
            raise ValueError("predicted_label must belong to class_order")
        for label_probability in self.class_probabilities.values():
            if not 0.0 <= label_probability <= 1.0:
                raise ValueError("class probabilities must be in [0,1]")
        if abs(sum(self.class_probabilities.values()) - 1.0) > 1e-6:
            raise ValueError("class probabilities must sum to one")
        state_fields = (
            self.hmm_state_id,
            self.hmm_state_path,
            self.hmm_state_probabilities,
        )
        if any(value is None for value in state_fields) and not all(
            value is None for value in state_fields
        ):
            raise ValueError("HMM state fields must be present together")
        if self.hmm_state_id is not None:
            if self.hmm_state_id >= len(self.hmm_state_probabilities or ()):
                raise ValueError("hmm_state_id is outside state probabilities")
            for state_probability in self.hmm_state_probabilities or ():
                if not 0.0 <= state_probability <= 1.0:
                    raise ValueError("HMM state probabilities must be in [0,1]")
            if abs(sum(self.hmm_state_probabilities or ()) - 1.0) > 1e-6:
                raise ValueError("HMM state probabilities must sum to one")
            if any(
                state_id < 0 or state_id >= len(self.hmm_state_probabilities or ())
                for state_id in self.hmm_state_path or ()
            ):
                raise ValueError("HMM state path is outside state probabilities")
        return self


class ClassifierResponseV5(ClassifierFactsV5):
    """Classifier response with exact release and variant identity echoes."""

    schema_version: ClassifierAPIVersion
    request_id: str
    classifier_selection_id: str = Field(pattern=r"^[0-9a-f]{64}$")
    classifier_release_id: str = Field(pattern=r"^[0-9a-f]{64}$")
    classifier_variant_id: ClassifierVariantID
    classifier_artifact_id: str = Field(min_length=1)
    classifier_artifact_sha256: str = Field(pattern=r"^[0-9a-f]{64}$")
    classifier_taxonomy_sha256: str = Field(pattern=r"^[0-9a-f]{64}$")


class ClassifierCapabilitiesV5(BaseModel):
    """Required per-release feature declaration used by Router activation preflight."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    schema_version: ClassifierAPIVersion
    api_major: int = Field(ge=5)
    classifier_selection_id: str = Field(pattern=r"^[0-9a-f]{64}$")
    classifier_release_id: str = Field(pattern=r"^[0-9a-f]{64}$")
    wire_schema: ClassifierAPIVersion
    class_order: tuple[str, ...] = Field(min_length=1)
    classifier_taxonomy_sha256: str = Field(pattern=r"^[0-9a-f]{64}$")
    classifier_variants: tuple[ClassifierVariantID, ...] = Field(min_length=1)
    supports_preview: bool

    @model_validator(mode="after")
    def validate_capability_schema(self) -> ClassifierCapabilitiesV5:
        if self.schema_version != self.wire_schema:
            raise ValueError("capability schema and classifier wire schema differ")
        if len(set(self.classifier_variants)) != len(self.classifier_variants):
            raise ValueError("classifier_variants must be unique")
        return self


@dataclass(frozen=True)
class ResolvedClassifierVariant:
    """One immutable classifier package and its inference implementation."""

    variant_id: ClassifierVariantID
    artifact_id: str
    package_sha256: str
    runtime: ClassifierRuntime


@dataclass(frozen=True)
class ResolvedClassifierRelease:
    """Exact loaded classifier release resolved by its content address."""

    release_id: str
    api_major: int
    wire_schema: ClassifierAPIVersion
    class_order: tuple[str, ...]
    taxonomy_sha256: str
    variants: Mapping[ClassifierVariantID, ResolvedClassifierVariant]

    def resolve_variant(
        self, variant_id: ClassifierVariantID
    ) -> ResolvedClassifierVariant:
        try:
            variant = self.variants[variant_id]
        except KeyError as error:
            raise UnknownClassifierVariant(variant_id.value) from error
        if variant.variant_id != variant_id:
            raise ClassifierAPIError("classifier variant identity mismatch")
        return variant


@dataclass(frozen=True)
class ResolvedClassifierSelection:
    """Immutable target selection that enables variants for one release."""

    selection_id: str
    release_id: str
    enabled_variants: frozenset[ClassifierVariantID]


class ClassifierRuntime(Protocol):
    """Pure inference adapter for one loaded release variant."""

    async def classify(
        self, request: ClassifierRequestV5, *, session_id: str | None
    ) -> ClassifierFactsV5: ...


class ClassifierReleaseResolver(Protocol):
    """Resolve an exact immutable release; implementations own physical dispatch."""

    async def resolve(self, release_id: str) -> ResolvedClassifierRelease: ...

    async def resolve_selection(
        self, selection_id: str
    ) -> ResolvedClassifierSelection: ...


class MappingClassifierReleaseResolver:
    """In-memory resolver for a self-hosted single process or isolated tests."""

    def __init__(
        self,
        releases: Mapping[str, ResolvedClassifierRelease],
        selections: Mapping[str, ResolvedClassifierSelection] | None = None,
    ) -> None:
        self._releases = dict(releases)
        self._selections = (
            dict(selections)
            if selections is not None
            else {
                release_id: ResolvedClassifierSelection(
                    selection_id=release_id,
                    release_id=release_id,
                    enabled_variants=frozenset(release.variants),
                )
                for release_id, release in self._releases.items()
            }
        )

    async def resolve(self, release_id: str) -> ResolvedClassifierRelease:
        if not is_classifier_release_id(release_id):
            raise UnknownClassifierRelease(release_id)
        try:
            release = self._releases[release_id]
        except KeyError as error:
            raise UnknownClassifierRelease(release_id) from error
        if release.release_id != release_id:
            raise ClassifierAPIError("classifier release identity mismatch")
        return release

    async def resolve_selection(self, selection_id: str) -> ResolvedClassifierSelection:
        if not is_classifier_release_id(selection_id):
            raise UnknownClassifierRelease(selection_id)
        try:
            selection = self._selections[selection_id]
        except KeyError as error:
            raise UnknownClassifierRelease(selection_id) from error
        if selection.selection_id != selection_id:
            raise ClassifierAPIError("classifier selection identity mismatch")
        return selection


def create_classifier_router(resolver: ClassifierReleaseResolver) -> APIRouter:
    """Build the versioned classifier API around an injected release resolver."""
    if resolver is None:
        raise ValueError("classifier release resolver is required")
    api_router = APIRouter()

    @api_router.get("/v5/capabilities", response_model=ClassifierCapabilitiesV5)
    async def capabilities(
        classifier_selection_id: str = Query(pattern=r"^[0-9a-f]{64}$"),
        classifier_release_id: str = Query(pattern=r"^[0-9a-f]{64}$"),
    ) -> ClassifierCapabilitiesV5:
        release = await _resolve_release(resolver, classifier_release_id)
        selection = await _resolve_selection(resolver, classifier_selection_id)
        _require_matching_selection(selection, release)
        return ClassifierCapabilitiesV5(
            schema_version=ClassifierAPIVersion.V5,
            api_major=release.api_major,
            classifier_selection_id=selection.selection_id,
            classifier_release_id=release.release_id,
            wire_schema=release.wire_schema,
            class_order=release.class_order,
            classifier_taxonomy_sha256=release.taxonomy_sha256,
            classifier_variants=tuple(
                sorted(selection.enabled_variants, key=lambda value: value.value)
            ),
            supports_preview=True,
        )

    @api_router.post("/v5/classify", response_model=ClassifierResponseV5)
    async def classify(
        request: ClassifierRequestV5,
        timeout_ms: str | None = Header(default=None, alias=CLASSIFIER_TIMEOUT_HEADER),
    ) -> ClassifierResponseV5 | JSONResponse:
        return await _classify(
            resolver,
            request,
            _router_budget_seconds(timeout_ms),
            session_id=request.session_id,
        )

    @api_router.post("/v5/preview", response_model=ClassifierResponseV5)
    async def preview(
        request: ClassifierRequestV5,
        timeout_ms: str | None = Header(default=None, alias=CLASSIFIER_TIMEOUT_HEADER),
    ) -> ClassifierResponseV5 | JSONResponse:
        # Previews are hypothetical, so they neither read nor overwrite the session's serving readout.
        return await _classify(
            resolver, request, _router_budget_seconds(timeout_ms), session_id=None
        )

    return api_router


def _router_budget_seconds(timeout_ms: str | None) -> float | None:
    """Parse the Router's remaining classify budget; absent means no caller bound."""
    if timeout_ms is None:
        return None
    if (
        not timeout_ms.isascii()
        or not timeout_ms.isdigit()
        or len(timeout_ms) > len(str(MAX_CLASSIFIER_TIMEOUT_MS))
    ):
        raise HTTPException(status_code=422, detail="classifier timeout is malformed")
    milliseconds = int(timeout_ms)
    if not 0 < milliseconds <= MAX_CLASSIFIER_TIMEOUT_MS:
        raise HTTPException(
            status_code=422, detail="classifier timeout is out of range"
        )
    return milliseconds / 1000


async def _classify(
    resolver: ClassifierReleaseResolver,
    request: ClassifierRequestV5,
    budget_seconds: float | None,
    *,
    session_id: str | None,
) -> ClassifierResponseV5 | JSONResponse:
    release = await _resolve_release(resolver, request.classifier_release_id)
    selection = await _resolve_selection(resolver, request.classifier_selection_id)
    _require_matching_selection(selection, release)
    try:
        variant = release.resolve_variant(request.classifier_variant_id)
    except UnknownClassifierVariant as error:
        raise HTTPException(
            status_code=404, detail="unknown classifier variant"
        ) from error
    if request.classifier_variant_id not in selection.enabled_variants:
        raise HTTPException(
            status_code=403, detail="classifier variant is disabled for selection"
        )
    try:
        async with asyncio.timeout(budget_seconds):
            facts = await variant.runtime.classify(request, session_id=session_id)
    except TimeoutError:
        return JSONResponse({"error": ClassifierErrorCode.UNAVAILABLE}, status_code=503)
    except ClassifierWarmingError:
        return JSONResponse(
            {"error": ClassifierErrorCode.WARMING},
            status_code=503,
            headers={"Retry-After": "2"},
        )
    except ClassifierVariantUnavailable:
        return JSONResponse({"error": ClassifierErrorCode.UNAVAILABLE}, status_code=503)
    except (ValidationError, ValueError) as error:
        raise HTTPException(
            status_code=422, detail="classifier request rejected"
        ) from error
    if facts.class_order != release.class_order:
        raise HTTPException(
            status_code=409, detail="classifier taxonomy does not match release"
        )
    return ClassifierResponseV5(
        schema_version=ClassifierAPIVersion.V5,
        request_id=request.request_id,
        classifier_selection_id=selection.selection_id,
        classifier_release_id=release.release_id,
        classifier_variant_id=variant.variant_id,
        classifier_artifact_id=variant.artifact_id,
        classifier_artifact_sha256=variant.package_sha256,
        classifier_taxonomy_sha256=release.taxonomy_sha256,
        **facts.model_dump(),
    )


async def _resolve_release(
    resolver: ClassifierReleaseResolver, release_id: str
) -> ResolvedClassifierRelease:
    try:
        release = await resolver.resolve(release_id)
    except UnknownClassifierRelease as error:
        raise HTTPException(
            status_code=404, detail="unknown classifier release"
        ) from error
    except ClassifierAPIError as error:
        raise HTTPException(
            status_code=409, detail="classifier release identity mismatch"
        ) from error
    if release.release_id != release_id or not is_classifier_release_id(
        release.release_id
    ):
        raise HTTPException(
            status_code=409, detail="classifier release identity mismatch"
        )
    if release.api_major != 5 or release.wire_schema is not ClassifierAPIVersion.V5:
        raise HTTPException(
            status_code=406, detail="unsupported classifier API release"
        )
    if ClassifierVariantID.PRIMARY not in release.variants:
        raise HTTPException(
            status_code=503, detail="classifier release has no primary variant"
        )
    try:
        ClassifierTaxonomy(
            class_order=release.class_order,
            sha256=release.taxonomy_sha256,
        )
    except ValidationError as error:
        raise HTTPException(
            status_code=409, detail="classifier taxonomy identity mismatch"
        ) from error
    for variant_id, variant in release.variants.items():
        if (
            variant.variant_id != variant_id
            or not variant.artifact_id
            or not is_classifier_release_id(variant.package_sha256)
        ):
            raise HTTPException(
                status_code=409, detail="classifier variant identity mismatch"
            )
    return release


async def _resolve_selection(
    resolver: ClassifierReleaseResolver, selection_id: str
) -> ResolvedClassifierSelection:
    try:
        selection = await resolver.resolve_selection(selection_id)
    except UnknownClassifierRelease as error:
        raise HTTPException(
            status_code=404, detail="unknown classifier selection"
        ) from error
    except ClassifierAPIError as error:
        raise HTTPException(
            status_code=409, detail="classifier selection identity mismatch"
        ) from error
    if selection.selection_id != selection_id or not is_classifier_release_id(
        selection.release_id
    ):
        raise HTTPException(
            status_code=409, detail="classifier selection identity mismatch"
        )
    return selection


def _require_matching_selection(
    selection: ResolvedClassifierSelection, release: ResolvedClassifierRelease
) -> None:
    if selection.release_id != release.release_id:
        raise HTTPException(
            status_code=409, detail="classifier selection does not pin this release"
        )
    if (
        not selection.enabled_variants
        or ClassifierVariantID.PRIMARY not in selection.enabled_variants
    ):
        raise HTTPException(
            status_code=409, detail="classifier selection has no primary variant"
        )
    if not selection.enabled_variants.issubset(release.variants):
        raise HTTPException(
            status_code=409, detail="classifier selection enables an unknown variant"
        )
