"""List-price repricing from the router catalog (``prices.generated.json``).

Regenerate with ``make generate`` at the repo root. List price is what a direct
arm pays its provider; router-billed cost (analytics ``actual_*_cost_usd``) can
differ when the router serves a cheaper model or a subscription-backed lane.
"""

from __future__ import annotations

import json
from dataclasses import dataclass
from functools import cache
from importlib import resources

PRICES_FILENAME = "prices.generated.json"


@dataclass(frozen=True)
class ModelPrice:
    input_usd_per_million: float
    output_usd_per_million: float
    cache_read_multiplier: float
    cache_write_multiplier: float

    def cost_usd(
        self, *, input_tokens: int, cache_read_tokens: int, cache_write_tokens: int, output_tokens: int
    ) -> float:
        """Mirror of the router's ``catalog.Cost``: ``input_tokens`` excludes cached reads."""
        weighted_input = (
            input_tokens
            + cache_write_tokens * self.cache_write_multiplier
            + cache_read_tokens * self.cache_read_multiplier
        )
        return (weighted_input * self.input_usd_per_million + output_tokens * self.output_usd_per_million) / 1_000_000


@dataclass(frozen=True)
class PriceTable:
    pricing_version: str
    models: dict[str, ModelPrice]

    def lookup(self, model: str) -> ModelPrice | None:
        """Exact name, else the name with a trailing ``-YYYYMMDD`` date suffix dropped."""
        if model in self.models:
            return self.models[model]
        stem, _, suffix = model.rpartition("-")
        if len(suffix) == 8 and suffix.isdigit():
            return self.models.get(stem)
        return None


@cache
def load_price_table() -> PriceTable:
    document = json.loads((resources.files("weave_bench") / PRICES_FILENAME).read_text())
    return PriceTable(
        pricing_version=document["pricing_version"],
        models={name: ModelPrice(**price) for name, price in document["models"].items()},
    )
