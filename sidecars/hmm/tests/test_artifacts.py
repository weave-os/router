from __future__ import annotations

import io
import json
import tarfile
from pathlib import Path

import numpy as np
import pytest

import hmm_sidecar.artifacts as artifact_module
from hmm_sidecar.artifacts import _safe_extract, resolve_artifacts
from scripts.export_artifact import (
    deterministic_tar,
    public_classifier_metadata,
    public_roster,
)


def test_public_classifier_metadata_is_a_strict_runtime_allowlist() -> None:
    metadata = public_classifier_metadata(
        {
            "classes": ["fast", "maximum"],
            "feature_dim": 3431,
            "private_provenance": {"fingerprint": "not-public"},
            "private_training_metadata": {"value": "not-public"},
        }
    )

    assert metadata == {"classes": ["fast", "maximum"], "feature_dim": 3431}


def test_public_roster_keeps_only_runtime_model_arms() -> None:
    roster = public_roster(
        {
            "private_routing_metadata": "not-public",
            "clusters": {
                "fast": {
                    "arms": ["provider/fast"],
                    "private_cluster_metadata": "not-public",
                },
                "maximum": {
                    "arms": ["provider/maximum"],
                    "private_cluster_metadata": "not-public",
                },
            },
        },
        ["fast", "maximum"],
    )

    assert roster == {
        "schema_version": "hmm_router_public_roster_v1",
        "clusters": {
            "fast": {"arms": ["provider/fast"]},
            "maximum": {"arms": ["provider/maximum"]},
        },
    }


def test_public_archive_is_byte_for_byte_reproducible(tmp_path: Path) -> None:
    source = tmp_path / "source"
    source.mkdir()
    (source / "model.json").write_text('{"weights": [1, 2, 3]}\n')
    first = tmp_path / "first.tar.gz"
    second = tmp_path / "second.tar.gz"

    deterministic_tar(source, first)
    deterministic_tar(source, second)

    assert first.read_bytes() == second.read_bytes()


def test_rejects_archive_path_traversal(tmp_path: Path) -> None:
    archive = tmp_path / "malicious.tar.gz"
    with tarfile.open(archive, "w:gz") as payload:
        info = tarfile.TarInfo("../escape.txt")
        content = b"escape"
        info.size = len(content)
        payload.addfile(info, io.BytesIO(content))

    with pytest.raises(ValueError, match="unsafe archive member path"):
        _safe_extract(archive, tmp_path / "output")

    assert not (tmp_path / "escape.txt").exists()


