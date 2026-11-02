"""Canonical JSON serialization shared across ml_infra and ml_dev.

Byte-compatibility contract: ``canonical_json``/``canonical_json_bytes`` in
this module produce output byte-for-byte identical to the historical private
copies of ``_canonical_json_bytes`` in ``ml_dev/pipeline_artifact_store.py``
and ``ml_dev/collector_gcs_ledger.py`` for every input, and identical to
``ml_dev/agent_flow/extraction/conversation_dataset/canonical_hashing.py:canonical_json``
for every non-NaN/non-Infinity input (this module is stricter: it raises
``ValueError`` on NaN/Infinity via ``allow_nan=False`` instead of emitting
invalid JSON). Hashes derived from this output are used as cache keys and
artifact identities throughout the repo -- do not change the serialization
parameters below.
"""

from __future__ import annotations

import json


def canonical_json(value: object) -> str:
    """Serialize ``value`` using the repo's canonical JSON byte contract.

    Sorted keys, no whitespace, non-ASCII preserved, NaN/Infinity rejected.
    """

    return json.dumps(
        value,
        allow_nan=False,
        ensure_ascii=False,
        separators=(",", ":"),
        sort_keys=True,
    )


def canonical_json_bytes(value: object) -> bytes:
    """Return :func:`canonical_json` encoded as UTF-8 bytes."""

    return canonical_json(value).encode("utf-8")
