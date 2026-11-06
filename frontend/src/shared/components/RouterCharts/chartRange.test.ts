import { addDays, startOfDay } from "date-fns";

import { describe, expect, it } from "vitest";

import { rateDivisor } from "./chartRange";

describe("rateDivisor", () => {
  it("counts every day in a past range, empty days included", () => {
    const range = {
      from: new Date(2025, 9, 23).toISOString(),
      to: new Date(2025, 10, 6).toISOString(),
    };
    expect(rateDivisor(range, "day")).toBe(14);
  });

  it("counts weeks as fractional days / 7, like hosted", () => {
    const range = {
      from: new Date(2025, 0, 1).toISOString(),
      to: new Date(2025, 0, 11).toISOString(),
    };
    expect(rateDivisor(range, "week")).toBeCloseTo(10 / 7);
  });

  it("ends a range that reaches today at the end of today", () => {
    const today = startOfDay(new Date());
    const range = {
      from: addDays(today, -2).toISOString(),
      to: new Date(today.getFullYear() + 1, 0, 1).toISOString(),
    };
    expect(rateDivisor(range, "day")).toBe(3);
  });

  it("never returns less than one bucket", () => {
    const today = startOfDay(new Date());
    const range = { from: today.toISOString(), to: new Date().toISOString() };
    expect(rateDivisor(range, "week")).toBe(1);
  });
});
