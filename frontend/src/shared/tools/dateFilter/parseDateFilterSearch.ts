import type { WeekStartsOn } from "../timeGranularity";

import * as chrono from "chrono-node";

import { DateScalar } from "../../objects/DateScalar";
import { addGranularity, startOfGranularity, TimeGranularity } from "../timeGranularity";
import { DateFilter, Filter } from "./dateFilter";
import { lastNMonthsStart } from "./lastNMonthsStart";

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

/** Accepts digits ("3") or number words ("three"); null when neither. */
function parseNumberWord(str: string): number | null {
  const asDigit = parseInt(str, 10);
  if (!isNaN(asDigit)) return asDigit;
  return WORD_TO_NUMBER[str.toLowerCase()] ?? null;
}

const toParts = (date: Date) => ({
  day: date.getDate(),
  month: date.getMonth() + 1,
  year: date.getFullYear(),
});

/** Weeks counted back from the start of the current week; 0 is this week, -1 last week. */
function weekRange(weeksBack: number, weekCount: number, weekStartsOn: WeekStartsOn) {
  const thisWeek = startOfGranularity(new Date(), TimeGranularity.Week, weekStartsOn);
  const start = addGranularity(thisWeek, TimeGranularity.Week, -weeksBack);
  return { end: addGranularity(start, TimeGranularity.Week, weekCount), start };
}

function createDateFilterParser(weekStartsOn: WeekStartsOn) {
  const dateFilterParser = chrono.casual.clone();

  dateFilterParser.parsers.unshift({
    extract: (context, match) => {
      if (match.index == null) return null;
      const isThisWeek = match[1].toLowerCase() === "this";
      const weekCount = isThisWeek || match[2] == null ? 1 : parseNumberWord(match[2]);
      if (weekCount == null) return null;
      const { end, start } = weekRange(isThisWeek ? 0 : weekCount, weekCount, weekStartsOn);
      return context.createParsingResult(match.index, match[0], toParts(start), toParts(end));
    },
    pattern: () => /^(this|last) (?:([1-9]\d*|[a-z]+) )?weeks?$/i,
  });

  // Open-ended and inclusive of today, so "last 3 days" starts 2 days ago — chrono's default
  // would start 3 days ago (today minus N), which titles as "Last 4 days".
  dateFilterParser.parsers.unshift({
    extract: (context, match) => {
      if (match.index == null) return null;
      const dayCount = parseNumberWord(match[1]);
      if (dayCount == null) return null;
      const start = addGranularity(new Date(), TimeGranularity.Day, -(dayCount - 1));
      return context.createParsingResult(match.index, match[0], toParts(start));
    },
    pattern: () => /^last ([1-9]\d*|[a-z]+) days?$/i,
  });

  // Open-ended from the same calendar day N months ago — same as menu presets.
  dateFilterParser.parsers.unshift({
    extract: (context, match) => {
      if (match.index == null) return null;
      const monthCount = parseNumberWord(match[1]);
      if (monthCount == null) return null;
      return context.createParsingResult(
        match.index,
        match[0],
        toParts(lastNMonthsStart(monthCount)),
      );
    },
    pattern: () => /^last ([1-9]\d*|[a-z]+) months?$/i,
  });

  dateFilterParser.parsers.push({
    extract: (context, match) => {
      if (match.index == null) return null;
      const year = parseInt(match[0], 10);
      if (year < 1000 || year > 9999) return null;
      // The "Year " prefix keeps chrono's UnlikelyFormatFilter from dropping a bare number.
      return context.createParsingResult(match.index, "Year " + match[0], { day: 1, year });
    },
    pattern: () => /^\d{4}$/,
  });

  // A bare month or year becomes the whole month or year.
  dateFilterParser.refiners.push({
    refine: (context, results) => {
      for (const result of results) {
        const certain = result.start.getCertainComponents();
        const month = result.start.get("month");
        const year = result.start.get("year");
        const monthOnly = certain.every(c => c === "month" || c === "year");
        const yearOnly = certain.every(c => c === "year" || c === "day");

        if (yearOnly && year != null) {
          result.start.assign("month", 1);
          result.start.assign("day", 1);
        }

        if (yearOnly && year != null && result.end == null) {
          context.debug(() => console.log(`Overriding end date to next year for "${result.text}"`));
          result.end = result.start.clone();
          result.end.assign("year", year + 1);
        }

        if (monthOnly && month != null) {
          result.start.assign("day", 1);
        }

        if (monthOnly && month != null && result.end == null) {
          context.debug(() =>
            console.log(`Overriding end date to next month for "${result.text}"`),
          );
          result.end = result.start.clone();
          result.end.assign("month", month === 12 ? 1 : month + 1);
          if (month === 12 && year != null) {
            result.end.assign("year", year + 1);
          }
        }
      }
      return results;
    },
  });

  // Filter ends are exclusive, so an explicit "X - Y" day range ends the day after Y.
  dateFilterParser.refiners.push({
    refine: (context, results) => {
      for (const result of results) {
        const isExplicitDayRange =
          result.end != null &&
          result.end.getCertainComponents().includes("day") &&
          result.start.getCertainComponents().includes("day") &&
          /\s*(-|to)\s*/.test(result.text);
        if (result.end == null || !isExplicitDayRange) continue;

        const endParts = toParts(addGranularity(result.end.date(), TimeGranularity.Day, 1));
        result.end.assign("day", endParts.day);
        result.end.assign("month", endParts.month);
        result.end.assign("year", endParts.year);
        context.debug(() =>
          console.log(`Making end date inclusive by adding one day for "${result.text}"`),
        );
      }
      return results;
    },
  });

  return dateFilterParser;
}

const parsersByWeekStart = new Map<WeekStartsOn, chrono.Chrono>();

/** Unparseable input yields an empty list; week phrases use the host's week start. */
export function parseDateFilterSearch(
  search: string,
  weekStartsOn: WeekStartsOn = 1,
): readonly DateFilter[] {
  let parser = parsersByWeekStart.get(weekStartsOn);
  if (parser == null) {
    parser = createDateFilterParser(weekStartsOn);
    parsersByWeekStart.set(weekStartsOn, parser);
  }

  return parser
    .parse(search)
    .map(result =>
      Filter.date(
        DateScalar.fromDate(result.start.date()),
        result.end != null ? DateScalar.fromDate(result.end.date()) : undefined,
      ),
    );
}
