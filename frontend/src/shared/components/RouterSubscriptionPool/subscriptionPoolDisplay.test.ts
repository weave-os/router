import { describe, expect, it } from "vitest";

import { RouterSubscriptionProvider, RouterSubscriptionSeatState } from "../../data";
import {
  describeRunway,
  forecastProviderRunway,
  limitWindowLabel,
  type PoolSeat,
  readLimitWindow,
  readLimitWindowPace,
  summarizeProviderPool,
} from "./subscriptionPoolDisplay";

const NOW = new Date("2026-11-02T12:00:00Z");
const SESSION_MINUTES = 300;
const WEEKLY_MINUTES = 10080;

function seat(overrides: Partial<PoolSeat>): PoolSeat {
  return {
    cooldownUntil: null,
    displayName: "Seat",
    enabled: true,
    id: "seat",
    limitWindows: [],
    limitsObservedAt: null,
    ownerName: "Ada",
    provider: RouterSubscriptionProvider.Claude,
    servedRequests: 0,
    state: RouterSubscriptionSeatState.Active,
    ...overrides,
  };
}

function weekly(usedFraction: number, resetsAt: string | null = null) {
  return {
    resetsAt: resetsAt == null ? null : resetsAt,
    usedFraction: usedFraction,
    windowMinutes: WEEKLY_MINUTES,
  };
}

describe("limitWindowLabel", () => {
  it("names the provider windows by their length", () => {
    expect(limitWindowLabel(SESSION_MINUTES)).toBe("5-hour");
    expect(limitWindowLabel(WEEKLY_MINUTES)).toBe("Weekly");
    expect(limitWindowLabel(3 * 24 * 60)).toBe("3-day");
  });
});

describe("readLimitWindow", () => {
  it("keeps usage while the reported reset is still ahead", () => {
    expect(readLimitWindow(weekly(0.85, "2026-11-04T09:00:00Z"), NOW)).toEqual({
      hasRefilled: false,
      usedFraction: 0.85,
    });
  });

  it("treats a window whose reset has passed as refilled, not still used", () => {
    expect(readLimitWindow(weekly(1, "2026-11-01T09:00:00Z"), NOW)).toEqual({
      hasRefilled: true,
      usedFraction: 0,
    });
  });
});

describe("summarizeProviderPool", () => {
  it("counts serving seats and averages weekly headroom over pooled seats only", () => {
    const seats = [
      seat({ id: "a", limitWindows: [weekly(0.2, "2026-11-04T09:00:00Z")] }),
      seat({
        id: "b",
        limitWindows: [weekly(1, "2026-11-04T09:00:00Z")],
        state: RouterSubscriptionSeatState.Exhausted,
      }),
      seat({
        id: "c",
        limitWindows: [weekly(0.5)],
        state: RouterSubscriptionSeatState.ReconnectRequired,
      }),
      seat({ id: "d", provider: RouterSubscriptionProvider.Codex, limitWindows: [weekly(0)] }),
    ];

    expect(summarizeProviderPool(RouterSubscriptionProvider.Claude, seats, NOW)).toEqual({
      pooledSeats: 2,
      provider: RouterSubscriptionProvider.Claude,
      servingSeats: 1,
      weeklyRemainingFraction: 0.4,
    });
  });

  it("reports no weekly headroom when no pooled seat reported a weekly window", () => {
    expect(
      summarizeProviderPool(RouterSubscriptionProvider.Claude, [seat({})], NOW)
        .weeklyRemainingFraction,
    ).toBeNull();
  });
});

describe("readLimitWindowPace", () => {
  it("compares what is left with what an even burn would leave", () => {
    const pace = readLimitWindowPace(weekly(0.7, "2026-11-04T12:00:00Z"), NOW);

    expect(pace?.hoursUntilReset).toBe(48);
    expect(pace?.evenBurnRemainingFraction).toBeCloseTo(48 / 168);
    expect(pace?.remainingFraction).toBeCloseTo(0.3);
    expect(pace?.isBehindPace).toBe(false);
  });

  it("flags a seat that has less left than an even burn would leave", () => {
    expect(readLimitWindowPace(weekly(0.9, "2026-11-06T12:00:00Z"), NOW)?.isBehindPace).toBe(true);
  });

  it("places no seat whose window lacks a reset or has already refilled", () => {
    expect(readLimitWindowPace(weekly(0.5), NOW)).toBeNull();
    expect(readLimitWindowPace(weekly(1, "2026-11-01T09:00:00Z"), NOW)).toBeNull();
  });
});

