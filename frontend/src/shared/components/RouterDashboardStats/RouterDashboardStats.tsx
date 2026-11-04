"use client";

import { formatDistanceToNow } from "date-fns";

import { Icon } from "@/shared/components/atoms/Icon";
import { MetricValue } from "@/shared/components/atoms/MetricValue";
import { Skeleton } from "@/shared/components/atoms/Skeleton";
import { CHART_CARD_TITLE_CLASS } from "@/shared/components/ChartCard";
import { ErrorCard } from "@/shared/components/ErrorCard";
import { LoadStateWrapper } from "@/shared/components/LoadStateWrapper";
import { Tooltip } from "@/shared/components/molecules/Tooltip";
import {
  RouterMetricGroup,
  RouterMetricType,
  RouterRoutingState,
  type DateRange,
} from "@/shared/data";
import { useRouterMetricSeries, useRouterReportSummary } from "@/shared/data/useRouterMetricSeries";
import { cn } from "@/shared/tools/cn";
import { formatBigDecimal, formatUSD } from "@/shared/tools/format";
import { LoadState } from "@/shared/tools/LoadState";
import { TypographyRole } from "@/shared/tools/typography";

export enum RouterSummaryCard {
  RouterSpend = "router-spend",
  Requests = "requests",
  Savings = "savings",
  Tokens = "tokens",
  TotalSpend = "total-spend",
}

export const SPEND_SUMMARY_CARDS: ReadonlySet<RouterSummaryCard> = new Set([
  RouterSummaryCard.TotalSpend,
  RouterSummaryCard.RouterSpend,
  RouterSummaryCard.Savings,
]);

export const ACTIVITY_SUMMARY_CARDS: ReadonlySet<RouterSummaryCard> = new Set([
  RouterSummaryCard.Requests,
  RouterSummaryCard.Tokens,
]);

const CARD_LABEL: Readonly<Record<RouterSummaryCard, string>> = {
  [RouterSummaryCard.RouterSpend]: "Router spend",
  [RouterSummaryCard.Requests]: "Requests",
  [RouterSummaryCard.Savings]: "Saved vs comparison model",
  [RouterSummaryCard.Tokens]: "Tokens",
  [RouterSummaryCard.TotalSpend]: "Total spend",
};

const CARD_ORDER: readonly RouterSummaryCard[] = [
  RouterSummaryCard.TotalSpend,
  RouterSummaryCard.RouterSpend,
  RouterSummaryCard.Savings,
  RouterSummaryCard.Requests,
  RouterSummaryCard.Tokens,
];

function savingsExplanation(comparisonModel: string) {
  return (
    `Compared with sending every request to ${comparisonModel}, adjusted for measured token ` +
    `use and task success. Passthrough, forced, utility, subscription and unmeasured requests ` +
    `add no savings.`
  );
}

function isDisplaySize(card: RouterSummaryCard) {
  return card === RouterSummaryCard.Requests || card === RouterSummaryCard.Tokens;
}

export interface RouterDashboardStatsProps {
  cards: ReadonlySet<RouterSummaryCard>;
  className?: string;
  /** Where "How we measure" on the savings card links to. */
  methodologyHref?: string;
  range: DateRange;
}

