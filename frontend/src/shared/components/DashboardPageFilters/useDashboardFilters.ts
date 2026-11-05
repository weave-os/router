"use client";

import { subDays } from "date-fns";

import { useCallback, useMemo, useState } from "react";

import { DateScalar } from "../../objects/DateScalar";
import { DateFilter, Filter } from "../../tools/dateFilter";

export type Granularity = "hour" | "day" | "week";

/** Days the hosted Router dashboard looks back by default, today included. */
const ROUTER_DASHBOARD_LOOKBACK_DAYS = 14;

const defaultStart = () =>
  DateScalar.fromDate(subDays(new Date(), ROUTER_DASHBOARD_LOOKBACK_DAYS - 1));

const defaultDateFilter = () => Filter.date(defaultStart());

export function isDefaultDateFilter(filter: DateFilter) {
  return filter.start === defaultStart() && filter.end == null;
}

export interface UseDashboardFiltersResult {
  filters: {
    dateFilter: DateFilter;
    granularity: Granularity;
    fromISO: string;
    /** ISO timestamp of the range end: the exclusive end day, or now for open ranges. */
    toISO: string;
  };
  setDateFilter: (start?: DateScalar, end?: DateScalar) => void;
  resetDateFilter: () => void;
  setGranularity: (g: Granularity) => void;
}

/**
 * Router-local filter state. Mirrors WW's useDashboardPageFilters /
 * useGranularitySelector contract but without nuqs/PostHog/GraphQL.
 * Dates follow hosted's date filter: a start day and an optional exclusive end day.
 */
export function useDashboardFilters(): UseDashboardFiltersResult {
  const [dateFilter, setDateFilterState] = useState(defaultDateFilter);
  const [granularity, setGranularity] = useState<Granularity>("day");

  const setDateFilter = useCallback(
    (start?: DateScalar, end?: DateScalar) => setDateFilterState(Filter.date(start, end)),
    [],
  );
  const resetDateFilter = useCallback(() => setDateFilterState(defaultDateFilter()), []);

  const { fromISO, toISO } = useMemo(() => {
    // oxlint-disable-next-line react/purity -- an open range is anchored to the moment it was picked.
    const to = dateFilter.end != null ? DateScalar.toDate(dateFilter.end) : new Date();
    return {
      fromISO: DateScalar.toDate(dateFilter.start ?? defaultStart()).toISOString(),
      toISO: to.toISOString(),
    };
  }, [dateFilter]);

  return {
    filters: { dateFilter, granularity, fromISO, toISO },
    setDateFilter,
    resetDateFilter,
    setGranularity,
  };
}
