"use client";

import { type LoadState } from "@/shared/tools/LoadState";

import { type RouterMetricSeries, type RouterMetricSeriesRequest } from "./metricSeries";
import { useRouterDashboardQuery } from "./RouterDashboardDataProvider";
import { type RouterReportSummary } from "./metricSeries";
import { type DateRange } from "./types";

/** One dashboard metric from the host's data source; reloads when any request field changes. */
export function useRouterMetricSeries({
  granularity,
  group,
  range,
  type,
}: RouterMetricSeriesRequest): LoadState<RouterMetricSeries> {
  return useRouterDashboardQuery(
    dataSource => dataSource.metrics.series({ granularity, group, range, type }),
    [granularity, group, range.from, range.to, type],
  );
}

/** The stat-card totals for `range`. */
export function useRouterReportSummary(range: DateRange): LoadState<RouterReportSummary> {
  return useRouterDashboardQuery(
    dataSource => dataSource.metrics.reportSummary(range),
    [range.from, range.to],
  );
}
