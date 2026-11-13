export const ONE_SECOND_IN_MS = 1000;
export const ONE_MINUTE_IN_MS = 60 * ONE_SECOND_IN_MS;
export const ONE_HOUR_IN_MS = 60 * ONE_MINUTE_IN_MS;
export const ONE_DAY_IN_MS = 24 * ONE_HOUR_IN_MS;
export const ONE_DAY_IN_HOURS = 24;

export const TWO_WEEKS_IN_WEEKS = 2;

/**
 * Must stay in sync with standupLookbackDays in the backend
 * (backend/internal/app/standupnotes/individual_standup.go), which clamps how
 * far back a standup report analyzes. When they disagree, the standup's charts
 * describe a different stretch of work than its bullets do.
 */
export const STANDUP_LOOKBACK_DAYS = 14;

/**
 * Returns true if `value` is a positive epoch-millisecond timestamp.
 *
 * Go's zero `time.Time{}` (year 0001) round-trips to a negative epoch
 * when marshalled to ms; this guard lets callers render "no date" for
 * those values instead of nonsense like "0001-01-01" or huge negative
 * durations.
 */
export function isValidEpochMs(value: number | string | null | undefined): boolean {
  return value != null && Number(value) > 0;
}

/**
 * Date formats for use with `date-fns` `format` function.
 */
export enum DateFormat {
  /**
   * Renders the full date and time in the user's time zone.
   *
   * For example: July 11th, 2024 at 12:32:39 PM
   */
  DateTimeHumanReadable = "PPPpp",

  /**
   * Renders the full name of the day of the week.
   *
   * For example: Monday
   */
  DayOfWeekFull = "EEEE",

  /**
   * Renders just the letter for the day of the week.
   *
   * For example: M
   */
  DayOfWeekLetter = "EEEEE",

  /**
   * Renders the hour and AM/PM.
   *
   * For example: 7am, 12pm
   */
  Hour = "haaa",

  /**
   * Renders the hour and minute.
   *
   * For example: 7:32am, 12:18pm
   */
  HourMinute = "h:mmaaa",

  /**
   * Renders the month and day.
   *
   * For example: July 11
   */
  MonthDay = "MMMM d",

  /** Renders an abbreviated month, day, and hour. For example: Jul 11, 3pm. */
  MonthDayHour = "LLL d, haaa",

  /**
   * Renders the (short) month and day.
   *
   * For example: Jul 11
   */
  MonthDayShort = "LLL d",

  /**
   * Renders the month, day, and year.
   *
   * For example: July 11, 2024
   */
  MonthDayYear = "MMMM d, yyyy",

  MonthDayYearShort = "MMM d, yyyy",

  /**
   * Renders the month, day, and year with slashes.
   *
   * For example: 07/08/24
   */
  MonthDayYearSlash = "MM/dd/yy",

  /**
   * Renders the month, day, and year with slashes and no leading zeros.
   *
   * For example: 7/8/24
   */
  MonthDayYearSlashShort = "M/d/yy",

  /**
   * Renders an abbreviated month, day, year, and time, labelled UTC.
   *
   * Callers are responsible for passing a UTC-based date — this only appends
   * the literal "UTC" suffix. Use for pipeline/snapshot timestamps where the
   * viewer's local timezone would shift the displayed day.
   *
   * For example: Jul 11, 2024 at 3:04 PM UTC
   */
  MonthDayYearTimeUtc = "MMM d, yyyy 'at' h:mm a 'UTC'",

  /**
   * Renders a sortable UTC timestamp, labelled UTC.
   *
   * Callers are responsible for passing a UTC-based date. Intended for
   * tooltips where the exact instant matters.
   *
   * For example: 2024-07-11 15:04:05 UTC
   */
  MonthDayYearTimeUtcExact = "yyyy-MM-dd HH:mm:ss 'UTC'",

  /**
   * Renders just the full month name.
   *
   * For example: January, February
   */
  MonthName = "MMMM",

  /**
   * Renders the abbreviated month name.
   *
   * For example: Jul
   */
  MonthShort = "LLL",

  /**
   * Renders the month and year.
   *
   * For example: Jul '24
   */
  MonthYear = "LLL ‘yy",

  /**
   * Renders the quarter and year.
   *
   * For example: Q1 2024
   */
  QuarterYear = "'Q'Q yyyy",

  /**
   * Renders the abbreviated day of the week, month, and day.
   *
   * For example: Thu, Jul 11
   */
  WeekdayMonthDayShort = "EEE, LLL d",

  /**
   * Renders the year.
   *
   * For example: 2024
   */
  Year = "yyyy",

  /**
   * Renders the year, month, and day.
   *
   * For example: 2024-7-8
   */
  YearMonthDay = "yyyy-M-d",

  /**
   * Renders the year, month, and day with leading zeros.
   *
   * For example: 2024-07-08
   */
  YearMonthDayFull = "yyyy-MM-dd",

  /**
   * Renders the full date with the hour and minute.
   *
   * For example: 2024-07-08 7:32am, 2024-12-25 12:18pm
   */
  YearMonthDayFullHourMinute = "yyyy-MM-dd h:mmaaa",
}

/**
 * Short timezone name (e.g. "EST", or "GMT+5:30" for zones without an
 * abbreviation) for the given instant in the given IANA timezone. date-fns's
 * `zzz` token only renders GMT offsets, so timezone-suffixed timestamps
 * should append this instead.
 */
export function getTimezoneAbbreviation(time: Date | number, timeZone: string): string {
  const parts = new Intl.DateTimeFormat("en-US", {
    timeZone,
    timeZoneName: "short",
  }).formatToParts(new Date(+time));
  return parts.find(part => part.type === "timeZoneName")?.value ?? "";
}
