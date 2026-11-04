import { type DateRange } from "../../data";
import { TimeGranularity } from "../../tools/timeGranularity";
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
