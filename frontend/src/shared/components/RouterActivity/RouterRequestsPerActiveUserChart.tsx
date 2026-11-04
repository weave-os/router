"use client";

import { useMemo } from "react";

import { type DateRange, type RouterMetricPoint, RouterMetricType } from "../../data";
import { useRouterMetricSeries } from "../../data/useRouterMetricSeries";
import { ChartColor } from "../../tools/chartColors";
import { formatBigDecimal } from "../../tools/format";
import { LoadState } from "../../tools/LoadState";
import { addGranularity, formatInterval, formatTime } from "../../tools/timeGranularity";
import { Chart, ChartAxisType, type ChartData, ChartSeriesType } from "../Chart";
import { ChartCard } from "../ChartCard";
import { type Granularity } from "../DashboardPageFilters/useDashboardFilters";
import { displayGranularity, previousRange } from "../RouterCharts/chartRange";
import { TIME_KEY } from "../RouterCharts/pivotSeries";
import { periodPercentChange, RouterChartHeadline } from "../RouterCharts/RouterChartHeadline";

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

function totalPerPerson(
  requests: readonly RouterMetricPoint[],
  activeUsers: readonly RouterMetricPoint[],
) {
  const people = activeUsers.reduce((sum, { value }) => sum + value, 0);
  return people === 0 ? null : requests.reduce((sum, { value }) => sum + value, 0) / people;
}

function median(values: readonly number[]) {
  if (values.length === 0) return null;
  const sorted = [...values].sort((a, b) => a - b);
  const middle = Math.floor(sorted.length / 2);
  return sorted.length % 2 === 0 ? (sorted[middle - 1] + sorted[middle]) / 2 : sorted[middle];
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
  const totalRequests = useRouterMetricSeries({
    granularity: null,
    range,
    type: RouterMetricType.ModelUsage,
  });
  const totalActive = useRouterMetricSeries({
    granularity: null,
    range,
    type: RouterMetricType.ActiveUsers,
  });
  const previousRequests = useRouterMetricSeries({
    granularity: null,
    range: previousWindow,
    type: RouterMetricType.ModelUsage,
  });
  const previousActive = useRouterMetricSeries({
    granularity: null,
    range: previousWindow,
    type: RouterMetricType.ActiveUsers,
  });

  const rows = useMemo(
    () =>
      LoadState.map(requests, activeUsers, (r, a) => requestsPerActivePerson(r.values, a.values)),
    [activeUsers, requests],
  );
  const mean = LoadState.map(totalRequests, totalActive, (r, a) =>
    totalPerPerson(r.values, a.values),
  );
  const percentChange = LoadState.map(
    totalRequests,
    totalActive,
    previousRequests,
    previousActive,
    (r, a, pr, pa) => {
      const now = totalPerPerson(r.values, a.values);
      return now == null
        ? null
        : periodPercentChange(now, totalPerPerson(pr.values, pa.values) ?? 0);
    },
  );
  const referenceLines = useMemo(
    () =>
      LoadState.map(rows, values => {
        const middle = median(values.map(row => row[PER_PERSON_KEY]));
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

  return (
    <ChartCard
      className={className}
      title="Requests per active person"
      subtitle="Router requests per bucket divided by the people active in it."
      headline={
        <RouterChartHeadline
          percentChange={percentChange}
          statistic={LoadState.map(mean, value =>
            value == null ? {} : { total: `${formatBigDecimal(value)} avg` },
          )}
        />
      }
    >
      <Chart
        config={{
          [PER_PERSON_KEY]: {
            color: ChartColor.Scale1,
            label: "Requests per person",
            type: ChartSeriesType.Bar,
          },
        }}
        data={LoadState.map(
          rows,
          values => values as ChartData<typeof TIME_KEY, string, number, number>,
        )}
        dependentKeys={PER_PERSON_KEYS}
        independentKey={TIME_KEY}
        independentType={ChartAxisType.Time}
        independentDomain={([min, max]) => [
          addGranularity(min, timeGranularity, -1).getTime(),
          addGranularity(max, timeGranularity, 1).getTime(),
        ]}
        formatValue={formatBigDecimal}
        formatIndependentValue={value => formatTime(value, timeGranularity)}
        formatIndependentValueTooltip={value => formatInterval(value, timeGranularity)}
        referenceLines={referenceLines}
      />
    </ChartCard>
  );
}
