import { addDays, differenceInCalendarDays, differenceInHours, min, startOfDay } from "date-fns";

import { type DateRange } from "../../data";
import { addGranularity, TimeGranularity } from "../../tools/timeGranularity";
import { type Granularity } from "../DashboardPageFilters/useDashboardFilters";

/** The equally long range just before `range`, for period-over-period change pills. */
export function previousRange(range: DateRange): DateRange {
  const from = new Date(range.from).getTime();
  const to = new Date(range.to).getTime();
  return { from: new Date(from - (to - from)).toISOString(), to: range.from };
}

/** The display granularity for a router bucket size; hourly buckets label like days. */
export function displayGranularity(granularity: Granularity): TimeGranularity {
  return granularity === "week" ? TimeGranularity.Week : TimeGranularity.Day;
}

/** The last bucket that lies wholly inside `range` and has already ended. */
export function latestCompleteBucket(
  times: readonly number[],
  range: DateRange,
  granularity: TimeGranularity,
) {
  const from = new Date(range.from).getTime();
  const end = Math.min(new Date(range.to).getTime(), Date.now());
  return times.findLast(
    time => time >= from && addGranularity(time, granularity, 1).getTime() <= end,
  );
}

const DAYS_PER_BUCKET: Readonly<Record<Exclude<Granularity, "hour">, number>> = { day: 1, week: 7 };

/**
 * Buckets `range` spans, in fractional buckets of whole days, like hosted's rate divisor
 * (`getFilterDayBasedDivisor`): empty days count, and a range reaching today ends at the end of
 * today so future days don't lower the average.
 */
export function rateDivisor(range: DateRange, granularity: Granularity) {
  const from = new Date(range.from);
  const to = new Date(range.to);
  if (granularity === "hour") {
    return Math.max(differenceInHours(min([to, new Date()]), from), 1);
  }
  const startOfToday = startOfDay(new Date());
  const end = to >= startOfToday ? addDays(startOfToday, 1) : to;
  return Math.max(differenceInCalendarDays(end, from) / DAYS_PER_BUCKET[granularity], 1);
}
