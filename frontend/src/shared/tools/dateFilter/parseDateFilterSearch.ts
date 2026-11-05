import * as chrono from "chrono-node";

import { DateScalar } from "../../objects/DateScalar";
import { TimeGranularity } from "../timeGranularity";
import { addGranularity, startOfGranularity } from "../timeGranularity";
import { DateFilter, Filter } from "./dateFilter";
import { lastNMonthsStart } from "./lastNMonthsStart";

const custom = chrono.casual.clone();

const WORD_TO_NUMBER: Record<string, number> = {
  eight: 8,
  eighteen: 18,
  eleven: 11,
  fifteen: 15,
  five: 5,
  four: 4,
  fourteen: 14,
  nine: 9,
  nineteen: 19,
  one: 1,
  seven: 7,
  seventeen: 17,
  six: 6,
  sixteen: 16,
  ten: 10,
  thirteen: 13,
  three: 3,
  twelve: 12,
  twenty: 20,
  two: 2,
};

/**
 * Parses a number from either a digit string ("3") or a word string ("three").
 * Returns the number, or null if the string is not a recognized number.
 */
function parseNumberWord(str: string): number | null {
  const asDigit = parseInt(str, 10);
  if (!isNaN(asDigit)) return asDigit;
  return WORD_TO_NUMBER[str.toLowerCase()] ?? null;
}

// Handle "last week"
custom.parsers.unshift({
  extract: (context, match) => {
    if (match.index == null) return null;

    const now = new Date();
    const end = startOfGranularity(now, TimeGranularity.Week);
    const start = addGranularity(end, TimeGranularity.Week, -1);

    return context.createParsingResult(
      match.index,
      match[0],
      { day: start.getDate(), month: start.getMonth() + 1, year: start.getFullYear() },
      { day: end.getDate(), month: end.getMonth() + 1, year: end.getFullYear() },
    );
  },
  pattern: () => /^last week$/i,
});

// Handle "this week"
custom.parsers.unshift({
  extract: (context, match) => {
    if (match.index == null) return null;

    const now = new Date();
    const start = startOfGranularity(now, TimeGranularity.Week);
    const end = addGranularity(start, TimeGranularity.Week, 1);

    return context.createParsingResult(
      match.index,
      match[0],
      { day: start.getDate(), month: start.getMonth() + 1, year: start.getFullYear() },
      { day: end.getDate(), month: end.getMonth() + 1, year: end.getFullYear() },
    );
  },
  pattern: () => /^this week$/i,
});

// Handle "last x days" (supports both digits like "3" and words like "three").
// Open-ended and inclusive of today, so "last 3 days" starts 2 days ago — chrono's default
// would start 3 days ago (today minus N), which titles as "Last 4 days".
custom.parsers.unshift({
  extract: (context, match) => {
    if (match.index == null) return null;

    const dayCount = parseNumberWord(match[1]);
    if (dayCount == null) return null;

    const start = addGranularity(new Date(), TimeGranularity.Day, -(dayCount - 1));

    return context.createParsingResult(match.index, match[0], {
      day: start.getDate(),
      month: start.getMonth() + 1,
      year: start.getFullYear(),
    });
  },
  pattern: () => /^last ([1-9]\d*|[a-z]+) days?$/i,
});

// Handle "last x weeks" (supports both digits like "3" and words like "three")
custom.parsers.unshift({
  extract: (context, match) => {
    if (match.index == null) return null;

    const weekMatch = match[0].match(/^last ([1-9]\d*|[a-z]+) weeks?$/i);
    if (weekMatch == null) return null;

    const weekCount = parseNumberWord(weekMatch[1]);
    if (weekCount == null || weekCount <= 0) return null;

    const now = new Date();
    const end = startOfGranularity(now, TimeGranularity.Week);
    const start = addGranularity(end, TimeGranularity.Week, -weekCount);

    return context.createParsingResult(
      match.index,
      match[0],
      { day: start.getDate(), month: start.getMonth() + 1, year: start.getFullYear() },
      { day: end.getDate(), month: end.getMonth() + 1, year: end.getFullYear() },
    );
  },
  pattern: () => /^last (?:[1-9]\d*|[a-z]+) weeks?$/i,
});

