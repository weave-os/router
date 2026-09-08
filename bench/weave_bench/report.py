"""Per-arm summaries and paired comparisons over loaded trials."""

from __future__ import annotations

import json
from collections import Counter, defaultdict
from collections.abc import Iterable, Sequence
from dataclasses import asdict, dataclass
from pathlib import Path

from weave_bench.analytics import (
    AnalyticsColumn,
    DecisionRow,
    by_session,
    row_cost_usd,
    row_fresh_input_tokens,
    row_int,
)
from weave_bench.arms import ArmSpec, Upstream
from weave_bench.prices import PriceTable, load_price_table
from weave_bench.stats import (
    Interval,
    McNemar,
    WinsTiesLosses,
    bootstrap_mean,
    mcnemar,
    mean_pass_at_k,
    paired_delta,
    percentile,
    sign_test_p,
    wilson,
    wins_ties_losses,
)
from weave_bench.trials import ErrorCategory, TrialRecord

TapRow = dict[str, object]


@dataclass(frozen=True)
class TokenTotals:
    requests: int
    input_tokens: int
    cache_read_tokens: int
    cache_write_tokens: int
    output_tokens: int


@dataclass(frozen=True)
class ArmSummary:
    arm: str
    n_tasks: int
    n_trials: int
    n_passed: int
    n_errored: int
    trial_pass_rate: Interval
    task_mean_pass_rate: Interval
    pass_at_k: float
    k: int
    all_attempts_pass_rate: float
    mean_rubric_score: float | None
    per_task_pass_rate: dict[str, float]
    error_categories: dict[str, int]
    agent_seconds_median: float
    agent_seconds_p90: float
    wall_clock_seconds: float
    codex_client_tokens: TokenTotals
    agent_cost_usd: float | None
    router_billed_usd: float | None
    # The same served tokens repriced at catalog list price; the gap to
    # ``router_billed_usd`` is the router's subscription/discount effect.
    router_list_price_usd: float | None
    router_tokens: TokenTotals | None
    router_served_models: dict[str, int]
    sessions_without_router_rows: list[str]
    n_beta_acknowledged: int | None
    tap_cost_usd: float | None
    tap_served_models: dict[str, int]
    tap_requests: int


@dataclass(frozen=True)
class PairSummary:
    router_arm: str
    control_arm: str
    n_common_tasks: int
    delta_task_mean: Interval
    wins_ties_losses: WinsTiesLosses
    sign_test_p: float
    mcnemar: McNemar
    mcnemar_chi2_corrected: float
    mcnemar_exact_p: float
    pass_at_k_router: float
    pass_at_k_control: float
    cost_ratio: float | None


@dataclass(frozen=True)
class BenchReport:
    benchmark: str
    run_id: str
    arms: list[ArmSummary]
    pairs: list[PairSummary]

    def to_json(self) -> str:
        return json.dumps(asdict(self), indent=2, sort_keys=True)


def per_task_pass_rate(trials: Sequence[TrialRecord]) -> dict[str, float]:
    attempts: dict[str, list[bool]] = defaultdict(list)
    for trial in trials:
        attempts[trial.task].append(trial.passed)
    return {task: sum(passes) / len(passes) for task, passes in attempts.items()}


def _attempts_by_task(trials: Sequence[TrialRecord]) -> dict[str, list[bool]]:
    attempts: dict[str, list[bool]] = defaultdict(list)
    for trial in trials:
        attempts[trial.task].append(trial.passed)
    return attempts


def _codex_client_tokens(trials: Sequence[TrialRecord]) -> TokenTotals:
    turns = [turn for trial in trials for turn in trial.codex_turns]
    return TokenTotals(
        requests=len(turns),
        input_tokens=sum(t.input_tokens - t.cached_input_tokens for t in turns),
        cache_read_tokens=sum(t.cached_input_tokens for t in turns),
        cache_write_tokens=0,
        output_tokens=sum(t.output_tokens for t in turns),
    )


def _router_tokens(rows: Sequence[DecisionRow]) -> TokenTotals:
    return TokenTotals(
        requests=len(rows),
        input_tokens=sum(row_fresh_input_tokens(r) for r in rows),
        cache_read_tokens=sum(row_int(r, AnalyticsColumn.CACHE_READ_TOKENS) for r in rows),
        cache_write_tokens=sum(row_int(r, AnalyticsColumn.CACHE_CREATION_TOKENS) for r in rows),
        output_tokens=sum(row_int(r, AnalyticsColumn.OUTPUT_TOKENS) for r in rows),
    )


