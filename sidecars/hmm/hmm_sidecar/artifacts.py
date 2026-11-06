from __future__ import annotations

import hashlib
import json
import os
import fcntl
import shutil
import tempfile
import urllib.parse
from dataclasses import dataclass
from pathlib import Path
from typing import Any

import httpx
import numpy as np

from .schemas import FrozenPackageManifest
from .classifier_package.manifest import (
    CLASSIFIER_PACKAGE_SCHEMA_VERSION,
    CONFIGURED_INPUT_MODE,
    is_classifier_package_manifest,
)
from .classifier_package.package_loader import (
    MAX_ARCHIVE_BYTES,
    MAX_ARCHIVE_MEMBERS,
    MAX_MEMBER_BYTES,
    extract_classifier_archive,
    verify_classifier_package,
)

# Upper bound on HMM_PACKAGE_REGISTRY entries (excluding the default package):
# every entry is downloaded, verified and loaded at boot, so the registry must
# stay small enough for startup to finish inside a readiness window.
MAX_PACKAGE_REGISTRY_ENTRIES = 8
DEFAULT_CACHE_DIR = Path("/tmp/workweave-hmm-artifacts")
# Weave registry model id of a published package: family + first 16 hex of the
# package sha256. Releases pin this as the classifier artifact id.
PACKAGE_MODEL_FAMILY = "hmm_router_sidecar_package"


@dataclass(frozen=True)
class FrozenArtifacts:
    root: Path
    manifest: FrozenPackageManifest | None
    package_sha256: str
    probe_vector: np.ndarray | None
    package_manifest: dict[str, Any] | None = None

    @property
    def model_id(self) -> str:
        if self.package_manifest is not None:
            return f"{PACKAGE_MODEL_FAMILY}_{self.package_sha256[:16]}"
        assert self.manifest is not None
        return self.manifest.model_id


@dataclass(frozen=True)
class PackageSource:
    """One pinned package location: an https URL or a local archive path."""

    sha256: str
    url: str = ""
    path: str = ""


@dataclass(frozen=True)
class ArtifactRegistry:
    """Every package the sidecar serves, all verified and materialized at boot.

    ``default`` is the HMM_PACKAGE_URL / HMM_PACKAGE_PATH package and is always
    present in ``by_sha256``. A request naming a sha absent from ``by_sha256`` is
    unservable; nothing is fetched on a live request.
    """

    default: FrozenArtifacts
    by_sha256: dict[str, FrozenArtifacts]

    def get(self, package_sha256: str | None) -> FrozenArtifacts | None:
        if package_sha256 is None or package_sha256 == "":
            return self.default
        return self.by_sha256.get(package_sha256.strip().lower())


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def _validate_sha256(value: str, label: str) -> str:
    value = value.strip().lower()
    if len(value) != 64 or any(char not in "0123456789abcdef" for char in value):
        raise ValueError(f"{label} must be 64 lowercase hex characters")
    return value


def _expected_sha256() -> str | None:
    value = os.environ.get("HMM_PACKAGE_SHA256", "").strip().lower()
    if not value:
        return None
    return _validate_sha256(value, "HMM_PACKAGE_SHA256")


def parse_package_registry(raw: str) -> list[PackageSource]:
    """Parse HMM_PACKAGE_REGISTRY: a JSON list of ``{"sha256": ..., "url": ...}``.

    A ``path`` may replace ``url`` for a pre-staged local archive. Entries are
    bounded, sha-unique, and each sha is validated before anything is fetched.
    """
    raw = raw.strip()
    if not raw:
        return []
    try:
        entries = json.loads(raw)
    except json.JSONDecodeError as exc:
        raise ValueError("HMM_PACKAGE_REGISTRY must be a JSON list") from exc
    if not isinstance(entries, list):
        raise ValueError("HMM_PACKAGE_REGISTRY must be a JSON list")
    if len(entries) > MAX_PACKAGE_REGISTRY_ENTRIES:
        raise ValueError(
            f"HMM_PACKAGE_REGISTRY has {len(entries)} entries; "
            f"at most {MAX_PACKAGE_REGISTRY_ENTRIES} are allowed"
        )
    sources: list[PackageSource] = []
    seen: set[str] = set()
    for index, entry in enumerate(entries):
        if not isinstance(entry, dict):
            raise ValueError(f"HMM_PACKAGE_REGISTRY[{index}] must be an object")
        sha256 = _validate_sha256(
            str(entry.get("sha256", "")), f"HMM_PACKAGE_REGISTRY[{index}].sha256"
        )
        url = str(entry.get("url", "")).strip()
        path = str(entry.get("path", "")).strip()
        if bool(url) == bool(path):
            raise ValueError(
                f"HMM_PACKAGE_REGISTRY[{index}] must set exactly one of url or path"
            )
        if url and urllib.parse.urlparse(url).scheme != "https":
            raise ValueError(f"HMM_PACKAGE_REGISTRY[{index}].url must use https")
        if sha256 in seen:
            raise ValueError(f"HMM_PACKAGE_REGISTRY repeats sha256 {sha256}")
        seen.add(sha256)
        sources.append(PackageSource(sha256=sha256, url=url, path=path))
    return sources


def _cache_dir() -> Path:
    return Path(os.environ.get("HMM_ARTIFACT_CACHE_DIR", str(DEFAULT_CACHE_DIR)))


