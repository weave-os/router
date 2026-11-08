"""Symbols imported by weave-os/weave; removing or changing one breaks a Weave gitlink bump."""

import hashlib

from hmm_sidecar.classifier_package.runtime_contract import CONFIGURED_INPUT_MODE
from hmm_sidecar.classifier_release import (
    classifier_package_artifact_id,
    qwen_semantic_configuration_bytes,
)


def test_classifier_package_artifact_id_is_stable() -> None:
    # Weave: classifier-service/classifier_service/worker.py, .github/scripts/classifier-release-bootstrap.py
    assert (
        classifier_package_artifact_id("b" * 64)
        == "hmm_router_sidecar_package_bbbbbbbbbbbbbbbb"
    )


def test_qwen_semantic_configuration_bytes_are_stable() -> None:
    # Weave: .github/scripts/classifier-release-bootstrap.py, .github/tests/classifier-release/test_bootstrap.py
    configuration = qwen_semantic_configuration_bytes("qwen-artifact", "c" * 64)
    assert (
        hashlib.sha256(configuration).hexdigest()
        == "7661d07b3a89cd411a3c30b689043ecd36e2294a9822ed7b88ee9c0f5c552159"
    )


def test_runtime_contract_reexports_configured_input_mode() -> None:
    # Weave: router-hmm-sidecar/artifacts.py
    assert CONFIGURED_INPUT_MODE == "raw_v5_configured_serving_v1"
