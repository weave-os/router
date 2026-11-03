from __future__ import annotations

import hashlib
import json
import logging
import os
import re
import sys
from contextlib import asynccontextmanager
from typing import Any

from fastapi import FastAPI
from fastapi.responses import JSONResponse

from . import SCHEMA_VERSION
from .artifacts import ArtifactRegistry, FrozenArtifacts, resolve_artifact_registry
from .embeddings import (
    Embedder,
    EmbeddingError,
    build_embedder,
    verify_embedding_contract,
)
from .policy import FrozenPolicy
from .package_policy import PackagePolicy
from .schemas import SELECTION_AUTHORITY_FIELDS

log = logging.getLogger(__name__)
VERSION = os.environ.get("VERSION", "dev")
LOG_LEVEL = os.environ.get("HMM_LOG_LEVEL", "INFO").upper()


def configure_logging() -> None:
    """Make this package's INFO records reach stdout.

    uvicorn configures only its own ``uvicorn*`` loggers and the root logger has
    no handler, so anything below WARNING emitted here falls through to
    ``logging.lastResort`` and is silently dropped — which is why the per-stage
    route timings were invisible until this ran.

    Deliberately not ``basicConfig``: a handler is attached only when nothing
    else has configured logging. If a host already owns the root logger, raising
    this logger's level is enough for records to propagate into that setup, and
    adding our own handler would duplicate every line.
    """
    package_log = logging.getLogger(__package__ or "hmm_sidecar")
    package_log.setLevel(getattr(logging, LOG_LEVEL, logging.INFO))
    if logging.getLogger().handlers or package_log.handlers:
        return
    handler = logging.StreamHandler(sys.stdout)
    handler.setFormatter(logging.Formatter("%(levelname)s: %(name)s %(message)s"))
    package_log.addHandler(handler)


def _probe_key(artifacts: FrozenArtifacts) -> tuple[object, ...]:
    contract = artifacts.manifest.embedding_contract
    return (
        contract.model,
        contract.dimensions,
        contract.task_type,
        contract.probe_text,
        contract.minimum_cosine_similarity,
        artifacts.probe_vector.tobytes(),
    )


async def load_policies(
    registry: ArtifactRegistry,
) -> tuple[dict[str, FrozenPolicy | PackagePolicy], float | None]:
    """Build one policy per registry package, probing the embedding endpoint
    once per distinct embedding contract (packages sharing a contract share the
    embedder and its probe). Returns the policies and the default's similarity.
    """
    policies: dict[str, FrozenPolicy | PackagePolicy] = {}
    probed: dict[tuple[object, ...], tuple[Embedder, float]] = {}
    similarity = None
    for package_sha256, artifacts in registry.by_sha256.items():
        if artifacts.package_manifest is not None:
            policies[package_sha256] = PackagePolicy(artifacts)
            continue
        key = _probe_key(artifacts)
        if key not in probed:
            embedder = build_embedder(artifacts.manifest.embedding_contract)
            probe_similarity = await verify_embedding_contract(
                embedder,
                artifacts.manifest.embedding_contract,
                artifacts.probe_vector,
            )
            probed[key] = (embedder, probe_similarity)
        embedder, probe_similarity = probed[key]
        policies[package_sha256] = FrozenPolicy(artifacts, embedder)
        if artifacts is registry.default:
            similarity = probe_similarity
    if registry.default.package_sha256 not in policies:
        raise RuntimeError("artifact registry default is not among its packages")
    log.info(
        "HMM sidecar probed %d embedding contract(s) for %d package(s)",
        len(probed),
        len(policies),
    )
    return policies, similarity


@asynccontextmanager
async def lifespan(application: FastAPI):
    configure_logging()
    try:
        registry = resolve_artifact_registry()
        policies, similarity = await load_policies(registry)
        application.state.artifacts = registry.default
        application.state.registry = registry
        application.state.policy = policies[registry.default.package_sha256]
        application.state.policies = policies
        application.state.embedding_probe_similarity = similarity
        application.state.startup_error = None
        log.info(
            "HMM sidecar loaded %d frozen package(s); default %s",
            len(policies),
            registry.default.package_sha256,
        )
    except Exception as exc:  # fail readiness while preserving liveness diagnostics
        log.exception("HMM sidecar failed to initialize")
        application.state.artifacts = None
        application.state.registry = None
        application.state.policy = None
        application.state.policies = {}
        application.state.embedding_probe_similarity = None
        application.state.startup_error = f"{type(exc).__name__}: {exc}"
    yield


app = FastAPI(title="WorkWeave frozen HMM sidecar", lifespan=lifespan)


def _artifacts() -> FrozenArtifacts | None:
    return getattr(app.state, "artifacts", None)


ARTIFACT_SHA256_FIELD = "artifact_sha256"


def _policy_for(artifact_sha256: object) -> FrozenPolicy | JSONResponse:
    """Select the boot-loaded policy for ``artifact_sha256`` (default when absent).

    Unknown shas are a 404: the registry is frozen at boot and nothing is
    fetched on a live request.
    """
    default: FrozenPolicy | None = getattr(app.state, "policy", None)
    if artifact_sha256 in (None, ""):
        if default is None:
            return JSONResponse({"error": "policy not loaded"}, status_code=503)
        return default
    if not isinstance(artifact_sha256, str):
        return JSONResponse(
            {"error": f"{ARTIFACT_SHA256_FIELD} must be a string"}, status_code=400
        )
    wanted = artifact_sha256.strip().lower()
    policies: dict[str, FrozenPolicy] = getattr(app.state, "policies", {}) or {}
    policy = policies.get(wanted)
    if policy is None:
        return JSONResponse(
            {
                "error": f"unknown {ARTIFACT_SHA256_FIELD}",
                ARTIFACT_SHA256_FIELD: wanted,
            },
            status_code=404,
        )
    return policy


