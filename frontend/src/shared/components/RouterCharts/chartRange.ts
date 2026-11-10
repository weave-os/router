import { addGranularity, TimeGranularity } from "@weave-os/ui/tools/timeGranularity";

import { type DateRange } from "../../data";
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
