"use client";

import { useMemo, useState } from "react";

import { type DateRange, type RouterMetricGroup, type RouterMetricType } from "../../data";
import { useRouterMetricSeries } from "../../data/useRouterMetricSeries";
import { formatBigDecimal } from "../../tools/format";
import { LoadState } from "../../tools/LoadState";
import { addGranularity, formatInterval, formatTime } from "../../tools/timeGranularity";
import {
  Chart,
  ChartAxisType,
  type ChartData,
  type ChartFillPattern,
  type ChartSeriesConfig,
  ChartSeriesType,
} from "../Chart";
import { ChartCard } from "../ChartCard";
import { type Granularity } from "../DashboardPageFilters/useDashboardFilters";
import { displayGranularity, previousRange } from "./chartRange";
import { ChartTabToggle } from "./ChartTabToggle";
import { pivotSeries, TIME_KEY, toShare } from "./pivotSeries";
import { periodPercentChange, RouterChartHeadline } from "./RouterChartHeadline";
import { type RouterSeriesConfigs } from "./routerSeriesConfigs";

enum GroupedMetricView {
  Share = "share",
  Volume = "volume",
}

const VIEW_OPTIONS = [
  { label: "Volume", value: GroupedMetricView.Volume },
  { label: "Share", value: GroupedMetricView.Share },
] as const;

export interface RouterGroupedChartCardProps {
  className?: string;
  /** Hatches or dots every series, e.g. the active-people areas. */
  fillPattern?: ChartFillPattern;
  formatValue?: (value: number) => string;
  granularity: Granularity;
  group: RouterMetricGroup;
  metric: RouterMetricType;
  range: DateRange;
  seriesConfigs: RouterSeriesConfigs;
  seriesType: ChartSeriesType.Area | ChartSeriesType.Bar;
  /** Offers the Volume | Share toggle that renders each bucket's groups as percents. */
  shareToggle?: boolean;
  title: string;
}

/** A grouped router metric per bucket, headlined by its per-bucket average and period change. */
export function RouterGroupedChartCard({
  className,
  fillPattern,
  formatValue = formatBigDecimal,
  granularity,
  group,
  metric,
  range,
  seriesConfigs,
  seriesType,
  shareToggle = false,
  title,
}: RouterGroupedChartCardProps) {
  const [view, setView] = useState(GroupedMetricView.Volume);
  const timeGranularity = displayGranularity(granularity);
  const current = useRouterMetricSeries({ granularity, group, range, type: metric });
  const previous = useRouterMetricSeries({
    granularity: null,
    group,
    range: previousRange(range),
    type: metric,
  });

  const pivoted = useMemo(
    () =>
      LoadState.map(current, series => {
        const pivot = pivotSeries(series.values);
        const order = Object.keys(seriesConfigs);
        const rank = (key: string) => (order.includes(key) ? order.indexOf(key) : order.length);
        return { ...pivot, keys: [...pivot.keys].sort((a, b) => rank(a) - rank(b)) };
      }),
    [current, seriesConfigs],
  );
  const shown = useMemo(
    () => (view === GroupedMetricView.Share ? LoadState.map(pivoted, toShare) : pivoted),
    [pivoted, view],
  );
  const average = LoadState.map(pivoted, ({ totals }) =>
    totals.length === 0 ? 0 : totals.reduce((sum, total) => sum + total, 0) / totals.length,
  );
  const percentChange = LoadState.map(current, previous, (now, before) => {
    const nowTotal = now.values.reduce((sum, point) => sum + point.value, 0);
    const beforeTotal = before.values.reduce((sum, point) => sum + point.value, 0);
    return periodPercentChange(nowTotal, beforeTotal);
  });

  const config = useMemo(() => {
    const keys = LoadState.unwrap(pivoted)?.keys ?? [];
    return Object.fromEntries(
      keys.map(key => {
        const base: ChartSeriesConfig<number, number> = seriesConfigs[key] ?? { label: key };
        return [key, { ...base, fillPattern, type: seriesType }];
      }),
    );
  }, [fillPattern, pivoted, seriesConfigs, seriesType]);
  const isShare = view === GroupedMetricView.Share;
  const formatShown = (value: number) => (isShare ? `${value.toFixed(0)}%` : formatValue(value));

  return (
    <ChartCard
      className={className}
      title={title}
      headline={
        <RouterChartHeadline
          percentChange={percentChange}
          statistic={LoadState.map(average, value => ({
            rate: { granularity: timeGranularity, rate: formatValue(value) },
          }))}
        />
      }
      topRight={
        shareToggle ? (
          <ChartTabToggle
            ariaLabel={`${title} view`}
            onChange={setView}
            options={VIEW_OPTIONS}
            value={view}
          />
        ) : undefined
      }
    >
      <Chart
        config={config}
        data={LoadState.map(
          shown,
          ({ rows }) => rows as ChartData<typeof TIME_KEY, string, number, number>,
        )}
        dependentKeys={LoadState.map(shown, ({ keys }) => keys)}
        groups={LoadState.map(shown, ({ keys }) => [keys])}
        independentKey={TIME_KEY}
        independentType={ChartAxisType.Time}
        independentDomain={([min, max]) => [
          addGranularity(min, timeGranularity, -1).getTime(),
          addGranularity(max, timeGranularity, 1).getTime(),
        ]}
        formatValue={formatShown}
        formatIndependentValue={value => formatTime(value, timeGranularity)}
        formatIndependentValueTooltip={value => formatInterval(value, timeGranularity)}
        legend
      />
    </ChartCard>
  );
}
