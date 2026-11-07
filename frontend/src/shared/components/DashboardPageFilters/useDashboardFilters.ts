"use client";

import { differenceInCalendarDays, subDays } from "date-fns";

import { useCallback, useMemo, useState } from "react";

import { useRouterDashboardDataSource } from "../../data";
import { DateScalar } from "../../objects/DateScalar";
import { DateFilter, Filter } from "../../tools/dateFilter";
import {
  addGranularity,
  startOfGranularity,
  TimeGranularity,
  type WeekStartsOn,
} from "../../tools/timeGranularity";

export type Granularity = "hour" | "day" | "week";

export const GRANULARITY_OPTIONS: readonly Granularity[] = ["hour", "day", "week"];

/** Hosted hides daily (and finer) buckets for ranges longer than this or with no start. */
const DAY_GRANULARITY_MAX_DAYS = 45;

const LONG_RANGE_GRANULARITIES: readonly Granularity[] = ["week"];

export function availableGranularities(dateFilter: DateFilter): readonly Granularity[] {
  if (dateFilter.start == null) return LONG_RANGE_GRANULARITIES;
  const days = differenceInCalendarDays(
    DateScalar.toDate(dateFilter.end ?? DateScalar.today()),
    DateScalar.toDate(dateFilter.start),
  );
  return days > DAY_GRANULARITY_MAX_DAYS ? LONG_RANGE_GRANULARITIES : GRANULARITY_OPTIONS;
}

/** Widens `filter` to whole weeks for weekly buckets, as hosted's `snapDateFilterToGranularity`. */
export function snapDateFilterToGranularity(
  filter: DateFilter,
  granularity: Granularity,
  weekStartsOn: WeekStartsOn,
): DateFilter {
  if (granularity !== "week") return filter;
  const snap = (date: DateScalar) =>
    DateScalar.fromDate(
      startOfGranularity(DateScalar.toDate(date), TimeGranularity.Week, weekStartsOn),
    );
  const start = filter.start != null ? snap(filter.start) : undefined;
  let end = filter.end != null ? snap(filter.end) : undefined;
  if (end != null && end !== filter.end) {
    end = DateScalar.fromDate(addGranularity(DateScalar.toDate(end), TimeGranularity.Week, 1));
  }
  return start === filter.start && end === filter.end ? filter : Filter.date(start, end);
}

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
  granularityOptions: readonly Granularity[];
}

/**
 * Router-local filter state. Mirrors WW's useDashboardPageFilters /
 * useGranularitySelector contract but without nuqs/PostHog/GraphQL.
 * Dates follow hosted's date filter: a start day and an optional exclusive end day.
 */
export function useDashboardFilters(): UseDashboardFiltersResult {
  const { weekStartsOn } = useRouterDashboardDataSource();
  const [dateFilter, setDateFilterState] = useState(defaultDateFilter);
  const [selectedGranularity, setSelectedGranularity] = useState<Granularity>("day");
  const granularityOptions = availableGranularities(dateFilter);
  const granularity = granularityOptions.includes(selectedGranularity)
    ? selectedGranularity
    : granularityOptions[0];

  const setGranularity = useCallback(
    (next: Granularity) => {
      setDateFilterState(current => snapDateFilterToGranularity(current, next, weekStartsOn));
      setSelectedGranularity(next);
    },
    [weekStartsOn],
  );

  const setDateFilter = useCallback(
    (start?: DateScalar, end?: DateScalar) => setDateFilterState(Filter.date(start, end)),
    [],
  );
  const resetDateFilter = useCallback(() => setDateFilterState(defaultDateFilter()), []);

  const { fromISO, toISO } = useMemo(() => {
    // oxlint-disable-next-line react/purity -- an open range is anchored to the moment it was picked.
    const to = dateFilter.end != null ? DateScalar.toDate(dateFilter.end) : new Date();
    return {
      fromISO: (dateFilter.start != null
        ? DateScalar.toDate(dateFilter.start)
        : new Date(0)
      ).toISOString(),
      toISO: to.toISOString(),
    };
  }, [dateFilter]);

  return {
    filters: { dateFilter, granularity, fromISO, toISO },
    setDateFilter,
    resetDateFilter,
    setGranularity,
    granularityOptions,
  };
}
