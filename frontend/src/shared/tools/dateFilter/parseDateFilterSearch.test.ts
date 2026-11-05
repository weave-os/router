import { DateScalar } from "../../objects/DateScalar";
import { Filter } from "./dateFilter";
import { parseDateFilterSearch } from "./parseDateFilterSearch";

vi.useFakeTimers().setSystemTime(new Date(2024, 2, 14, 12));

/**
 * Converts a date string (`YYYY-MM-DD`) to a `DateScalar`.
 */
function date(date: string): DateScalar {
  return date as DateScalar;
}

describe("parseDateFilterSearch", () => {
  test("fails if the search string is not a valid date filter", () => {
    expect(parseDateFilterSearch("invalid")).toEqual([]);
  });

  // Not implemented yet, but we should do this at some point. See W-113
  test.skip("parses a partial match", () => {
    expect(parseDateFilterSearch("last we")).toEqual([Filter.date(date("2024-03-11"))]);
    expect(parseDateFilterSearch("this we")).toEqual([Filter.date(date("2024-03-11"))]);
    expect(parseDateFilterSearch("last mo")).toEqual([
      Filter.date(date("2024-02-01"), date("2024-03-01")),
    ]);
    expect(parseDateFilterSearch("this mo")).toEqual([
      Filter.date(date("2024-03-01"), date("2024-04-01")),
    ]);
  });

  test("parses a week", () => {
    expect(parseDateFilterSearch("last week")).toEqual([
      Filter.date(date("2024-03-04"), date("2024-03-11")),
    ]);

    expect(parseDateFilterSearch("this week")).toEqual([
      Filter.date(date("2024-03-11"), date("2024-03-18")),
    ]);
  });

  test("parses a month", () => {
    expect(parseDateFilterSearch("march")).toEqual([
      Filter.date(date("2024-03-01"), date("2024-04-01")),
    ]);

    expect(parseDateFilterSearch("this month")).toEqual([
      Filter.date(date("2024-03-01"), date("2024-04-01")),
    ]);

    expect(parseDateFilterSearch("march 2023")).toEqual([
      Filter.date(date("2023-03-01"), date("2023-04-01")),
    ]);

    expect(parseDateFilterSearch("last month")).toEqual([
      Filter.date(date("2024-02-01"), date("2024-03-01")),
    ]);

    expect(parseDateFilterSearch("3 months ago")).toEqual([
      Filter.date(date("2023-12-01"), date("2024-01-01")),
    ]);
  });

  test("parses last x days inclusive of today", () => {
    expect(parseDateFilterSearch("last 3 days")).toEqual([Filter.date(date("2024-03-12"))]);

    expect(parseDateFilterSearch("last 7 days")).toEqual([Filter.date(date("2024-03-08"))]);

    // Singular form
    expect(parseDateFilterSearch("last 1 day")).toEqual([Filter.date(date("2024-03-14"))]);

    expect(parseDateFilterSearch("Last Three Days")).toEqual([Filter.date(date("2024-03-12"))]);

    expect(parseDateFilterSearch("last bazillion days")).toEqual([]);
  });

  test("parses last x weeks", () => {
    expect(parseDateFilterSearch("last 2 weeks")).toEqual([
      Filter.date(date("2024-02-26"), date("2024-03-11")),
    ]);

    expect(parseDateFilterSearch("last 3 weeks")).toEqual([
      Filter.date(date("2024-02-19"), date("2024-03-11")),
    ]);

    // Singular form
    expect(parseDateFilterSearch("last 1 week")).toEqual([
      Filter.date(date("2024-03-04"), date("2024-03-11")),
    ]);
  });

  test("parses last x weeks with word numbers", () => {
    expect(parseDateFilterSearch("last one week")).toEqual([
      Filter.date(date("2024-03-04"), date("2024-03-11")),
    ]);

    expect(parseDateFilterSearch("last two weeks")).toEqual([
      Filter.date(date("2024-02-26"), date("2024-03-11")),
    ]);

    expect(parseDateFilterSearch("last three weeks")).toEqual([
      Filter.date(date("2024-02-19"), date("2024-03-11")),
    ]);

    expect(parseDateFilterSearch("last four weeks")).toEqual([
      Filter.date(date("2024-02-12"), date("2024-03-11")),
    ]);

    // Case insensitive
    expect(parseDateFilterSearch("Last Three Weeks")).toEqual([
      Filter.date(date("2024-02-19"), date("2024-03-11")),
    ]);

    // Unrecognized words should not match
    expect(parseDateFilterSearch("last bazillion weeks")).toEqual([]);
  });

  test("parses last x months", () => {
    // Open-ended from the same calendar day N months ago
    expect(parseDateFilterSearch("last 2 months")).toEqual([Filter.date(date("2024-01-14"))]);

    expect(parseDateFilterSearch("last 3 months")).toEqual([Filter.date(date("2023-12-14"))]);

    expect(parseDateFilterSearch("last 6 months")).toEqual([Filter.date(date("2023-09-14"))]);

    expect(parseDateFilterSearch("last 12 months")).toEqual([Filter.date(date("2023-03-14"))]);

    // Test singular form
    expect(parseDateFilterSearch("last 1 month")).toEqual([Filter.date(date("2024-02-14"))]);

    // Test larger numbers
    expect(parseDateFilterSearch("last 24 months")).toEqual([Filter.date(date("2022-03-14"))]);
  });

  test("parses last x months with word numbers", () => {
    expect(parseDateFilterSearch("last one month")).toEqual([Filter.date(date("2024-02-14"))]);

    expect(parseDateFilterSearch("last two months")).toEqual([Filter.date(date("2024-01-14"))]);

    expect(parseDateFilterSearch("last three months")).toEqual([Filter.date(date("2023-12-14"))]);

    expect(parseDateFilterSearch("last six months")).toEqual([Filter.date(date("2023-09-14"))]);

    expect(parseDateFilterSearch("last twelve months")).toEqual([Filter.date(date("2023-03-14"))]);

    // Case insensitive
    expect(parseDateFilterSearch("Last Three Months")).toEqual([Filter.date(date("2023-12-14"))]);

    // Unrecognized words should not match
    expect(parseDateFilterSearch("last bazillion months")).toEqual([]);
  });

  test("parses a month range", () => {
    expect(parseDateFilterSearch("october 2024 - december 2024")).toEqual([
      Filter.date(date("2024-10-01"), date("2024-12-01")),
    ]);

    expect(parseDateFilterSearch("october 2024 to december 2024")).toEqual([
      Filter.date(date("2024-10-01"), date("2024-12-01")),
    ]);
  });

  test("parses specific date ranges inclusively", () => {
    expect(parseDateFilterSearch("september 1 - september 17")).toEqual([
      Filter.date(date("2024-09-01"), date("2024-09-18")),
    ]);

    expect(parseDateFilterSearch("sep 1 - sep 17")).toEqual([
      Filter.date(date("2024-09-01"), date("2024-09-18")),
    ]);

    expect(parseDateFilterSearch("january 5 to january 10")).toEqual([
      Filter.date(date("2024-01-05"), date("2024-01-11")),
    ]);

    expect(parseDateFilterSearch("january 28 - february 5")).toEqual([
      Filter.date(date("2024-01-28"), date("2024-02-06")),
    ]);
  });

  test("parses a year", () => {
    expect(parseDateFilterSearch("2024")).toEqual([
      Filter.date(date("2024-01-01"), date("2025-01-01")),
    ]);

    expect(parseDateFilterSearch("this year")).toEqual([
      Filter.date(date("2024-01-01"), date("2025-01-01")),
    ]);
  });
});