// Handle "last x months" (supports both digits like "3" and words like "three").
// Open-ended from the same calendar day N months ago — same as menu presets.
custom.parsers.unshift({
  extract: (context, match) => {
    if (match.index == null) return null;

    const monthMatch = match[0].match(/^last ([1-9]\d*|[a-z]+) months?$/i);
    if (monthMatch == null) return null;

    const monthCount = parseNumberWord(monthMatch[1]);
    if (monthCount == null || monthCount <= 0) return null;

    const start = lastNMonthsStart(monthCount);

    return context.createParsingResult(match.index, match[0], {
      day: start.getDate(),
      month: start.getMonth() + 1,
      year: start.getFullYear(),
    });
  },
  pattern: () => /^last (?:[1-9]\d*|[a-z]+) months?$/i,
});

// Parses a year string into a date
custom.parsers.push({
  extract: (context, match) => {
    if (match.index == null) return null;
    const year = parseInt(match[0], 10);
    if (isNaN(year) || year < 1000 || year > 9999) return null;

    // Note: we need to include the "Year " prefix so the "UnlikelyFormatFilter" doesn't remove it.
    return context.createParsingResult(match.index, "Year " + match[0], { day: 1, year });
  },
  pattern: () => /^\d{4}$/,
});

// Converts specific months/years into a date range
custom.refiners.push({
  refine: (context, results) => {
    for (const result of results) {
      const certain = result.start.getCertainComponents();

      const month = result.start.get("month");
      const year = result.start.get("year");

      const monthOnly = certain.every(c => c === "month" || c === "year");
      const yearOnly = certain.every(c => c === "year" || c === "day");

      if (yearOnly && year != null) {
        // If the user specifies just a year, suggest that year as a range
        result.start.assign("month", 1);
        result.start.assign("day", 1);
      }

      if (yearOnly && year != null && result.end == null) {
        context.debug(() => console.log(`Overriding end date to next year for "${result.text}"`));
        result.end = result.start.clone();
        result.end.assign("year", year + 1);
      }

      if (monthOnly && month != null) {
        // If the user specifies just a month, suggest that month as a range
        result.start.assign("day", 1);
      }

      if (monthOnly && month != null && result.end == null) {
        context.debug(() => console.log(`Overriding end date to next month for "${result.text}"`));
        result.end = result.start.clone();
        result.end.assign("month", month + 1);

        if (month === 12 && result.start.get("year") != null) {
          result.end.assign("year", result.start.get("year")! + 1);
        }
      }
    }

    return results;
  },
});

// Make end dates inclusive by adding one day when both start and end dates are specified with specific days
custom.refiners.push({
  refine: (context, results) => {
    for (const result of results) {
      if (result.end != null) {
        const endCertain = result.end.getCertainComponents();
        const startCertain = result.start.getCertainComponents();

        const endHasDay = endCertain.includes("day");
        const startHasDay = startCertain.includes("day");

        const isExplicitRange = /\s*(-|to)\s*/.test(result.text);

        if (endHasDay && startHasDay && isExplicitRange) {
          const endDate = result.end.date();
          endDate.setDate(endDate.getDate() + 1);
          result.end.assign("day", endDate.getDate());
          result.end.assign("month", endDate.getMonth() + 1);
          result.end.assign("year", endDate.getFullYear());

          context.debug(() =>
            console.log(`Making end date inclusive by adding one day for "${result.text}"`),
          );
        }
      }
    }

    return results;
  },
});

/**
 * Attempts to parse a search string into a date filter.
 */
export function parseDateFilterSearch(search: string): readonly DateFilter[] {
  const parsed = custom.parse(search);
  // const parsed = custom.parse(search, undefined, { debug: f => f() });

  return parsed.map(result =>
    Filter.date(
      DateScalar.fromDate(result.start.date()),
      result.end != null ? DateScalar.fromDate(result.end.date()) : undefined,
    ),
  );
}
