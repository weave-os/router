"""Work ratios: how many tokens a comparison model needs for the same work.

For two cells of one harness, the work ratio is the comparison (requested)
model's token volume over the served model's on the tasks both ran, separately
for input (fresh + cache write + cache read) and output, with a task-bootstrap
90% interval. Below 1 the comparison model needed fewer tokens than the served
one, so pricing the served model's tokens at the comparison model's rates
overstates what the comparison model would have cost.

Cache behaviour is left out of the ratios: eval sessions are short and cache
poorly, while production requests already carry their own cache mix in the cost
the ratios scale (``adjusted_comparison_cost``).
"""

from __future__ import annotations

import random
from collections.abc import Callable, Iterable, Mapping, Sequence
from dataclasses import dataclass
from enum import StrEnum

from weave_bench.analytics import AnalyticsColumn, DecisionRow, row_fresh_input_tokens, row_int
from weave_bench.efficiency.cells import Cell

# A ratio above 1 raises savings, so it is credited only when measured on at
# least this many tasks: the core set every cell ran.
CREDIT_MIN_TASKS = 9
BOOTSTRAP_DRAWS = 10_000
BOOTSTRAP_INTERVAL = 0.90
BOOTSTRAP_SEED = 11

TURN_TYPE_COLUMN = "turn_type"
UPSTREAM_STATUS_COLUMN = "upstream_status_code"
# The router's own side requests (titles, compaction, classification, probes):
# not the pinned model's work, so they are left out of the guard and the tokens.
UTILITY_TURN_TYPES = frozenset({"title_gen", "compaction", "classifier", "probe", "recap"})
OK_STATUS = 200
# A refused attempt that is retried leaves a failed row beside the success;
# past half, the trial's tokens no longer describe the pinned model's work.
MAX_FAILED_REQUEST_SHARE = 0.5
# Claude Code's web-search side requests can only run on Anthropic, so the
# router serves them there whatever the pin; a larger share means the pin failed.
MAX_ANTHROPIC_SIDE_REQUEST_SHARE = 0.10
ANTHROPIC_PROVIDER_PREFIX = "anthropic"


@dataclass(frozen=True)
class TokenUsage:
    fresh_input: float
    cache_write: float
    cache_read: float
    output: float

    def __add__(self, other: TokenUsage) -> TokenUsage:
        return TokenUsage(
            self.fresh_input + other.fresh_input,
            self.cache_write + other.cache_write,
            self.cache_read + other.cache_read,
            self.output + other.output,
        )

    @property
    def input_total(self) -> float:
        return self.fresh_input + self.cache_write + self.cache_read

    def scaled(self, factor: float) -> TokenUsage:
        return TokenUsage(
            self.fresh_input * factor, self.cache_write * factor, self.cache_read * factor, self.output * factor
        )


ZERO_USAGE = TokenUsage(0, 0, 0, 0)


def _is_utility(row: DecisionRow) -> bool:
    return row.get(TURN_TYPE_COLUMN) in UTILITY_TURN_TYPES


def row_usage(row: DecisionRow) -> TokenUsage:
    return TokenUsage(
        fresh_input=row_fresh_input_tokens(row),
        cache_write=row_int(row, AnalyticsColumn.CACHE_CREATION_TOKENS),
        cache_read=row_int(row, AnalyticsColumn.CACHE_READ_TOKENS),
        output=row_int(row, AnalyticsColumn.OUTPUT_TOKENS),
    )


def trial_usage(rows: Iterable[DecisionRow]) -> TokenUsage:
    total = ZERO_USAGE
    for row in rows:
        if not _is_utility(row):
            total += row_usage(row)
    return total


def mean_usage(trials: Sequence[TokenUsage]) -> TokenUsage:
    total = ZERO_USAGE
    for usage in trials:
        total += usage
    return total.scaled(1 / len(trials))


class Violation(StrEnum):
    """Why a trial's router rows are not a clean measurement of the pinned model."""

    NO_ROWS = "no_router_rows"
    FAILED_REQUESTS = "requests_failed"
    WRONG_MODEL = "served_by_unpinned_model"
    UNFINISHED = "trial_did_not_finish"


def _anthropic_side_request(cell: Cell, row: DecisionRow) -> bool:
    return (
        row.get(AnalyticsColumn.DECISION_MODEL) != cell.model
        and str(row.get(AnalyticsColumn.DECISION_PROVIDER) or "").startswith(ANTHROPIC_PROVIDER_PREFIX)
        and not cell.model.startswith("claude-")
    )


