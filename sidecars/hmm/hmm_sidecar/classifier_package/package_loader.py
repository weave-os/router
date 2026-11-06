"""Digest-checked, bounded loader for portable classifier package archives."""

from __future__ import annotations

import fcntl
import hashlib
import io
import json
import re
import shutil
import tarfile
import tempfile
from pathlib import Path, PurePosixPath
from typing import Any

from hmm_sidecar.classifier_package.manifest import is_classifier_package_manifest

MAX_ARCHIVE_BYTES = 128 * 1024 * 1024
MAX_ARCHIVE_MEMBERS = 128
MAX_MEMBER_BYTES = 64 * 1024 * 1024
_SHA256_PATTERN = re.compile(r"^[0-9a-f]{64}$")


def load_classifier_package(
    archive_bytes: bytes, expected_sha256: str, cache_dir: Path
) -> tuple[Path, dict[str, Any]]:
    """Verify and materialize one exact package without registry or cloud access."""
    if len(archive_bytes) > MAX_ARCHIVE_BYTES:
        raise ValueError("classifier package exceeds maximum archive size")
    if not _SHA256_PATTERN.fullmatch(expected_sha256):
        raise ValueError("expected_sha256 must be a lowercase SHA-256")
    archive_sha256 = hashlib.sha256(archive_bytes).hexdigest()
    if archive_sha256 != expected_sha256:
        raise ValueError("classifier package digest does not match expected_sha256")

    cache_dir.mkdir(parents=True, exist_ok=True)
    package_root = cache_dir / f"package-{archive_sha256}"
    lock_path = cache_dir / f"package-{archive_sha256}.lock"
    with lock_path.open("w") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        marker = package_root / ".complete"
        if marker.is_file():
            try:
                return package_root, verify_classifier_package(package_root)
            except (OSError, ValueError, json.JSONDecodeError):
                shutil.rmtree(package_root)

        temporary_root = Path(
            tempfile.mkdtemp(prefix=f"extract-{archive_sha256}-", dir=cache_dir)
        )
        try:
            extract_classifier_archive(archive_bytes, temporary_root)
            manifest = verify_classifier_package(temporary_root)
            (temporary_root / ".complete").write_text("complete\n")
            shutil.rmtree(package_root, ignore_errors=True)
            temporary_root.replace(package_root)
            return package_root, manifest
        finally:
            shutil.rmtree(temporary_root, ignore_errors=True)


def extract_classifier_archive(archive_bytes: bytes, destination: Path) -> None:
    """Extract only bounded regular files and directories below destination."""
    if len(archive_bytes) > MAX_ARCHIVE_BYTES:
        raise ValueError("classifier package exceeds maximum archive size")
    destination.mkdir(parents=True, exist_ok=True)
    root = destination.resolve()
    with tarfile.open(fileobj=io.BytesIO(archive_bytes), mode="r:*") as archive:
        members = archive.getmembers()
        if len(members) > MAX_ARCHIVE_MEMBERS:
            raise ValueError("classifier package has too many archive members")
        for member in members:
            if not member.isfile() and not member.isdir():
                raise ValueError(f"unsupported archive member: {member.name}")
            if member.size > MAX_MEMBER_BYTES:
                raise ValueError(f"archive member is too large: {member.name}")
            path = PurePosixPath(member.name)
            if path.is_absolute() or ".." in path.parts:
                raise ValueError(f"unsafe archive member path: {member.name}")
            target = (destination / Path(*path.parts)).resolve()
            if root not in (target, *target.parents):
                raise ValueError(f"unsafe archive member path: {member.name}")
        archive.extractall(destination, members=members, filter="data")


def verify_classifier_package(root: Path) -> dict[str, Any]:
    """Verify package schema and every classifier/HMM file digest."""
    root_path = root.resolve()
    manifest_candidate = root / "manifest.json"
    manifest_path = manifest_candidate.resolve()
    if (
        root_path not in manifest_path.parents
        or manifest_candidate.is_symlink()
        or not manifest_path.is_file()
    ):
        raise ValueError("classifier package manifest is missing or unsafe")
    raw_manifest = json.loads(manifest_path.read_text())
    if not isinstance(raw_manifest, dict) or not is_classifier_package_manifest(
        raw_manifest
    ):
        raise ValueError("unsupported classifier package manifest")
    files = _package_files(raw_manifest)
    for relative_path, expected_sha256 in files.items():
        path_component = PurePosixPath(relative_path)
        if path_component.is_absolute() or ".." in path_component.parts:
            raise ValueError(f"unsafe classifier manifest path: {relative_path}")
        if not _SHA256_PATTERN.fullmatch(expected_sha256):
            raise ValueError(f"invalid classifier manifest digest for {relative_path}")
        file_candidate = root / Path(*path_component.parts)
        file_path = file_candidate.resolve()
        if (
            root_path not in (file_path, *file_path.parents)
            or file_candidate.is_symlink()
            or not file_path.is_file()
        ):
            raise ValueError(f"classifier package file is missing: {relative_path}")
        if _file_sha256(file_path) != expected_sha256:
            raise ValueError(
                f"classifier package file digest mismatch: {relative_path}"
            )
    return raw_manifest


def _package_files(manifest: dict[str, Any]) -> dict[str, str]:
    files: dict[str, str] = {}
    for directory, field in (
        ("classifier", "classifier_files"),
        ("hmm_bundle", "hmm_bundle_files"),
    ):
        entries = manifest.get(field)
        if not isinstance(entries, dict) or not entries:
            raise ValueError(f"classifier package manifest lacks {field}")
        for relative_path, digest in entries.items():
            if not isinstance(relative_path, str) or not isinstance(digest, str):
                raise ValueError(
                    f"classifier package manifest {field} must map strings"
                )
            files[f"{directory}/{relative_path}"] = digest
    return files


def _file_sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()
