import { tz, TZDate } from "@date-fns/tz";
import {
  addDays,
  addMonths,
  addQuarters,
  addWeeks,
  addYears,
  differenceInDays,
  differenceInMonths,
  differenceInQuarters,
  differenceInWeeks,
  differenceInYears,
  format,
  startOfDay,
  startOfMonth,
  startOfQuarter,
  startOfWeek,
  startOfYear,
} from "date-fns";

import { DateFormat } from "./time";
import { unreachable } from "./types";

/** Default timezone for the formatting and bucketing helpers below. */
const UTC_TIMEZONE = "UTC";

/** Dashboard bucket sizes; values match the hosted GraphQL enum so filters round-trip. */
export enum TimeGranularity {
  Day = "day",
  Month = "month",
  Quarter = "quarter",
  TwoWeek = "two_week",
  Week = "week",
  Year = "year",
}

export type WeekStartsOn = 0 | 1;

export const GRANULARITY_NAME: Readonly<Record<TimeGranularity, string>> = {
  [TimeGranularity.Day]: "Day",
  [TimeGranularity.Month]: "Month",
  [TimeGranularity.Quarter]: "Quarter",
  [TimeGranularity.TwoWeek]: "Biweekly",
  [TimeGranularity.Week]: "Week",
  [TimeGranularity.Year]: "Year",
};

/** Time granularities in order of decreasing frequency. */
export const GRANULARITY_ORDER: readonly TimeGranularity[] = [
  TimeGranularity.Day,
  TimeGranularity.Week,
  TimeGranularity.TwoWeek,
  TimeGranularity.Month,
  TimeGranularity.Quarter,
  TimeGranularity.Year,
];

export const AI_ROI_ALLOWED_GRANULARITIES: readonly TimeGranularity[] = [
  TimeGranularity.Week,
  TimeGranularity.Month,
  TimeGranularity.Quarter,
  TimeGranularity.Year,
];

export const COMPUTE_SPEND_ALLOWED_GRANULARITIES: readonly TimeGranularity[] = [
  TimeGranularity.Month,
  TimeGranularity.Quarter,
  TimeGranularity.Year,
];

/** Time granularities supported for code review quality (month and longer). */
export const CODE_REVIEW_QUALITY_GRANULARITIES: readonly TimeGranularity[] = [
  TimeGranularity.Month,
  TimeGranularity.Quarter,
  TimeGranularity.Year,
];

/**
 * Time granularities supported for R&D capitalization reports. Quarter is first so it stays
 * the default for tax reporting; month is opt-in.
 */
export const RD_CAPITALIZATION_GRANULARITIES: readonly TimeGranularity[] = [
  TimeGranularity.Quarter,
  TimeGranularity.Month,
  TimeGranularity.Year,
];

/**
 * The name of the given granularity, referring to how often it happens. For example, "Weekly".
 */
export const GRANULARITY_TEMPORAL_NAME: Readonly<Record<TimeGranularity, string>> = {
  [TimeGranularity.Day]: "Daily",
  [TimeGranularity.Month]: "Monthly",
  [TimeGranularity.Quarter]: "Quarterly",
  [TimeGranularity.TwoWeek]: "Biweekly",
  [TimeGranularity.Week]: "Weekly",
  [TimeGranularity.Year]: "Yearly",
};

/**
 * Number of days in each time granularity period. Used for converting
 * daily rates to other granularities (matches backend conversion).
 */
export const GRANULARITY_DAYS: Readonly<Record<TimeGranularity, number>> = {
  [TimeGranularity.Day]: 1,
  [TimeGranularity.Month]: 30,
  [TimeGranularity.Quarter]: 90,
  [TimeGranularity.TwoWeek]: 14,
  [TimeGranularity.Week]: 7,
  [TimeGranularity.Year]: 365,
};

/**
 * Converts a daily rate to the specified granularity.
 * @param dailyValue - The value per day
 * @param granularity - The target granularity
 * @returns The value scaled for the target granularity, without rounding.
 */
export function convertFromDailyRate(dailyValue: number, granularity: TimeGranularity): number {
  return dailyValue * GRANULARITY_DAYS[granularity];
}

/**
 * Converts a value from the specified granularity to a daily rate.
 * @param value - The value in the source granularity
 * @param granularity - The source granularity
 * @returns The value scaled to a daily rate
 */
export function convertToDailyRate(value: number, granularity: TimeGranularity): number {
  return value / GRANULARITY_DAYS[granularity];
}

/**
 * Works like `date-fns` `addXXX` functions, for the given granularity.
 */
export function addGranularity(
  time: Date | number,
  granularity: TimeGranularity,
  amount: number,
): Date {
  switch (granularity) {
    case TimeGranularity.Day:
      return addDays(time, amount);

    case TimeGranularity.Week:
      return addWeeks(time, amount);

    case TimeGranularity.TwoWeek:
      return addWeeks(time, amount * 2);

    case TimeGranularity.Month:
      return addMonths(time, amount);

    case TimeGranularity.Quarter:
      return addQuarters(time, amount);

    case TimeGranularity.Year:
      return addYears(time, amount);

    default:
      unreachable(granularity);
  }
}