describe("forecastProviderRunway", () => {
  const claude = RouterSubscriptionProvider.Claude;

  it("runs a burning-fast pool dry well before its seat refills", () => {
    const seats = [seat({ id: "a", limitWindows: [weekly(0.9, "2026-11-06T12:00:00Z")] })];

    const runway = forecastProviderRunway(claude, seats, NOW);

    expect(runway?.isBurningFast).toBe(true);
    expect(runway?.lowestPoint).toEqual({ hoursFromNow: 8, remainingFraction: 0 });
    expect(runway?.refillHours).toEqual([96]);
  });

  it("bottoms out just before a seat refills when the pool lands near empty", () => {
    const seats = [seat({ id: "a", limitWindows: [weekly(0.4, "2026-11-06T12:00:00Z")] })];

    const runway = forecastProviderRunway(claude, seats, NOW);

    expect(runway?.isBurningFast).toBe(false);
    expect(runway?.lowestPoint.hoursFromNow).toBeGreaterThan(95);
    expect(runway?.lowestPoint.hoursFromNow).toBeLessThan(96);
    expect(runway?.refillsAfterLowestPoint).toBe(1);
  });

  it("shows the pre-refill low and refill at the exact fractional reset time", () => {
    const seats = [seat({ id: "a", limitWindows: [weekly(0.4, "2026-11-06T12:30:00Z")] })];

    const runway = forecastProviderRunway(claude, seats, NOW);
    const beforeRefill = runway?.points.find(
      point => point.hoursFromNow > 96 && point.hoursFromNow < 96.5,
    );
    const atRefill = runway?.points.find(point => point.hoursFromNow === 96.5);

    expect(beforeRefill).toEqual(runway?.lowestPoint);
    expect(beforeRefill?.remainingFraction).toBeLessThan(runway!.points[96].remainingFraction);
    expect(atRefill?.remainingFraction).toBe(1);
    expect(runway?.refillsAfterLowestPoint).toBe(1);
  });

  it("stays on track when the pool burns slower than an even pace", () => {
    const seats = [seat({ id: "a", limitWindows: [weekly(0.2, "2026-11-04T12:00:00Z")] })];

    const runway = forecastProviderRunway(claude, seats, NOW);

    expect(runway?.isBurningFast).toBe(false);
    expect(runway?.points[0].remainingFraction).toBeCloseTo(0.8);
  });

  it("leaves seats without a current weekly reading out and counts them", () => {
    const seats = [
      seat({ id: "a", limitWindows: [weekly(0.2, "2026-11-04T12:00:00Z")] }),
      seat({ id: "b", limitWindows: [weekly(0.5)] }),
      seat({ id: "c", limitWindows: [] }),
    ];

    expect(forecastProviderRunway(claude, seats, NOW)?.uncountedSeats).toBe(2);
  });

  it("returns nothing when no pooled seat has a current weekly reading", () => {
    expect(forecastProviderRunway(claude, [seat({})], NOW)).toBeNull();
    expect(
      forecastProviderRunway(
        claude,
        [
          seat({
            limitWindows: [weekly(0.5, "2026-11-04T12:00:00Z")],
            state: RouterSubscriptionSeatState.ReconnectRequired,
          }),
        ],
        NOW,
      ),
    ).toBeNull();
  });
});

describe("describeRunway", () => {
  const claude = RouterSubscriptionProvider.Claude;

  it("says where the pool bottoms out and how many seats refill after", () => {
    const seats = [
      seat({ id: "a", limitWindows: [weekly(0.4, "2026-11-06T12:00:00Z")] }),
      seat({ id: "b", limitWindows: [] }),
    ];

    const runway = forecastProviderRunway(claude, seats, NOW);

    expect(runway).not.toBeNull();
    expect(describeRunway(runway!, NOW)).toBe(
      "Drops to ~7% in 4 days, before 1 seat refills. 1 seat without a current reading isn't counted.",
    );
  });

  it("says the pool stays above its current level when it refills within hours", () => {
    const seats = [seat({ id: "a", limitWindows: [weekly(0.5, "2026-11-02T14:00:00Z")] })];

    const runway = forecastProviderRunway(claude, seats, NOW);

    expect(runway).not.toBeNull();
    expect(describeRunway(runway!, NOW)).toBe("Stays above ~49% all week.");
  });

  it("reports when a near-empty pool runs dry in the next few hours", () => {
    const seats = [seat({ id: "a", limitWindows: [weekly(0.95, "2026-11-07T12:00:00Z")] })];

    const runway = forecastProviderRunway(claude, seats, NOW);

    expect(runway).not.toBeNull();
    expect(describeRunway(runway!, NOW)).toBe("Drops to ~0% in 3 hours.");
  });

  it("says an empty pool is out of limit until its first seat refills", () => {
    const seats = [seat({ id: "a", limitWindows: [weekly(1, "2026-11-04T12:00:00Z")] })];

    const runway = forecastProviderRunway(claude, seats, NOW);

    expect(runway).not.toBeNull();
    expect(describeRunway(runway!, NOW)).toBe("Out of limit now; first seat refills in 2 days.");
  });
});
