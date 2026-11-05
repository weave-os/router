import { format, startOfYear } from "date-fns";

import { DateScalar } from "../../objects/DateScalar";
import { pluralize } from "../strings";
import { DateFormat } from "../time";
import { TimeGranularity } from "../timeGranularity";
import {
  addGranularity,
  differenceInGranularity,
  expandedDifferenceInGranularity,
  startOfGranularity,
  WeekStartsOn,
} from "../timeGranularity";
import { DateFilter } from "./dateFilter";
import { lastNMonthsStart } from "./lastNMonthsStart";

export interface GetDateFilterTitleOptions {
  /**
   * Whether to start the title with a lowercase letter (where applicable).
   *
   * @default false
   */
  lowercase?: boolean;

  /**
   * First day of the week for week-boundary detection (0 = Sunday, 1 = Monday).
   *
   * @default 1
   */
  weekStartsOn?: WeekStartsOn;
}

/** Gets the year for a start date. */
function getStartYear(date: Date): number {
  return date.getFullYear();
}

/** Gets the year for an end date. */
function getEndYear(date: Date): number {
  return new Date(date.getTime() - 1).getFullYear();
}

/**
 * Returns a plain English description of the date filter.
 */
export function getDateFilterTitle(filter: DateFilter, options: GetDateFilterTitleOptions = {}) {
  const { end, start } = filter;
  const { lowercase = false } = options;

  // Check for invalid ranges
  if (start != null && end != null && start >= end) {
    return "(invalid)";
  }

  // All time case
  if (start == null && end == null) {
    return lowercase ? "all time" : "All time";
  }

  // Only end date specified - "Until X"
  if (start == null && end != null) {
    return formatUntilDate(end, options);
  }

  // Only start date specified - "Since X" or "Last X period"
  if (start != null && end == null) {
    return formatSinceDate(start, options);
  }

  // Both dates specified - date range
  if (start != null && end != null) {
    return formatDateRange(start, end, options);
  }

  // Should never reach here
  return "(invalid)";
}

/**
 * Formats "Until X" date strings
 */
function formatUntilDate(to: DateScalar, { lowercase = false }: GetDateFilterTitleOptions): string {
  const toDate = addGranularity(DateScalar.toDate(to), TimeGranularity.Day, -1);
  const currentYear = getStartYear(new Date());
  const toYear = getEndYear(toDate);

  const prefix = lowercase ? "until" : "Until";

  if (toYear === currentYear) {
    return `${prefix} ${format(toDate, DateFormat.MonthDay)}`;
  }

  return `${prefix} ${format(toDate, DateFormat.MonthDayYear)}`;
}

/**
 * Formats "Since X" or "Last X period" date strings
 */
function formatSinceDate(
  from: DateScalar,
  { lowercase = false, weekStartsOn = 1 }: GetDateFilterTitleOptions,
): string {
  const fromDate = DateScalar.toDate(from);
  const nowDate = new Date();
  const currentYear = getStartYear(nowDate);
  const fromYear = getStartYear(fromDate);

  // Check if the date is at a breakpoint
  const isYearStart = from === DateScalar.fromDate(startOfYear(fromDate));
  const isMonthStart =
    from === DateScalar.fromDate(startOfGranularity(fromDate, TimeGranularity.Month, weekStartsOn));
  const isWeekStart =
    from === DateScalar.fromDate(startOfGranularity(fromDate, TimeGranularity.Week, weekStartsOn));

  // For recent dates, use "Last X days/weeks/months/years"
  const daysDiff = expandedDifferenceInGranularity(nowDate, fromDate, TimeGranularity.Day);
  const weeksDiff = expandedDifferenceInGranularity(
    nowDate,
    fromDate,
    TimeGranularity.Week,
    weekStartsOn,
  );
  const monthsDiff = expandedDifferenceInGranularity(nowDate, fromDate, TimeGranularity.Month);
  const yearsDiff = expandedDifferenceInGranularity(nowDate, fromDate, TimeGranularity.Year);

  if (daysDiff === 1) {
    return lowercase ? "today" : "Today";
  }

  if (isWeekStart && weeksDiff === 1) {
    return lowercase ? "this week" : "This week";
  }

  const lastPrefix = lowercase ? "last" : "Last";

  // Same-date “last N months” presets (open-ended from N months ago). Prefer
  // equality to the helper so end-of-month clamp (Mar 31 → Feb 28) still titles
  // as “Last 1 month” rather than “Since February 28”. Check before week
  // heuristics so a Monday-aligned month start is not titled “Last 4 weeks”.
  for (const n of [1, 2, 3, 6, 12] as const) {
    if (from === DateScalar.fromDate(lastNMonthsStart(n, nowDate))) {
      return `${lastPrefix} ${n} ${pluralize(n, "month")}`;
    }
  }

  // Handle "last 2 weeks"
  if (isWeekStart && weeksDiff === 2) {
    return `${lastPrefix} 2 weeks`;
  }

  if (daysDiff <= 5) {
    return `${lastPrefix} ${daysDiff} ${pluralize(daysDiff, "day")}`;
  }

  if (isWeekStart && weeksDiff < 5) {
    return `${lastPrefix} ${weeksDiff} ${pluralize(weeksDiff, "week")}`;
  }

  // Calendar-month “last N months” (open-ended from month start) — still used
  // for manually chosen month-start ranges that are not a preset same-date.
  if (isMonthStart && monthsDiff > 0 && monthsDiff <= 12) {
    return `${lastPrefix} ${monthsDiff} ${pluralize(monthsDiff, "month")}`;
  }

  // Handle years
  if (isYearStart && yearsDiff > 0) {
    return `${lastPrefix} ${yearsDiff} ${pluralize(yearsDiff, "year")}`;
  }

  const sincePrefix = lowercase ? "since" : "Since";

  // For non-breakpoint dates, use "Since X"
  if (fromYear === currentYear) {
    return `${sincePrefix} ${format(fromDate, DateFormat.MonthDay)}`;
  }

  return `${sincePrefix} ${format(fromDate, DateFormat.MonthDayYear)}`;
}