def _loaded_artifact_sha256s() -> list[str]:
    registry: ArtifactRegistry | None = getattr(app.state, "registry", None)
    if registry is None:
        return []
    return sorted(registry.by_sha256)


@app.get("/livez")
def livez() -> JSONResponse:
    return JSONResponse({"status": "ok", "service": "hmm-sidecar", "version": VERSION})


IMAGE_DIGEST_PATTERN = re.compile(r"sha256:[0-9a-f]{64}")


def _classifier_image_digest() -> str | None:
    """Digest of the image serving this package; releases pin it, so it is required."""
    value = os.environ.get("CLASSIFIER_IMAGE_DIGEST", "").strip()
    return value if IMAGE_DIGEST_PATTERN.fullmatch(value) else None


def taxonomy_sha256(class_order: tuple[str, ...]) -> str:
    """Match the Go registry's digest of ``json.Marshal(class_order)``."""
    encoded = json.dumps(list(class_order), separators=(",", ":"), ensure_ascii=False)
    return hashlib.sha256(encoded.encode()).hexdigest()


@app.get("/health")
@app.get("/readyz")
def ready() -> JSONResponse:
    artifacts = _artifacts()
    policy: FrozenPolicy | None = getattr(app.state, "policy", None)
    image_digest = _classifier_image_digest()
    error = getattr(app.state, "startup_error", None)
    if policy is not None and image_digest is None:
        error = "CLASSIFIER_IMAGE_DIGEST must be set to sha256:<64 hex>"
    ready_to_serve = policy is not None and image_digest is not None
    class_order = tuple(policy.classifier.classes) if policy is not None else ()
    body = {
        "ready": ready_to_serve,
        "status": "healthy" if ready_to_serve else "unhealthy",
        "runtime_state": "frozen_policy" if ready_to_serve else "unservable",
        "service": "hmm-sidecar",
        "version": VERSION,
        "schema_version": SCHEMA_VERSION,
        "classifier_artifact_id": artifacts.model_id if artifacts else None,
        "classifier_artifact_sha256": artifacts.package_sha256 if artifacts else None,
        "classifier_image_digest": image_digest,
        "classifier_class_order": list(class_order),
        "classifier_taxonomy_sha256": (
            taxonomy_sha256(class_order) if class_order else None
        ),
        "classifier_artifact_sha256s": _loaded_artifact_sha256s(),
        "embedding_probe_similarity": getattr(
            app.state, "embedding_probe_similarity", None
        ),
        "error": error,
    }
    return JSONResponse(body, status_code=200 if ready_to_serve else 503)


@app.get("/capabilities")
def capabilities() -> JSONResponse:
    return JSONResponse(
        {
            "schema_version": SCHEMA_VERSION,
            "supported_schema_versions": [SCHEMA_VERSION],
            "reports_outcomes": False,
            "reports_feedback": False,
            "honors_preferred_models": False,
            "honors_quality_price_bias": False,
            "supports_debug_route_detail": True,
            "supports_preview": True,
            "supports_shadow": True,
            "learning": {
                "enabled": False,
                "state": "frozen_policy",
                "reason": "self-hosted sidecar serves immutable artifacts",
            },
        }
    )


@app.post("/outcome", status_code=204)
def outcome() -> None:
    return None


@app.post("/feedback", status_code=204)
def feedback() -> None:
    return None


def _reject_request(payload: dict[str, Any]) -> JSONResponse | None:
    if payload.get("schema_version") != SCHEMA_VERSION:
        return JSONResponse({"error": "unsupported policy schema"}, status_code=400)
    forbidden = sorted(SELECTION_AUTHORITY_FIELDS.intersection(payload))
    if forbidden:
        return JSONResponse(
            {"error": "selection fields are not accepted", "fields": forbidden},
            status_code=400,
        )
    return None


async def _classify(payload: dict[str, Any], operation: str) -> JSONResponse:
    policy = _policy_for(payload.get(ARTIFACT_SHA256_FIELD))
    if isinstance(policy, JSONResponse):
        return policy
    rejected = _reject_request(payload)
    if rejected is not None:
        return rejected
    try:
        result, timings = await policy.classify_with_timings(payload)
    except (ValueError, EmbeddingError) as exc:
        log.warning("HMM %s request rejected: %s", operation, exc)
        return JSONResponse({"error": f"{operation} request rejected"}, status_code=422)
    except Exception as exc:
        log.exception("HMM %s failed", operation)
        return JSONResponse(
            {"error": f"{operation} failed: {type(exc).__name__}"}, status_code=503
        )
    # One line per decision, durations and counts only: the classification is
    # serialized ahead of the upstream request and the body carries no timings.
    log.info("hmm %s timing %s", operation, timings.log_fields())
    return JSONResponse(result.model_dump(mode="json", exclude_none=True))


@app.post("/route")
async def route(payload: dict[str, Any]) -> JSONResponse:
    return await _classify(payload, "route")


@app.post("/preview")
async def preview(payload: dict[str, Any]) -> JSONResponse:
    if payload.get("execution_mode") != "preview":
        return JSONResponse(
            {"error": "preview execution mode required"}, status_code=400
        )
    return await _classify(payload, "preview")
