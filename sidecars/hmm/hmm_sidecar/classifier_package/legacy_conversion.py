"""Verified, deterministic conversion of v8 roster packages to classifier-only v1."""

from __future__ import annotations

import gzip
import hashlib
import io
import json
import re
import tarfile
from dataclasses import dataclass
from enum import StrEnum
from pathlib import PurePosixPath
from typing import Any

from hmm_sidecar.classifier_package.core import canonical_json
from hmm_sidecar.classifier_package.manifest import (
    CLASSIFIER_PACKAGE_SCHEMA_VERSION,
    CONFIGURED_INPUT_MODE,
    ClassifierSelectionOwner,
)
from hmm_sidecar.classifier_package.package_loader import (
    MAX_ARCHIVE_BYTES,
    MAX_ARCHIVE_MEMBERS,
    MAX_MEMBER_BYTES,
    verify_classifier_package,
)
from hmm_sidecar.classifier_package.runtime_contract import parse_runtime_contract


class LegacyPackageSchemaVersion(StrEnum):
    """Legacy package schema accepted by the one-way converter."""

    V8 = "hmm_router_sidecar_package_v8"


class ConversionSchemaVersion(StrEnum):
    """Schema for the source-linkage evidence embedded in converted packages."""

    V1 = "hmm_router_classifier_package_conversion_v1"


class TrainingPrivacyAttestationSchemaVersion(StrEnum):
    V1 = "ai_training_privacy_attestation_v1"


class TrainingPrivacyPolicy(StrEnum):
    EXCLUDE_OPTOUT_ORGS = "exclude_ai_training_opt_out_organizations"


LEGACY_PACKAGE_SCHEMA_VERSION = LegacyPackageSchemaVersion.V8
CONVERSION_SCHEMA_VERSION = ConversionSchemaVersion.V1
REQUIRED_CLASSIFIER_ONLY_FILES = ("manifest.json", "classifier/", "hmm_bundle/")
MAX_ARCHIVE_UNCOMPRESSED_BYTES = 512 * 1024 * 1024
MAX_LEGACY_MANIFEST_BYTES = 4 * 1024 * 1024
_SHA256_PATTERN = re.compile(r"^[0-9a-f]{64}$")
_LEGACY_MANIFEST_FIELDS = frozenset(
    {
        "schema_version",
        "package_id",
        "classifier_dir",
        "hmm_bundle_dir",
        "classifier_files",
        "classifier_contract",
        "hmm_bundle_files",
        "required_files",
        "training_privacy",
        "runtime_contract",
        "arm_selector_dir",
        "arm_selector_files",
        "arm_selector_contract",
        "roster_path",
        "roster_sha256",
    }
)
_LEGACY_RUNTIME_FIELDS = frozenset(
    {
        "route_endpoint",
        "outcome_endpoint",
        "sidecar_api_schema_version",
        "input_mode",
        "raw_v5_runtime_contract",
        "raw_v5_serving_input",
        "raw_v5_serving_input_sha256",
        "policy_defaults",
    }
)
_RAW_RUNTIME_FIELDS = frozenset(
    {
        "contract_version",
        "input_source",
        "serving_config",
        "interaction_state",
        "sequence_representation",
        "embedding_provider",
        "embedding_model",
        "embedding_dim",
        "document_serializer_version",
        "document_serializer_sha256",
        "state_encoder_contract_sha256",
        "serving_input_sha256",
        "hmm_training_contract_version",
        "hmm_training_contract_sha256",
        "hmm_states",
        "pca_dim",
        "classes",
        "classifier_feature_layout_sha256",
        "classifier_feature_dim",
        "no_canonical_fallback",
        "reference_probes",
    }
)


@dataclass(frozen=True)
class ConvertedClassifierPackage:
    """Converted bytes and immutable source evidence for a private publisher."""

    archive_bytes: bytes
    archive_sha256: str
    manifest: dict[str, Any]
    source_archive_sha256: str
    source_manifest_sha256: str


