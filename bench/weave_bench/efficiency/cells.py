"""Measurement cells: one coding harness with one model pinned through the router.

A cell's trials send every request with ``x-weave-force-model: <model>``, so the
router serves that model whatever it would have picked. Two cells of the same
harness compare as a work ratio; a cell of a model outside its own family's
harness (Claude in Codex, GPT in Claude Code) is *non-native* and is applied at
a more conservative bound (``estimates.EfficiencyEstimate.bound_rule``).
"""

from __future__ import annotations

import re
from collections.abc import Mapping
from dataclasses import dataclass
from enum import StrEnum


class Harness(StrEnum):
    CLAUDE_CODE = "claude-code"
    CODEX = "codex"


HARNESS_SLUG: Mapping[Harness, str] = {Harness.CLAUDE_CODE: "cc", Harness.CODEX: "cx"}


class ModelFamily(StrEnum):
    """Served-model id prefixes of the families that ship their own harness."""

    CLAUDE = "claude-"
    GPT = "gpt-"


NATIVE_HARNESS: Mapping[ModelFamily, Harness] = {
    ModelFamily.CLAUDE: Harness.CLAUDE_CODE,
    ModelFamily.GPT: Harness.CODEX,
}

# What each harness asks for when the router is not pinned; it is also the
# comparison ("always-on") model each harness's ratios are measured against.
REQUESTED_MODEL: Mapping[Harness, str] = {
    Harness.CLAUDE_CODE: "claude-opus-5-5",
    Harness.CODEX: "gpt-6-astra",
}

# The cells behind the published ratio table (results/). Any catalog model can
# be measured with ``--models``; these are the defaults.
PUBLISHED_MODELS: Mapping[Harness, tuple[str, ...]] = {
    Harness.CLAUDE_CODE: (
        "claude-opus-5-5",
        "gpt-6-luna",
        "claude-haiku-5-5",
        "claude-sonnet-5-5",
        "claude-sonnet-5",
        "claude-fable-5-1",
        "gpt-6.1-sol",
    ),
    Harness.CODEX: (
        "gpt-6-astra",
        "gpt-6-luna",
        "claude-opus-5-5",
        "gpt-6.1-sol",
        "gpt-5.6-sol",
    ),
}


def model_family(model: str) -> ModelFamily | None:
    return next((family for family in ModelFamily if model.startswith(family)), None)


@dataclass(frozen=True, order=True)
class Cell:
    harness: Harness
    model: str

    @property
    def native(self) -> bool:
        family = model_family(self.model)
        return family is not None and NATIVE_HARNESS[family] is self.harness

    @property
    def slug(self) -> str:
        model_slug = re.sub(r"[^a-z0-9]+", "-", self.model.lower()).strip("-")
        return f"{HARNESS_SLUG[self.harness]}-{model_slug}"


def published_cells() -> tuple[Cell, ...]:
    return tuple(Cell(harness, model) for harness, models in PUBLISHED_MODELS.items() for model in models)