def _router_list_price_usd(rows: Sequence[DecisionRow], prices: PriceTable) -> float | None:
    total = 0.0
    for row in rows:
        price = prices.lookup(str(row.get(AnalyticsColumn.DECISION_MODEL, "")))
        if price is None:
            return None
        total += price.cost_usd(
            input_tokens=row_fresh_input_tokens(row),
            cache_read_tokens=row_int(row, AnalyticsColumn.CACHE_READ_TOKENS),
            cache_write_tokens=row_int(row, AnalyticsColumn.CACHE_CREATION_TOKENS),
            output_tokens=row_int(row, AnalyticsColumn.OUTPUT_TOKENS),
        )
    return total


def _direct_list_price_usd(arm: ArmSpec, trials: Sequence[TrialRecord], prices: PriceTable) -> float | None:
    """A direct arm's spend repriced from its own Codex transcript at the pinned
    catalog list price, so it doesn't depend on Harbor's optional LiteLLM cost."""
    price = prices.lookup(arm.codex_model)
    if price is None:
        return None
    tokens = _codex_client_tokens(trials)
    if tokens.requests == 0:
        return None
    return price.cost_usd(
        input_tokens=tokens.input_tokens,
        cache_read_tokens=tokens.cache_read_tokens,
        cache_write_tokens=tokens.cache_write_tokens,
        output_tokens=tokens.output_tokens,
    )


def _optional_sum(values: Iterable[float | None]) -> float | None:
    present = [v for v in values if v is not None]
    return sum(present) if present else None


def summarize_arm(
    arm: ArmSpec,
    trials: Sequence[TrialRecord],
    *,
    k: int,
    analytics_rows: Sequence[DecisionRow] | None,
    tap_rows: Sequence[TapRow],
) -> ArmSummary:
    per_task = per_task_pass_rate(trials)
    attempts = _attempts_by_task(trials)
    n_passed = sum(t.passed for t in trials)
    rubric_scores = [t.rubric_score for t in trials if t.rubric_score is not None]
    agent_seconds = [t.agent_seconds for t in trials if t.agent_seconds > 0]
    session_ids = {t.session_id for t in trials if t.session_id}

    router_rows: list[DecisionRow] = []
    served_models: Counter[str] = Counter()
    sessions_without_rows: list[str] = []
    if analytics_rows is not None and arm.via_router:
        rows_by_session = by_session(analytics_rows)
        for trial in trials:
            session_rows = rows_by_session.get(trial.session_id or "", [])
            if not session_rows:
                sessions_without_rows.append(trial.trial_name)
            router_rows.extend(session_rows)
        served_models.update(str(r.get(AnalyticsColumn.DECISION_MODEL, "")) for r in router_rows)

    arm_tap_rows = [r for r in tap_rows if str(r.get("session_id", "")) in session_ids]
    tap_served: Counter[str] = Counter(str(r.get("served_model") or "") for r in arm_tap_rows)

    prices = load_price_table()
    agent_cost_usd = _optional_sum(t.agent_cost_usd for t in trials)
    if arm.upstream is Upstream.DIRECT:
        list_price_usd = _direct_list_price_usd(arm, trials, prices)
        if list_price_usd is not None:
            agent_cost_usd = list_price_usd

    return ArmSummary(
        arm=arm.name,
        n_tasks=len(per_task),
        n_trials=len(trials),
        n_passed=n_passed,
        n_errored=sum(t.errored for t in trials),
        trial_pass_rate=wilson(n_passed, len(trials)),
        task_mean_pass_rate=bootstrap_mean(list(per_task.values())),
        pass_at_k=mean_pass_at_k(attempts, k),
        k=k,
        all_attempts_pass_rate=(sum(all(a) for a in attempts.values()) / len(attempts)) if attempts else 0.0,
        mean_rubric_score=(sum(rubric_scores) / len(rubric_scores)) if rubric_scores else None,
        per_task_pass_rate=dict(sorted(per_task.items())),
        error_categories={
            category.value: count
            for category, count in Counter(t.error_category for t in trials if t.errored).items()
            if category is not ErrorCategory.NONE
        },
        agent_seconds_median=percentile(agent_seconds, 0.5),
        agent_seconds_p90=percentile(agent_seconds, 0.9),
        wall_clock_seconds=(
            (max(t.finished_at for t in trials) - min(t.started_at for t in trials)).total_seconds() if trials else 0.0
        ),
        codex_client_tokens=_codex_client_tokens(trials),
        agent_cost_usd=agent_cost_usd,
        router_billed_usd=sum(row_cost_usd(r) for r in router_rows) if router_rows else None,
        router_list_price_usd=_router_list_price_usd(router_rows, prices) if router_rows else None,
        router_tokens=_router_tokens(router_rows) if router_rows else None,
        router_served_models=dict(served_models.most_common()),
        sessions_without_router_rows=sessions_without_rows,
        n_beta_acknowledged=sum(bool(t.beta_acknowledged) for t in trials) if arm.beta_preflight else None,
        tap_cost_usd=_optional_sum(
            float(r["cost_usd"]) if isinstance(r.get("cost_usd"), (int, float)) else None for r in arm_tap_rows
        ),
        tap_served_models=dict(tap_served.most_common()),
        tap_requests=len(arm_tap_rows),
    )


