import { describe, expect, it } from "vitest";

import { DateScalar } from "../../objects/DateScalar";
import { Filter } from "../../tools/dateFilter";
import { availableGranularities, snapDateFilterToGranularity } from "./useDashboardFilters";

const day = (value: string) => value as DateScalar;

describe("availableGranularities", () => {
  it("offers every granularity for ranges up to 45 days", () => {
    expect(availableGranularities(Filter.date(day("2025-01-01"), day("2025-02-15")))).toEqual([
      "hour",
      "day",
      "week",
    ]);
  });

  it("drops daily and hourly buckets beyond 45 days or with no start", () => {
    expect(availableGranularities(Filter.date(day("2025-01-01"), day("2025-02-16")))).toEqual([
      "week",
    ]);
    expect(availableGranularities(Filter.date(undefined, day("2025-02-16")))).toEqual(["week"]);
  });
});

describe("snapDateFilterToGranularity", () => {
  it("widens a range to whole weeks from the week start", () => {
    // Thursday Jan 2 through Friday Jan 10, 2025.
    const filter = Filter.date(day("2025-01-02"), day("2025-01-10"));
    expect(snapDateFilterToGranularity(filter, "week", 1)).toEqual(
      Filter.date(day("2024-12-30"), day("2025-01-13")),
    );
    expect(snapDateFilterToGranularity(filter, "week", 0)).toEqual(
      Filter.date(day("2024-12-29"), day("2025-01-12")),
    );
  });

  it("keeps aligned and non-weekly filters as they are", () => {
    const aligned = Filter.date(day("2024-12-30"), day("2025-01-13"));
    expect(snapDateFilterToGranularity(aligned, "week", 1)).toBe(aligned);
    const filter = Filter.date(day("2025-01-02"));
    expect(snapDateFilterToGranularity(filter, "day", 1)).toBe(filter);
  });
});
