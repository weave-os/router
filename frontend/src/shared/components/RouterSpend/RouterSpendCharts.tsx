"use client";

import { type ReactNode, useMemo, useState } from "react";

import { Skeleton } from "@/shared/components/atoms/Skeleton";
import { Text } from "@/shared/components/atoms/Text";
import {
  Chart,
  ChartAxisType,
  type ChartData,
  type ChartSeriesConfig,
  ChartSeriesType,
} from "@/shared/components/Chart";
import { ChartCard } from "@/shared/components/ChartCard";
import { type Granularity } from "@/shared/components/DashboardPageFilters/useDashboardFilters";
import { ErrorCard } from "@/shared/components/ErrorCard";
import { LoadStateWrapper } from "@/shared/components/LoadStateWrapper";
import { ChartTabToggle } from "@/shared/components/RouterCharts/ChartTabToggle";
import { displayGranularity, previousRange } from "@/shared/components/RouterCharts/chartRange";
import { TIME_KEY } from "@/shared/components/RouterCharts/pivotSeries";
import { periodPercentChange, RouterChartHeadline } from "@/shared/components/RouterCharts/RouterChartHeadline";
import {
  type DateRange,
  RouterCostType,
  RouterMetricGroup,
  type RouterMetricPoint,
  RouterMetricType,
} from "@/shared/data";
import { useRouterMetricSeries, useRouterReportSummary } from "@/shared/data/useRouterMetricSeries";
import { MODEL_FAMILY_COLOR, MODEL_FAMILY_LABEL, modelFamily, type RouterModelFamily } from "@/shared/objects/RouterModelFamily";
import { ChartColorToken } from "@/shared/tools/chartColors";
import { formatPercentage, formatUSD } from "@/shared/tools/format";
import { LoadState } from "@/shared/tools/LoadState";
import { addGranularity, formatInterval, formatTime } from "@/shared/tools/timeGranularity";

interface SpendChartProps {
  className?: string;
  granularity: Granularity;
  range: DateRange;
}

interface CostBucket {
  actual: number;
  comparison: number;
  time: number;
}

type ChartRows = ChartData<typeof TIME_KEY, string, number, number>;

const COMPARISON_FALLBACK = "the comparison model";
const NO_PERIOD_CHANGE = LoadState.loaded(null);
const SAVINGS_KEY = "saved";
const EXTRA_COST_KEY = "extra";

export enum RouterCostView {
  Breakdown = "breakdown",
  Trend = "trend",
}

const COST_VIEW_OPTIONS = [
  { label: "Trend", value: RouterCostView.Trend },
  { label: "Breakdown", value: RouterCostView.Breakdown },
] as const;

/** Catalog and comparison cost per bucket, oldest first. */
export function costBuckets(points: readonly RouterMetricPoint[]): CostBucket[] {
  const byTime = new Map<number, CostBucket>();
  for (const { group, time, value } of points) {
    const bucketTime = time == null ? 0 : new Date(time).getTime();
    const bucket = byTime.get(bucketTime) ?? { actual: 0, comparison: 0, time: bucketTime };
    if (group === RouterCostType.Actual) bucket.actual += value;
    else if (group === RouterCostType.Comparison) bucket.comparison += value;
    byTime.set(bucketTime, bucket);
  }
  return [...byTime.values()].sort((a, b) => a.time - b.time);
}

function costTotals(points: readonly RouterMetricPoint[]) {
  return costBuckets(points).reduce(
    (sum, { actual, comparison }) => ({ actual: sum.actual + actual, comparison: sum.comparison + comparison }),
    { actual: 0, comparison: 0 },
  );
}

function savingsRate({ actual, comparison }: { actual: number; comparison: number }) {
  return comparison === 0 ? null : (comparison - actual) / comparison;
}

function formatSavings(savings: number) {
  return savings >= 0 ? `${formatUSD(savings)} saved` : `${formatUSD(Math.abs(savings))} extra`;
}

function useComparisonLabel(range: DateRange) {
  const summary = useRouterReportSummary(range);
  return LoadState.unwrap(summary)?.comparisonModel || COMPARISON_FALLBACK;
}

/** Bucketed cost for the range plus the same-length window before it, for period change. */
function useCostSeries(granularity: Granularity, range: DateRange) {
  const current = useRouterMetricSeries({
    granularity,
    group: RouterMetricGroup.CostType,
    range,
    type: RouterMetricType.Cost,
  });
  const previous = useRouterMetricSeries({
    granularity: null,
    group: RouterMetricGroup.CostType,
    range: previousRange(range),
    type: RouterMetricType.Cost,
  });
  return { current, previous };
}

