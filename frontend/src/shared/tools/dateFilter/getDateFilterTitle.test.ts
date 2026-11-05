import { DateScalar } from "../../objects/DateScalar";
import { TimeGranularity } from "../timeGranularity";
import { addGranularity, startOfGranularity } from "../timeGranularity";
import { Filter } from "./dateFilter";
import { getDateFilterTitle } from "./getDateFilterTitle";
import { lastNMonthsStart } from "./lastNMonthsStart";

/**
 * Converts a date string (`YYYY-MM-DD`) to a `DateScalar`.
 */
function date(date: string): DateScalar {
  return date as DateScalar;
}

describe("getDateFilterTitle", () => {
  test("all time", () => {
    // Thursday, March 14, 2024
    vi.useFakeTimers().setSystemTime(new Date("2024-03-14"));

    expect(getDateFilterTitle(Filter.date())).toBe("All time");
  });

  test("from all time, to any date", () => {
    // Thursday, March 14, 2024
    vi.useFakeTimers().setSystemTime(new Date("2024-03-14"));

    expect(getDateFilterTitle(Filter.date(undefined, date("2022-03-14")))).toBe(
      "Until March 13, 2022",
    );
    expect(getDateFilterTitle(Filter.date(undefined, date("2023-03-14")))).toBe(
      "Until March 13, 2023",
    );
    expect(getDateFilterTitle(Filter.date(undefined, date("2024-01-29")))).toBe("Until January 28");
    expect(getDateFilterTitle(Filter.date(undefined, date("2024-03-14")))).toBe("Until March 13");
  });

  test("from breakpoint, up to current time", () => {
    // Thursday, March 14, 2024
    vi.useFakeTimers().setSystemTime(new Date("2024-03-14"));

    expect(getDateFilterTitle(Filter.date(date("2022-01-01"), undefined))).toBe("Last 3 years");
    expect(getDateFilterTitle(Filter.date(date("2023-01-01"), undefined))).toBe("Last 2 years");
    expect(getDateFilterTitle(Filter.date(date("2024-01-01"), undefined))).toBe("Last 3 months");
    expect(getDateFilterTitle(Filter.date(date("2024-02-01"), undefined))).toBe("Last 2 months");
    expect(getDateFilterTitle(Filter.date(date("2024-03-01"), undefined))).toBe("Last 1 month");
    expect(getDateFilterTitle(Filter.date(date("2024-02-26"), undefined))).toBe("Last 3 weeks");
    expect(getDateFilterTitle(Filter.date(date("2024-03-04"), undefined))).toBe("Last 2 weeks");
    expect(getDateFilterTitle(Filter.date(date("2024-03-11"), undefined))).toBe("This week");
  });

  test("recognizes a Sunday-start current week", () => {
    // Thursday, March 14, 2024
    vi.useFakeTimers().setSystemTime(new Date("2024-03-14"));

    expect(
      getDateFilterTitle(Filter.date(date("2024-03-10"), undefined), { weekStartsOn: 0 }),
    ).toBe("This week");
  });

  test("same-date last N months titles", () => {
    // Thursday, March 14, 2024
    vi.useFakeTimers().setSystemTime(new Date("2024-03-14"));

    expect(
      getDateFilterTitle(Filter.date(DateScalar.fromDate(lastNMonthsStart(1)), undefined)),
    ).toBe("Last 1 month");
    expect(
      getDateFilterTitle(Filter.date(DateScalar.fromDate(lastNMonthsStart(2)), undefined)),
    ).toBe("Last 2 months");
    expect(
      getDateFilterTitle(Filter.date(DateScalar.fromDate(lastNMonthsStart(3)), undefined)),
    ).toBe("Last 3 months");
    expect(
      getDateFilterTitle(Filter.date(DateScalar.fromDate(lastNMonthsStart(6)), undefined)),
    ).toBe("Last 6 months");
    expect(
      getDateFilterTitle(Filter.date(DateScalar.fromDate(lastNMonthsStart(12)), undefined)),
    ).toBe("Last 12 months");
  });

  test("same-date last N months titles with end-of-month clamp", () => {
    vi.useFakeTimers().setSystemTime(new Date(2025, 2, 31, 12));

    expect(
      getDateFilterTitle(Filter.date(DateScalar.fromDate(lastNMonthsStart(1)), undefined)),
    ).toBe("Last 1 month");
  });

  test("calendar-month start ranges still title as last N months", () => {
    // Thursday, March 14, 2024
    vi.useFakeTimers().setSystemTime(new Date("2024-03-14"));

    expect(
      getDateFilterTitle(
        Filter.date(
          DateScalar.fromDate(startOfGranularity(new Date(), TimeGranularity.Month)),
          undefined,
        ),
      ),
    ).toBe("Last 1 month");
    expect(
      getDateFilterTitle(
        Filter.date(
          DateScalar.fromDate(
            addGranularity(
              startOfGranularity(new Date(), TimeGranularity.Month),
              TimeGranularity.Month,
              -1,
            ),
          ),
          undefined,
        ),
      ),
    ).toBe("Last 2 months");
    expect(
      getDateFilterTitle(
        Filter.date(
          DateScalar.fromDate(
            addGranularity(
              startOfGranularity(new Date(), TimeGranularity.Month),
              TimeGranularity.Month,
              -2,
            ),
          ),
          undefined,
        ),
      ),
    ).toBe("Last 3 months");
  });

  test("from arbitrary date, up to current time", () => {
    // Thursday, March 14, 2024
    vi.useFakeTimers().setSystemTime(new Date("2024-03-14"));

    expect(getDateFilterTitle(Filter.date(date("2023-01-18"), undefined))).toBe(
      "Since January 18, 2023",
    );
    expect(getDateFilterTitle(Filter.date(date("2023-09-28"), undefined))).toBe(
      "Since September 28, 2023",
    );
    expect(getDateFilterTitle(Filter.date(date("2024-01-18"), undefined))).toBe("Since January 18");
    expect(getDateFilterTitle(Filter.date(date("2024-02-04"), undefined))).toBe("Since February 4");
    expect(getDateFilterTitle(Filter.date(date("2024-03-02"), undefined))).toBe("Since March 2");
    expect(getDateFilterTitle(Filter.date(date("2024-03-10"), undefined))).toBe("Last 4 days");
    expect(getDateFilterTitle(Filter.date(date("2024-03-12"), undefined))).toBe("Last 2 days");
    expect(getDateFilterTitle(Filter.date(date("2024-03-13"), undefined))).toBe("Today");
  });

  test("from breakpoint, to breakpoint", () => {
    // Thursday, March 14, 2024
    vi.useFakeTimers().setSystemTime(new Date("2024-03-14"));

    expect(getDateFilterTitle(Filter.date(date("2023-01-01"), date("2024-01-01")))).toBe("2023");
    expect(getDateFilterTitle(Filter.date(date("2024-01-01"), date("2025-01-01")))).toBe("2024");
    expect(getDateFilterTitle(Filter.date(date("2020-01-01"), date("2024-01-01")))).toBe(
      "2020-2023",
    );
    expect(getDateFilterTitle(Filter.date(date("2023-03-01"), date("2024-02-01")))).toBe(
      "March 2023 - January 2024",
    );
    expect(getDateFilterTitle(Filter.date(date("2023-03-01"), date("2024-03-01")))).toBe(
      "March 2023 - February 2024",
    );
    expect(getDateFilterTitle(Filter.date(date("2023-01-01"), date("2023-03-01")))).toBe(
      "January - February 2023",
    );
    expect(getDateFilterTitle(Filter.date(date("2024-01-01"), date("2024-02-01")))).toBe("January");
    expect(getDateFilterTitle(Filter.date(date("2024-01-01"), date("2024-03-01")))).toBe(
      "January - February",
    );
    expect(getDateFilterTitle(Filter.date(date("2025-01-01"), date("2025-03-01")))).toBe(
      "January - February 2025",
    );

    // Test "Last 2 weeks" as date range (last week + this week)
    expect(getDateFilterTitle(Filter.date(date("2024-03-04"), date("2024-03-18")))).toBe(
      "Last 2 weeks",
    );
  });

  test("from arbitrary date, to arbitrary date", () => {
    // Thursday, March 14, 2024
    vi.useFakeTimers().setSystemTime(new Date("2024-03-14"));

    expect(getDateFilterTitle(Filter.date(date("2024-02-14"), date("2024-03-14")))).toBe(
      "February 14 - March 13",
    );
    expect(getDateFilterTitle(Filter.date(date("2024-01-03"), date("2024-08-09")))).toBe(
      "January 3 - August 8",
    );
    expect(getDateFilterTitle(Filter.date(date("2023-01-03"), date("2024-08-09")))).toBe(
      "January 3, 2023 - August 8",
    );
    expect(getDateFilterTitle(Filter.date(date("2024-01-03"), date("2025-08-09")))).toBe(
      "January 3 - August 8, 2025",
    );
    expect(getDateFilterTitle(Filter.date(date("2023-01-03"), date("2023-08-09")))).toBe(
      "January 3 - August 8, 2023",
    );
    expect(getDateFilterTitle(Filter.date(date("2023-01-03"), date("2025-08-09")))).toBe(
      "January 3, 2023 - August 8, 2025",
    );
  });

  test("broken cases", () => {
    // Thursday, March 14, 2024
    vi.useFakeTimers().setSystemTime(new Date("2024-03-14"));

    expect(getDateFilterTitle(Filter.date(date("2024-03-14"), date("2024-03-14")))).toBe(
      "(invalid)",
    );
    expect(getDateFilterTitle(Filter.date(date("2024-04-01"), date("2024-03-01")))).toBe(
      "(invalid)",
    );
  });
});
