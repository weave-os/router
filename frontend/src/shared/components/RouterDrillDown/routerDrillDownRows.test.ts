import { describe, expect, it } from "vitest";

import { totalPairs, usageType } from "./routerDrillDownRows";

const PAIR = {
  catalogCostUsd: 2,
  comparisonCostUsd: 5,
  decisionModel: "claude-sonnet-4-5",
  providers: ["anthropic"],
  requestedModel: "claude-opus-4-5",
  requests: 4,
  subscriptionRequests: 0,
};

describe("totalPairs", () => {
  it("sums costs and requests and lists distinct models and providers", () => {
    const totals = totalPairs([
      PAIR,
      {
        ...PAIR,
        decisionModel: "gpt-5",
        providers: ["openai", "anthropic"],
        requests: 1,
        subscriptionRequests: 1,
      },
    ]);
    expect(totals).toMatchObject({
      catalogCostUsd: 4,
      comparisonCostUsd: 10,
      decisionModels: ["claude-sonnet-4-5", "gpt-5"],
      providers: ["anthropic", "openai"],
      requestedModels: ["claude-opus-4-5"],
      requests: 5,
      subscriptionRequests: 1,
    });
  });
});

describe("usageType", () => {
  it("labels API, subscription and mixed rows", () => {
    expect(usageType(totalPairs([PAIR]))).toBe("API");
    expect(usageType(totalPairs([{ ...PAIR, subscriptionRequests: 4 }]))).toBe("Subscription");
    expect(usageType(totalPairs([{ ...PAIR, subscriptionRequests: 1 }]))).toBe(
      "3 API / 1 subscription",
    );
  });
});
