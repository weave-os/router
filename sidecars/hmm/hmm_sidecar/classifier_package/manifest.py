"""Portable classifier package vocabulary shared by serving and release tooling."""

from __future__ import annotations

from enum import StrEnum
from typing import Any


class ClassifierPackageSchemaVersion(StrEnum):
    """Classifier package manifest versions accepted by the sidecar."""

    V1 = "hmm_router_classifier_package_v1"


class ClassifierInputMode(StrEnum):
    """Request projection modes declared by a classifier package."""

    RAW_V5_CONFIGURED_SERVING = "raw_v5_configured_serving_v1"


class ClassifierSelectionOwner(StrEnum):
    """Owner of routing-arm selection for classifier-only packages."""

    GO = "go"


CLASSIFIER_PACKAGE_SCHEMA_VERSION = ClassifierPackageSchemaVersion.V1
CONFIGURED_INPUT_MODE = ClassifierInputMode.RAW_V5_CONFIGURED_SERVING


def is_classifier_package_manifest(manifest: dict[str, Any]) -> bool:
    """Report whether manifest declares the configured raw-V5 serving package."""
    runtime_contract = manifest.get("runtime_contract")
    return (
        manifest.get("schema_version") == CLASSIFIER_PACKAGE_SCHEMA_VERSION
        and isinstance(runtime_contract, dict)
        and runtime_contract.get("input_mode") == CONFIGURED_INPUT_MODE
        and runtime_contract.get("selection_owner") == ClassifierSelectionOwner.GO
    )