function TimeChart({
  config,
  dependentKeys,
  formatValue,
  granularity,
  referenceLines,
  rows,
}: {
  config: Record<string, ChartSeriesConfig<number, number>>;
  dependentKeys: readonly string[];
  formatValue: (value: number) => string;
  granularity: Granularity;
  referenceLines?: LoadState<readonly { type: "dependent"; value: number }[]>;
  rows: LoadState<readonly Record<string, number>[]>;
}) {
  const timeGranularity = displayGranularity(granularity);
  const keys = useMemo(() => LoadState.loaded(dependentKeys), [dependentKeys]);
  return (
    <Chart
      config={config}
      data={LoadState.map(rows, values => values as ChartRows)}
      dependentKeys={keys}
      groups={LoadState.loaded([dependentKeys])}
      independentKey={TIME_KEY}
      independentType={ChartAxisType.Time}
      independentDomain={([min, max]) => [
        addGranularity(min, timeGranularity, -1).getTime(),
        addGranularity(max, timeGranularity, 1).getTime(),
      ]}
      formatValue={formatValue}
      formatIndependentValue={value => formatTime(value, timeGranularity)}
      formatIndependentValueTooltip={value => formatInterval(value, timeGranularity)}
      referenceLines={referenceLines}
    />
  );
}

const TREND_KEYS = [RouterCostType.Actual, RouterCostType.Comparison] as const;
const BREAKDOWN_KEYS = [RouterCostType.Actual, SAVINGS_KEY, EXTRA_COST_KEY] as const;

/** Catalog cost against the comparison model: two lines, or bars split into cost and saving. */
export function RouterCostBreakdownChart({
  className,
  defaultView = RouterCostView.Breakdown,
  granularity,
  range,
}: SpendChartProps & { defaultView?: RouterCostView }) {
  const [view, setView] = useState(defaultView);
  const comparisonModel = useComparisonLabel(range);
  const { current, previous } = useCostSeries(granularity, range);
  const buckets = useMemo(() => LoadState.map(current, ({ values }) => costBuckets(values)), [current]);
  const rows = useMemo(
    () =>
      LoadState.map(buckets, values =>
        values.map(({ actual, comparison, time }): Record<string, number> =>
          view === RouterCostView.Trend
            ? { [RouterCostType.Actual]: actual, [RouterCostType.Comparison]: comparison, [TIME_KEY]: time }
            : {
                [EXTRA_COST_KEY]: Math.max(actual - comparison, 0),
                [RouterCostType.Actual]: Math.min(actual, comparison),
                [SAVINGS_KEY]: Math.max(comparison - actual, 0),
                [TIME_KEY]: time,
              },
        ),
      ),
    [buckets, view],
  );
  const config: Record<string, ChartSeriesConfig<number, number>> =
    view === RouterCostView.Trend
      ? {
          [RouterCostType.Actual]: { color: ChartColorToken.Blue, label: "Catalog cost", type: ChartSeriesType.Line },
          [RouterCostType.Comparison]: {
            color: ChartColorToken.Red,
            label: `Comparison cost (${comparisonModel})`,
            type: ChartSeriesType.Line,
          },
        }
      : {
          [EXTRA_COST_KEY]: {
            color: ChartColorToken.Red,
            dashed: true,
            label: `Extra cost vs ${comparisonModel}`,
            type: ChartSeriesType.Bar,
          },
          [RouterCostType.Actual]: { color: ChartColorToken.Scale1, label: "Catalog cost", type: ChartSeriesType.Bar },
          [SAVINGS_KEY]: { color: ChartColorToken.Green, label: `Saved vs ${comparisonModel}`, type: ChartSeriesType.Bar },
        };
  const savings = LoadState.map(current, ({ values }) => {
    const { actual, comparison } = costTotals(values);
    return comparison - actual;
  });
  const percentChange = LoadState.map(current, previous, (now, before) => {
    const nowTotals = costTotals(now.values);
    const beforeTotals = costTotals(before.values);
    return periodPercentChange(nowTotals.comparison - nowTotals.actual, beforeTotals.comparison - beforeTotals.actual);
  });

  return (
    <ChartCard
      className={className}
      title="Router cost savings"
      subtitle={
        view === RouterCostView.Trend
          ? `Estimated API cost vs ${comparisonModel}. Subscription requests contribute $0.`
          : `Each bar is the larger of catalog cost and ${comparisonModel} cost, split into catalog cost and the saving or extra cost.`
      }
      headline={
        <RouterChartHeadline
          percentChange={percentChange}
          statistic={LoadState.map(savings, value => ({ total: formatSavings(value) }))}
        />
      }
      topRight={<ChartTabToggle ariaLabel="Cost view" onChange={setView} options={COST_VIEW_OPTIONS} value={view} />}
    >
      <TimeChart
        config={config}
        dependentKeys={view === RouterCostView.Trend ? TREND_KEYS : BREAKDOWN_KEYS}
        formatValue={formatUSD}
        granularity={granularity}
        rows={rows}
      />
    </ChartCard>
  );
}

