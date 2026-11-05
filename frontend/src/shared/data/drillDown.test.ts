import { describe, expect, it } from "vitest";

import { drillDownRange } from "./drillDown";
import { RouterMetricType } from "./metricSeries";

const RANGE = { from: "2026-10-01T00:00:00.000Z", to: "2026-10-10T12:00:00.000Z" };
const BASE = { granularity: "day" as const, range: RANGE, type: RouterMetricType.Cost };

describe("drillDownRange", () => {
  it("keeps the chart range for totals", () => {
    expect(drillDownRange({ ...BASE, granularity: null })).toEqual(RANGE);
  });

  it("narrows to the clicked bucket", () => {
    expect(
      drillDownRange({ ...BASE, bucketStart: Date.parse("2026-10-03T00:00:00.000Z") }),
    ).toEqual({
      from: "2026-10-03T00:00:00.000Z",
      to: "2026-10-04T00:00:00.000Z",
    });
  });

  it("clips the bucket to the chart range", () => {
    expect(
      drillDownRange({ ...BASE, bucketStart: Date.parse("2026-10-10T00:00:00.000Z") }).to,
    ).toBe(RANGE.to);
  });

  it("keeps the range start for cumulative charts", () => {
    expect(
      drillDownRange({
        ...BASE,
        bucketStart: Date.parse("2026-10-03T00:00:00.000Z"),
        cumulative: true,
      }),
    ).toEqual({ from: RANGE.from, to: "2026-10-04T00:00:00.000Z" });
  });
});
