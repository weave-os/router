"use client";

import { type LoadState } from "@weave-os/ui/tools/LoadState";

import { type RouterMetricSeries, type RouterMetricSeriesRequest } from "./metricSeries";
import {
  type RouterPerson,
  type RouterReportSummary,
  type RouterRoutingState,
} from "./metricSeries";
import { useRouterDashboardQuery } from "./RouterDashboardDataProvider";
import { type DateRange } from "./types";

/** One dashboard metric from the host's data source; reloads when any request field changes. */
export function useRouterMetricSeries({
  billing,
  granularity,
  group,
  range,
  type,
}: RouterMetricSeriesRequest): LoadState<RouterMetricSeries> {
  return useRouterDashboardQuery(
    dataSource => dataSource.metrics.series({ billing, granularity, group, range, type }),
    [billing, granularity, group, range.from, range.to, type],
  );
}

/** The stat-card totals for `range`, optionally only routed or passthrough requests. */
export function useRouterReportSummary(
  range: DateRange,
  routingState?: RouterRoutingState,
): LoadState<RouterReportSummary> {
  return useRouterDashboardQuery(
    dataSource => dataSource.metrics.reportSummary(range, routingState),
    [range.from, range.to, routingState],
  );
}

export function useRouterPeople(): LoadState<readonly RouterPerson[]> {
  return useRouterDashboardQuery(dataSource => dataSource.metrics.people(), []);
}
