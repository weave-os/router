"use client";

import {
  Chart,
  ChartAxisType,
  type ChartData,
  ChartSeriesType,
} from "@weave-os/ui/components/Chart";
import { ChartCard } from "@weave-os/ui/components/ChartCard";
import { AccountChartColor, ChartColor } from "@weave-os/ui/tools/chartColors";
import { formatBigDecimal } from "@weave-os/ui/tools/format";
import { LoadState } from "@weave-os/ui/tools/LoadState";
import { formatInterval, formatTime } from "@weave-os/ui/tools/timeGranularity";
import { median } from "d3-array";

import { useMemo } from "react";

import {
  type DateRange,
  RouterMetricGroup,
  type RouterMetricPoint,
  RouterMetricType,
  useRouterDrillDown,
} from "../../data";
import { useRouterMetricSeries } from "../../data/useRouterMetricSeries";
import { type Granularity } from "../DashboardPageFilters/useDashboardFilters";
import {
  displayGranularity,
  latestCompleteBucket,
  previousRange,
} from "../RouterCharts/chartRange";
import { TIME_KEY } from "../RouterCharts/pivotSeries";
import { periodPercentChange, RouterChartHeadline } from "../RouterCharts/RouterChartHeadline";
import { ROUTER_SOURCE_MARK } from "../RouterCharts/routerSourceMark";
import { RouterConnectEmptyState } from "../RouterConnectEmptyState";

const PER_PERSON_KEY = "requests_per_person";
const PER_PERSON_KEYS = LoadState.loaded([PER_PERSON_KEY] as const);

function totalsByTime(points: readonly RouterMetricPoint[]) {
  const totals = new Map<number, number>();
  for (const { time, value } of points) {
    if (time == null) continue;
    const bucket = new Date(time).getTime();
    totals.set(bucket, (totals.get(bucket) ?? 0) + value);
  }
  return totals;
}

/** Requests ÷ active people per bucket; buckets with nobody active are left out. */
export function requestsPerActivePerson(
  requests: readonly RouterMetricPoint[],
  activeUsers: readonly RouterMetricPoint[],
) {
  const requestTotals = totalsByTime(requests);
  return [...totalsByTime(activeUsers)]
    .filter(([, people]) => people > 0)
    .sort(([a], [b]) => a - b)
    .map(([time, people]) => ({
      [PER_PERSON_KEY]: (requestTotals.get(time) ?? 0) / people,
      [TIME_KEY]: time,
    }));
}

/** The mean over buckets of requests ÷ active people; null with nobody active. */
function meanPerPerson(
  requests: readonly RouterMetricPoint[],
  activeUsers: readonly RouterMetricPoint[],
) {
  const perBucket = requestsPerActivePerson(requests, activeUsers);
  if (perBucket.length === 0) return null;
  return perBucket.reduce((sum, row) => sum + row[PER_PERSON_KEY], 0) / perBucket.length;
}

export function RouterRequestsPerActiveUserChart({
  className,
  granularity,
  range,
}: {
  className?: string;
  granularity: Granularity;
  range: DateRange;
}) {
  const timeGranularity = displayGranularity(granularity);
  const requests = useRouterMetricSeries({ granularity, range, type: RouterMetricType.ModelUsage });
  const activeUsers = useRouterMetricSeries({
    granularity,
    range,
    type: RouterMetricType.ActiveUsers,
  });
  const previousWindow = useMemo(() => previousRange(range), [range]);
  const previousRequests = useRouterMetricSeries({
    granularity,
    range: previousWindow,
    type: RouterMetricType.ModelUsage,
  });
  const previousActive = useRouterMetricSeries({
    granularity,
    range: previousWindow,
    type: RouterMetricType.ActiveUsers,
  });

  const rows = useMemo(
    () =>
      LoadState.map(requests, activeUsers, (r, a) => requestsPerActivePerson(r.values, a.values)),
    [activeUsers, requests],
  );
  const mean = LoadState.map(requests, activeUsers, (r, a) => meanPerPerson(r.values, a.values));
  const percentChange = LoadState.map(
    requests,
    activeUsers,
    previousRequests,
    previousActive,
    (r, a, pr, pa) => {
      const now = meanPerPerson(r.values, a.values);
      return now == null ? null : periodPercentChange(now, meanPerPerson(pr.values, pa.values));
    },
  );
  const referenceLines = useMemo(
    () =>
      LoadState.map(rows, perBucket => {
        const middle = median(perBucket.map(row => row[PER_PERSON_KEY]));
        return middle == null
          ? []
          : [
              {
                dashed: true,
                label: `Median ${formatBigDecimal(middle)}`,
                side: "right" as const,
                type: "dependent" as const,
                value: middle,
              },
            ];
      }),
    [rows],
  );
  const { drillDownModal, openDrillDown } = useRouterDrillDown({
    granularity,
    group: RouterMetricGroup.UserLink,
    range,
    type: RouterMetricType.ActiveUsers,
  });

  return (
    <ChartCard
      chartId="router_requests_per_active_user"
      sources={ROUTER_SOURCE_MARK}
      className={className}
      title="Requests per active person"
      subtitle="Router requests divided by the people who sent them, per bucket, linked and unlinked together."
      headline={
        <RouterChartHeadline
          percentChange={percentChange}
          statistic={LoadState.map(mean, value =>
            value == null ? {} : { total: `${formatBigDecimal(value)} avg` },
          )}
        />
      }
    >
      {LoadState.isLoaded(rows) && rows.value.length === 0 ? (
        <RouterConnectEmptyState message="Route requests through Weave Router to track usage per person." />
      ) : (
        <Chart
          config={{
            [PER_PERSON_KEY]: {
              color: ChartColor.Scale1,
              label: "Requests per active person",
              type: ChartSeriesType.Bar,
            },
          }}
          data={LoadState.map(
            rows,
            perBucket => perBucket as ChartData<typeof TIME_KEY, string, number, number>,
          )}
          dependentKeys={PER_PERSON_KEYS}
          independentKey={TIME_KEY}
          independentType={ChartAxisType.Category}
          formatValue={formatBigDecimal}
          formatIndependentValue={value => formatTime(value, timeGranularity)}
          formatIndependentValueTooltip={value => formatInterval(value, timeGranularity)}
          highlightedIndependentValue={latestCompleteBucket(
            (LoadState.unwrap(rows) ?? []).map(row => row[TIME_KEY]),
            range,
            timeGranularity,
          )}
          mutedBarColor={AccountChartColor.Gray}
          referenceLines={referenceLines}
          onClickDataPoint={
            openDrillDown == null ? undefined : time => openDrillDown({ bucketStart: time })
          }
        />
      )}
      {drillDownModal}
    </ChartCard>
  );
}