def trial_violations(cell: Cell, rows: Sequence[DecisionRow]) -> list[Violation]:
    measured = [row for row in rows if not _is_utility(row)]
    if not measured:
        return [Violation.NO_ROWS]
    violations: list[Violation] = []
    failed = sum(1 for row in measured if row.get(UPSTREAM_STATUS_COLUMN) != OK_STATUS)
    if failed > MAX_FAILED_REQUEST_SHARE * len(measured):
        violations.append(Violation.FAILED_REQUESTS)
    checked = [row for row in measured if not _anthropic_side_request(cell, row)]
    if len(measured) - len(checked) > MAX_ANTHROPIC_SIDE_REQUEST_SHARE * len(measured):
        checked = measured
    if any(row.get(AnalyticsColumn.DECISION_MODEL) != cell.model for row in checked):
        violations.append(Violation.WRONG_MODEL)
    return violations


class BoundRule(StrEnum):
    """Which part of an ``EfficiencyEstimate`` its bound applies."""

    POINT = "point"
    LOWER = "lower"
    HELD_AT_ONE = "held"


@dataclass(frozen=True)
class EfficiencyEstimate:
    """The comparison model's token volume over the served model's on the same
    tasks. Below 1, the comparison model needed fewer tokens."""

    ratio: float
    lower: float
    upper: float
    tasks: int

    def bound_rule(self, *, native: bool) -> BoundRule:
        """The point estimate for a model in its own harness, the lower bound
        otherwise. Above 1 the comparison model would have needed more tokens,
        which raises savings, so that is credited only at a lower bound above 1
        measured on at least ``CREDIT_MIN_TASKS`` tasks; otherwise it is held at 1."""
        estimate = self.ratio if native else self.lower
        if estimate <= 1.0:
            return BoundRule.POINT if native else BoundRule.LOWER
        if self.tasks < CREDIT_MIN_TASKS or self.lower <= 1.0:
            return BoundRule.HELD_AT_ONE
        return BoundRule.LOWER

    def credited(self, *, native: bool) -> float:
        """The bound the "credited" rule applies; it may exceed 1."""
        rule = self.bound_rule(native=native)
        if rule is BoundRule.POINT:
            return self.ratio
        if rule is BoundRule.LOWER:
            return self.lower
        return 1.0

    def capped(self, *, native: bool) -> float:
        """The same estimate capped at 1, so applying it can only lower savings."""
        return min(self.ratio if native else self.lower, 1.0)


def volume_ratio(
    comparison: Mapping[str, TokenUsage],
    served: Mapping[str, TokenUsage],
    volume: Callable[[TokenUsage], float],
    *,
    draws: int = BOOTSTRAP_DRAWS,
    interval: float = BOOTSTRAP_INTERVAL,
    seed: int = BOOTSTRAP_SEED,
) -> EfficiencyEstimate:
    """Ratio of summed per-task volumes over the tasks both cells ran, with a
    task-bootstrap interval. Per-task volumes are heavy-tailed, so a mean of
    per-task ratios would be dominated by one task."""
    tasks = sorted(set(comparison) & set(served))
    if not tasks:
        raise ValueError("cells share no tasks")
    pairs = [(volume(comparison[task]), volume(served[task])) for task in tasks]

    def ratio_of(sample: Sequence[tuple[float, float]]) -> float:
        served_volume = sum(served_value for _, served_value in sample)
        if not served_volume:
            return float("inf")
        return sum(comparison_value for comparison_value, _ in sample) / served_volume

    generator = random.Random(seed)
    resampled = sorted(ratio_of([generator.choice(pairs) for _ in pairs]) for _ in range(draws))
    tail = (1 - interval) / 2
    return EfficiencyEstimate(
        ratio=ratio_of(pairs),
        lower=resampled[int(tail * draws)],
        upper=resampled[min(int((1 - tail) * draws), draws - 1)],
        tasks=len(tasks),
    )


@dataclass(frozen=True)
class WorkRatios:
    """Input and output stay apart because output tokens cost several times input."""

    input: EfficiencyEstimate
    output: EfficiencyEstimate
    native: bool

    @property
    def input_credited(self) -> float:
        return self.input.credited(native=self.native)

    @property
    def output_credited(self) -> float:
        return self.output.credited(native=self.native)

    @property
    def input_capped(self) -> float:
        return self.input.capped(native=self.native)

    @property
    def output_capped(self) -> float:
        return self.output.capped(native=self.native)


def work_ratios(comparison: Mapping[str, TokenUsage], served: Mapping[str, TokenUsage], *, native: bool) -> WorkRatios:
    return WorkRatios(
        input=volume_ratio(comparison, served, lambda usage: usage.input_total),
        output=volume_ratio(comparison, served, lambda usage: usage.output),
        native=native,
    )
