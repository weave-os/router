from __future__ import annotations

import logging
import os
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
) -> tuple[dict[str, FrozenPolicy], float]:
    """Build one policy per registry package, probing the embedding endpoint
    once per distinct embedding contract (packages sharing a contract share the
    embedder and its probe). Returns the policies and the default's similarity.
    """
    policies: dict[str, FrozenPolicy] = {}
    probed: dict[tuple[object, ...], tuple[Embedder, float]] = {}
    similarity = None
    for package_sha256, artifacts in registry.by_sha256.items():
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
    if similarity is None:
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


@app.get("/health")
@app.get("/readyz")
def ready() -> JSONResponse:
    artifacts = _artifacts()
    loaded = getattr(app.state, "policy", None) is not None
    body = {
        "ready": loaded,
        "status": "healthy" if loaded else "unhealthy",
        "runtime_state": "frozen_policy" if loaded else "unservable",
        "service": "hmm-sidecar",
        "version": VERSION,
        "schema_version": SCHEMA_VERSION,
        "policy_artifact_id": artifacts.manifest.model_id if artifacts else None,
        "policy_artifact_sha256": artifacts.package_sha256 if artifacts else None,
        "policy_artifact_sha256s": _loaded_artifact_sha256s(),
        "embedding_probe_similarity": getattr(
            app.state, "embedding_probe_similarity", None
        ),
        "error": getattr(app.state, "startup_error", None),
    }
    return JSONResponse(body, status_code=200 if loaded else 503)


@app.get("/capabilities")
def capabilities() -> JSONResponse:
    return JSONResponse(
        {
            "schema_version": SCHEMA_VERSION,
            "reports_outcomes": False,
            "reports_feedback": False,
            "honors_preferred_models": False,
            "honors_quality_price_bias": False,
            "supports_debug_route_detail": True,
            "supports_preview": True,
            "supports_shadow": True,
            "reports_ranked_fallback": True,
            "authoritative_per_turn_selection": False,
            "learning": {
                "enabled": False,
                "state": "frozen_policy",
                "reason": "self-hosted sidecar serves immutable artifacts",
            },
        }
    )


@app.get("/roster")
def roster(artifact_sha256: str | None = None) -> JSONResponse:
    """Return the frozen roster: flat arm union + per-cluster ordered arm lists."""
    if artifact_sha256 in (None, "") and getattr(app.state, "policy", None) is None:
        return JSONResponse({"error": "policy unavailable"}, status_code=503)
    policy = _policy_for(artifact_sha256)
    if isinstance(policy, JSONResponse):
        return policy
    clusters: dict[str, list[str]] = {
        label: [str(arm) for arm in cluster.get("arms") or []]
        for label, cluster in policy.clusters.items()
    }
    return JSONResponse(
        {
            "schema_version": SCHEMA_VERSION,
            "roster_version": policy.roster_version,
            "roster_ids": policy.roster_ids(),
            "clusters": clusters,
        }
    )


@app.post("/outcome", status_code=204)
def outcome() -> None:
    return None


@app.post("/feedback", status_code=204)
def feedback() -> None:
    return None


@app.post("/route")
async def route(payload: dict[str, Any]) -> JSONResponse:
    policy = _policy_for(payload.get(ARTIFACT_SHA256_FIELD))
    if isinstance(policy, JSONResponse):
        return policy
    if payload.get("schema_version") not in (None, SCHEMA_VERSION):
        return JSONResponse({"error": "unsupported policy schema"}, status_code=400)
    try:
        result, timings = await policy.route_with_timings(payload)
    except (ValueError, EmbeddingError) as exc:
        log.warning("HMM route request rejected: %s", exc)
        return JSONResponse({"error": "route request rejected"}, status_code=422)
    except Exception as exc:
        log.exception("HMM route failed")
        return JSONResponse(
            {"error": f"route failed: {type(exc).__name__}"}, status_code=503
        )
    # One line per decision, durations and counts only. The routing decision is
    # serialized ahead of the upstream request, so this is the only place the
    # per-stage cost of that wait is observable; the response contract carries
    # no timing fields and the caller sees a single opaque total.
    log.info("hmm route timing %s", timings.log_fields())
    return JSONResponse(
        {
            "schema_version": SCHEMA_VERSION,
            **result.model_dump(mode="json"),
        }
    )


@app.post("/preview")
async def preview(payload: dict[str, Any]) -> JSONResponse:
    policy = _policy_for(payload.get(ARTIFACT_SHA256_FIELD))
    if isinstance(policy, JSONResponse):
        return policy
    if payload.get("schema_version") not in (None, SCHEMA_VERSION):
        return JSONResponse({"error": "unsupported policy schema"}, status_code=400)
    if payload.get("execution_mode") != "preview":
        return JSONResponse(
            {"error": "preview execution mode required"}, status_code=400
        )
    try:
        result = await policy.preview(payload)
    except (ValueError, EmbeddingError) as exc:
        log.warning("HMM preview request rejected: %s", exc)
        return JSONResponse({"error": "preview request rejected"}, status_code=422)
    except Exception as exc:
        log.exception("HMM preview failed")
        return JSONResponse(
            {"error": f"preview failed: {type(exc).__name__}"}, status_code=503
        )
    return JSONResponse(
        {
            "schema_version": SCHEMA_VERSION,
            **result.model_dump(mode="json"),
        }
    )
