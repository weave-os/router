"use client";

import { tz } from "@date-fns/tz";
import { format } from "date-fns";
import { useMemo } from "react";

import { Skeleton } from "@/shared/components/atoms/Skeleton";
import { Heatmap, HeatmapDensity, HeatmapLegend, HeatmapTexture } from "@/shared/components/Chart";
import { ChartCard } from "@/shared/components/ChartCard";
import { ErrorCard } from "@/shared/components/ErrorCard";
import { LoadStateWrapper } from "@/shared/components/LoadStateWrapper";
import { type DateRange, type RouterMetricPoint, RouterMetricType } from "@/shared/data";
import { useRouterMetricSeries } from "@/shared/data/useRouterMetricSeries";
import { ChartColor } from "@/shared/tools/chartColors";
import { formatBigDecimal } from "@/shared/tools/format";
import { LoadState } from "@/shared/tools/LoadState";
import {
  addGranularityUTC,
  startOfGranularityUTC,
  TimeGranularity,
} from "@/shared/tools/timeGranularity";

const HEATMAP_COLOR = ChartColor.Scale2;
const WEEKDAYS = ["Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"];
const MONTH_LABEL_SPAN_COLUMNS = 3;
const utc = tz("UTC");

/** One month label per column where the month changes; a label too close to the next is dropped. */
export function monthColumnLabels(weekStarts: readonly Date[]) {
  const labels = weekStarts.map((weekStart, index) =>
    index === 0 || weekStart.getUTCMonth() !== weekStarts[index - 1].getUTCMonth()
      ? format(weekStart, "MMM", { in: utc })
      : "",
  );
  return labels.map((label, index) =>
    labels.slice(index + 1, index + MONTH_LABEL_SPAN_COLUMNS).some(next => next !== "") ? "" : label,
  );
}

/**
 * Daily requests as weekday rows × week columns (weeks start Monday). A day in range with no
 * requests is 0; a day outside it is null, so a missing day never reads as zero.
 */
export function dailyRequestsGrid(values: readonly RouterMetricPoint[], range: DateRange) {
  const requestsByDay = new Map<number, number>();
  for (const { time, value } of values) {
    if (time == null) continue;
    const day = startOfGranularityUTC(new Date(time), TimeGranularity.Day).getTime();
    requestsByDay.set(day, (requestsByDay.get(day) ?? 0) + value);
  }
  const filterStart = startOfGranularityUTC(new Date(range.from), TimeGranularity.Day);
  const filterEnd = new Date(range.to);
  const weekStarts: Date[] = [];
  for (
    let weekStart = startOfGranularityUTC(filterStart, TimeGranularity.Week);
    weekStart < filterEnd;
    weekStart = addGranularityUTC(weekStart, TimeGranularity.Week, 1)
  ) {
    weekStarts.push(weekStart);
  }
  const now = Date.now();
  const cells = WEEKDAYS.map((_, weekdayIndex) =>
    weekStarts.map(weekStart => {
      const day = addGranularityUTC(weekStart, TimeGranularity.Day, weekdayIndex);
      const inRange = day >= filterStart && day < filterEnd && day.getTime() <= now;
      return {
        label: format(day, "EEE, MMM d", { in: utc }),
        value: requestsByDay.get(day.getTime()) ?? (inRange ? 0 : null),
      };
    }),
  );
  return {
    cells,
    columns: weekStarts.map(weekStart => format(weekStart, "MMM d", { in: utc })),
    monthLabels: monthColumnLabels(weekStarts),
  };
}

function formatRequests(requests: number) {
  return `${formatBigDecimal(requests)} ${requests === 1 ? "request" : "requests"}`;
}

/** Router requests per day by weekday. Always daily, whatever the page's bucket size. */
export function RouterDailyRequestsHeatmap({ className, range }: { className?: string; range: DateRange }) {
  const daily = useRouterMetricSeries({
    granularity: "day",
    range,
    type: RouterMetricType.ModelUsage,
  });
  const grid = useMemo(
    () => LoadState.map(daily, ({ values }) => (values.length === 0 ? null : dailyRequestsGrid(values, range))),
    [daily, range],
  );

  return (
    <ChartCard
      autoHeight
      className={className}
      headerBottom={
        <LoadStateWrapper state={grid}>
          {dailyGrid =>
            dailyGrid == null ? null : (
              <HeatmapLegend
                baseColor={HEATMAP_COLOR}
                className="mt-2"
                label="Requests per day"
                showMissing={dailyGrid.cells.some(row => row.some(({ value }) => value == null))}
                texture={HeatmapTexture.Halftone}
              />
            )
          }
        </LoadStateWrapper>
      }
      title="Daily requests"
      subtitle="Router requests per day, by weekday. Always daily, whatever the bucket size."
    >
      <LoadStateWrapper
        state={grid}
        loading={<Skeleton className="h-48 w-full" />}
        error={() => <ErrorCard title="Couldn't load daily requests" description="Try refreshing the page." />}
      >
        {dailyGrid =>
          dailyGrid == null ? (
            <ErrorCard title="No requests yet" description="Route requests through Weave Router to see when people use it." />
          ) : (
            <Heatmap
              baseColor={HEATMAP_COLOR}
              cells={dailyGrid.cells}
              columns={dailyGrid.columns}
              density={HeatmapDensity.Compact}
              formatValue={formatRequests}
              legendLabel={null}
              renderColumnHeader={columnIndex => dailyGrid.monthLabels[columnIndex]}
              rowHeaderWidth={48}
              rows={WEEKDAYS}
              texture={HeatmapTexture.Halftone}
            />
          )
        }
      </LoadStateWrapper>
    </ChartCard>
  );
}
