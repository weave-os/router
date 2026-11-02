"""Training privacy helpers for conversation datasets."""

from __future__ import annotations

from typing import Any

TRAINING_PRIVACY_ATTESTATION_SCHEMA_VERSION = "ai_training_privacy_attestation_v1"
TRAINING_PRIVACY_POLICY = "exclude_ai_training_opt_out_organizations"


def opt_out_org_ids_sha256(opt_out_org_ids: set[str]) -> str:
    raise NotImplementedError("not on the classifier serving path")


def assert_training_privacy_attestation(
    payload: Any,
    *,
    context: str,
) -> dict[str, Any]:
    """Fail unless an artifact carries the training privacy attestation."""
    if not isinstance(payload, dict):
        raise RuntimeError(f"{context} lacks required AI-training privacy attestation")
    if payload.get("schema_version") != TRAINING_PRIVACY_ATTESTATION_SCHEMA_VERSION:
        raise RuntimeError(
            f"{context} has invalid AI-training privacy attestation schema: "
            f"{payload.get('schema_version')!r}"
        )
    if payload.get("policy") != TRAINING_PRIVACY_POLICY:
        raise RuntimeError(
            f"{context} has invalid AI-training privacy policy: "
            f"{payload.get('policy')!r}"
        )
    digest = payload.get("opt_out_org_ids_sha256")
    if not isinstance(digest, str) or len(digest) != 64:
        raise RuntimeError(
            f"{context} AI-training privacy attestation lacks opt-out digest"
        )
    return payload
