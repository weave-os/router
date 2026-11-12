"use client";

import { addDays, addMonths, addWeeks, startOfDay, startOfMonth, startOfWeek } from "date-fns";

import { useCallback, useMemo, useState } from "react";

export type Granularity = "hour" | "day" | "week";

export interface DateRange {
  /** Stable key used in the dropdown and persisted state. */
  id: string;
  label: string;
  /** Pill text for the selected range, e.g. "since October 22"; defaults to the lowercased label. */
  pillLabel?: (start: Date) => string;
  /** Default granularity if the user hasn't picked one explicitly. */
  defaultGranularity: Granularity;
  start: (now: Date) => Date;
}

const MONTH_DAY = new Intl.DateTimeFormat("en-US", { day: "numeric", month: "long" });

/** Days the hosted Router dashboard looks back by default, today included. */
const ROUTER_DASHBOARD_LOOKBACK_DAYS = 14;

/**
 * Mirrors WorkWeave's default date filter set (week- and month-anchored
 * windows ending at "now"), trimmed to the granularities the router
 * supports.
 */
export const DATE_RANGES: readonly DateRange[] = [
  {
    id: "last-14-days",
    label: "Last 14 days",
    pillLabel: start => `since ${MONTH_DAY.format(start)}`,
    defaultGranularity: "day",
    start: now => startOfDay(addDays(now, -(ROUTER_DASHBOARD_LOOKBACK_DAYS - 1))),
  },
  {
    id: "this-week",
    label: "This week",
    defaultGranularity: "day",
    start: now => startOfWeek(now, { weekStartsOn: 1 }),
  },
  {
    id: "last-month",
    label: "Last month",
    defaultGranularity: "day",
    start: now => addWeeks(startOfWeek(now, { weekStartsOn: 1 }), -4),
  },
  {
    id: "last-2-months",
    label: "Last 2 months",
    defaultGranularity: "day",
    start: now => addWeeks(startOfWeek(now, { weekStartsOn: 1 }), -8),
  },
  {
    id: "last-3-months",
    label: "Last 3 months",
    defaultGranularity: "day",
    start: now => addWeeks(startOfWeek(now, { weekStartsOn: 1 }), -12),
  },
  {
    id: "last-5-months",
    label: "Last 5 months",
    defaultGranularity: "day",
    start: now => addMonths(startOfMonth(now), -5),
  },
  {
    id: "last-11-months",
    label: "Last 11 months",
    defaultGranularity: "day",
    start: now => addMonths(startOfMonth(now), -11),
  },
] as const;

export const DEFAULT_RANGE_ID = "last-14-days";

export interface UseDashboardFiltersResult {
  filters: {
    range: DateRange;
    granularity: Granularity;
    fromISO: string;
    /** ISO timestamp of the range end (now). */
    toISO: string;
  };
  setRangeId: (id: string) => void;
  setGranularity: (g: Granularity) => void;
}

/**
 * Router-local filter state. Mirrors WW's useDashboardPageFilters /
 * useGranularitySelector contract but without nuqs/PostHog/GraphQL.
 * Granularity defaults from the selected range; the user can override
 * it with setGranularity, and the override is reset whenever the range
 * changes.
 */
export function useDashboardFilters(
  initialRangeId: string = DEFAULT_RANGE_ID,
): UseDashboardFiltersResult {
  const [rangeId, setRangeIdState] = useState(initialRangeId);
  const [granularityOverride, setGranularityOverride] = useState<Granularity | null>(null);

  const range = useMemo(() => DATE_RANGES.find(r => r.id === rangeId) ?? DATE_RANGES[0], [rangeId]);
  const granularity = granularityOverride ?? range.defaultGranularity;

  const setRangeId = useCallback((id: string) => {
    setRangeIdState(id);
    setGranularityOverride(null);
  }, []);

  const setGranularity = useCallback((g: Granularity) => {
    setGranularityOverride(g);
  }, []);

  const { fromISO, toISO } = useMemo(() => {
    // oxlint-disable-next-line react/purity -- a range is anchored to the moment it was picked.
    const to = new Date();
    const from = range.start(to);
    return { fromISO: from.toISOString(), toISO: to.toISOString() };
  }, [range]);

  return {
    filters: { range, granularity, fromISO, toISO },
    setRangeId,
    setGranularity,
  };
}
