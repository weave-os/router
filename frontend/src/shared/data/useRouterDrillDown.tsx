"use client";

import type { RouterDrillDownRequest } from "./drillDown";

import { useCallback, useState } from "react";

import { isUnavailable } from "./availability";
import { useRouterDashboardDataSource } from "./RouterDashboardDataProvider";

type RouterDrillDownScope = Omit<RouterDrillDownRequest, "bucketStart" | "key" | "models">;

export interface OpenRouterDrillDownOptions {
  bucketStart?: number;
  key?: string;
  models?: readonly string[];
}

/** Opens the host's drill-down for a clicked point; `openDrillDown` is undefined when the host has none. */
export function useRouterDrillDown({
  billing,
  cumulative,
  granularity,
  group,
  range,
  routingState,
  type,
}: RouterDrillDownScope) {
  const { drillDown } = useRouterDashboardDataSource();
  const [request, setRequest] = useState<RouterDrillDownRequest>();
  const open = useCallback(
    ({ bucketStart, key, models }: OpenRouterDrillDownOptions = {}) =>
      setRequest({
        billing,
        bucketStart,
        cumulative,
        granularity,
        group,
        key,
        models,
        range,
        routingState,
        type,
      }),
    [billing, cumulative, granularity, group, range, routingState, type],
  );

  if (isUnavailable(drillDown)) return { drillDownModal: null, openDrillDown: undefined };
  return {
    drillDownModal:
      request == null ? null : (
        <drillDown.Modal request={request} onClose={() => setRequest(undefined)} />
      ),
    openDrillDown: open,
  };
}
