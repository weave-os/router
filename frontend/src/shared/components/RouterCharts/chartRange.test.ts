import { describe, expect, it } from "vitest";

import { bucketCount } from "./chartRange";

describe("bucketCount", () => {
  it("counts every day in the range, empty days included", () => {
    const range = {
      from: new Date(2025, 9, 23).toISOString(),
      to: new Date(2025, 10, 5, 15).toISOString(),
    };
    expect(bucketCount(range, "day", 1)).toBe(14);
  });

  it("counts weeks from the configured week start", () => {
    // Sunday Oct 5 through Saturday Oct 18, 2025.
    const range = {
      from: new Date(2025, 9, 5).toISOString(),
      to: new Date(2025, 9, 19).toISOString(),
    };
    expect(bucketCount(range, "week", 0)).toBe(2);
    expect(bucketCount(range, "week", 1)).toBe(3);
  });

  it("stops at now so ranges ending in the future don't count empty future days", () => {
    const now = new Date();
    const range = {
      from: new Date(now.getFullYear(), now.getMonth(), now.getDate() - 2).toISOString(),
      to: new Date(now.getFullYear() + 1, 0, 1).toISOString(),
    };
    expect(bucketCount(range, "day", 1)).toBe(3);
  });
});