def _download(url: str, destination: Path) -> None:
    parsed = urllib.parse.urlparse(url)
    if parsed.scheme != "https":
        raise ValueError("HMM_PACKAGE_URL must use https")
    destination.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.NamedTemporaryFile(dir=destination.parent, delete=False) as handle:
        temporary = Path(handle.name)
        total = 0
        try:
            with httpx.stream(
                "GET", url, follow_redirects=True, timeout=120.0
            ) as response:
                response.raise_for_status()
                if response.url.scheme != "https":
                    raise ValueError("HMM_PACKAGE_URL redirected to a non-HTTPS URL")
                for chunk in response.iter_bytes():
                    total += len(chunk)
                    if total > MAX_ARCHIVE_BYTES:
                        raise ValueError("HMM package exceeds maximum archive size")
                    handle.write(chunk)
            temporary.replace(destination)
        finally:
            temporary.unlink(missing_ok=True)


def _safe_extract(archive: Path, destination: Path) -> None:
    extract_classifier_archive(archive.read_bytes(), destination)


def _verify_files(root: Path, files: dict[str, str]) -> None:
    for relative, expected in files.items():
        path = root / relative
        if not path.is_file():
            raise ValueError(f"package file is missing: {relative}")
        actual = sha256_file(path)
        if actual != expected:
            raise ValueError(
                f"package file sha256 mismatch for {relative}: "
                f"expected {expected}, got {actual}"
            )


def _verify_manifest(root: Path) -> FrozenPackageManifest | dict[str, Any]:
    raw = json.loads((root / "manifest.json").read_text())
    if isinstance(raw, dict) and is_classifier_package_manifest(raw):
        return verify_classifier_package(root)
    manifest = FrozenPackageManifest.model_validate(raw)
    _verify_files(root, manifest.files)
    return manifest


def _materialize_archive(archive: Path, package_sha256: str, cache: Path) -> Path:
    root = cache / f"package-{package_sha256}"
    marker = root / ".complete"
    lock_path = cache / f"package-{package_sha256}.lock"
    with lock_path.open("w") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        if marker.is_file():
            return root
        temporary = cache / f"extract-{package_sha256}-{os.getpid()}"
        shutil.rmtree(temporary, ignore_errors=True)
        try:
            temporary.mkdir(parents=True)
            _safe_extract(archive, temporary)
            _verify_manifest(temporary)
            (temporary / ".complete").write_text("complete\n")
            shutil.rmtree(root, ignore_errors=True)
            temporary.replace(root)
        finally:
            shutil.rmtree(temporary, ignore_errors=True)
    return root


def resolve_artifacts() -> FrozenArtifacts:
    """Resolve the default package named by HMM_PACKAGE_PATH / HMM_PACKAGE_URL."""
    package_path_raw = os.environ.get("HMM_PACKAGE_PATH", "").strip()
    package_url = os.environ.get("HMM_PACKAGE_URL", "").strip()
    if bool(package_path_raw) == bool(package_url):
        raise ValueError("set exactly one of HMM_PACKAGE_PATH or HMM_PACKAGE_URL")
    expected = _expected_sha256()
    if package_url and expected is None:
        raise ValueError("HMM_PACKAGE_SHA256 is required with HMM_PACKAGE_URL")
    return _resolve_package(
        PackageSource(sha256=expected or "", url=package_url, path=package_path_raw),
        _cache_dir(),
    )


def resolve_artifact_registry() -> ArtifactRegistry:
    """Resolve the default package plus every HMM_PACKAGE_REGISTRY entry.

    All packages are downloaded (when remote), digest-checked, extracted and
    manifest-verified here, at boot. A failure in any entry fails the whole
    registry so readiness never reports a partially servable set.
    """
    default = resolve_artifacts()
    by_sha256 = {default.package_sha256: default}
    cache = _cache_dir()
    for source in parse_package_registry(os.environ.get("HMM_PACKAGE_REGISTRY", "")):
        if source.sha256 in by_sha256:
            continue
        by_sha256[source.sha256] = _resolve_package(source, cache)
    return ArtifactRegistry(default=default, by_sha256=by_sha256)


def _resolve_package(source: PackageSource, cache: Path) -> FrozenArtifacts:
    expected = source.sha256 or None
    cache.mkdir(parents=True, exist_ok=True)
    if source.url:
        if expected is None:
            raise ValueError("a package sha256 is required with a package url")
        archive = cache / f"download-{expected}.tar.gz"
        if not archive.is_file():
            _download(source.url, archive)
    else:
        archive = Path(source.path)
    if not archive.is_file():
        raise FileNotFoundError(f"HMM package not found: {archive}")
    actual = sha256_file(archive)
    if expected is not None and actual != expected:
        raise ValueError(
            f"HMM package sha256 mismatch: expected {expected}, got {actual}"
        )
    root = _materialize_archive(archive, actual, cache)
    manifest = _verify_manifest(root)
    if isinstance(manifest, dict):
        return FrozenArtifacts(root, None, actual, None, package_manifest=manifest)
    probe_path = root / manifest.embedding_contract.probe_vector_file
    probe = np.asarray(np.load(probe_path, allow_pickle=False), dtype=np.float64)
    if probe.shape != (manifest.embedding_contract.dimensions,):
        raise ValueError(
            f"embedding probe has shape {probe.shape}; expected "
            f"({manifest.embedding_contract.dimensions},)"
        )
    return FrozenArtifacts(root, manifest, actual, probe)
