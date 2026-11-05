"use client";

import { useMemo, useState } from "react";

import {
  type DateRange,
  RouterMetricGroup,
  type RouterMetricPoint,
  RouterMetricType,
  useRouterDrillDown,
} from "../../data";
import { useRouterMetricSeries } from "../../data/useRouterMetricSeries";
import { modelColorTokens } from "../../objects/RouterModelFamily";
import { AccountChartColorToken, ChartColorToken } from "../../tools/chartColors";
import {
  formatBigDecimal,
  formatPercentage,
  formatRouterModelName,
  formatUSD,
} from "../../tools/format";
import { LoadState } from "../../tools/LoadState";
import { addGranularity, formatInterval, formatTime } from "../../tools/timeGranularity";
import { Skeleton } from "../atoms/Skeleton";
import {
  Chart,
  ChartAxisType,
  type ChartData,
  ChartFillPattern,
  type ChartSeriesConfig,
  ChartSeriesType,
  SmallMultiples,
  SmallMultiplesScale,
} from "../Chart";
import { ChartCard } from "../ChartCard";
import { type Granularity } from "../DashboardPageFilters/useDashboardFilters";
import { ErrorCard } from "../ErrorCard";
import { LoadStateWrapper } from "../LoadStateWrapper";
import { BreakdownBar, type BreakdownSlice } from "../molecules/BreakdownBar";
import { Tabs } from "../molecules/Tabs";
import { displayGranularity } from "../RouterCharts/chartRange";
import { TIME_KEY } from "../RouterCharts/pivotSeries";
import { RouterChartHeadline } from "../RouterCharts/RouterChartHeadline";
import { ROUTER_SOURCE_MARK } from "../RouterCharts/routerSourceMark";

interface ModelChartProps {
  className?: string;
  granularity: Granularity;
  range: DateRange;
}

export enum ModelMixDisplay {
  Bands = "bands",
  Bars = "bars",
}

const HERO_VIEW_LABELS: Record<ModelMixDisplay, string> = {
  [ModelMixDisplay.Bands]: "Area",
  [ModelMixDisplay.Bars]: "Bars",
};
const HERO_VIEWS = [ModelMixDisplay.Bands, ModelMixDisplay.Bars];
const MAX_MODELS = 5;
const NO_PERIOD_CHANGE = LoadState.loaded(null);
const OTHER_GROUP_ID = "__other__";
const OTHER_MODELS_COLOR = AccountChartColorToken.Gray;
// Shares stack to 100%; without a fixed domain the axis pads up to several hundred percent.
const SHARE_DOMAIN: [number, number] = [0, 1];
// Solid bands with card-colored seams; a dot on every bucket would crowd the seams.
const BAND_STYLE = { dot: false, fillOpacity: 1, stroke: "var(--wv-bg-surface)" } as const;

type MixShareRow = Record<string, number>;

function useRequestsByModel(granularity: Granularity, range: DateRange) {
  return useRouterMetricSeries({
    granularity,
    group: RouterMetricGroup.Model,
    range,
    type: RouterMetricType.ModelUsage,
  });
}

function modelTotals(points: readonly RouterMetricPoint[]) {
  const totals = new Map<string, number>();
  for (const { group, value } of points) {
    if (group == null || group === "") continue;
    totals.set(group, (totals.get(group) ?? 0) + value);
  }
  return [...totals].sort(([, a], [, b]) => b - a);
}

/** The busiest models (busiest at the bottom of each stack) with the rest folded into Other. */
export function modelMixShareRows(points: readonly RouterMetricPoint[]) {
  const ranked = modelTotals(points).map(([model]) => model);
  const shown = new Set(ranked.slice(0, MAX_MODELS));
  const models = ranked.length > MAX_MODELS ? [...shown, OTHER_GROUP_ID] : [...shown];
  const byTime = new Map<number, Map<string, number>>();
  for (const { group, time, value } of points) {
    if (group == null || group === "" || time == null) continue;
    const bucket = new Date(time).getTime();
    const key = shown.has(group) ? group : OTHER_GROUP_ID;
    const byModel = byTime.get(bucket) ?? new Map<string, number>();
    byModel.set(key, (byModel.get(key) ?? 0) + value);
    byTime.set(bucket, byModel);
  }
  const rows = [...byTime]
    .sort(([a], [b]) => a - b)
    .map(([time, byModel]) => {
      const total = [...byModel.values()].reduce((sum, value) => sum + value, 0);
      const row: MixShareRow = { [TIME_KEY]: time };
      for (const model of models) row[model] = total === 0 ? 0 : (byModel.get(model) ?? 0) / total;
      return row;
    });
  return { models, rows };
}

