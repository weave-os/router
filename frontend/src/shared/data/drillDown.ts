import type {
  RouterBilling,
  RouterMetricGroup,
  RouterMetricType,
  RouterRoutingState,
} from "./metricSeries";
import type { DateRange, MetricsGranularity } from "./types";
import type { ComponentType } from "react";

import { addDays, addHours, addWeeks, max, min } from "date-fns";

/** The data point a viewer clicked, in the chart's own vocabulary. */
export interface RouterDrillDownRequest {
  billing?: RouterBilling;
  /** Clicked bucket start, epoch milliseconds; omitted for whole-range totals. */
  bucketStart?: number;
  /** Cumulative charts keep the range start so the drill-down explains the running total. */
  cumulative?: boolean;
  granularity: MetricsGranularity | null;
  group?: RouterMetricGroup;
  /** The clicked series within `group`. */
  key?: string;
  /** Decision models to keep, e.g. every model of a clicked maker. */
  models?: readonly string[];
  /** The chart's date range. */
  range: DateRange;
  routingState?: RouterRoutingState;
  type: RouterMetricType;
}

export interface RouterDrillDownModalProps {
  request: RouterDrillDownRequest;
  onClose: () => void;
}

export interface DrillDownDataSource {
  /** Explains a clicked data point; self-hosted renders the shared `RouterDrillDownModal`. */
  Modal: ComponentType<RouterDrillDownModalProps>;
}

export interface RouterDrillDownModelPair {
  catalogCostUsd: number;
  comparisonCostUsd: number;
  decisionModel: string;
  providers: readonly string[];
  requestedModel: string;
  requests: number;
  subscriptionRequests: number;
}

export interface RouterDrillDownSession {
  clientApp: string;
  finishedAt: number;
  models: readonly RouterDrillDownModelPair[];
  sessionId: string;
  startedAt: number;
}

export interface RouterDrillDownPerson {
  displayName: string;
  email: string;
  id: string;
  linked: boolean;
  sessions: readonly RouterDrillDownSession[];
}

export interface RouterDrillDownResult {
  comparisonModel: string;
  people: readonly RouterDrillDownPerson[];
}

/** Loads the people, sessions and model pairs behind a request for the shared modal. */
export type LoadRouterDrillDown = (
  request: RouterDrillDownRequest,
) => Promise<RouterDrillDownResult>;

const ADD_GRANULARITY = { day: addDays, hour: addHours, week: addWeeks } as const;

/** The clicked bucket intersected with the chart range; cumulative charts keep the range start. */
export function drillDownRange({
  bucketStart,
  cumulative,
  granularity,
  range,
}: RouterDrillDownRequest): DateRange {
  if (bucketStart == null || granularity == null) return range;
  const from = new Date(range.from);
  const to = new Date(range.to);
  return {
    from: (cumulative === true ? from : max([from, new Date(bucketStart)])).toISOString(),
    to: min([to, ADD_GRANULARITY[granularity](bucketStart, 1)]).toISOString(),
  };
}
