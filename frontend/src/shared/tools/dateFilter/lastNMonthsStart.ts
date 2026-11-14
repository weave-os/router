import { DateScalar } from "../../objects/DateScalar";
import { TimeGranularity } from "../timeGranularity";
import { addGranularity, startOfGranularity } from "../timeGranularity";

/**
 * Start date for a “last N months” filter: the same calendar day N months before
 * `now` (open-ended through today).
 *
 * Uses date-fns `addMonths` via {@link addGranularity}, which clamps end-of-month
 * overflows (e.g. Mar 31 minus 1 month → Feb 28/29). Keep FE / Go
 * `subtractMonthsClamp` / MCP `_last_n_months_start` aligned on that clamp.
 *
 * Examples (on 2024-03-14):
 * - N=1 → 2024-02-14
 * - N=3 → 2023-12-14
 * - N=6 → 2023-09-14
 *
 * Examples (on 2024-03-31, end-of-month clamp):
 * - N=1 → 2024-02-29 (2024 is a leap year)
 * - N=1 on 2025-03-31 → 2025-02-28
 */
export function lastNMonthsStart(monthCount: number, now: Date = new Date()): Date {
  return startOfGranularity(
    addGranularity(now, TimeGranularity.Month, -monthCount),
    TimeGranularity.Day,
  );
}

/**
 * {@link lastNMonthsStart} as a {@link DateScalar} for filter construction.
 */
export function lastNMonthsStartScalar(monthCount: number, now: Date = new Date()): DateScalar {
  return DateScalar.fromDate(lastNMonthsStart(monthCount, now));
}
