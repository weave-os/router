"use client";

import { useMemo, useState } from "react";

import {
  type DateRange,
  type RouterMetricGroup,
  type RouterMetricPoint,
  type RouterMetricType,
} from "../../data";
import { useRouterDrillDown } from "../../data/useRouterDrillDown";
import { useRouterMetricSeries } from "../../data/useRouterMetricSeries";
import { formatBigDecimal, formatInteger } from "../../tools/format";
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
import { displayGranularity, previousRange, rateDivisor } from "./chartRange";
import { ChartTabToggle } from "./ChartTabToggle";
import { usePartialBucketTick } from "./partialBucketTick";
import { pivotSeries, TIME_KEY, toShare } from "./pivotSeries";
import { periodPercentChange, RouterChartHeadline } from "./RouterChartHeadline";
import { type RouterSeriesConfigs } from "./routerSeriesConfigs";
import { ROUTER_METRIC_SOURCE_MARK } from "./routerSourceMark";

enum GroupedMetricView {
  Share = "share",
  Volume = "volume",
}

/** How a grouped card's headline averages its buckets, as in hosted's chart registry. */
export enum GroupedStatistic {
  /** Averages only buckets with data; distinct counts like active people stay per-bucket. */
  Mean = "mean",
  /** Divides the total by every bucket in the range, empty ones included. */
  Sum = "sum",
}

const sumValues = (points: readonly RouterMetricPoint[]) =>
  points.reduce((sum, point) => sum + point.value, 0);

const VIEW_OPTIONS = [
  { label: "Volume", value: GroupedMetricView.Volume },
  { label: "Share", value: GroupedMetricView.Share },
] as const;

export interface RouterGroupedChartCardProps {
  /** Deep-link id; also gives the card its overflow menu. */
  chartId: string;
  className?: string;
  /** Hatches or dots every series, e.g. the active-people areas. */
  fillPattern?: ChartFillPattern;
  formatValue?: (value: number) => string;
  granularity: Granularity;
  group: RouterMetricGroup;
  /** Flags ticks whose bucket the date range cuts short; on by default. */
  markPartialBuckets?: boolean;
  metric: RouterMetricType;
  range: DateRange;
  seriesConfigs: RouterSeriesConfigs;
  seriesType: ChartSeriesType.Area | ChartSeriesType.Bar;
  /** Offers the Volume | Share toggle that renders each bucket's groups as percents. */
  shareToggle?: boolean;
  statistic: GroupedStatistic;
  /** Headlines the average as whole `<unit>/<bucket>`, e.g. "7 users/day". */
  statisticUnit?: string;
  title: string;
}

/** A grouped router metric per bucket, headlined by its per-bucket average and period change. */
export function RouterGroupedChartCard({
  chartId,
  className,
  fillPattern,
  formatValue = formatBigDecimal,
  granularity,
  group,
  markPartialBuckets = true,
  metric,
  range,
  seriesConfigs,
  seriesType,
  shareToggle = false,
  statistic,
  statisticUnit,
  title,
}: RouterGroupedChartCardProps) {
  const [view, setView] = useState(GroupedMetricView.Volume);
  const timeGranularity = displayGranularity(granularity);
  const current = useRouterMetricSeries({ granularity, group, range, type: metric });
  const previousPeriodRange = useMemo(() => previousRange(range), [range]);
  const previous = useRouterMetricSeries({
    granularity,
    group,
    range: previousPeriodRange,
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
  const perBucket = (points: readonly RouterMetricPoint[], dateRange: DateRange) =>
    sumValues(points) /
    (statistic === GroupedStatistic.Mean
      ? Math.max(new Set(points.map(point => point.time)).size, 1)
      : rateDivisor(dateRange, granularity));
  const average = LoadState.map(current, now => perBucket(now.values, range));
  const percentChange = LoadState.map(average, previous, (nowAverage, previousSeries) =>
    periodPercentChange(nowAverage, perBucket(previousSeries.values, previousPeriodRange)),
  );

  const config = useMemo(() => {
    const keys = LoadState.unwrap(pivoted)?.keys ?? [];
    return Object.fromEntries(
      keys.map(key => {
        const base: ChartSeriesConfig<number, number> = seriesConfigs[key] ?? { label: key };
        return [key, { ...base, fillPattern, type: seriesType }];
      }),
    );
  }, [fillPattern, pivoted, seriesConfigs, seriesType]);
  const partialBucketTick = usePartialBucketTick(range, timeGranularity);
  const { drillDownModal, openDrillDown } = useRouterDrillDown({
    granularity,
    group,
    range,
    type: metric,
  });
  const isShare = view === GroupedMetricView.Share;
  const formatShown = (value: number) => (isShare ? `${value.toFixed(0)}%` : formatValue(value));

  return (
    <ChartCard
      chartId={chartId}
      className={className}
      sources={ROUTER_METRIC_SOURCE_MARK}
      title={title}
      headline={
        <RouterChartHeadline
          percentChange={percentChange}
          statistic={LoadState.map(average, value =>
            statisticUnit == null
              ? { rate: { granularity: timeGranularity, rate: formatValue(value) } }
              : { total: `${formatInteger(value)} ${statisticUnit}/${timeGranularity}` },
          )}
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
        // A bucket of padding either side centres bars on their ticks; an area instead runs
        // edge to edge, so it ends on the first and last buckets.
        independentDomain={([min, max]) =>
          seriesType === ChartSeriesType.Area
            ? [min, max]
            : [
                addGranularity(min, timeGranularity, -1).getTime(),
                addGranularity(max, timeGranularity, 1).getTime(),
              ]
        }
        formatValue={formatShown}
        formatIndependentValue={value => formatTime(value, timeGranularity)}
        formatIndependentValueTooltip={value => formatInterval(value, timeGranularity)}
        independentTick={markPartialBuckets ? partialBucketTick : undefined}
        legend
        onClickDataPoint={
          openDrillDown == null
            ? undefined
            : (time, key) => openDrillDown({ bucketStart: time, key })
        }
      />
      {drillDownModal}
    </ChartCard>
  );
}
