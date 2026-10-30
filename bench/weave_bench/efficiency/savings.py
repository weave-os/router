"""How work ratios and quality factors turn naive savings into adjusted savings.

Per request, against an always-on comparison model X:

    naive comparison cost    = served tokens priced at X's rates (input side, output side)
    adjusted comparison cost = (X input cost × input ratio + X output cost × output ratio) ÷ penalty
    adjusted savings         = adjusted comparison cost − what the request actually cost

The ratios are the (harness, X, served model) cell's work ratios under one bound
rule; the penalty is that harness's Terminal-Bench pass-rate penalty against
always-X (``quality.capped_pass_rate_penalty``, never below 1). A request whose
served model has no measured cell gets no savings: it compares at its own cost.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum

from weave_bench.efficiency.estimates import WorkRatios


class BoundSet(StrEnum):
    # Every ratio held at or below 1, so applying it can only lower savings: the floor.
    CAPPED = "capped"
    # Ratios above 1 also count, at their lower bound, once measured on enough tasks.
    CREDITED = "credited"


@dataclass(frozen=True)
class RequestCost:
    comparison_input_usd: float
    comparison_output_usd: float
    actual_usd: float


def ratio_bounds(work: WorkRatios, bound_set: BoundSet) -> tuple[float, float]:
    if bound_set is BoundSet.CAPPED:
        return work.input_capped, work.output_capped
    return work.input_credited, work.output_credited


def adjusted_comparison_cost(
    cost: RequestCost, work: WorkRatios | None, bound_set: BoundSet, *, penalty: float = 1.0
) -> float:
    if work is None:
        return cost.actual_usd
    if penalty < 1.0:
        raise ValueError(f"pass-rate penalty {penalty} is below 1")
    input_ratio, output_ratio = ratio_bounds(work, bound_set)
    return (cost.comparison_input_usd * input_ratio + cost.comparison_output_usd * output_ratio) / penalty


def adjusted_savings(cost: RequestCost, work: WorkRatios | None, bound_set: BoundSet, *, penalty: float = 1.0) -> float:
    return adjusted_comparison_cost(cost, work, bound_set, penalty=penalty) - cost.actual_usd


def naive_savings(cost: RequestCost) -> float:
    return cost.comparison_input_usd + cost.comparison_output_usd - cost.actual_usd