/**
 * Each model's share of requests per bucket. `hero` leads the page: flush instead of in a card,
 * with an area/bars switch and no value axis since shares always fill 0-100%.
 */
export function RouterModelMixShareChart({
  className,
  display: initialDisplay = ModelMixDisplay.Bars,
  granularity,
  hero = false,
  range,
}: ModelChartProps & { display?: ModelMixDisplay; hero?: boolean }) {
  const [display, setDisplay] = useState(initialDisplay);
  const [legendPortal, setLegendPortal] = useState<HTMLDivElement | null>(null);
  const timeGranularity = displayGranularity(granularity);
  const seriesType = display === ModelMixDisplay.Bands ? ChartSeriesType.Area : ChartSeriesType.Bar;
  const requests = useRequestsByModel(granularity, range);
  const mix = useMemo(
    () => LoadState.map(requests, ({ values }) => modelMixShareRows(values)),
    [requests],
  );
  const models = useMemo(() => LoadState.map(mix, ({ models: list }) => list), [mix]);
  const groups = useMemo(() => LoadState.map(models, list => [list]), [models]);
  const config = useMemo(() => {
    const list = LoadState.unwrap(models) ?? [];
    const named = list.filter(model => model !== OTHER_GROUP_ID);
    const colors = modelColorTokens(named);
    const entries: [string, ChartSeriesConfig<number, number>][] = named.map((model, index) => [
      model,
      {
        color: colors[index],
        ...BAND_STYLE,
        label: formatRouterModelName(model),
        type: seriesType,
      },
    ]);
    if (list.includes(OTHER_GROUP_ID)) {
      entries.push([
        OTHER_GROUP_ID,
        {
          color: OTHER_MODELS_COLOR,
          ...BAND_STYLE,
          // Dots mark Other as a remainder of many models, not one more model.
          fillPattern: ChartFillPattern.Dots,
          label: "Other models",
          type: seriesType,
        },
      ]);
    }
    return Object.fromEntries(entries);
  }, [models, seriesType]);
  const rows = LoadState.map(
    mix,
    ({ rows: values }) => values as ChartData<typeof TIME_KEY, string, number, number>,
  );
  const firstTime = LoadState.unwrap(mix)?.rows[0]?.[TIME_KEY];
  const isEmpty = LoadState.isLoaded(mix) && mix.value.rows.length === 0;
  const { drillDownModal, openDrillDown } = useRouterDrillDown({
    granularity,
    group: RouterMetricGroup.Model,
    range,
    type: RouterMetricType.ModelUsage,
  });

  return (
    <ChartCard
      sources={ROUTER_SOURCE_MARK}
      className={className}
      fullBleed={hero}
      topRight={
        hero ? (
          <Tabs
            value={display}
            onValueChange={value => {
              const next = HERO_VIEWS.find(view => view === value);
              if (next != null) setDisplay(next);
            }}
          >
            <Tabs.List aria-label="Chart style">
              {HERO_VIEWS.map(view => (
                <Tabs.Trigger key={view} value={view}>
                  {HERO_VIEW_LABELS[view]}
                </Tabs.Trigger>
              ))}
            </Tabs.List>
          </Tabs>
        ) : undefined
      }
      headerBottom={
        hero && !isEmpty ? (
          <div ref={setLegendPortal} className="mt-4 min-w-0 self-start" />
        ) : undefined
      }
      title="Model mix share"
      subtitle={
        display === ModelMixDisplay.Bands
          ? "Each model's share of router requests per bucket. The bands always add up to 100%."
          : "Each model's share of router requests per bucket."
      }
    >
      {isEmpty ? (
        <ErrorCard
          title="No requests yet"
          description="Route requests through Weave Router to see your model mix."
        />
      ) : (
        <Chart
          allowDependentTickDecimals
          config={config}
          data={rows}
          dependentDomain={SHARE_DOMAIN}
          dependentKeys={models}
          groups={groups}
          independentKey={TIME_KEY}
          independentType={ChartAxisType.Time}
          // Bars need half a bucket of room at each end; bands run edge to edge.
          independentDomain={([min, max]) =>
            display === ModelMixDisplay.Bands
              ? [min, max]
              : [
                  addGranularity(min, timeGranularity, -1).getTime(),
                  addGranularity(max, timeGranularity, 1).getTime(),
                ]
          }
          formatValue={formatPercentage}
          // The hero runs edge to edge, where the first date would sit clipped on the left edge.
          formatIndependentValue={value =>
            hero && value === firstTime ? "" : formatTime(value, timeGranularity)
          }
          formatIndependentValueTooltip={value => formatInterval(value, timeGranularity)}
          hideDependentAxis={hero}
          legend={!hero || legendPortal != null}
          legendPortal={hero ? legendPortal : undefined}
          legendPortalClassName="-ml-2 flex flex-wrap items-center gap-x-1 text-caption"
          onClickDataPoint={
            openDrillDown == null
              ? undefined
              : (time, model) =>
                  openDrillDown({
                    bucketStart: time,
                    key: model === OTHER_GROUP_ID ? undefined : model,
                  })
          }
        />
      )}
      {drillDownModal}
    </ChartCard>
  );
}