/**
 * Formats the given time. Defaults to UTC; pass `timeZone` (an IANA id) to
 * format in another zone — used by custom-query charts to render buckets in
 * the organization's timezone, matching the backend's tz-aware metric buckets.
 */
export function formatTime(
  time: Date | number,
  granularity: TimeGranularity,
  timeZone: string = UTC_TIMEZONE,
): string {
  switch (granularity) {
    case TimeGranularity.Day:
      return format(time, DateFormat.YearMonthDay, { in: tz(timeZone) });

    case TimeGranularity.Week:
    case TimeGranularity.TwoWeek:
      return format(time, DateFormat.YearMonthDay, { in: tz(timeZone) });

    case TimeGranularity.Month:
      return format(time, DateFormat.MonthYear, { in: tz(timeZone) });

    case TimeGranularity.Quarter:
      return format(time, DateFormat.QuarterYear, { in: tz(timeZone) });

    case TimeGranularity.Year:
      return format(time, DateFormat.Year, { in: tz(timeZone) });

    default:
      unreachable(granularity);
  }
}

/**
 * Formats the given time, assuming it's representing the start of a period of the given
 * granularity. Defaults to UTC; pass `timeZone` (an IANA id) to format in another zone.
 */
export function formatInterval(
  time: Date | number,
  granularity: TimeGranularity,
  timeZone: string = UTC_TIMEZONE,
): string {
  switch (granularity) {
    case TimeGranularity.Day: {
      return format(time, DateFormat.YearMonthDay, { in: tz(timeZone) });
    }

    case TimeGranularity.Week:
    case TimeGranularity.TwoWeek: {
      // Last instant of the period, computed in `timeZone` so the end date
      // matches the tz-local start (browser-local addWeeks/addDays would drift
      // when the viewer's timezone differs from the target zone).
      const end = addGranularityInTimezone(time, granularity, 1, timeZone).getTime() - 1;
      return `${format(time, DateFormat.YearMonthDay, { in: tz(timeZone) })} to ${format(end, DateFormat.YearMonthDay, { in: tz(timeZone) })}`;
    }

    case TimeGranularity.Month: {
      return format(time, DateFormat.MonthYear, { in: tz(timeZone) });
    }

    case TimeGranularity.Quarter: {
      return format(time, DateFormat.QuarterYear, { in: tz(timeZone) });
    }

    case TimeGranularity.Year: {
      return format(time, DateFormat.Year, { in: tz(timeZone) });
    }

    default:
      unreachable(granularity);
  }
}

/**
 * Returns the number of units between the given start and end dates, for the given granularity.
 */
export function differenceInGranularity(
  end: Date,
  start: Date,
  granularity: TimeGranularity,
): number {
  switch (granularity) {
    case TimeGranularity.Day:
      return differenceInDays(end, start);

    case TimeGranularity.Week:
      return differenceInWeeks(end, start);

    case TimeGranularity.TwoWeek:
      return Math.floor(differenceInWeeks(end, start) / 2);

    case TimeGranularity.Month:
      return differenceInMonths(end, start);

    case TimeGranularity.Quarter:
      return differenceInQuarters(end, start);

    case TimeGranularity.Year:
      return differenceInYears(end, start);

    default:
      unreachable(granularity);
  }
}

/**
 * Returns the number of units between the given start and end dates, for the given granularity,
 * including any partial units on the start/end as a full unit.
 */
export function expandedDifferenceInGranularity(
  end: Date,
  start: Date,
  granularity: TimeGranularity,
  weekStartsOn: WeekStartsOn = 1,
): number {
  // We need to make sure partial granularities are counted as full granularities, so we need to
  // expand to the edges of the granularity.
  const startDate = startOfGranularity(start, granularity, weekStartsOn);

  let newEnd = startOfGranularity(end, granularity, weekStartsOn);
  if (newEnd < end) {
    newEnd = addGranularity(newEnd, granularity, 1);
  }

  return differenceInGranularity(newEnd, startDate, granularity);
}

/**
 * Anchors for biweekly bucketing so frontend and backend buckets line up.
 * Sunday-start origin: Jan 4, 1970. Monday-start (ISO) origin: Jan 5, 1970.
 */
const TWO_WEEK_ANCHOR_SUNDAY = new Date(1970, 0, 4);
const TWO_WEEK_ANCHOR_MONDAY = new Date(1970, 0, 5);

/**
 * Works like `date-fns` `startOfXXX` functions, for the given granularity.
 * @param weekStartsOn - First day of the week (0 = Sunday, 1 = Monday). Defaults to Monday.
 */