def convert_legacy_classifier_package(
    archive_bytes: bytes, expected_sha256: str
) -> ConvertedClassifierPackage:
    """Convert one exact v8 artifact without loading models or publishing data.

    Classifier/HMM member bytes and their raw-V5 contract are preserved exactly.
    The roster and arm selector are verified against the source manifest, then
    omitted. Unknown members, unknown contract fields, missing privacy evidence,
    and unsupported raw-V5 configurations fail closed.
    """
    if len(archive_bytes) > MAX_ARCHIVE_BYTES:
        raise ValueError("legacy HMM package exceeds maximum archive size")
    if not _SHA256_PATTERN.fullmatch(expected_sha256):
        raise ValueError("expected_sha256 must be a lowercase SHA-256")
    source_archive_sha256 = hashlib.sha256(archive_bytes).hexdigest()
    if source_archive_sha256 != expected_sha256:
        raise ValueError("legacy HMM package digest does not match expected_sha256")

    source_files, source_directories = _read_archive(archive_bytes)
    raw_manifest = source_files.get("manifest.json")
    if raw_manifest is None or len(raw_manifest) > MAX_LEGACY_MANIFEST_BYTES:
        raise ValueError("legacy HMM package manifest is missing or too large")
    source_manifest_sha256 = hashlib.sha256(raw_manifest).hexdigest()
    manifest = _decode_json_object(raw_manifest, "legacy HMM package manifest")
    unknown_manifest_fields = set(manifest) - _LEGACY_MANIFEST_FIELDS
    if unknown_manifest_fields:
        raise ValueError(
            "legacy HMM manifest has unsupported fields: "
            + ", ".join(sorted(unknown_manifest_fields))
        )
    if manifest.get("schema_version") != LEGACY_PACKAGE_SCHEMA_VERSION.value:
        raise ValueError("legacy package schema must be hmm_router_sidecar_package_v8")
    if (
        not isinstance(manifest.get("package_id"), str)
        or not manifest["package_id"].strip()
    ):
        raise ValueError("legacy package_id must be a nonempty string")
    for path_field in ("classifier_dir", "hmm_bundle_dir", "roster_path"):
        if (
            not isinstance(manifest.get(path_field), str)
            or not manifest[path_field].strip()
        ):
            raise ValueError(f"legacy {path_field} must be a nonempty source path")
    required_files = manifest.get("required_files")
    if required_files != ["manifest.json", "classifier/", "hmm_bundle/", "roster.json"]:
        raise ValueError("legacy v8 package has an unsupported required_files contract")

    classifier_files = _validate_component_files(
        manifest.get("classifier_files"), "classifier_files"
    )
    hmm_bundle_files = _validate_component_files(
        manifest.get("hmm_bundle_files"), "hmm_bundle_files"
    )
    arm_selector_files = _validate_optional_component_files(
        manifest.get("arm_selector_files"), "arm_selector_files"
    )
    roster_sha256 = _digest(manifest.get("roster_sha256"), "roster_sha256")
    if (
        hashlib.sha256(source_files.get("roster.json", b"")).hexdigest()
        != roster_sha256
    ):
        raise ValueError("legacy roster bytes do not match roster_sha256")
    if not source_files.get("roster.json"):
        raise ValueError("legacy roster is empty")

    expected_source_files = {"manifest.json", "roster.json"}
    expected_source_files.update(f"classifier/{name}" for name in classifier_files)
    expected_source_files.update(f"hmm_bundle/{name}" for name in hmm_bundle_files)
    expected_source_files.update(f"arm_selector/{name}" for name in arm_selector_files)
    if set(source_files) != expected_source_files:
        unexpected = sorted(set(source_files) - expected_source_files)
        missing = sorted(expected_source_files - set(source_files))
        raise ValueError(
            f"legacy package members differ from its manifest; unexpected={unexpected}, missing={missing}"
        )
    for directory, component_files in (
        ("classifier", classifier_files),
        ("hmm_bundle", hmm_bundle_files),
        ("arm_selector", arm_selector_files),
    ):
        for relative_path, sha256 in component_files.items():
            member = f"{directory}/{relative_path}"
            if hashlib.sha256(source_files[member]).hexdigest() != sha256:
                raise ValueError(f"legacy {member} bytes do not match the manifest")
    allowed_directories = {"classifier", "hmm_bundle"}
    if arm_selector_files:
        allowed_directories.add("arm_selector")
    unsupported_directories = {
        directory
        for directory in source_directories
        if not any(
            directory == allowed or directory.startswith(f"{allowed}/")
            for allowed in allowed_directories
        )
    }
    if unsupported_directories:
        raise ValueError(
            "legacy package contains unsupported directories: "
            + ", ".join(sorted(unsupported_directories))
        )

    training_privacy = _validated_training_privacy(manifest.get("training_privacy"))
    classifier_contract = manifest.get("classifier_contract")
    if not isinstance(classifier_contract, dict):
        raise ValueError("legacy classifier_contract must be an object")
    runtime_contract = _classifier_only_runtime_contract(
        manifest.get("runtime_contract")
    )
    raw_runtime_contract = runtime_contract["raw_v5_runtime_contract"]
    classifier_classes = classifier_contract.get("classes")
    if not isinstance(classifier_classes, list) or not classifier_classes:
        raise ValueError("legacy classifier_contract must declare classes")
    if classifier_classes != list(raw_runtime_contract["classes"]):
        raise ValueError("classifier classes differ from the raw-V5 runtime contract")

    converted_manifest: dict[str, Any] = {
        "schema_version": CLASSIFIER_PACKAGE_SCHEMA_VERSION.value,
        "package_id": f"classifier-only-{source_archive_sha256[:16]}",
        "classifier_files": classifier_files,
        "classifier_contract": classifier_contract,
        "hmm_bundle_files": hmm_bundle_files,
        "required_files": list(REQUIRED_CLASSIFIER_ONLY_FILES),
        "training_privacy": training_privacy,
        "runtime_contract": runtime_contract,
        "conversion_provenance": {
            "schema_version": CONVERSION_SCHEMA_VERSION.value,
            "source_package_schema_version": LEGACY_PACKAGE_SCHEMA_VERSION.value,
            "source_archive_sha256": source_archive_sha256,
            "source_manifest_sha256": source_manifest_sha256,
        },
    }
    converted_members = {
        "manifest.json": canonical_json(converted_manifest).encode("utf-8")
    }
    converted_members.update(
        {
            f"classifier/{name}": source_files[f"classifier/{name}"]
            for name in classifier_files
        }
    )
    converted_members.update(
        {
            f"hmm_bundle/{name}": source_files[f"hmm_bundle/{name}"]
            for name in hmm_bundle_files
        }
    )
    converted_archive = _deterministic_archive(converted_members)
    if len(converted_archive) > MAX_ARCHIVE_BYTES:
        raise ValueError("converted classifier package exceeds maximum archive size")
    verified_manifest = _verify_converted_archive(converted_archive)
    if verified_manifest != converted_manifest:
        raise ValueError(
            "converted classifier package manifest changed during verification"
        )
    return ConvertedClassifierPackage(
        archive_bytes=converted_archive,
        archive_sha256=hashlib.sha256(converted_archive).hexdigest(),
        manifest=converted_manifest,
        source_archive_sha256=source_archive_sha256,
        source_manifest_sha256=source_manifest_sha256,
    )