def arm_spend_usd(summary: ArmSummary) -> float | None:
    """What the arm actually cost: router-billed for routed arms, the tap's
    OpenRouter ``usage.cost`` for tap arms, Codex's own list-price total otherwise."""
    if summary.router_billed_usd is not None:
        return summary.router_billed_usd
    if summary.tap_cost_usd is not None:
        return summary.tap_cost_usd
    return summary.agent_cost_usd


def compare(
    router: ArmSummary,
    control: ArmSummary,
    router_trials: Sequence[TrialRecord],
    control_trials: Sequence[TrialRecord],
) -> PairSummary:
    outcome = wins_ties_losses(router.per_task_pass_rate, control.per_task_pass_rate)
    discordant = mcnemar(
        {t.pairing_key: t.passed for t in router_trials},
        {t.pairing_key: t.passed for t in control_trials},
    )
    router_spend, control_spend = arm_spend_usd(router), arm_spend_usd(control)
    return PairSummary(
        router_arm=router.arm,
        control_arm=control.arm,
        n_common_tasks=len(set(router.per_task_pass_rate) & set(control.per_task_pass_rate)),
        delta_task_mean=paired_delta(router.per_task_pass_rate, control.per_task_pass_rate),
        wins_ties_losses=outcome,
        sign_test_p=sign_test_p(outcome),
        mcnemar=discordant,
        mcnemar_chi2_corrected=discordant.chi2_corrected,
        mcnemar_exact_p=discordant.exact_p,
        pass_at_k_router=router.pass_at_k,
        pass_at_k_control=control.pass_at_k,
        cost_ratio=(router_spend / control_spend) if router_spend and control_spend else None,
    )


def build_report(
    *,
    benchmark: str,
    run_id: str,
    trials_by_arm: dict[ArmSpec, list[TrialRecord]],
    k: int,
    analytics_rows: Sequence[DecisionRow] | None,
    tap_rows: Sequence[TapRow],
) -> BenchReport:
    summaries = {
        arm: summarize_arm(arm, trials, k=k, analytics_rows=analytics_rows, tap_rows=tap_rows)
        for arm, trials in trials_by_arm.items()
    }
    # Every /beta arm is compared against every other arm; without one (e.g. a
    # force-model-vs-direct check) the first listed arm is the treatment.
    treatment_arms = [arm for arm in summaries if arm.beta_preflight] or list(summaries)[:1]
    pairs = [
        compare(summaries[treatment], summaries[control], trials_by_arm[treatment], trials_by_arm[control])
        for treatment in treatment_arms
        for control in summaries
        if control not in treatment_arms
    ]
    return BenchReport(benchmark=benchmark, run_id=run_id, arms=list(summaries.values()), pairs=pairs)


def load_tap_rows(path: Path | None) -> list[TapRow]:
    if path is None or not path.exists():
        return []
    return [json.loads(line) for line in path.read_text().splitlines() if line.strip()]