const CUMULATIVE_KEY = "cumulative_savings";
const CUMULATIVE_KEYS = [CUMULATIVE_KEY] as const;

export function RouterCumulativeSavingsChart({ className, granularity, range }: SpendChartProps) {
  const comparisonModel = useComparisonLabel(range);
  const { current } = useCostSeries(granularity, range);
  const rows = useMemo(
    () =>
      LoadState.map(current, ({ values }) => {
        let running = 0;
        return costBuckets(values).map(({ actual, comparison, time }) => {
          running += comparison - actual;
          return { [CUMULATIVE_KEY]: running, [TIME_KEY]: time };
        });
      }),
    [current],
  );
  const total = LoadState.map(rows, values => values.at(-1)?.[CUMULATIVE_KEY] ?? 0);

  return (
    <ChartCard
      className={className}
      title="Cumulative savings"
      subtitle={`Running total of catalog cost saved vs ${comparisonModel} across the selected time range. Below $0, routing has cost more so far.`}
      headline={<RouterChartHeadline percentChange={NO_PERIOD_CHANGE} statistic={LoadState.map(total, value => ({ total: formatSavings(value) }))} />}
    >
      <TimeChart
        config={{
          [CUMULATIVE_KEY]: {
            color: ChartColorToken.Green,
            label: `Cumulative savings vs ${comparisonModel}`,
            opacity: 0.3,
            type: ChartSeriesType.Area,
          },
        }}
        dependentKeys={CUMULATIVE_KEYS}
        formatValue={formatUSD}
        granularity={granularity}
        rows={rows}
      />
    </ChartCard>
  );
}

const RATE_KEY = "savings_rate";
const RATE_KEYS = [RATE_KEY] as const;
const ZERO_REFERENCE_LINES = LoadState.loaded([{ type: "dependent" as const, value: 0 }]);

export function RouterSavingsRateChart({ className, granularity, range }: SpendChartProps) {
  const comparisonModel = useComparisonLabel(range);
  const { current, previous } = useCostSeries(granularity, range);
  const rows = useMemo(
    () =>
      LoadState.map(current, ({ values }) =>
        costBuckets(values).flatMap(bucket => {
          const rate = savingsRate(bucket);
          return rate == null ? [] : [{ [RATE_KEY]: rate, [TIME_KEY]: bucket.time }];
        }),
      ),
    [current],
  );
  const rate = LoadState.map(current, ({ values }) => savingsRate(costTotals(values)));
  const rateChange = LoadState.map(current, previous, (now, before) => {
    const nowRate = savingsRate(costTotals(now.values));
    const beforeRate = savingsRate(costTotals(before.values));
    return nowRate == null || beforeRate == null ? null : (nowRate - beforeRate) * 100;
  });

  return (
    <ChartCard
      className={className}
      title="Savings rate"
      subtitle={`Savings divided by comparison cost (${comparisonModel}) per bucket.`}
      headline={
        <RouterChartHeadline
          changeTooltip="Percentage-point change from the previous period"
          changeUnit="pp"
          percentChange={rateChange}
          statistic={LoadState.map(rate, value => (value == null ? {} : { total: formatPercentage(value) }))}
        />
      }
    >
      <TimeChart
        config={{ [RATE_KEY]: { color: ChartColorToken.Green, label: "Savings rate", type: ChartSeriesType.Line } }}
        dependentKeys={RATE_KEYS}
        formatValue={formatPercentage}
        granularity={granularity}
        referenceLines={ZERO_REFERENCE_LINES}
        rows={rows}
      />
    </ChartCard>
  );
}

const PER_REQUEST_KEY = "cost_per_request";
const PER_REQUEST_KEYS = [PER_REQUEST_KEY] as const;

function totalByTime(points: readonly RouterMetricPoint[]) {
  const totals = new Map<number, number>();
  for (const { time, value } of points) {
    const bucket = time == null ? 0 : new Date(time).getTime();
    totals.set(bucket, (totals.get(bucket) ?? 0) + value);
  }
  return totals;
}