/**
 * Formats date range strings
 */
function formatDateRange(
  from: DateScalar,
  to: DateScalar,
  { lowercase = false, weekStartsOn = 1 }: GetDateFilterTitleOptions,
): string {
  const fromDate = DateScalar.toDate(from);
  const toDate = DateScalar.toDate(to);

  const currentYear = getStartYear(new Date());
  const fromYear = getStartYear(fromDate);
  const toYear = getEndYear(toDate);

  // Check if dates are at year breakpoints
  const isFromYearStart =
    from === DateScalar.fromDate(startOfGranularity(fromDate, TimeGranularity.Year, weekStartsOn));
  const isToYearEnd =
    to === DateScalar.fromDate(startOfGranularity(toDate, TimeGranularity.Year, weekStartsOn));

  // Full year(s) case
  if (isFromYearStart && isToYearEnd && fromYear === toYear) {
    return `${fromYear}`;
  }

  if (isFromYearStart && isToYearEnd) {
    return `${fromYear}-${toYear}`;
  }

  // Check if dates are at week breakpoints
  const isFromWeekStart =
    from === DateScalar.fromDate(startOfGranularity(fromDate, TimeGranularity.Week, weekStartsOn));
  const isToWeekEnd =
    to === DateScalar.fromDate(startOfGranularity(toDate, TimeGranularity.Week, weekStartsOn));

  // Week ranges - handle special case for "this week" and similar week-based filters
  if (isFromWeekStart && isToWeekEnd) {
    const weeksDiff = differenceInGranularity(toDate, fromDate, TimeGranularity.Week);

    // If it's exactly one week, check if it's the current week
    if (weeksDiff === 1) {
      const now = new Date();
      const currentWeekStart = startOfGranularity(now, TimeGranularity.Week, weekStartsOn);

      if (from === DateScalar.fromDate(currentWeekStart)) {
        return lowercase ? "this week" : "This week";
      }
    }

    // If it's exactly two weeks, check if it's "last 2 weeks" (last week + this week)
    if (weeksDiff === 2) {
      const now = new Date();
      const currentWeekStart = startOfGranularity(now, TimeGranularity.Week, weekStartsOn);
      const lastWeekStart = addGranularity(currentWeekStart, TimeGranularity.Week, -1);

      if (from === DateScalar.fromDate(lastWeekStart)) {
        return lowercase ? "last 2 weeks" : "Last 2 weeks";
      }
    }

    // For other week ranges, show the date range without subtracting a day
    const toDateAdjusted = addGranularity(toDate, TimeGranularity.Day, -1);

    if (fromYear === toYear && fromYear === currentYear) {
      return `${format(fromDate, DateFormat.MonthDay)} - ${format(toDateAdjusted, DateFormat.MonthDay)}`;
    }

    return `${format(fromDate, DateFormat.MonthDayYear)} - ${format(toDateAdjusted, DateFormat.MonthDayYear)}`;
  }

  // Check if dates are at month breakpoints
  const isFromMonthStart =
    from === DateScalar.fromDate(startOfGranularity(fromDate, TimeGranularity.Month, weekStartsOn));
  const isToMonthEnd =
    to === DateScalar.fromDate(startOfGranularity(toDate, TimeGranularity.Month, weekStartsOn));

  // Month ranges
  if (isFromMonthStart && isToMonthEnd) {
    const fromMonth = format(fromDate, DateFormat.MonthName);
    const toMonth = format(addGranularity(toDate, TimeGranularity.Month, -1), DateFormat.MonthName);

    if (fromYear === toYear && fromYear === currentYear && fromMonth === toMonth) {
      return fromMonth;
    }

    if (fromYear === toYear && fromYear === currentYear) {
      return `${fromMonth} - ${toMonth}`;
    }

    if (fromYear === toYear) {
      return `${fromMonth} - ${toMonth} ${fromYear}`;
    }

    return `${fromMonth} ${fromYear} - ${toMonth} ${toYear}`;
  }

  // Arbitrary date ranges
  return formatArbitraryDateRange(fromDate, toDate);
}

/**
 * Formats arbitrary date range strings (non-breakpoint dates)
 */
export function formatArbitraryDateRange(fromDate: Date, toDate: Date): string {
  const fromYear = getStartYear(fromDate);
  const toYear = getEndYear(toDate);
  const currentYear = getStartYear(new Date());

  const toDateAdjusted = addGranularity(toDate, TimeGranularity.Day, -1);

  if (fromYear === toYear && fromYear === currentYear) {
    return `${format(fromDate, DateFormat.MonthDay)} - ${format(toDateAdjusted, DateFormat.MonthDay)}`;
  }

  if (fromYear === toYear) {
    return `${format(fromDate, DateFormat.MonthDay)} - ${format(toDateAdjusted, DateFormat.MonthDayYear)}`;
  }

  if (fromYear === currentYear) {
    return `${format(fromDate, DateFormat.MonthDay)} - ${format(toDateAdjusted, DateFormat.MonthDayYear)}`;
  }

  if (toYear === currentYear) {
    return `${format(fromDate, DateFormat.MonthDayYear)} - ${format(toDateAdjusted, DateFormat.MonthDay)}`;
  }

  return `${format(fromDate, DateFormat.MonthDayYear)} - ${format(toDateAdjusted, DateFormat.MonthDayYear)}`;
}
