"""Training privacy helpers for conversation datasets."""

from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path
from typing import Any


AI_TRAINING_OPT_OUT_JOIN = (
    "LEFT JOIN organization_data_privacy_settings dps\n"
    "  ON dps.organization_id = {alias}.organization_id"
)
AI_TRAINING_ALLOWED_PREDICATE = (
    "COALESCE(dps.ai_training_opt_out, false) = false"
)
TRAINING_PRIVACY_ATTESTATION_SCHEMA_VERSION = "ai_training_privacy_attestation_v1"
TRAINING_PRIVACY_SNAPSHOT_SCHEMA_VERSION = "ai_training_opt_out_snapshot_v1"
TRAINING_PRIVACY_POLICY = "exclude_ai_training_opt_out_organizations"
TRAINING_PRIVACY_OPT_OUT_SOURCE = "organization_data_privacy_settings"


@dataclass(frozen=True)
class TrainingPrivacySanitizeResult:
    """Summary of a CSV training-privacy sanitation pass."""

    input_path: str
    output_path: str
    total_rows: int
    kept_rows: int
    removed_rows: int
    opt_out_organization_ids: list[str]
    removed_by_organization_id: dict[str, int]

    def to_json_dict(self) -> dict[str, Any]:
        raise NotImplementedError("not on the classifier serving path")


def normalize_organization_id(value: object) -> str:
    raise NotImplementedError("not on the classifier serving path")


def _raise_csv_field_size_limit() -> None:
    raise NotImplementedError("not on the classifier serving path")


def opt_out_org_ids_sha256(opt_out_org_ids: set[str]) -> str:
    raise NotImplementedError("not on the classifier serving path")


def opt_out_snapshot_provenance_path(snapshot_path: Path) -> Path:
    raise NotImplementedError("not on the classifier serving path")


def write_opt_out_org_ids_snapshot(
    *,
    snapshot_path: Path,
    opt_out_org_ids: set[str],
    provenance_path: Path | None = None,
    source_dsn: str | None = None,
) -> dict[str, Any]:
    raise NotImplementedError("not on the classifier serving path")


def assert_opt_out_snapshot_provenance(
    *,
    snapshot_path: Path,
    opt_out_org_ids: set[str],
    provenance_path: Path | None = None,
) -> dict[str, Any]:
    raise NotImplementedError("not on the classifier serving path")


def training_privacy_attestation(
    *,
    input_path: Path,
    opt_out_org_ids: set[str],
    context: str,
    row_count: int | None = None,
    extra: dict[str, Any] | None = None,
) -> dict[str, Any]:
    raise NotImplementedError("not on the classifier serving path")


def assert_training_privacy_attestation(
    payload: Any,
    *,
    context: str,
) -> dict[str, Any]:
    """Fail unless an artifact carries the training privacy attestation."""
    if not isinstance(payload, dict):
        raise RuntimeError(
            f"{context} lacks required AI-training privacy attestation"
        )
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


def fetch_ai_training_opt_out_org_ids(conn: Any) -> set[str]:
    raise NotImplementedError("not on the classifier serving path")


def _read_opt_out_org_ids(path: Path, *, allow_empty: bool = False) -> set[str]:
    raise NotImplementedError("not on the classifier serving path")


def load_opt_out_org_ids(path: Path) -> set[str]:
    raise NotImplementedError("not on the classifier serving path")


def load_provenanced_opt_out_org_ids(
    path: Path,
    *,
    provenance_path: Path | None = None,
) -> set[str]:
    raise NotImplementedError("not on the classifier serving path")


def load_fresh_provenanced_opt_out_org_ids(path: Path) -> set[str]:
    raise NotImplementedError("not on the classifier serving path")


def load_default_opt_out_org_ids(path: Path) -> set[str] | None:
    raise NotImplementedError("not on the classifier serving path")


def sanitize_training_csv(
    *,
    input_path: Path,
    output_path: Path,
    opt_out_org_ids: set[str],
    audit_path: Path | None = None,
) -> TrainingPrivacySanitizeResult:
    raise NotImplementedError("not on the classifier serving path")


def assert_csv_training_allowed(
    *,
    input_path: Path,
    opt_out_org_ids: set[str],
    context: str,
    require_organization_id: bool = True,
    progress_description: str | None = None,
) -> None:
    raise NotImplementedError("not on the classifier serving path")


def assert_records_training_allowed(
    *,
    rows: list[dict[str, Any]],
    opt_out_org_ids: set[str],
    context: str,
    require_organization_id: bool = True,
) -> None:
    raise NotImplementedError("not on the classifier serving path")
