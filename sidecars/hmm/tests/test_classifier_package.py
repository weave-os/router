from __future__ import annotations

import asyncio
import hashlib
import json
import os
import tarfile
from pathlib import Path
from types import SimpleNamespace

import numpy as np
import pytest

from hmm_sidecar import artifacts
from hmm_sidecar.classifier_package import gemini_embeddings

FIXTURES = Path(__file__).parent / "fixtures" / "classifier_package"


def _manifest(classifier_files: dict[str, str], hmm_files: dict[str, str]) -> dict:
    return {
        "schema_version": "hmm_router_classifier_package_v1",
        "runtime_contract": {"input_mode": "raw_v5_configured_serving_v1"},
        "classifier_files": classifier_files,
        "hmm_bundle_files": hmm_files,
    }


def _sha(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def _write_package(root: Path, *, tamper: bool = False) -> None:
    (root / "classifier").mkdir(parents=True)
    (root / "hmm_bundle").mkdir()
    (root / "classifier" / "model.json").write_bytes(b"classifier")
    (root / "hmm_bundle" / "manifest.json").write_bytes(b"bundle")
    manifest = _manifest(
        {"model.json": _sha(b"classifier")},
        {"manifest.json": _sha(b"bundle" if not tamper else b"other")},
    )
    (root / "manifest.json").write_text(json.dumps(manifest))


def test_manifest_detection_requires_the_configured_input_mode() -> None:
    manifest = _manifest({}, {})
    assert artifacts.is_classifier_package_manifest(manifest)
    manifest["runtime_contract"] = {"input_mode": "legacy"}
    assert not artifacts.is_classifier_package_manifest(manifest)
    assert not artifacts.is_classifier_package_manifest(
        {"schema_version": "hmm_router_frozen_package_v1"}
    )


def test_manifest_verifies_every_classifier_and_bundle_file(
    tmp_path: Path,
) -> None:
    _write_package(tmp_path)
    manifest = artifacts._verify_manifest(tmp_path)
    assert isinstance(manifest, dict)

    tampered = tmp_path / "tampered"
    _write_package(tampered, tamper=True)
    with pytest.raises(ValueError, match="hmm_bundle/manifest.json"):
        artifacts._verify_manifest(tampered)


def test_artifact_id_matches_the_registry_model_id() -> None:
    sha = "ee62a7cc4f188b5e" + "0" * 48
    loaded = artifacts.FrozenArtifacts(Path("."), None, sha, None, package_manifest={})
    assert loaded.model_id == "hmm_router_sidecar_package_ee62a7cc4f188b5e"


@pytest.mark.parametrize(
    ("env", "expected"),
    [
        ({"GEMINI_API_KEY": "k"}, gemini_embeddings.GeminiTransport.API_KEY),
        ({}, gemini_embeddings.GeminiTransport.VERTEX),
        (
            {"GEMINI_API_KEY": "k", "HMM_GEMINI_TRANSPORT": "vertex"},
            gemini_embeddings.GeminiTransport.VERTEX,
        ),
    ],
)
def test_gemini_transport_resolution(monkeypatch, env, expected) -> None:
    for name in ("GEMINI_API_KEY", "GOOGLE_API_KEY", "HMM_GEMINI_TRANSPORT"):
        monkeypatch.delenv(name, raising=False)
    for name, value in env.items():
        monkeypatch.setenv(name, value)
    assert gemini_embeddings.resolve_gemini_transport() is expected


def test_gemini_transport_rejects_unknown_values(monkeypatch) -> None:
    monkeypatch.setenv("HMM_GEMINI_TRANSPORT", "voyage")
    with pytest.raises(gemini_embeddings.EmbeddingError):
        gemini_embeddings.resolve_gemini_transport()


class _FakeModels:
    def __init__(self) -> None:
        self.calls: list[tuple[str, str, object]] = []

    async def embed_content(self, *, model, contents, config):
        self.calls.append((model, contents, config))
        return SimpleNamespace(
            embeddings=[SimpleNamespace(values=[float(len(contents))] * 3)]
        )


def test_api_key_transport_embeds_each_text_with_the_pinned_contract(
    monkeypatch,
) -> None:
    models = _FakeModels()
    monkeypatch.setenv("GEMINI_API_KEY", "k")
    monkeypatch.delenv("HMM_GEMINI_TRANSPORT", raising=False)
    monkeypatch.setattr(
        gemini_embeddings,
        "_client",
        lambda *_args: SimpleNamespace(aio=SimpleNamespace(models=models)),
    )
    vectors = asyncio.run(
        gemini_embeddings.embed_texts_uncached(
            ["a", "bb", "a"],
            model="google/gemini-embedding-2",
            vertex_output_dimensionality=3072,
            batch_size=8,
        )
    )
    assert vectors == {"a": [1.0] * 3, "bb": [2.0] * 3}
    assert sorted(call[1] for call in models.calls) == ["a", "bb"]
    model, _, config = models.calls[0]
    assert model == "gemini-embedding-2"
    assert config.output_dimensionality == 3072
    assert config.auto_truncate is None


def test_vertex_transport_disables_auto_truncation(monkeypatch) -> None:
    models = _FakeModels()
    monkeypatch.setenv("HMM_GEMINI_TRANSPORT", "vertex")
    monkeypatch.setenv("HMM_VERTEX_PROJECT", "p")
    monkeypatch.setattr(
        gemini_embeddings,
        "_client",
        lambda *_args: SimpleNamespace(aio=SimpleNamespace(models=models)),
    )
    asyncio.run(
        gemini_embeddings.embed_texts_uncached(["a"], model="google/gemini-embedding-2")
    )
    assert models.calls[0][2].auto_truncate is False


def _stub_vector(text: str, dim: int) -> list[float]:
    seed = int.from_bytes(hashlib.sha256(text.encode()).digest()[:8], "little")
    vector = np.random.default_rng(seed).standard_normal(dim)
    return (vector / np.linalg.norm(vector)).tolist()


@pytest.mark.skipif(
    not os.environ.get("HMM_RAW_V5_PARITY_PACKAGE"),
    reason="set HMM_RAW_V5_PARITY_PACKAGE to a raw-V5 package tarball to run parity",
)
def test_matches_the_production_golden(monkeypatch, tmp_path: Path) -> None:
    """Golden readouts were produced by Weave's production sidecar on the same
    package with the same deterministic stub embeddings."""
    from hmm_sidecar.classifier_package import agent_flow_sidecar
    from hmm_sidecar.classifier_package.serving import PackageClassifier

    async def stub_embeddings(texts, *, model, **kwargs):
        dim = kwargs.get("vertex_output_dimensionality") or 3072
        vectors = {text: _stub_vector(text, dim) for text in texts}
        return (vectors, None) if kwargs.get("return_info") else vectors

    monkeypatch.setattr(agent_flow_sidecar, "embed_texts_uncached", stub_embeddings)
    monkeypatch.setenv("HMM_VERTEX_PROJECT", "stub")
    with tarfile.open(os.environ["HMM_RAW_V5_PARITY_PACKAGE"]) as archive:
        archive.extractall(tmp_path, filter="data")
    manifest = artifacts._verify_manifest(tmp_path)
    assert isinstance(manifest, dict)
    classifier = PackageClassifier(tmp_path, manifest)
    cases = json.loads((FIXTURES / "cases.json").read_text())
    golden = {
        row["route_id"]: row
        for row in json.loads((FIXTURES / "golden.json").read_text())
    }

    for case in cases:
        readout, _ = asyncio.run(classifier.classify(case))
        expected = golden[case["route_id"]]
        assert readout.label == expected["label"]
        assert readout.state_id == expected["state_id"]
        assert list(readout.state_path) == expected["state_path"]
        np.testing.assert_allclose(
            [readout.probabilities[label] for label in sorted(expected["probs"])],
            [expected["probs"][label] for label in sorted(expected["probs"])],
            rtol=0,
            atol=1e-9,
        )
        np.testing.assert_allclose(
            readout.state_probabilities, expected["gamma_row"], rtol=0, atol=1e-9
        )


@pytest.mark.skipif(
    not os.environ.get("HMM_RAW_V5_PARITY_PACKAGE"),
    reason="set HMM_RAW_V5_PARITY_PACKAGE to a raw-V5 package tarball",
)
def test_rejects_hmm_bundle_the_classifier_was_not_trained_on(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    from hmm_sidecar.classifier_package.serving import PackageClassifier, PackageError

    monkeypatch.setenv("HMM_VERTEX_PROJECT", "stub")
    with tarfile.open(os.environ["HMM_RAW_V5_PARITY_PACKAGE"]) as archive:
        archive.extractall(tmp_path, filter="data")
    manifest = artifacts._verify_manifest(tmp_path)
    bundle = tmp_path / "hmm_bundle" / "bundle.json"
    bundle.write_bytes(bundle.read_bytes() + b"\n")
    with pytest.raises(PackageError, match="not trained on"):
        PackageClassifier(tmp_path, manifest)


@pytest.mark.skipif(
    not os.environ.get("HMM_RAW_V5_PARITY_PACKAGE"),
    reason="set HMM_RAW_V5_PARITY_PACKAGE to a raw-V5 package tarball",
)
def test_overrun_inference_serves_the_sessions_prior_state(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    from hmm_sidecar.classifier_package import agent_flow_sidecar
    from hmm_sidecar.classifier_package.serving import (
        PackageClassifier,
        ServingOptions,
    )

    embed_delay_seconds = {"value": 0.0}

    async def stub_embeddings(texts, *, model, **kwargs):
        await asyncio.sleep(embed_delay_seconds["value"])
        dim = kwargs.get("vertex_output_dimensionality") or 3072
        vectors = {text: _stub_vector(text, dim) for text in texts}
        return (vectors, None) if kwargs.get("return_info") else vectors

    monkeypatch.setattr(agent_flow_sidecar, "embed_texts_uncached", stub_embeddings)
    monkeypatch.setenv("HMM_VERTEX_PROJECT", "stub")
    with tarfile.open(os.environ["HMM_RAW_V5_PARITY_PACKAGE"]) as archive:
        archive.extractall(tmp_path, filter="data")
    manifest = artifacts._verify_manifest(tmp_path)
    classifier = PackageClassifier(
        tmp_path, manifest, ServingOptions(infer_deadline_seconds=0.2)
    )
    cases = json.loads((FIXTURES / "cases.json").read_text())
    prefix_lengths: list[int] = []
    serving_features = classifier.selector.serving_features

    def recording_serving_features(**kwargs):
        prefix_lengths.append(kwargs["prefix_length"])
        return serving_features(**kwargs)

    monkeypatch.setattr(
        classifier.selector, "serving_features", recording_serving_features
    )

    async def scenario() -> None:
        first, _ = await classifier.classify(cases[0], session_id="session-a")
        assert not first.degraded
        embed_delay_seconds["value"] = 1.0
        degraded, timings = await classifier.classify(cases[1], session_id="session-a")
        assert degraded.degraded
        assert timings is None
        assert degraded.state_path == first.state_path
        assert degraded.probabilities == first.probabilities
        # The fallback readout keeps the length it was computed at even when
        # the request trims a different number of older turns.
        trimmed = {**cases[1], "omitted_user_turn_count": 5}
        trimmed_degraded, _ = await classifier.classify(trimmed, session_id="session-a")
        assert trimmed_degraded.degraded
        assert prefix_lengths[-1] == prefix_lengths[0]
        with pytest.raises(TimeoutError):
            await classifier.classify(cases[1], session_id="session-b")

    asyncio.run(scenario())
