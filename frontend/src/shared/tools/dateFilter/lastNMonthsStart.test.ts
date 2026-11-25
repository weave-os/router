import { DateScalar } from "../../objects/DateScalar";
import { lastNMonthsStart, lastNMonthsStartScalar } from "./lastNMonthsStart";

describe("lastNMonthsStart", () => {
  beforeEach(() => {
    // Local calendar date so month arithmetic is timezone-stable (UTC midnight
    // strings shift to the previous local day in US timezones).
    vi.useFakeTimers().setSystemTime(new Date(2024, 2, 14, 12));
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  test("returns same calendar day N months ago", () => {
    expect(lastNMonthsStart(1)).toEqual(new Date(2024, 1, 14));
    expect(lastNMonthsStart(2)).toEqual(new Date(2024, 0, 14));
    expect(lastNMonthsStart(3)).toEqual(new Date(2023, 11, 14));
    expect(lastNMonthsStart(6)).toEqual(new Date(2023, 8, 14));
    expect(lastNMonthsStart(12)).toEqual(new Date(2023, 2, 14));
  });

  test("does not collapse at month start", () => {
    // Last 1 month on Aug 1 is Jul 1 → Aug 1 (~full month), not Aug 1 alone
    expect(lastNMonthsStart(1, new Date(2026, 7, 1))).toEqual(new Date(2026, 6, 1));
    expect(lastNMonthsStart(3, new Date(2026, 7, 1))).toEqual(new Date(2026, 4, 1));
  });

  test("clamps end-of-month overflows", () => {
    // Mar 31 minus 1 month → Feb 29 (2024 leap) / Feb 28 (2025)
    expect(lastNMonthsStart(1, new Date(2024, 2, 31))).toEqual(new Date(2024, 1, 29));
    expect(lastNMonthsStart(1, new Date(2025, 2, 31))).toEqual(new Date(2025, 1, 28));
  });

  test("accepts an explicit now", () => {
    expect(lastNMonthsStart(3, new Date(2026, 7, 17))).toEqual(new Date(2026, 4, 17));
    expect(lastNMonthsStart(1, new Date(2026, 7, 18))).toEqual(new Date(2026, 6, 18));
  });

  test("lastNMonthsStartScalar wraps as DateScalar", () => {
    expect(lastNMonthsStartScalar(3)).toBe(DateScalar.fromDate(new Date(2023, 11, 14)));
  });
});
