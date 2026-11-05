import { RouterCostType, type RouterMetricPoint } from "../../data";
import { costBuckets, costTotals, cumulativeSavings, rangeSavings } from "./RouterSpendCharts";

function point(group: RouterCostType, time: number, value: number): RouterMetricPoint {
  return { group, time, value };
}

const priced = [
  point(RouterCostType.Actual, 2, 4),
  point(RouterCostType.Comparison, 2, 5),
  point(RouterCostType.Actual, 1, 1),
  point(RouterCostType.Comparison, 1, 3),
];

const unpriced = [point(RouterCostType.Actual, 1, 1), point(RouterCostType.Actual, 2, 4)];

describe("costBuckets", () => {
  it("leaves the comparison null in buckets the comparison model couldn't price", () => {
    expect(costBuckets(unpriced)).toEqual([
      { actual: 1, comparison: null, time: 1 },
      { actual: 4, comparison: null, time: 2 },
    ]);
  });
});

describe("costTotals", () => {
  it("sums both cost types across buckets", () => {
    expect(costTotals(priced)).toEqual({ actual: 5, comparison: 8 });
  });

  it("keeps a $0 comparison distinct from a missing one", () => {
    expect(costTotals([point(RouterCostType.Comparison, 1, 0)]).comparison).toBe(0);
    expect(costTotals(unpriced).comparison).toBeNull();
  });
});

describe("rangeSavings", () => {
  it("is the comparison cost minus catalog cost", () => {
    expect(rangeSavings(priced)).toBe(3);
  });

  it("is null without a priced comparison, including an empty range", () => {
    expect(rangeSavings(unpriced)).toBeNull();
    expect(rangeSavings([])).toBeNull();
  });
});

describe("cumulativeSavings", () => {
  it("accumulates savings oldest first, skipping unpriced buckets", () => {
    expect(
      cumulativeSavings([
        { actual: 1, comparison: 3, time: 1 },
        { actual: 2, comparison: null, time: 2 },
        { actual: 4, comparison: 3, time: 3 },
      ]),
    ).toEqual([
      { savings: 2, time: 1 },
      { savings: 1, time: 3 },
    ]);
  });
});