def _read_archive(archive_bytes: bytes) -> tuple[dict[str, bytes], set[str]]:
    files: dict[str, bytes] = {}
    directories: set[str] = set()
    total_bytes = 0
    with tarfile.open(fileobj=io.BytesIO(archive_bytes), mode="r:*") as archive:
        members = archive.getmembers()
        if len(members) > MAX_ARCHIVE_MEMBERS:
            raise ValueError("legacy HMM package has too many archive members")
        for member in members:
            if not member.isfile() and not member.isdir():
                raise ValueError(f"unsupported legacy archive member: {member.name}")
            if member.size > MAX_MEMBER_BYTES:
                raise ValueError(f"legacy archive member is too large: {member.name}")
            path = PurePosixPath(member.name)
            if path.is_absolute() or ".." in path.parts:
                raise ValueError(f"unsafe legacy archive member path: {member.name}")
            relative_path = path.as_posix().rstrip("/")
            if not relative_path or relative_path in (".", "./"):
                continue
            if member.isdir():
                if relative_path in directories or relative_path in files:
                    raise ValueError(
                        f"duplicate legacy archive member: {relative_path}"
                    )
                directories.add(relative_path)
                continue
            if relative_path in files or relative_path in directories:
                raise ValueError(f"duplicate legacy archive member: {relative_path}")
            if member.size < 0:
                raise ValueError(
                    f"legacy archive member has a negative size: {member.name}"
                )
            total_bytes += member.size
            if total_bytes > MAX_ARCHIVE_UNCOMPRESSED_BYTES:
                raise ValueError("legacy HMM package exceeds maximum uncompressed size")
            handle = archive.extractfile(member)
            if handle is None:
                raise ValueError(f"legacy archive member cannot be read: {member.name}")
            payload = handle.read(member.size + 1)
            if len(payload) != member.size:
                raise ValueError(f"legacy archive member size mismatch: {member.name}")
            files[relative_path] = payload
    return files, directories