export function startOfGranularity(
  time: Date | number,
  granularity: TimeGranularity,
  weekStartsOn: WeekStartsOn = 1,
): Date {
  switch (granularity) {
    case TimeGranularity.Day:
      return startOfDay(time);

    case TimeGranularity.Week:
      return startOfWeek(time, { weekStartsOn });

    case TimeGranularity.TwoWeek: {
      const weekStart = startOfWeek(time, { weekStartsOn });
      // Snap to every-other week-start from the anchor. When the week count from
      // the anchor is odd, step back one week to start a new biweekly bucket.
      // Build the anchor in the same timezone context as `weekStart` — comparing a
      // zone-local week start 00:00 against the browser-local anchor would leave a
      // fractional-week offset that truncates the count and flips bucket parity.
      const originDay = weekStartsOn === 0 ? 4 : 5;
      const fallbackAnchor = weekStartsOn === 0 ? TWO_WEEK_ANCHOR_SUNDAY : TWO_WEEK_ANCHOR_MONDAY;
      const anchor =
        weekStart instanceof TZDate && weekStart.timeZone != null
          ? new TZDate(1970, 0, originDay, weekStart.timeZone)
          : fallbackAnchor;
      const weeksSinceAnchor = differenceInWeeks(weekStart, anchor);
      return weeksSinceAnchor % 2 === 0 ? weekStart : addWeeks(weekStart, -1);
    }

    case TimeGranularity.Month:
      return startOfMonth(time);

    case TimeGranularity.Quarter:
      return startOfQuarter(time);

    case TimeGranularity.Year:
      return startOfYear(time);

    default:
      unreachable(granularity);
  }
}

/**
 * Works like `addGranularity`, but performs the arithmetic in the given IANA
 * timezone so that tz-anchored dates (e.g. from `startOfGranularityInTimezone`)
 * are shifted correctly regardless of the browser's local timezone.
 */
export function addGranularityInTimezone(
  time: Date | number,
  granularity: TimeGranularity,
  amount: number,
  timeZone: string,
): Date {
  const zoned = new TZDate(+time, timeZone);
  const result = addGranularity(zoned, granularity, amount);
  return new Date(result.getTime());
}

/**
 * Works like `addGranularity`, but performs the arithmetic in UTC so that
 * UTC-anchored dates (e.g. from `startOfGranularityUTC`) are shifted correctly
 * regardless of the browser's local timezone.
 */
export function addGranularityUTC(
  time: Date | number,
  granularity: TimeGranularity,
  amount: number,
): Date {
  return addGranularityInTimezone(time, granularity, amount, UTC_TIMEZONE);
}

/**
 * Returns the start of the given granularity, computed in the given IANA
 * timezone. The result is the UTC instant of that tz-local period start, so
 * callers must format it back in the same `timeZone` to display the local date.
 * Used by custom-query charts to bucket by the organization's timezone, matching
 * the backend's tz-aware metric bucketing.
 */
export function startOfGranularityInTimezone(
  time: Date | number,
  granularity: TimeGranularity,
  timeZone: string,
  weekStartsOn: WeekStartsOn = 1,
): Date {
  const zoned = new TZDate(+time, timeZone);
  const start = startOfGranularity(zoned, granularity, weekStartsOn);
  return new Date(start.getTime());
}

/**
 * Returns the start of the given granularity, in UTC.
 */
export function startOfGranularityUTC(
  time: Date | number,
  granularity: TimeGranularity,
  weekStartsOn: WeekStartsOn = 1,
): Date {
  return startOfGranularityInTimezone(time, granularity, UTC_TIMEZONE, weekStartsOn);
}

/**
 * Returns an appropriate granularity for code review quality metrics.
 * Falls back to Month if the provided granularity is not supported (Day or Week).
 */
export function getCodeReviewQualityGranularity(granularity: TimeGranularity): TimeGranularity {
  return CODE_REVIEW_QUALITY_GRANULARITIES.includes(granularity)
    ? granularity
    : TimeGranularity.Month;
}

/**
 * Checks if the period containing the given end date is complete (fully in the past).
 * A period is complete when the current date is past the end of that period.
 *
 * @param periodEndDate - The exclusive end date of the period to check (e.g., April 1 for Q1)
 * @param granularity - The time granularity of the period
 * @returns true if the period is complete, false if it's ongoing or in the future
 */
export function isPeriodComplete(periodEndDate: Date, granularity: TimeGranularity): boolean {
  const now = new Date();
  const currentPeriodStart = startOfGranularity(now, granularity);

  // periodEndDate is the exclusive end date (e.g., April 1 for Q1)
  // Subtract 1 day to get a date within the actual period (e.g., March 31 for Q1)
  const dayBeforeEnd = addDays(periodEndDate, -1);
  const selectedPeriodStart = startOfGranularity(dayBeforeEnd, granularity);

  // The period is complete if the current period starts after the selected period
  return currentPeriodStart > selectedPeriodStart;
}
