import { addDays, addHours, addWeeks, startOfDay, startOfHour, startOfWeek } from "date-fns";

import { type DateRange } from "../../data";
import { addGranularity, TimeGranularity, type WeekStartsOn } from "../../tools/timeGranularity";
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

const BUCKET_STEPS: Readonly<
  Record<
    Granularity,
    { next: (date: Date) => Date; start: (date: Date, weekStartsOn: WeekStartsOn) => Date }
  >
> = {
  day: { next: date => addDays(date, 1), start: date => startOfDay(date) },
  hour: { next: date => addHours(date, 1), start: date => startOfHour(date) },
  week: {
    next: date => addWeeks(date, 1),
    start: (date, weekStartsOn) => startOfWeek(date, { weekStartsOn }),
  },
};

/**
 * Buckets `range` spans, empty ones included, so per-bucket averages divide by every day in the
 * range the way hosted's mean statistic does.
 */
export function bucketCount(
  range: DateRange,
  granularity: Granularity,
  weekStartsOn: WeekStartsOn,
) {
  const { next, start } = BUCKET_STEPS[granularity];
  const to = new Date(range.to);
  let count = 0;
  for (let bucket = start(new Date(range.from), weekStartsOn); bucket < to; bucket = next(bucket)) {
    count++;
  }
  return Math.max(count, 1);
}