export function RouterCostPerRequestChart({ className, granularity, range }: SpendChartProps) {
  const { current: cost } = useCostSeries(granularity, range);
  const requests = useRouterMetricSeries({ granularity, range, type: RouterMetricType.ModelUsage });
  const rows = useMemo(
    () =>
      LoadState.map(cost, requests, (costSeries, requestSeries) => {
        const requestTotals = totalByTime(requestSeries.values);
        return costBuckets(costSeries.values).flatMap(({ actual, time }) => {
          const count = requestTotals.get(time) ?? 0;
          return count === 0 ? [] : [{ [PER_REQUEST_KEY]: actual / count, [TIME_KEY]: time }];
        });
      }),
    [cost, requests],
  );
  const average = LoadState.map(cost, requests, (costSeries, requestSeries) => {
    const count = requestSeries.values.reduce((sum, { value }) => sum + value, 0);
    return count === 0 ? null : costTotals(costSeries.values).actual / count;
  });

  return (
    <ChartCard
      className={className}
      title="Cost per request"
      subtitle="Catalog cost divided by requests, per bucket. Subscription-served requests count as $0."
      headline={
        <RouterChartHeadline
          percentChange={NO_PERIOD_CHANGE}
          statistic={LoadState.map(average, value => (value == null ? {} : { total: `${formatUSD(value)} avg` }))}
        />
      }
    >
      <TimeChart
        config={{ [PER_REQUEST_KEY]: { color: ChartColorToken.Scale1, label: "Cost per request", type: ChartSeriesType.Line } }}
        dependentKeys={PER_REQUEST_KEYS}
        formatValue={formatUSD}
        granularity={granularity}
        rows={rows}
      />
    </ChartCard>
  );
}

/** Catalog cost over the range per model maker, largest first. */
export function spendByMaker(points: readonly RouterMetricPoint[]) {
  const totals = new Map<RouterModelFamily, number>();
  for (const { group, value } of points) {
    const family = modelFamily(group ?? "");
    totals.set(family, (totals.get(family) ?? 0) + value);
  }
  return [...totals].filter(([, spend]) => spend > 0).sort(([, a], [, b]) => b - a);
}

function RankedBars({ rows }: { rows: readonly (readonly [RouterModelFamily, number])[] }): ReactNode {
  const max = rows[0]?.[1] ?? 0;
  return (
    <ul className="flex flex-col gap-3" aria-label="Catalog cost by model maker">
      {rows.map(([family, spend]) => (
        <li key={family} className="flex flex-col gap-1">
          <div className="flex items-baseline justify-between gap-2">
            <Text className="text-body">{MODEL_FAMILY_LABEL[family]}</Text>
            <Text className="text-body tabular-nums">{formatUSD(spend)}</Text>
          </div>
          <div className="h-2 rounded-sm bg-wv-gray-100">
            <div
              className="h-full rounded-sm"
              style={{ backgroundColor: MODEL_FAMILY_COLOR[family], width: `${max === 0 ? 0 : (spend / max) * 100}%` }}
            />
          </div>
        </li>
      ))}
    </ul>
  );
}

export function RouterSpendByMakerChart({ className, range }: Omit<SpendChartProps, "granularity">) {
  const spend = useRouterMetricSeries({
    granularity: null,
    group: RouterMetricGroup.Model,
    range,
    type: RouterMetricType.ModelSpend,
  });
  const rows = useMemo(() => LoadState.map(spend, ({ values }) => spendByMaker(values)), [spend]);
  const total = LoadState.map(rows, values => values.reduce((sum, [, value]) => sum + value, 0));

  return (
    <ChartCard
      autoHeight
      className={className}
      title="Catalog cost by model maker"
      subtitle="Catalog cost over the range by model maker. Subscription requests contribute $0."
      headline={<RouterChartHeadline percentChange={NO_PERIOD_CHANGE} statistic={LoadState.map(total, value => ({ total: formatUSD(value) }))} />}
    >
      <LoadStateWrapper
        state={rows}
        loading={<Skeleton className="h-40 w-full" />}
        error={() => <ErrorCard title="Couldn't load catalog cost by maker" description="Try refreshing the page." />}
      >
        {values =>
          values.length === 0 ? (
            <ErrorCard title="No API spend yet" description="Route requests through Weave Router to see catalog cost by model maker." />
          ) : (
            <RankedBars rows={values} />
          )
        }
      </LoadStateWrapper>
    </ChartCard>
  );
}
