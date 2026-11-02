"""Deterministic overflow projection for raw V5 router model inputs.

Raw V5 evidence remains exact in Stage 2 and in the source sequence contract.
Only a sequence element that exceeds the model-input character budget is
projected here.  The projection is deliberately structural: it recognizes
JSON and Python-literal containers and first tries a reversible minified/table
encoding that retains every value.  Only a container that still cannot fit is
reduced to schema/counts plus an exact representative item; every reduced span
is hashed.  It never calls a language model, a learned compressor, or the
retired canonicalizer.

The structured-data strategy is inspired by Headroom SmartCrusher's table and
schema factoring (Apache-2.0, https://github.com/headroomlabs-ai/headroom), but
is implemented locally so the router contract has no optional Rust or neural
runtime dependency.  Arbitrary source code is not paraphrased.  If an
oversized fenced code block still prevents the element from fitting, an exact
head/tail excerpt with an omission marker is used.  A final explicit excerpt
is reserved for otherwise-unstructured overflow.
"""

from __future__ import annotations

import re
from dataclasses import dataclass

from hmm_sidecar.raw_v5.raw_v5_contract import (
    RAW_V5_MODEL_INPUT_MAX_CHARS,
    RAW_V5_MODEL_INPUT_STRUCTURED_LITERAL_MIN_CHARS,
)


_STRUCTURED_MARKER = "__workweave_structured_excerpt_v1__"
_LOSSLESS_STRUCTURED_MARKER = "__workweave_structured_compaction_v1__"
_TABLE_MARKER = "__workweave_table_v1__"
_ARRAY_MARKER = "__workweave_array_excerpt_v1__"
_STRING_MARKER = "__workweave_string_excerpt_v1__"
_PYTHON_VALUE_MARKER = "__workweave_python_value_v1__"
_PROJECTION_MARKER = "WORKWEAVE_RAW_V5_MODEL_INPUT_V1"
_CODE_MARKER = "WORKWEAVE_CODE_EXCERPT_V1"
_OVERFLOW_MARKER = "WORKWEAVE_RAW_V5_OVERFLOW_V1"
_RESERVED_STRUCTURAL_KEYS = frozenset(
    {
        _STRUCTURED_MARKER,
        _LOSSLESS_STRUCTURED_MARKER,
        _TABLE_MARKER,
        _ARRAY_MARKER,
        _STRING_MARKER,
        _PYTHON_VALUE_MARKER,
    }
)
_FENCED_BLOCK_RE = re.compile(
    r"(?ms)^(?P<indent>[ \t]*)(?P<fence>`{3,}|~{3,})(?P<info>[^\n]*)\n"
    r"(?P<body>.*?)(?P=indent)(?P=fence)[ \t]*(?=\n|\Z)"
)


@dataclass(frozen=True, slots=True)
class RawV5ModelInputProjection:
    """One auditable exact-or-projected model-input document."""

    text: str
    changed: bool
    source_chars: int
    projected_chars: int
    source_sha256: str
    reducers: tuple[str, ...]


def _value_type(value: object) -> str:
    raise NotImplementedError("not on the classifier serving path")


def _stable_sequence(value: object) -> list[object]:
    raise NotImplementedError("not on the classifier serving path")


def _mapping_items(value: dict[object, object]) -> list[tuple[object, object]]:
    # JSON objects and the scheduling dumps that motivated this projection use
    # string keys. Retain Python insertion order: it can itself carry meaning.
    raise NotImplementedError("not on the classifier serving path")


def _schema_for_values(
    values: list[object],
    *,
    depth: int = 0,
) -> dict[str, object]:
    raise NotImplementedError("not on the classifier serving path")


def _sample_string(value: str) -> object:
    raise NotImplementedError("not on the classifier serving path")


def _sample_value(value: object, *, depth: int = 0) -> object:
    raise NotImplementedError("not on the classifier serving path")


def _lossless_json_value(value: object) -> object:
    raise NotImplementedError("not on the classifier serving path")


def _parse_structured_literal(source: str) -> tuple[str, object] | None:
    raise NotImplementedError("not on the classifier serving path")


def _balanced_container_end(text: str, start: int) -> int | None:
    raise NotImplementedError("not on the classifier serving path")


def _structured_summary_replacement(
    source: str,
    *,
    syntax: str,
    value: object,
) -> str:
    raise NotImplementedError("not on the classifier serving path")


def _structured_lossless_replacement(
    source: str,
    *,
    syntax: str,
    value: object,
) -> str:
    raise NotImplementedError("not on the classifier serving path")


def _reduce_structured_literals(
    text: str,
    *,
    lossless: bool,
    minimum_source_chars: int = RAW_V5_MODEL_INPUT_STRUCTURED_LITERAL_MIN_CHARS,
    require_shorter_replacement: bool = True,
) -> tuple[str, int]:
    raise NotImplementedError("not on the classifier serving path")


def _bounded_exact_excerpt(
    source: str,
    *,
    max_chars: int,
    marker: str,
) -> str:
    raise NotImplementedError("not on the classifier serving path")


def _projection_provenance_marker(
    *,
    source_chars: int,
    source_sha256: str,
) -> str:
    raise NotImplementedError("not on the classifier serving path")


def _insert_projection_provenance(
    text: str,
    *,
    source_chars: int,
    source_sha256: str,
) -> str:
    raise NotImplementedError("not on the classifier serving path")


def _reduce_fenced_code_blocks(text: str) -> tuple[str, int]:
    raise NotImplementedError("not on the classifier serving path")


def project_raw_v5_model_input(
    text: str,
    *,
    max_chars: int = RAW_V5_MODEL_INPUT_MAX_CHARS,
) -> RawV5ModelInputProjection:
    raise NotImplementedError("not on the classifier serving path")


def normalize_raw_v5_model_input(
    text: str,
    *,
    max_chars: int = RAW_V5_MODEL_INPUT_MAX_CHARS,
    force_structured: bool = False,
) -> RawV5ModelInputProjection:
    raise NotImplementedError("not on the classifier serving path")


__all__ = [
    "RawV5ModelInputProjection",
    "normalize_raw_v5_model_input",
    "project_raw_v5_model_input",
]