// Three models plus Other fill the 2 × 2 grid.
const TOP_REQUEST_MODELS = 3;
const OTHER_REQUEST_MODELS_KEY = "__other_models__";

/**
 * One series per model over the range's buckets, the busiest three then the rest folded into
 * Other; a missing bucket reads as 0.
 */
export function modelRequestSeries(points: readonly RouterMetricPoint[]) {
  const times = [
    ...new Set(points.flatMap(({ time }) => (time == null ? [] : [new Date(time).getTime()]))),
  ].sort((a, b) => a - b);
  const byModel = new Map<string, Map<number, number>>();
  for (const { group, time, value } of points) {
    if (group == null || group === "" || time == null) continue;
    const byTime = byModel.get(group) ?? new Map<number, number>();
    const bucket = new Date(time).getTime();
    byTime.set(bucket, (byTime.get(bucket) ?? 0) + value);
    byModel.set(group, byTime);
  }
  const ranked = [...byModel].map(([model, byTime]) => ({
    key: model,
    label: formatRouterModelName(model),
    total: [...byTime.values()].reduce((sum, value) => sum + value, 0),
    values: times.map(time => byTime.get(time) ?? 0),
  }));
  ranked.sort((a, b) => b.total - a.total);
  const top = ranked.slice(0, TOP_REQUEST_MODELS);
  const rest = ranked.slice(TOP_REQUEST_MODELS);
  const colors = modelColorTokens(top.map(({ key }) => key));
  const allRequests = ranked.reduce((sum, { total }) => sum + total, 0);
  const panels = top.map((model, index) => ({ ...model, color: colors[index] }));
  if (rest.length > 0) {
    panels.push({
      color: AccountChartColorToken.Gray,
      key: OTHER_REQUEST_MODELS_KEY,
      label: "Other models",
      total: rest.reduce((sum, { total }) => sum + total, 0),
      values: times.map((_, index) => rest.reduce((sum, { values }) => sum + values[index], 0)),
    });
  }
  return {
    series: panels.map(model => ({
      ...model,
      summary: `${formatBigDecimal(model.total)} · ${formatPercentage(allRequests === 0 ? 0 : model.total / allRequests)}`,
    })),
    times,
  };
}

