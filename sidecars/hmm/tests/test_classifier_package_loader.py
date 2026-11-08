from __future__ import annotations

import hashlib
import io
import json
import tarfile
from pathlib import Path

import pytest

from hmm_sidecar.classifier_package.package_loader import load_classifier_package


def _archive_bytes(members: dict[str, bytes]) -> bytes:
    output = io.BytesIO()
    with tarfile.open(fileobj=output, mode="w:gz") as archive:
        for name, payload in members.items():
            info = tarfile.TarInfo(name)
            info.size = len(payload)
            archive.addfile(info, io.BytesIO(payload))
    return output.getvalue()


def _package_archive() -> bytes:
    classifier_bytes = b'{"classes":["fast","maximum"]}'
    bundle_bytes = b'{"states":2}'
    manifest = {
        "schema_version": "hmm_router_classifier_package_v1",
        "runtime_contract": {
            "input_mode": "raw_v5_configured_serving_v1",
            "selection_owner": "go",
        },
        "classifier_files": {
            "model.json": hashlib.sha256(classifier_bytes).hexdigest()
        },
        "hmm_bundle_files": {"bundle.json": hashlib.sha256(bundle_bytes).hexdigest()},
    }
    return _archive_bytes(
        {
            "manifest.json": json.dumps(manifest).encode(),
            "classifier/model.json": classifier_bytes,
            "hmm_bundle/bundle.json": bundle_bytes,
        }
    )


def test_loader_resolves_exact_content_addressed_package(tmp_path: Path) -> None:
    archive = _package_archive()
    expected_sha256 = hashlib.sha256(archive).hexdigest()

    root, manifest = load_classifier_package(archive, expected_sha256, tmp_path)

    assert manifest["schema_version"] == "hmm_router_classifier_package_v1"
    assert (root / "classifier/model.json").is_file()
    assert (root / "hmm_bundle/bundle.json").is_file()


def test_loader_rejects_wrong_outer_digest(tmp_path: Path) -> None:
    with pytest.raises(ValueError, match="digest does not match"):
        load_classifier_package(_package_archive(), "0" * 64, tmp_path)


def test_loader_rejects_archive_path_traversal(tmp_path: Path) -> None:
    archive = _archive_bytes({"../escape": b"no"})

    with pytest.raises(ValueError, match="unsafe archive member path"):
        load_classifier_package(archive, hashlib.sha256(archive).hexdigest(), tmp_path)


def test_loader_rechecks_cached_files_before_returning_package(tmp_path: Path) -> None:
    archive = _package_archive()
    expected_sha256 = hashlib.sha256(archive).hexdigest()
    root, _ = load_classifier_package(archive, expected_sha256, tmp_path)
    (root / "classifier/model.json").write_bytes(b"tampered")

    repaired_root, _ = load_classifier_package(archive, expected_sha256, tmp_path)

    assert (
        repaired_root / "classifier/model.json"
    ).read_bytes() == b'{"classes":["fast","maximum"]}'


def test_loader_rejects_archives_that_expand_past_the_extracted_limit(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    archive = _package_archive()
    monkeypatch.setattr(
        "hmm_sidecar.classifier_package.package_loader.MAX_EXTRACTED_BYTES", 16
    )

    archive_sha256 = hashlib.sha256(archive).hexdigest()
    with pytest.raises(ValueError, match="maximum extracted size"):
        load_classifier_package(archive, archive_sha256, tmp_path)
    assert not (tmp_path / f"package-{archive_sha256}").exists()
    assert not list(tmp_path.glob(f"extract-{archive_sha256}-*"))