def _decode_json_object(payload: bytes, label: str) -> dict[str, Any]:
    def unique_pairs(pairs: list[tuple[str, Any]]) -> dict[str, Any]:
        parsed: dict[str, Any] = {}
        for key, value in pairs:
            if key in parsed:
                raise ValueError(f"{label} repeats JSON field {key!r}")
            parsed[key] = value
        return parsed

    try:
        value = json.loads(payload.decode("utf-8"), object_pairs_hook=unique_pairs)
    except (UnicodeDecodeError, json.JSONDecodeError) as error:
        raise ValueError(f"{label} is not valid UTF-8 JSON") from error
    if not isinstance(value, dict):
        raise ValueError(f"{label} must be a JSON object")
    return value


def _validate_component_files(value: object, field: str) -> dict[str, str]:
    if not isinstance(value, dict) or not value:
        raise ValueError(f"legacy {field} must be a nonempty file digest map")
    files: dict[str, str] = {}
    for relative_path, sha256 in value.items():
        if not isinstance(relative_path, str):
            raise ValueError(f"legacy {field} contains a non-string path")
        _validate_relative_file_path(relative_path, field)
        files[relative_path] = _digest(sha256, f"{field}.{relative_path}")
    return files


def _validate_optional_component_files(value: object, field: str) -> dict[str, str]:
    if value is None:
        return {}
    if not isinstance(value, dict):
        raise ValueError(f"legacy {field} must be an object when present")
    if not value:
        raise ValueError(f"legacy {field} cannot be empty when present")
    return _validate_component_files(value, field)


def _validate_relative_file_path(value: str, field: str) -> None:
    path = PurePosixPath(value)
    if (
        not value
        or path.is_absolute()
        or ".." in path.parts
        or "\\" in value
        or path.as_posix() != value
    ):
        raise ValueError(f"legacy {field} contains an unsafe path: {value!r}")


def _digest(value: object, label: str) -> str:
    if not isinstance(value, str) or _SHA256_PATTERN.fullmatch(value) is None:
        raise ValueError(f"{label} must be a lowercase SHA-256")
    return value


def _validated_training_privacy(value: object) -> dict[str, dict[str, Any]]:
    if not isinstance(value, dict):
        raise ValueError("legacy package lacks training-privacy attestations")
    attestations: dict[str, dict[str, Any]] = {}
    for artifact in ("classifier", "hmm_corpus"):
        attestation = value.get(artifact)
        if not isinstance(attestation, dict):
            raise ValueError(f"legacy package lacks {artifact} privacy attestation")
        if (
            attestation.get("schema_version")
            != TrainingPrivacyAttestationSchemaVersion.V1.value
            or attestation.get("policy")
            != TrainingPrivacyPolicy.EXCLUDE_OPTOUT_ORGS.value
        ):
            raise ValueError(f"legacy {artifact} privacy attestation is unsupported")
        _digest(
            attestation.get("opt_out_org_ids_sha256"),
            f"training_privacy.{artifact}.opt_out_org_ids_sha256",
        )
        attestations[artifact] = attestation
    return attestations


