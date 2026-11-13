"""Comparison arms: which upstream serves Codex and how the model is pinned.

A *router* arm sends Codex to the Weave router and opens every trial with a
``/beta`` turn, so the router's beta policy picks the model per request. A
*router-force* arm also goes through the router but pins the model with the
``x-weave-force-model`` header — the headless equivalent of ``/force-model``.
A *direct* arm talks to the provider's own API. An *openrouter* arm goes
through the recording tap (``weave_bench.tap``) to OpenRouter's
Responses API, optionally with the ``auto-beta-router`` plugin.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum


class Upstream(StrEnum):
    ROUTER = "router"
    ROUTER_FORCE = "router-force"
    DIRECT = "direct"
    OPENROUTER = "openrouter"


class ReasoningEffort(StrEnum):
    """Codex ``model_reasoning_effort``. Atlas pins ``high`` (Scale's QA
    protocol); the Astra control ran ``max``."""

    MEDIUM = "medium"
    HIGH = "high"
    MAX = "max"


class CostTier(StrEnum):
    """OpenRouter Auto Router plugin ``cost_tier`` bands, cheapest to most capable.
    The published Atlas comparisons ran ``xhigh`` (2026-09-05) and ``max`` (2026-09-06)."""

    LOW = "low"
    MEDIUM = "medium"
    HIGH = "high"
    XHIGH = "xhigh"
    MAX = "max"


class HarborModel(StrEnum):
    """Harbor ``--model`` ids. Harbor maps the ``openai/`` prefix to
    ``OPENAI_API_KEY`` and strips it before handing the model to Codex."""

    GPT_5_6_SOL = "openai/gpt-5.6-sol"
    GPT_5_6_LUNA = "openai/gpt-5.6-luna"
    GPT_6_ASTRA = "openai/gpt-6-astra"
    OPENROUTER_AUTO = "openrouter/auto"
    OPENROUTER_AUTO_BETA = "openrouter/auto-beta"


OPENROUTER_MODEL_PREFIX = "openrouter/"


@dataclass(frozen=True)
class ArmSpec:
    name: str
    upstream: Upstream
    harbor_model: HarborModel
    reasoning_effort: ReasoningEffort = ReasoningEffort.HIGH
    cost_tier: CostTier | None = None

    @property
    def beta_preflight(self) -> bool:
        return self.upstream is Upstream.ROUTER

    @property
    def via_router(self) -> bool:
        """Traffic lands in the router's analytics export (billed cost, served model)."""
        return self.upstream in (Upstream.ROUTER, Upstream.ROUTER_FORCE)

    @property
    def codex_model(self) -> str:
        """The model name Codex itself sees (provider prefix stripped by Harbor)."""
        return self.harbor_model.split("/", 1)[1]

    @property
    def force_model(self) -> str | None:
        return self.codex_model if self.upstream is Upstream.ROUTER_FORCE else None

    @property
    def openrouter_plugin_id(self) -> str | None:
        """Auto Router plugin id: the metarouter suffix plus ``-router``."""
        if self.upstream is not Upstream.OPENROUTER or self.cost_tier is None:
            return None
        return f"{self.codex_model}-router"


# Router arms still pass a model because Codex requires one; the router's beta
# policy ignores it and picks per request.
ARMS: dict[str, ArmSpec] = {
    spec.name: spec
    for spec in (
        ArmSpec("router", Upstream.ROUTER, HarborModel.GPT_5_6_SOL),
        ArmSpec("sol", Upstream.DIRECT, HarborModel.GPT_5_6_SOL),
        ArmSpec("luna", Upstream.DIRECT, HarborModel.GPT_5_6_LUNA),
        ArmSpec("astra", Upstream.DIRECT, HarborModel.GPT_6_ASTRA, ReasoningEffort.MAX),
        ArmSpec("router-sol", Upstream.ROUTER_FORCE, HarborModel.GPT_5_6_SOL),
        ArmSpec("router-luna", Upstream.ROUTER_FORCE, HarborModel.GPT_5_6_LUNA),
        ArmSpec("router-astra", Upstream.ROUTER_FORCE, HarborModel.GPT_6_ASTRA),
        ArmSpec("openrouter", Upstream.OPENROUTER, HarborModel.OPENROUTER_AUTO),
        ArmSpec(
            "openrouter-beta",
            Upstream.OPENROUTER,
            HarborModel.OPENROUTER_AUTO_BETA,
            cost_tier=CostTier.MAX,
        ),
        ArmSpec(
            "openrouter-beta-xhigh",
            Upstream.OPENROUTER,
            HarborModel.OPENROUTER_AUTO_BETA,
            cost_tier=CostTier.XHIGH,
        ),
    )
}

DEFAULT_ARMS = ("router", "sol")


def parse_arms(arms: str) -> tuple[ArmSpec, ...]:
    """Comma-separated ``--arms``; empty selects the published router-vs-Sol pair."""
    names = tuple(name.strip() for name in arms.split(",")) if arms else DEFAULT_ARMS
    unknown = [name for name in names if name not in ARMS]
    if unknown:
        raise ValueError(f"unknown arms {unknown}; known: {sorted(ARMS)}")
    return tuple(ARMS[name] for name in names)