export function RouterModelRequestsSmallMultiples({
  className,
  granularity,
  range,
}: ModelChartProps) {
  const timeGranularity = displayGranularity(granularity);
  const requests = useRequestsByModel(granularity, range);
  const series = useMemo(
    () => LoadState.map(requests, ({ values }) => modelRequestSeries(values)),
    [requests],
  );

  return (
    <ChartCard
      sources={ROUTER_SOURCE_MARK}
      autoHeight
      className={className}
      title="Requests by model"
      subtitle="Requests per bucket for the busiest models, each on its own scale, with its total and share."
    >
      <LoadStateWrapper
        state={series}
        loading={<Skeleton className="h-64 w-full" />}
        error={() => (
          <ErrorCard
            title="Couldn't load requests by model"
            description="Try refreshing the page."
          />
        )}
      >
        {({ series: models, times }) =>
          models.length === 0 ? (
            <ErrorCard
              title="No requests yet"
              description="Route requests through Weave Router to see requests by model."
            />
          ) : (
            <SmallMultiples
              color={ChartColorToken.Scale2}
              formatTime={time => formatInterval(time, timeGranularity)}
              formatValue={formatBigDecimal}
              scale={SmallMultiplesScale.Independent}
              series={models}
              times={times}
            />
          )
        }
      </LoadStateWrapper>
    </ChartCard>
  );
}

/** Each model's share of catalog cost over the range: the top models, the rest folded into Other. */
export function modelSpendSlices(points: readonly RouterMetricPoint[]): BreakdownSlice[] {
  const ranked = modelTotals(points).filter(([, value]) => value > 0);
  const top = ranked.slice(0, MAX_MODELS);
  const colors = modelColorTokens(top.map(([model]) => model));
  const slices: BreakdownSlice[] = top.map(([model, amount], index) => ({
    amount,
    color: colors[index],
    id: model,
    label: formatRouterModelName(model),
  }));
  const other = ranked.slice(MAX_MODELS).reduce((sum, [, value]) => sum + value, 0);
  if (other > 0) {
    slices.push({
      amount: other,
      color: OTHER_MODELS_COLOR,
      id: OTHER_GROUP_ID,
      isOther: true,
      label: "Other models",
    });
  }
  return slices;
}

export function RouterModelSpendRanking({
  className,
  range,
}: Omit<ModelChartProps, "granularity">) {
  const spend = useRouterMetricSeries({
    granularity: null,
    group: RouterMetricGroup.Model,
    range,
    type: RouterMetricType.ModelSpend,
  });
  const breakdown = useMemo(
    () =>
      LoadState.map(spend, ({ values }) => {
        const slices = modelSpendSlices(values);
        return {
          otherModels: modelTotals(values)
            .filter(([, value]) => value > 0)
            .slice(MAX_MODELS)
            .map(([model]) => model),
          slices,
          total: slices.reduce((sum, { amount }) => sum + amount, 0),
        };
      }),
    [spend],
  );
  const isEmpty = LoadState.isLoaded(breakdown) && breakdown.value.slices.length === 0;
  const { drillDownModal, openDrillDown } = useRouterDrillDown({
    granularity: null,
    group: RouterMetricGroup.Model,
    range,
    type: RouterMetricType.ModelSpend,
  });

  return (
    <ChartCard
      chartId="router_model_spend"
      sources={ROUTER_SOURCE_MARK}
      className={className}
      title="Catalog cost by model"
      subtitle="Each model's share of catalog cost over the range. Subscription requests contribute $0."
      headline={
        isEmpty ? undefined : (
          <RouterChartHeadline
            percentChange={NO_PERIOD_CHANGE}
            statistic={LoadState.map(breakdown, ({ total }) => ({ total: formatUSD(total) }))}
          />
        )
      }
    >
      <div aria-live="polite" className="h-full">
        <LoadStateWrapper
          state={breakdown}
          loading={<Skeleton className="h-64 w-full" />}
          error={() => (
            <ErrorCard
              title="Couldn't load catalog cost by model"
              description="Try refreshing the page."
            />
          )}
        >
          {({ otherModels, slices, total }) =>
            slices.length === 0 ? (
              <ErrorCard
                title="No API spend yet"
                description="Route requests through Weave Router to see catalog cost by model."
              />
            ) : (
              <BreakdownBar
                formatAmount={formatUSD}
                sliceActions={
                  openDrillDown == null
                    ? undefined
                    : {
                        getActionLabel: ({ label }) => `View ${label} sessions`,
                        isSliceInteractive: () => true,
                        onSliceClick: model =>
                          model === OTHER_GROUP_ID
                            ? openDrillDown({ models: otherModels })
                            : openDrillDown({ key: model }),
                      }
                }
                slices={slices}
                total={total}
              />
            )
          }
        </LoadStateWrapper>
      </div>
      {drillDownModal}
    </ChartCard>
  );
}
