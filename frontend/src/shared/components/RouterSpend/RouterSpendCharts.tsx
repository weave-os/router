"use client";

import { useMemo, useState } from "react";

import {
  type DateRange,
  RouterBilling,
  RouterCostType,
  RouterMetricGroup,
  type RouterMetricPoint,
  RouterMetricType,
  useRouterDrillDown,
} from "../../data";
import { useRouterMetricSeries, useRouterReportSummary } from "../../data/useRouterMetricSeries";
import {
  MODEL_FAMILY_LABEL,
  modelFamily,
  type RouterModelFamily,
} from "../../objects/RouterModelFamily";
import { AccountChartColorToken, ChartColorToken } from "../../tools/chartColors";
import { formatRouterModelName, formatUSD, formatUSDCompact } from "../../tools/format";
import { LoadState } from "../../tools/LoadState";
import { addGranularity, formatInterval, formatTime } from "../../tools/timeGranularity";
import { Text } from "../atoms/Text";
import {
  Chart,
  ChartAxisType,
  type ChartData,
  type ChartReferenceLine,
  type ChartSeriesConfig,
  ChartSeriesType,
} from "../Chart";
import { ChartCard } from "../ChartCard";
import { type Granularity } from "../DashboardPageFilters/useDashboardFilters";
import { ErrorCard } from "../ErrorCard";
import { Listogram, ListogramSelector } from "../Listogram";
import { LoadStateWrapper } from "../LoadStateWrapper";
import {
  displayGranularity,
  latestCompleteBucket,
  previousRange,
} from "../RouterCharts/chartRange";
import { TIME_KEY } from "../RouterCharts/pivotSeries";
import { periodPercentChange, RouterChartHeadline } from "../RouterCharts/RouterChartHeadline";
import { ROUTER_SOURCE_MARK } from "../RouterCharts/routerSourceMark";
import { RouterConnectEmptyState } from "../RouterConnectEmptyState";

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
const SAVINGS_KEY = "saved";
const EXTRA_COST_KEY = "extra";

export enum RouterCostView {
  Breakdown = "breakdown",
  Trend = "trend",
}

const COST_VIEW_LABELS: Record<RouterCostView, string> = {
  [RouterCostView.Breakdown]: "Breakdown",
  [RouterCostView.Trend]: "Trend",
};

const COST_VIEWS = [RouterCostView.Trend, RouterCostView.Breakdown] as const;

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
    (sum, { actual, comparison }) => ({
      actual: sum.actual + actual,
      comparison: sum.comparison + comparison,
    }),
    { actual: 0, comparison: 0 },
  );
}

/** Savings as a percentage of the comparison cost; null when nothing could be compared. */
function savingsRate({ actual, comparison }: { actual: number; comparison: number }) {
  return comparison === 0 ? null : ((comparison - actual) / comparison) * 100;
}

function useComparisonLabel(range: DateRange) {
  const comparisonModel = LoadState.unwrap(useRouterReportSummary(range))?.comparisonModel;
  return comparisonModel ? formatRouterModelName(comparisonModel) : COMPARISON_FALLBACK;
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
  categorical = false,
  config,
  dependentKeys,
  formatValue,
  granularity,
  legend = false,
  allowDependentTickDecimals = false,
  annotations,
  highlightedIndependentValue,
  mutedBarColor,
  onClickDataPoint,
  referenceLines,
  rows,
}: {
  onClickDataPoint?: (time: number) => void;
  /** Bars sit on one category per bucket instead of a padded time axis. */
  categorical?: boolean;
  highlightedIndependentValue?: number;
  mutedBarColor?: string;
  config: Record<string, ChartSeriesConfig<number, number>>;
  legend?: boolean;
  dependentKeys: readonly string[];
  formatValue: (value: number) => string;
  granularity: Granularity;
  referenceLines?: LoadState<readonly ChartReferenceLine<number, number>[]>;
  allowDependentTickDecimals?: boolean;
  annotations?: readonly { dependentKey: string; independentValue: number; label: string }[];
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
      independentType={categorical ? ChartAxisType.Category : ChartAxisType.Time}
      independentDomain={
        categorical
          ? undefined
          : ([min, max]) => [
              addGranularity(min, timeGranularity, -1).getTime(),
              addGranularity(max, timeGranularity, 1).getTime(),
            ]
      }
      formatValue={formatValue}
      formatIndependentValue={value => formatTime(value, timeGranularity)}
      formatIndependentValueTooltip={value => formatInterval(value, timeGranularity)}
      allowDependentTickDecimals={allowDependentTickDecimals}
      annotations={annotations}
      highlightedIndependentValue={highlightedIndependentValue}
      mutedBarColor={mutedBarColor}
      referenceLines={referenceLines}
      legend={legend}
      onClickDataPoint={onClickDataPoint}
    />
  );
}

