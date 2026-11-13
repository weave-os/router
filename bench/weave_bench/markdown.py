"""Render a ``BenchReport`` in the RESULTS_*.md table layout."""

from __future__ import annotations

from weave_bench.report import ArmSummary, BenchReport, PairSummary, TokenTotals, arm_spend_usd
from weave_bench.stats import Interval


def _pct(value: float) -> str:
    return f"{100 * value:.1f}%"


def _interval(interval: Interval) -> str:
    return f"{_pct(interval.point)} [{100 * interval.low:.1f}, {100 * interval.high:.1f}]"


def _pp(interval: Interval) -> str:
    return f"{100 * interval.point:+.1f} pp [{100 * interval.low:+.1f}, {100 * interval.high:+.1f}]"


def _usd(value: float | None) -> str:
    return f"${value:,.2f}" if value is not None else "—"


def _minutes(seconds: float) -> str:
    return f"{seconds / 60:.1f} min"


def _spend_cell(summary: ArmSummary) -> str:
    spend = arm_spend_usd(summary)
    if summary.router_billed_usd is not None:
        list_price = (
            f" ({_usd(summary.router_list_price_usd)} list)" if summary.router_list_price_usd is not None else ""
        )
        return f"{_usd(spend)} router-billed{list_price}"
    if summary.tap_cost_usd is not None:
        return f"{_usd(spend)} OpenRouter-billed"
    return f"{_usd(spend)} list"


def _served_mix(served: dict[str, int]) -> str:
    total = sum(served.values())
    if not total:
        return "—"
    return " · ".join(f"{model} {count:,} ({100 * count / total:.1f}%)" for model, count in served.items())


def _tokens_row(label: str, totals: TokenTotals) -> str:
    return (
        f"| {label} | {totals.requests:,} | {totals.input_tokens:,} | {totals.cache_read_tokens:,} "
        f"| {totals.cache_write_tokens:,} | {totals.output_tokens:,} |"
    )


def _arm_row(summary: ArmSummary) -> str:
    spend = arm_spend_usd(summary)
    rubric = f"{summary.mean_rubric_score:.3f}" if summary.mean_rubric_score is not None else "—"
    per_trial = _usd(spend / summary.n_trials) if spend is not None and summary.n_trials else "—"
    cells = (
        f"`{summary.arm}`",
        f"**{_interval(summary.trial_pass_rate)}**",
        _interval(summary.task_mean_pass_rate),
        _pct(summary.pass_at_k),
        _pct(summary.all_attempts_pass_rate),
        rubric,
        str(summary.n_errored),
        f"**{_spend_cell(summary)}**",
        per_trial,
        _minutes(summary.agent_seconds_median),
        _minutes(summary.agent_seconds_p90),
    )
    return "| " + " | ".join(cells) + " |"


def _pair_row(pair: PairSummary) -> str:
    wtl = pair.wins_ties_losses
    ratio = f"{pair.cost_ratio:.2f}×" if pair.cost_ratio is not None else "—"
    return (
        f"| `{pair.router_arm}` vs `{pair.control_arm}` | **{_pp(pair.delta_task_mean)}** "
        f"| {wtl.wins} / {wtl.ties} / {wtl.losses} | {pair.sign_test_p:.2f} "
        f"| {pair.mcnemar_chi2_corrected:.2f} (exact p {pair.mcnemar_exact_p:.2f}) "
        f"| {pair.mcnemar.a_only} / {pair.mcnemar.b_only} "
        f"| {_pct(pair.pass_at_k_router)} vs {_pct(pair.pass_at_k_control)} | **{ratio}** |"
    )


def render_markdown(report: BenchReport) -> str:
    k = report.arms[0].k if report.arms else 1
    lines = [
        f"# {report.benchmark} — run `{report.run_id}`",
        "",
        "## Headline",
        "",
        f"| Arm | Trial pass (95% Wilson) | Task-mean pass (95% CI, task-level) | pass@{k} | all-{k} pass "
        "| Rubric mean | Errored trials | Cost (arm) | $/trial | Median agent time | P90 agent time |",
        "|---|---|---|---|---|---|---|---|---|---|---|",
        *(_arm_row(summary) for summary in report.arms),
    ]
    if report.pairs:
        lines += [
            "",
            "## Paired comparison",
            "",
            f"| Pair | Δ task-mean pass (95% CI) | Router wins / ties / losses | Sign test p | McNemar χ² (cc) "
            f"| Trial pairs router-only / control-only | pass@{k} router vs control | Cost ratio router/control |",
            "|---|---|---|---|---|---|---|---|",
            *(_pair_row(pair) for pair in report.pairs),
        ]
    lines += [
        "",
        "## Served model mix",
        "",
        "| Arm | Source | Served model → requests (share) | Beta acks |",
        "|---|---|---|---|",
    ]
    for summary in report.arms:
        if summary.router_served_models:
            acks = f"{summary.n_beta_acknowledged}/{summary.n_trials}"
            lines.append(
                f"| `{summary.arm}` | router analytics | {_served_mix(summary.router_served_models)} | {acks} |"
            )
        elif summary.tap_served_models:
            lines.append(f"| `{summary.arm}` | OpenRouter tap | {_served_mix(summary.tap_served_models)} | — |")
        else:
            lines.append(f"| `{summary.arm}` | Codex client | pinned model (100%) | — |")
    lines += [
        "",
        "## Errors",
        "",
        "| Arm | " + " | ".join(f"`{s.arm}`" for s in report.arms) + " |",
        "|---|" + "---|" * len(report.arms),
    ]
    categories = sorted({category for s in report.arms for category in s.error_categories})
    for category in categories:
        counts = " | ".join(str(s.error_categories.get(category, 0)) for s in report.arms)
        lines.append(f"| {category} | {counts} |")
    lines.append("| **Total errored** | " + " | ".join(f"**{s.n_errored}**" for s in report.arms) + " |")
    lines += [
        "",
        "## Tokens",
        "",
        "| Arm (source) | requests | fresh input | cache read | cache write | output |",
        "|---|---|---|---|---|---|",
    ]
    for summary in report.arms:
        if summary.router_tokens is not None:
            lines.append(_tokens_row(f"`{summary.arm}` (router analytics)", summary.router_tokens))
        lines.append(_tokens_row(f"`{summary.arm}` (Codex client view)", summary.codex_client_tokens))
    unrouted = [(s.arm, s.sessions_without_router_rows) for s in report.arms if s.sessions_without_router_rows]
    if unrouted:
        lines += ["", "## Router trials with no analytics rows", ""]
        for arm, trial_names in unrouted:
            lines.append(
                f"- `{arm}`: {len(trial_names)} trials — {', '.join(trial_names[:10])}{' …' if len(trial_names) > 10 else ''}"
            )
    lines += [
        "",
        "Cache-write tokens are only visible in router analytics (`cache_creation_tokens`); "
        "Codex's client view reports fresh vs cached input only.",
    ]
    return "\n".join(lines) + "\n"
