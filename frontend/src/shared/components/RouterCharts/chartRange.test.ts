import { describe, expect, it } from "vitest";

import { bucketCount } from "./chartRange";

describe("bucketCount", () => {
  it("counts every day in the range, empty days included", () => {
    const range = {
      from: new Date(2026, 9, 23).toISOString(),
      to: new Date(2026, 10, 5, 15).toISOString(),
    };
    expect(bucketCount(range, "day", 1)).toBe(14);
  });

  it("counts weeks from the configured week start", () => {
    // Sunday Nov 1 through Saturday Nov 14, 2026.
    const range = {
      from: new Date(2026, 10, 1).toISOString(),
      to: new Date(2026, 10, 15).toISOString(),
    };
    expect(bucketCount(range, "week", 0)).toBe(2);
    expect(bucketCount(range, "week", 1)).toBe(3);
  });
});