def test_rejects_outer_package_digest_mismatch(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    archive = tmp_path / "package.tar.gz"
    archive.write_bytes(b"not-the-pinned-package")
    monkeypatch.setenv("HMM_PACKAGE_PATH", str(archive))
    monkeypatch.delenv("HMM_PACKAGE_URL", raising=False)
    monkeypatch.setenv("HMM_PACKAGE_SHA256", "0" * 64)
    monkeypatch.setenv("HMM_ARTIFACT_CACHE_DIR", str(tmp_path / "cache"))

    with pytest.raises(ValueError, match="package sha256 mismatch"):
        resolve_artifacts()


def test_url_requires_a_pinned_digest(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.delenv("HMM_PACKAGE_PATH", raising=False)
    monkeypatch.setenv("HMM_PACKAGE_URL", "https://example.test/model.tar.gz")
    monkeypatch.delenv("HMM_PACKAGE_SHA256", raising=False)

    with pytest.raises(ValueError, match="required with HMM_PACKAGE_URL"):
        resolve_artifacts()


def test_materialize_archive_replaces_an_incomplete_cache_directory(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    archive = tmp_path / "package.tar.gz"
    archive.write_bytes(b"fixture")
    package_sha256 = "a" * 64
    stale = tmp_path / f"package-{package_sha256}"
    stale.mkdir()
    (stale / "broken").write_text("incomplete")

    def fake_extract(source: Path, destination: Path) -> None:
        assert source == archive
        (destination / "payload").write_text("verified")

    monkeypatch.setattr(artifact_module, "_safe_extract", fake_extract)
    monkeypatch.setattr(artifact_module, "_verify_manifest", lambda root: None)

    root = artifact_module._materialize_archive(archive, package_sha256, tmp_path)

    assert root.name == f"package-{package_sha256}"
    assert (root / ".complete").is_file()
    assert (root / "payload").read_text() == "verified"
    assert not (root / "broken").exists()


def test_package_registry_parses_bounded_https_entries() -> None:
    raw = (
        '[{"sha256": "' + "a" * 64 + '", "url": "https://example.test/a.tar.gz"},'
        ' {"sha256": "' + "B" * 64 + '", "path": "/srv/b.tar.gz"}]'
    )

    sources = artifact_module.parse_package_registry(raw)

    assert [source.sha256 for source in sources] == ["a" * 64, "b" * 64]
    assert sources[0].url == "https://example.test/a.tar.gz"
    assert sources[1].path == "/srv/b.tar.gz"
    assert artifact_module.parse_package_registry("") == []


@pytest.mark.parametrize(
    ("raw", "match"),
    [
        ("not json", "must be a JSON list"),
        ('{"sha256": "x"}', "must be a JSON list"),
        ('[{"sha256": "abc", "url": "https://x.test/a"}]', "64 lowercase hex"),
        (
            '[{"sha256": "' + "a" * 64 + '", "url": "http://x.test/a"}]',
            "must use https",
        ),
        ('[{"sha256": "' + "a" * 64 + '"}]', "exactly one of url or path"),
        (
            '[{"sha256": "' + "a" * 64 + '", "url": "https://x.test/a"},'
            ' {"sha256": "' + "a" * 64 + '", "url": "https://x.test/b"}]',
            "repeats sha256",
        ),
    ],
)
def test_package_registry_rejects_malformed_entries(raw: str, match: str) -> None:
    with pytest.raises(ValueError, match=match):
        artifact_module.parse_package_registry(raw)


def test_package_registry_is_bounded() -> None:
    entries = [
        {"sha256": format(index, "064x"), "url": f"https://x.test/{index}.tar.gz"}
        for index in range(artifact_module.MAX_PACKAGE_REGISTRY_ENTRIES + 1)
    ]

    with pytest.raises(ValueError, match="at most"):
        artifact_module.parse_package_registry(json.dumps(entries))


def test_registry_verifies_and_loads_every_package_at_boot(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    default_sha = "d" * 64
    pinned_sha = "e" * 64
    monkeypatch.setenv("HMM_PACKAGE_PATH", str(tmp_path / "default.tar.gz"))
    monkeypatch.delenv("HMM_PACKAGE_URL", raising=False)
    monkeypatch.setenv("HMM_PACKAGE_SHA256", default_sha)
    monkeypatch.setenv("HMM_ARTIFACT_CACHE_DIR", str(tmp_path / "cache"))
    monkeypatch.setenv(
        "HMM_PACKAGE_REGISTRY",
        json.dumps(
            [
                {"sha256": default_sha, "url": "https://x.test/default.tar.gz"},
                {"sha256": pinned_sha, "url": "https://x.test/pinned.tar.gz"},
            ]
        ),
    )
    resolved: list[artifact_module.PackageSource] = []

    def fake_resolve(source: artifact_module.PackageSource, cache: Path):
        resolved.append(source)
        return artifact_module.FrozenArtifacts(
            root=cache / source.sha256,
            manifest=None,  # type: ignore[arg-type]
            package_sha256=source.sha256,
            probe_vector=np.zeros(1),
        )

    monkeypatch.setattr(artifact_module, "_resolve_package", fake_resolve)

    registry = artifact_module.resolve_artifact_registry()

    # The default entry is resolved once (from HMM_PACKAGE_PATH) and the
    # registry duplicate of it is skipped; the pinned sha is resolved at boot.
    assert [source.sha256 for source in resolved] == [default_sha, pinned_sha]
    assert resolved[0].path.endswith("default.tar.gz")
    assert resolved[1].url == "https://x.test/pinned.tar.gz"
    assert registry.default.package_sha256 == default_sha
    assert set(registry.by_sha256) == {default_sha, pinned_sha}
    assert registry.get(None) is registry.default
    assert registry.get(pinned_sha.upper()).package_sha256 == pinned_sha
    assert registry.get("f" * 64) is None


def test_registry_entry_digest_mismatch_fails_boot(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    pinned_archive = tmp_path / "pinned.tar.gz"
    pinned_archive.write_bytes(b"not-the-pinned-package")
    monkeypatch.setenv("HMM_ARTIFACT_CACHE_DIR", str(tmp_path / "cache"))
    monkeypatch.setenv(
        "HMM_PACKAGE_REGISTRY",
        json.dumps([{"sha256": "0" * 64, "path": str(pinned_archive)}]),
    )
    default = artifact_module.FrozenArtifacts(
        root=tmp_path,
        manifest=None,  # type: ignore[arg-type]
        package_sha256="d" * 64,
        probe_vector=np.zeros(1),
    )
    monkeypatch.setattr(artifact_module, "resolve_artifacts", lambda: default)

    with pytest.raises(ValueError, match="package sha256 mismatch"):
        artifact_module.resolve_artifact_registry()
