import { cleanup, render } from "@testing-library/react";

import { LoadState } from "../../tools/LoadState";
import { Chart } from "../Chart";
import { ProviderRunwayChart } from "./ProviderRunwayChart";

vi.mock("../Chart", async importOriginal => ({
  ...(await importOriginal<typeof import("../Chart")>()),
  Chart: vi.fn(() => null),
}));

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

test("uses fractional percentage ticks and one reference line per reset time", () => {
  render(
    <ProviderRunwayChart
      label="Weekly limit left"
      now={new Date("2026-11-02T12:00:00Z")}
      runway={{
        isBurningFast: false,
        lowestPoint: { hoursFromNow: 0, remainingFraction: 0.5 },
        points: [{ hoursFromNow: 0, remainingFraction: 0.5 }],
        refillHours: [24, 24, 48],
        refillsAfterLowestPoint: 0,
        uncountedSeats: 0,
      }}
    />,
  );

  const chartProps = vi.mocked(Chart).mock.calls[0][0];
  expect(chartProps.allowDependentTickDecimals).toBe(true);
  expect(LoadState.unwrap(chartProps.referenceLines!)).toEqual([
    { dashed: true, type: "independent", value: 24 },
    { dashed: true, type: "independent", value: 48 },
  ]);
});
