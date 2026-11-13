"""Immutable contract shared with the Go task-domain adapter."""

from __future__ import annotations

import hashlib
import json
import re
from pathlib import Path
from typing import Final

SCHEMA: Final = "task_domain_classifier_v1"
PROJECTION: Final = "initial_logical_user_turn_v1"
SYSTEM_PROMPT: Final = (
    "Classify the work requested in the current user turn. Use earlier turns only "
    "to resolve context. Return exactly five comma-separated integers, each 0 "
    "or 1, in this order: ui,logic,data,infra,docs. No other text."
)
MAX_INPUT_BYTES: Final = 32_768
MAX_INPUT_TOKENS: Final = 8_192
BUDGET_HEADER: Final = "X-Task-Domain-Budget-Ms"
MAX_BUDGET_MILLISECONDS: Final = 10_000
OUTPUT: Final = re.compile(r"[01](?:,[01]){4}")
DIGEST: Final = re.compile(r"[a-f0-9]{64}")
REQUIRED_FILES: Final = frozenset({
    "model.safetensors", "config.json", "tokenizer.json",
    "tokenizer_config.json", "generation_config.json",
})


def verify_release(manifest_path: Path, model_path: Path, expected_sha256: str) -> str:
    payload: bytes = manifest_path.read_bytes()
    digest: str = hashlib.sha256(payload).hexdigest()
    if digest != expected_sha256:
        raise ValueError("task release digest mismatch")
    manifest: dict = json.loads(payload)
    if set(manifest) != {"schema_version", "projection_version", "prompt_sha256", "files", "evidence"}:
        raise ValueError("invalid release fields")
    if (manifest["schema_version"] != SCHEMA or manifest["projection_version"] != PROJECTION
            or manifest["prompt_sha256"] != hashlib.sha256(SYSTEM_PROMPT.encode()).hexdigest()):
        raise ValueError("unsupported task classifier contract")
    files: dict[str, str] = manifest["files"]
    if not REQUIRED_FILES.issubset(files):
        raise ValueError("missing model/tokenizer identity")
    actual_files: set[str] = {p.name for p in model_path.iterdir()}
    if actual_files != set(files):
        raise ValueError("model directory differs from pinned inventory")
    for filename, expected in files.items():
        artifact: Path = model_path / filename
        if Path(filename).name != filename or artifact.is_symlink() or not artifact.is_file() or not DIGEST.fullmatch(expected):
            raise ValueError("invalid model artifact")
        with artifact.open("rb") as source:
            observed: str = hashlib.file_digest(source, "sha256").hexdigest()
        if observed != expected:
            raise ValueError("model artifact digest mismatch")
    evidence: dict[str, str] = manifest["evidence"]
    if not evidence or any(not DIGEST.fullmatch(k) or not DIGEST.fullmatch(v) for k, v in evidence.items()):
        raise ValueError("invalid roster evidence identities")
    return digest