def _classifier_only_runtime_contract(value: object) -> dict[str, Any]:
    if not isinstance(value, dict):
        raise ValueError("legacy runtime_contract must be an object")
    unknown_fields = set(value) - _LEGACY_RUNTIME_FIELDS
    if unknown_fields:
        raise ValueError(
            "legacy runtime contract has unsupported fields: "
            + ", ".join(sorted(unknown_fields))
        )
    if value.get("input_mode") != "raw_v5_configured_serving_v1":
        raise ValueError("legacy runtime contract is not configured raw-V5 serving")
    raw_runtime = value.get("raw_v5_runtime_contract")
    if not isinstance(raw_runtime, dict):
        raise ValueError("legacy runtime contract lacks raw_v5_runtime_contract")
    unknown_raw_fields = set(raw_runtime) - _RAW_RUNTIME_FIELDS
    if unknown_raw_fields:
        raise ValueError(
            "legacy raw-V5 runtime contract has unsupported fields: "
            + ", ".join(sorted(unknown_raw_fields))
        )
    try:
        parsed_contract = parse_runtime_contract(raw_runtime)
    except (TypeError, ValueError) as error:
        raise ValueError("legacy raw-V5 serving contract is unsupported") from error
    if parsed_contract.classes != tuple(raw_runtime["classes"]):
        raise ValueError("legacy raw-V5 class taxonomy is invalid")
    if (
        raw_runtime.get("embedding_provider")
        != parsed_contract.serving_config.provider.value
    ):
        raise ValueError("legacy embedding provider differs from serving_config")
    serving_input = value.get("raw_v5_serving_input")
    if not isinstance(serving_input, dict):
        raise ValueError("legacy runtime contract lacks raw_v5_serving_input")
    serving_input_sha256 = _digest(
        value.get("raw_v5_serving_input_sha256"),
        "runtime_contract.raw_v5_serving_input_sha256",
    )
    if raw_runtime.get("serving_input_sha256") not in (None, serving_input_sha256):
        raise ValueError("legacy serving-input digest differs from raw-V5 contract")
    return {
        "route_endpoint": value.get("route_endpoint", "/route"),
        "outcome_endpoint": value.get("outcome_endpoint", "/outcome"),
        "sidecar_api_schema_version": value.get(
            "sidecar_api_schema_version", "policy_router_v4"
        ),
        "input_mode": CONFIGURED_INPUT_MODE.value,
        "raw_v5_runtime_contract": raw_runtime,
        "raw_v5_serving_input": serving_input,
        "raw_v5_serving_input_sha256": serving_input_sha256,
        "selection_owner": ClassifierSelectionOwner.GO.value,
    }


def _deterministic_archive(files: dict[str, bytes]) -> bytes:
    output = io.BytesIO()
    with gzip.GzipFile(fileobj=output, mode="wb", filename="", mtime=0) as gzip_file:
        with tarfile.open(
            fileobj=gzip_file, mode="w", format=tarfile.PAX_FORMAT
        ) as archive:
            for relative_path in sorted(files):
                payload = files[relative_path]
                info = tarfile.TarInfo(relative_path)
                info.size = len(payload)
                info.mode = 0o644
                info.uid = 0
                info.gid = 0
                info.mtime = 0
                info.uname = ""
                info.gname = ""
                archive.addfile(info, io.BytesIO(payload))
    return output.getvalue()


def _verify_converted_archive(archive_bytes: bytes) -> dict[str, Any]:
    import tempfile
    from pathlib import Path

    with tempfile.TemporaryDirectory(
        prefix="classifier-package-conversion-"
    ) as directory:
        package_root = Path(directory)
        with tarfile.open(fileobj=io.BytesIO(archive_bytes), mode="r:*") as archive:
            archive.extractall(package_root, filter="data")
        return verify_classifier_package(package_root)