const TREND_KEYS = [RouterCostType.Actual, RouterCostType.Comparison] as const;
const BREAKDOWN_KEYS = [RouterCostType.Actual, SAVINGS_KEY, EXTRA_COST_KEY] as const;

/** Hosted's cost drill-down: the sessions behind a clicked cost bucket. */
function useRouterCostDrillDown({
  cumulative,
  granularity,
  range,
}: {
  cumulative?: boolean;
  granularity: Granularity;
  range: DateRange;
}) {
  const { drillDownModal, openDrillDown } = useRouterDrillDown({
    cumulative,
    granularity,
    group: RouterMetricGroup.CostType,
    range,
    type: RouterMetricType.Cost,
  });
  return {
    drillDownModal,
    onClickDataPoint:
      openDrillDown == null ? undefined : (time: number) => openDrillDown({ bucketStart: time }),
  };
}

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
  const buckets = useMemo(
    () => LoadState.map(current, ({ values }) => costBuckets(values)),
    [current],
  );
  const rows = useMemo(
    () =>
      LoadState.map(buckets, values =>
        values.map(({ actual, comparison, time }): Record<string, number> =>
          view === RouterCostView.Trend
            ? {
                [RouterCostType.Actual]: actual,
                [RouterCostType.Comparison]: comparison,
                [TIME_KEY]: time,
              }
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
  const config: Record<string, ChartSeriesConfig<number, number>> = view === RouterCostView.Trend
    ? {
        [RouterCostType.Actual]: {
          color: ChartColorToken.Blue,
          label: "Catalog cost",
          type: ChartSeriesType.Line,
        },
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
        [RouterCostType.Actual]: {
          color: ChartColorToken.Scale1,
          label: "Catalog cost",
          type: ChartSeriesType.Bar,
        },
        [SAVINGS_KEY]: {
          color: ChartColorToken.Green,
          label: `Saved vs ${comparisonModel}`,
          type: ChartSeriesType.Bar,
        },
      };
  const savings = LoadState.map(current, ({ values }) => {
    const { actual, comparison } = costTotals(values);
    return comparison - actual;
  });
  const percentChange = LoadState.map(current, previous, (now, before) => {
    const nowTotals = costTotals(now.values);
    const beforeTotals = costTotals(before.values);
    return periodPercentChange(
      nowTotals.comparison - nowTotals.actual,
      beforeTotals.comparison - beforeTotals.actual,
    );
  });

  const { drillDownModal, onClickDataPoint } = useRouterCostDrillDown({ granularity, range });

  return (
    <ChartCard
      chartId="router_cost_breakdown"
      sources={ROUTER_SOURCE_MARK}
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
          statistic={LoadState.map(savings, value => ({
            total: value >= 0 ? `${formatUSD(value)} saved` : `${formatUSD(Math.abs(value))} extra`,
          }))}
        />
      }
      topRight={
        <ListogramSelector
          labels={COST_VIEW_LABELS}
          option={view}
          options={COST_VIEWS}
          setOption={setView}
        />
      }
    >
      <TimeChart
        onClickDataPoint={onClickDataPoint}
        categorical={view === RouterCostView.Breakdown}
        config={config}
        dependentKeys={view === RouterCostView.Trend ? TREND_KEYS : BREAKDOWN_KEYS}
        formatValue={formatUSDCompact}
        granularity={granularity}
        legend
        rows={rows}
      />
      {drillDownModal}
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

  const { drillDownModal, onClickDataPoint } = useRouterCostDrillDown({
    granularity,
    range,
    cumulative: true,
  });

  return (
    <ChartCard
      chartId="router_cumulative_savings"
      sources={ROUTER_SOURCE_MARK}
      className={className}
      title="Cumulative savings"
      subtitle={`Running total of catalog cost saved vs ${comparisonModel} across the selected time range. Below $0, routing has cost more so far.`}
    >
      {isEmpty(rows) ? (
        <RouterConnectEmptyState message="Route requests through Weave Router to track cumulative savings." />
      ) : (
        <TimeChart
          onClickDataPoint={onClickDataPoint}
          config={{
            [CUMULATIVE_KEY]: {
              color: ChartColorToken.Green,
              label: `Cumulative savings vs ${comparisonModel}`,
              opacity: 0.3,
              type: ChartSeriesType.Area,
            },
          }}
          dependentKeys={CUMULATIVE_KEYS}
          formatValue={formatUSDCompact}
          granularity={granularity}
          rows={rows}
        />
      )}
      {drillDownModal}
    </ChartCard>
  );
}

function isEmpty(rows: LoadState<readonly unknown[]>) {
  return LoadState.isLoaded(rows) && rows.value.length === 0;
}

const RATE_KEY = "savings_rate";

function formatSavingsRate(rate: number) {
  return `${rate.toFixed(1)}%`;
}
const RATE_KEYS = [RATE_KEY] as const;
const ZERO_REFERENCE_LINE = { type: "dependent" as const, value: 0 };

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
  // The spend-weighted range rate rather than a mean of bucket rates, so the line matches the headline.
  const referenceLines = LoadState.map(rate, value =>
    value == null
      ? [ZERO_REFERENCE_LINE]
      : [
          ZERO_REFERENCE_LINE,
          {
            dashed: true,
            label: `Overall ${formatSavingsRate(value)}`,
            side: "right" as const,
            type: "dependent" as const,
            value,
          },
        ],
  );
  const rateChange = LoadState.map(current, previous, (now, before) => {
    const nowRate = savingsRate(costTotals(now.values));
    const beforeRate = savingsRate(costTotals(before.values));
    return nowRate == null || beforeRate == null ? null : nowRate - beforeRate;
  });

  const { drillDownModal, onClickDataPoint } = useRouterCostDrillDown({ granularity, range });

  return (
    <ChartCard
      chartId="router_savings_rate"
      sources={ROUTER_SOURCE_MARK}
      className={className}
      title="Savings rate"
      subtitle={`Savings divided by comparison cost (${comparisonModel}) per bucket.`}
      headline={
        <RouterChartHeadline
          changeTooltip="Percentage-point change from the previous period"
          changeUnit="pp"
          percentChange={rateChange}
          statistic={LoadState.map(rate, value =>
            value == null ? {} : { total: formatSavingsRate(value) },
          )}
        />
      }
    >
      {isEmpty(rows) ? (
        <RouterConnectEmptyState message="Route requests through Weave Router to track your savings rate." />
      ) : (
        <TimeChart
          onClickDataPoint={onClickDataPoint}
          config={{
            [RATE_KEY]: {
              color: ChartColorToken.Green,
              label: `Savings rate vs ${comparisonModel}`,
              type: ChartSeriesType.Bar,
            },
          }}
          categorical
          dependentKeys={RATE_KEYS}
          formatValue={formatSavingsRate}
          granularity={granularity}
          highlightedIndependentValue={latestCompleteBucket(
            (LoadState.unwrap(rows) ?? []).map(row => row[TIME_KEY]),
            range,
            displayGranularity(granularity),
          )}
          mutedBarColor={AccountChartColorToken.Gray}
          referenceLines={referenceLines}
          rows={rows}
        />
      )}
      {drillDownModal}
    </ChartCard>
  );
}

const PER_REQUEST_KEY = "cost_per_request";
// Cost per request is often a fraction of a cent, which a two-decimal currency format rounds to $0.00.
const USD_PER_REQUEST_FORMATTER = new Intl.NumberFormat(undefined, {
  currency: "USD",
  maximumSignificantDigits: 3,
  style: "currency",
});
const formatUSDPerRequest = (value: number) => USD_PER_REQUEST_FORMATTER.format(value);
const PER_REQUEST_KEYS = [PER_REQUEST_KEY] as const;

function totalByTime(points: readonly RouterMetricPoint[]) {
  const totals = new Map<number, number>();
  for (const { time, value } of points) {
    const bucket = time == null ? 0 : new Date(time).getTime();
    totals.set(bucket, (totals.get(bucket) ?? 0) + value);
  }
  return totals;
}

// A bucket at least this many times the range's median cost per request is marked as unusual.
const UNUSUAL_BUCKET_RATIO = 1.5;
// Fewer buckets than this give the median too little to stand for "usual".
const MIN_BUCKETS_FOR_UNUSUAL = 4;

/** Spend over requests for the whole range, so busy buckets weigh more than quiet ones. */
function rangeCostPerRequest(
  spend: readonly RouterMetricPoint[],
  requests: readonly RouterMetricPoint[],
) {
  const count = requests.reduce((sum, { value }) => sum + value, 0);
  return count === 0 ? null : spend.reduce((sum, { value }) => sum + value, 0) / count;
}

/** Buckets to annotate as unusual, each with its cost per request as a multiple of the range median. */
export function unusualCostPerRequestBuckets(rows: readonly { ratio: number; time: number }[]) {
  if (rows.length < MIN_BUCKETS_FOR_UNUSUAL) return [];
  const sorted = rows.map(({ ratio }) => ratio).sort((a, b) => a - b);
  const middle = Math.floor(sorted.length / 2);
  const usual =
    sorted.length % 2 === 0 ? (sorted[middle - 1] + sorted[middle]) / 2 : sorted[middle];
  if (usual <= 0) return [];
  return rows.flatMap(({ ratio, time }) =>
    ratio >= usual * UNUSUAL_BUCKET_RATIO ? [{ multiple: ratio / usual, time }] : [],
  );
}

enum CostPerRequestScope {
  All = "all",
  PayPerUse = "pay_per_use",
}

const COST_PER_REQUEST_SCOPES = [CostPerRequestScope.All, CostPerRequestScope.PayPerUse] as const;

const COST_PER_REQUEST_SCOPE_LABELS: Record<CostPerRequestScope, string> = {
  [CostPerRequestScope.All]: "All requests",
  [CostPerRequestScope.PayPerUse]: "Pay-per-use only",
};

const COST_PER_REQUEST_SCOPE_SUBTITLES: Record<CostPerRequestScope, string> = {
  [CostPerRequestScope.All]:
    "Catalog cost divided by requests, per bucket. Subscription-served requests count as $0.",
  [CostPerRequestScope.PayPerUse]:
    "Catalog cost divided by API requests per bucket, excluding subscription requests.",
};

export function RouterCostPerRequestChart({ className, granularity, range }: SpendChartProps) {
  const [scope, setScope] = useState(CostPerRequestScope.All);
  // Pay-per-use leaves out subscription-served requests, which count as $0 and pull the figure down.
  const billing = scope === CostPerRequestScope.PayPerUse ? RouterBilling.API : undefined;
  const previousWindow = useMemo(() => previousRange(range), [range]);
  const shared = { billing, granularity };
  const spend = useRouterMetricSeries({
    ...shared,
    range,
    type: RouterMetricType.ModelSpend,
  });
  const requests = useRouterMetricSeries({
    ...shared,
    range,
    type: RouterMetricType.ModelUsage,
  });
  const previousSpend = useRouterMetricSeries({
    ...shared,
    range: previousWindow,
    type: RouterMetricType.ModelSpend,
  });
  const previousRequests = useRouterMetricSeries({
    ...shared,
    range: previousWindow,
    type: RouterMetricType.ModelUsage,
  });
  const rows = useMemo(
    () =>
      LoadState.map(spend, requests, (spendSeries, requestSeries) => {
        const requestTotals = totalByTime(requestSeries.values);
        return [...totalByTime(spendSeries.values)]
          .filter(([time]) => (requestTotals.get(time) ?? 0) > 0)
          .sort(([a], [b]) => a - b)
          .map(([time, value]) => ({ ratio: value / requestTotals.get(time)!, time }));
      }),
    [requests, spend],
  );
  const chartRows = LoadState.map(rows, values =>
    values.map(({ ratio, time }) => ({ [PER_REQUEST_KEY]: ratio, [TIME_KEY]: time })),
  );
  const annotations = unusualCostPerRequestBuckets(LoadState.unwrap(rows) ?? []).map(
    ({ multiple, time }) => ({
      dependentKey: PER_REQUEST_KEY,
      independentValue: time,
      label: `${multiple.toFixed(1)}× median`,
    }),
  );
  const average = LoadState.map(spend, requests, (spendSeries, requestSeries) =>
    rangeCostPerRequest(spendSeries.values, requestSeries.values),
  );
  const percentChange = LoadState.map(
    spend,
    requests,
    previousSpend,
    previousRequests,
    (spendSeries, requestSeries, previousSpendSeries, previousRequestSeries) => {
      const now = rangeCostPerRequest(spendSeries.values, requestSeries.values);
      return now == null
        ? null
        : periodPercentChange(
            now,
            rangeCostPerRequest(previousSpendSeries.values, previousRequestSeries.values),
          );
    },
  );

  const { drillDownModal, openDrillDown } = useRouterDrillDown({
    billing,
    granularity,
    range,
    type: RouterMetricType.ModelSpend,
  });
  const onClickDataPoint =
    openDrillDown == null ? undefined : (time: number) => openDrillDown({ bucketStart: time });

  return (
    <ChartCard
      chartId="router_cost_per_request"
      sources={ROUTER_SOURCE_MARK}
      className={className}
      title="Cost per request"
      subtitle={COST_PER_REQUEST_SCOPE_SUBTITLES[scope]}
      topRight={
        <ListogramSelector
          labels={COST_PER_REQUEST_SCOPE_LABELS}
          option={scope}
          options={COST_PER_REQUEST_SCOPES}
          setOption={setScope}
        />
      }
      headline={
        <RouterChartHeadline
          invertChangeColor
          percentChange={percentChange}
          statistic={LoadState.map(average, value =>
            value == null ? {} : { total: formatUSDPerRequest(value) },
          )}
        />
      }
    >
      {isEmpty(chartRows) ? (
        <RouterConnectEmptyState message="Route requests through Weave Router to track cost per request." />
      ) : (
        <TimeChart
          onClickDataPoint={onClickDataPoint}
          allowDependentTickDecimals
          annotations={annotations}
          config={{
            [PER_REQUEST_KEY]: {
              color: ChartColorToken.Scale1,
              label: "Cost per request",
              type: ChartSeriesType.Line,
            },
          }}
          dependentKeys={PER_REQUEST_KEYS}
          formatValue={formatUSDPerRequest}
          granularity={granularity}
          rows={chartRows}
        />
      )}
      {drillDownModal}
    </ChartCard>
  );
}

/**
 * Each maker's catalog cost summed over the range, largest first. Makers present only through
 * subscription-served ($0) requests stay in the list at $0.
 */
export function spendByMaker(points: readonly RouterMetricPoint[]) {
  const totals = new Map<RouterModelFamily, number>();
  for (const { group, value } of points) {
    if (group == null || group === "") continue;
    const family = modelFamily(group);
    totals.set(family, (totals.get(family) ?? 0) + value);
  }
  return [...totals]
    .map(([family, spend]) => ({ family, spend }))
    .sort((a, b) => b.spend - a.spend);
}

export function RouterSpendByMakerChart({
  className,
  range,
}: Omit<SpendChartProps, "granularity">) {
  const spend = useRouterMetricSeries({
    granularity: null,
    group: RouterMetricGroup.Model,
    range,
    type: RouterMetricType.ModelSpend,
  });
  const makerTotals = useMemo(
    () => LoadState.map(spend, ({ values }) => spendByMaker(values)),
    [spend],
  );
  const { drillDownModal, openDrillDown } = useRouterDrillDown({
    granularity: null,
    range,
    type: RouterMetricType.ModelSpend,
  });
  // A maker's row stands for several models, so its drill-down lists that maker's models.
  const openMaker = (family: RouterModelFamily) => {
    if (openDrillDown == null || !LoadState.isLoaded(spend)) return undefined;
    const models = [
      ...new Set(
        spend.value.values.flatMap(({ group }) =>
          group != null && group !== "" && modelFamily(group) === family ? [group] : [],
        ),
      ),
    ];
    return () => openDrillDown({ models });
  };

  return (
    <ChartCard
      chartId="router_spend_by_maker"
      sources={ROUTER_SOURCE_MARK}
      className={className}
      title="Catalog cost by model maker"
      subtitle="Catalog cost over the range by model maker. Subscription requests contribute $0."
    >
      <LoadStateWrapper
        state={makerTotals}
        loading={<Listogram.Loading rows={4} />}
        error={() => (
          <ErrorCard
            title="Couldn't load catalog cost by maker"
            description="Try refreshing the page."
          />
        )}
      >
        {totals =>
          totals.length === 0 ? (
            <RouterConnectEmptyState message="Route requests through Weave Router to see catalog cost by model maker." />
          ) : (
            <Listogram>
              <ul className="flex flex-col gap-1">
                {totals.map(({ family, spend: makerSpend }) => (
                  <li key={family}>
                    <Listogram.Row
                      formatValue={formatUSD}
                      maxValue={totals[0].spend}
                      onClick={openMaker(family)}
                      value={makerSpend}
                    >
                      <Text className="truncate text-body">{MODEL_FAMILY_LABEL[family]}</Text>
                    </Listogram.Row>
                  </li>
                ))}
              </ul>
            </Listogram>
          )
        }
      </LoadStateWrapper>
      {drillDownModal}
    </ChartCard>
  );
}
