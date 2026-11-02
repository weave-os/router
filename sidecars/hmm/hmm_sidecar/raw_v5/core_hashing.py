"""SHA-256 hashing helpers shared across ml_infra and ml_dev.

Byte-compatibility contract: results here are cache keys and artifact
identities used throughout the repo. ``sha256_texts`` replicates the exact
element-delimiting protocol used by
``ml_dev/code_output/nonparametric/embed.py:_sha256_texts`` (each element's
UTF-8 bytes followed by a single NUL delimiter, so ``["ab", "c"]`` and
``["a", "bc"]`` hash differently). ``self_sha256`` replicates the
model-self-hash pattern used throughout ``ml_dev/pipeline_artifact_store.py``
and ``ml_dev/collector_gcs_ledger.py``.
"""

from __future__ import annotations

from collections.abc import Sequence
import hashlib
from pathlib import Path

from pydantic import BaseModel

from hmm_sidecar.raw_v5.core_canonical import canonical_json_bytes


def sha256_bytes(value: bytes) -> str:
    """Return the lowercase SHA-256 hex digest of raw bytes."""

    return hashlib.sha256(value).hexdigest()


def sha256_text(value: str) -> str:
    return hashlib.sha256(value.encode("utf-8")).hexdigest()


def sha256_file(path: Path, *, chunk_bytes: int = 1_048_576) -> str:
    """Return the lowercase SHA-256 hex digest of a file, streamed in chunks."""

    digest = hashlib.sha256()
    with path.open("rb") as handle:
        while True:
            chunk = handle.read(chunk_bytes)
            if not chunk:
                break
            digest.update(chunk)
    return digest.hexdigest()


def sha256_texts(values: Sequence[str]) -> str:
    """Return one SHA-256 hex digest over an ordered sequence of strings.

    Source: ``ml_dev/code_output/nonparametric/embed.py:_sha256_texts``. Each
    element's UTF-8 bytes are hashed followed by a single NUL delimiter, so
    the digest is sensitive to element boundaries (``["ab", "c"]`` differs
    from ``["a", "bc"]``).
    """

    digest = hashlib.sha256()
    for value in values:
        digest.update(value.encode("utf-8"))
        digest.update(b"\0")
    return digest.hexdigest()


def self_sha256(model: BaseModel, *, exclude_field: str) -> str:
    """Return the canonical self-hash of ``model`` with one field excluded.

    Matches the self-hash pattern used throughout
    ``ml_dev/pipeline_artifact_store.py`` and ``ml_dev/collector_gcs_ledger.py``:
    dump the model to JSON-mode with ``exclude_field`` omitted, canonicalize,
    and hash.
    """

    return sha256_bytes(
        canonical_json_bytes(model.model_dump(mode="json", exclude={exclude_field}))
    )