/** The stat cards above each overview tab: totals for the selected range. */
export function RouterDashboardStats({
  cards,
  className,
  methodologyHref,
  range,
}: RouterDashboardStatsProps) {
  const summaryState = useRouterReportSummary(range);
  const routingState = useRouterMetricSeries({
    granularity: null,
    group: RouterMetricGroup.RoutingState,
    range,
    type: RouterMetricType.Cost,
  });
  const routedSpend = LoadState.map(
    routingState,
    series =>
      series.values.find(point => point.group === RouterRoutingState.Routed)?.value ?? 0,
  );
  const visible = CARD_ORDER.filter(card => cards.has(card));

  return (
    <div
      aria-live="polite"
      className={cn(
        "grid grid-cols-1 gap-2.5 @xl:grid-cols-[repeat(auto-fit,minmax(12rem,1fr))]",
        className,
      )}
    >
      <LoadStateWrapper
        state={summaryState}
        loading={visible.map(card => (
          <StatCard
            key={card}
            label={CARD_LABEL[card]}
            loading
            size={isDisplaySize(card) ? TypographyRole.Display : TypographyRole.PageTitle}
            value="—"
          />
        ))}
        error={() => (
          <ErrorCard
            className="col-span-full"
            title="Could not load router totals"
            description="Refresh to try again."
          />
        )}
      >
        {summary => {
          const savingsRate =
            summary.comparisonCostUSD > 0 ? summary.savingsUSD / summary.comparisonCostUSD : null;
          const routed = LoadState.unwrap(routedSpend);
          const avgTokens =
            summary.requestCount > 0 ? summary.totalTokens / summary.requestCount : null;
          return (
            <>
              {cards.has(RouterSummaryCard.TotalSpend) && (
                <StatCard
                  label="Total spend"
                  sub="Estimated API cost; subscription requests $0"
                  value={formatUSD(summary.totalCostUSD)}
                />
              )}
              {cards.has(RouterSummaryCard.RouterSpend) && (
                <StatCard
                  label="Router spend"
                  loading={routed == null}
                  sub="Routed users only; passthrough users excluded"
                  value={formatUSD(routed ?? 0)}
                />
              )}
              {cards.has(RouterSummaryCard.Savings) && (
                <StatCard
                  accent={summary.savingsUSD < 0 ? "danger" : "success"}
                  label={`Saved vs ${summary.comparisonModel}`}
                  methodologyHref={methodologyHref}
                  sub={
                    savingsRate == null
                      ? undefined
                      : `${(savingsRate * 100).toFixed(1)}% saved vs ${summary.comparisonModel}`
                  }
                  tooltip={savingsExplanation(summary.comparisonModel)}
                  value={formatUSD(summary.savingsUSD)}
                />
              )}
              {cards.has(RouterSummaryCard.Requests) && (
                <StatCard
                  label="Requests"
                  note={summary.requestCount === 0 ? "No requests in this range" : undefined}
                  size={TypographyRole.Display}
                  sub={
                    summary.lastRequestAt == null
                      ? undefined
                      : `Latest request started ${formatDistanceToNow(
                          new Date(summary.lastRequestAt),
                        )} ago`
                  }
                  value={formatBigDecimal(summary.requestCount)}
                />
              )}
              {cards.has(RouterSummaryCard.Tokens) && (
                <StatCard
                  label="Tokens"
                  size={TypographyRole.Display}
                  sub={avgTokens == null ? undefined : `${formatBigDecimal(avgTokens)} avg / req`}
                  value={formatBigDecimal(summary.totalTokens)}
                />
              )}
            </>
          );
        }}
      </LoadStateWrapper>
    </div>
  );
}

interface StatCardProps {
  accent?: "danger" | "default" | "success";
  label: string;
  loading?: boolean;
  methodologyHref?: string;
  note?: string;
  size?: TypographyRole.Display | TypographyRole.PageTitle;
  sub?: string;
  tooltip?: string;
  value: string;
}

function StatCard({
  accent = "default",
  label,
  loading = false,
  methodologyHref,
  note,
  size = TypographyRole.PageTitle,
  sub,
  tooltip,
  value,
}: StatCardProps) {
  const accentClass =
    accent === "success"
      ? "text-wv-status-success"
      : accent === "danger"
        ? "text-wv-status-danger"
        : "text-wv-primary";
  const card = (
    <div
      {...(tooltip == null ? {} : { tabIndex: 0 })}
      className={cn(
        "card-default surface-card flex flex-col rounded-lg p-4 text-left",
        tooltip != null &&
          "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-wv-focus",
      )}
    >
      <span
        className={cn(
          CHART_CARD_TITLE_CLASS,
          "text-card-title text-[var(--wv-card-heading)]",
          methodologyHref != null && "pr-28",
        )}
      >
        {label}
        {tooltip != null && <Icon type="Info" className="size-3.5" aria-hidden />}
      </span>
      <span className="mt-2 block">
        {loading ? (
          <Skeleton.Text
            as="span"
            size={size === TypographyRole.Display ? "h1" : "h3"}
            className="block w-24"
          />
        ) : (
          <MetricValue
            className={cn("block tracking-tight", accentClass)}
            size={size}
            value={value}
          />
        )}
        {sub != null && !loading && (
          <span className="mt-1 block text-caption text-wv-secondary" title={sub}>
            {sub}
          </span>
        )}
        {note != null && !loading && (
          <span aria-live="polite" className="mt-0.5 block text-caption italic text-wv-secondary">
            {note}
          </span>
        )}
      </span>
    </div>
  );
  const explainedCard =
    tooltip == null ? (
      card
    ) : (
      <Tooltip content={tooltip} contentClassName="max-w-xs" interactiveChild>
        {card}
      </Tooltip>
    );
  if (methodologyHref == null) return explainedCard;
  return (
    <div className="relative flex flex-col">
      {explainedCard}
      <a
        href={methodologyHref}
        target="_blank"
        rel="noreferrer"
        className="absolute right-2 top-2 flex min-h-11 items-center rounded px-2 text-caption text-wv-secondary underline underline-offset-2 transition-colors hover:text-wv-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-wv-focus"
      >
        How we measure
      </a>
    </div>
  );
}
