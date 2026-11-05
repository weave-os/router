import { addHours, formatDistanceStrict } from "date-fns";

import {
  type RouterSubscriptionLimitWindow,
  type RouterSubscriptionPoolSeat,
  RouterSubscriptionProvider,
  RouterSubscriptionSeatState,
} from "../../data";
import { pluralize } from "../../tools/strings";

export type PoolSeat = RouterSubscriptionPoolSeat;
export type PoolLimitWindow = RouterSubscriptionLimitWindow;

const MINUTES_PER_HOUR = 60;
const MINUTES_PER_DAY = 24 * MINUTES_PER_HOUR;
const MINUTES_PER_WEEK = 7 * MINUTES_PER_DAY;
const MS_PER_HOUR = MINUTES_PER_HOUR * 60 * 1000;
export const HOURS_PER_DAY = 24;

export const RUNWAY_HORIZON_HOURS = 7 * HOURS_PER_DAY;

/** A lowest point this soon is where the pool is now, so the forecast reads as "stays above". */
const LOWEST_POINT_IS_NOW_HOURS = 6;
const NEAR_CURRENT_REMAINING_TOLERANCE = 0.01;

/** Share of a window a seat may sit below an even burn before it counts as behind pace. */
const PACE_TOLERANCE = 0.05;

/** Windows shorter than a day are the rolling session limit; the rest are the weekly limit. */
export function isSessionWindow(limitWindow: PoolLimitWindow): boolean {
  return limitWindow.windowMinutes < MINUTES_PER_DAY;
}

export function limitWindowLabel(windowMinutes: number): string {
  if (windowMinutes === MINUTES_PER_WEEK) return "Weekly";
  if (windowMinutes % MINUTES_PER_DAY === 0) return `${windowMinutes / MINUTES_PER_DAY}-day`;
  return `${Math.round(windowMinutes / MINUTES_PER_HOUR)}-hour`;
}

export interface LimitWindowReading {
  /** True once the reported reset has passed: the reading no longer describes the window. */
  hasRefilled: boolean;
  usedFraction: number;
}

export function readLimitWindow(limitWindow: PoolLimitWindow, now: Date): LimitWindowReading {
  const hasRefilled = limitWindow.resetsAt != null && new Date(limitWindow.resetsAt) <= now;
  return { hasRefilled, usedFraction: hasRefilled ? 0 : limitWindow.usedFraction };
}

/** Seats the router can lease right now; cooldown and exhaustion are temporary absences. */
export function isServingSeat(seat: PoolSeat): boolean {
  return (
    seat.enabled &&
    (seat.state === RouterSubscriptionSeatState.Active ||
      seat.state === RouterSubscriptionSeatState.Unknown)
  );
}

/** Seats that never serve until someone acts: disabled or waiting on a fresh sign-in. */
export function isOutOfPoolSeat(seat: PoolSeat): boolean {
  return !seat.enabled || seat.state === RouterSubscriptionSeatState.ReconnectRequired;
}

export interface ProviderPoolSummary {
  pooledSeats: number;
  provider: RouterSubscriptionProvider;
  servingSeats: number;
  /** Mean weekly limit left across pooled seats that reported one; null when none did. */
  weeklyRemainingFraction: number | null;
}

export function summarizeProviderPool(
  provider: RouterSubscriptionProvider,
  seats: readonly PoolSeat[],
  now: Date,
): ProviderPoolSummary {
  const pooledSeats = seats.filter(seat => seat.provider === provider && !isOutOfPoolSeat(seat));
  const weeklyRemaining = pooledSeats.flatMap(seat =>
    seat.limitWindows
      .filter(limitWindow => !isSessionWindow(limitWindow))
      .map(limitWindow => 1 - readLimitWindow(limitWindow, now).usedFraction),
  );
  return {
    pooledSeats: pooledSeats.length,
    provider,
    servingSeats: pooledSeats.filter(isServingSeat).length,
    weeklyRemainingFraction:
      weeklyRemaining.length === 0
        ? null
        : weeklyRemaining.reduce((total, remaining) => total + remaining, 0) /
          weeklyRemaining.length,
  };
}

export interface LimitWindowPace {
  /** Share of the window left had the seat burned evenly since its last reset. */
  evenBurnRemainingFraction: number;
  hoursUntilReset: number;
  isBehindPace: boolean;
  remainingFraction: number;
  windowHours: number;
}

/** Null when the window has no reset time or has refilled: the reading no longer places the seat. */
export function readLimitWindowPace(
  limitWindow: PoolLimitWindow,
  now: Date,
): LimitWindowPace | null {
  if (limitWindow.resetsAt == null) return null;
  const reading = readLimitWindow(limitWindow, now);
  if (reading.hasRefilled) return null;

  const windowHours = limitWindow.windowMinutes / MINUTES_PER_HOUR;
  const hoursUntilReset = (new Date(limitWindow.resetsAt).getTime() - now.getTime()) / MS_PER_HOUR;
  const evenBurnRemainingFraction = Math.min(1, hoursUntilReset / windowHours);
  const remainingFraction = 1 - reading.usedFraction;
  return {
    evenBurnRemainingFraction,
    hoursUntilReset,
    isBehindPace: remainingFraction < evenBurnRemainingFraction - PACE_TOLERANCE,
    remainingFraction,
    windowHours,
  };
}

export interface RunwayPoint {
  hoursFromNow: number;
  remainingFraction: number;
}

export interface ProviderRunway {
  isBurningFast: boolean;
  lowestPoint: RunwayPoint;
  points: RunwayPoint[];
  /** Hours from now of every seat refill in the horizon; a seat appears once per refill. */
  refillHours: readonly number[];
  refillsAfterLowestPoint: number;
  /** Pooled seats whose weekly window has no current reading, so the forecast leaves them out. */
  uncountedSeats: number;
}

interface SeatBurn {
  burnPerHour: number;
  pace: LimitWindowPace;
}

function remainingAfter(seatBurn: SeatBurn, hoursFromNow: number): number {
  const { burnPerHour, pace } = seatBurn;
  if (hoursFromNow < pace.hoursUntilReset) {
    return Math.max(0, pace.remainingFraction - burnPerHour * hoursFromNow);
  }
  const hoursIntoCycle = (hoursFromNow - pace.hoursUntilReset) % pace.windowHours;
  return Math.max(0, 1 - burnPerHour * hoursIntoCycle);
}

function refillHoursWithin(seatBurn: SeatBurn): number[] {
  const { pace } = seatBurn;
  const refills: number[] = [];
  for (
    let hoursFromNow = pace.hoursUntilReset;
    hoursFromNow <= RUNWAY_HORIZON_HOURS;
    hoursFromNow += pace.windowHours
  ) {
    refills.push(hoursFromNow);
  }
  return refills;
}

function mean(values: readonly number[]): number {
  return values.reduce((total, value) => total + value, 0) / values.length;
}

/**
 * The pool's weekly limit left over the next 7 days if every seat keeps burning at the rate it
 * has averaged since its last reset, and refills to full at its reset. Null when no pooled seat
 * has a current weekly reading.
 */
export function forecastProviderRunway(
  provider: RouterSubscriptionProvider,
  seats: readonly PoolSeat[],
  now: Date,
): ProviderRunway | null {
  const pooledSeats = seats.filter(seat => seat.provider === provider && !isOutOfPoolSeat(seat));
  const seatBurns = pooledSeats.flatMap(seat => {
    const weeklyWindow = seat.limitWindows.find(limitWindow => !isSessionWindow(limitWindow));
    const pace = weeklyWindow == null ? null : readLimitWindowPace(weeklyWindow, now);
    if (pace == null) return [];
    const hoursIntoWindow = pace.windowHours - pace.hoursUntilReset;
    const burnPerHour = hoursIntoWindow > 0 ? (1 - pace.remainingFraction) / hoursIntoWindow : 0;
    return [{ burnPerHour, pace }];
  });
  if (seatBurns.length === 0) return null;

  const refillHours = seatBurns.flatMap(refillHoursWithin).sort((a, b) => a - b);
  const sampleHours = [
    ...Array.from({ length: RUNWAY_HORIZON_HOURS + 1 }, (_, hoursFromNow) => hoursFromNow),
    ...refillHours.flatMap(hoursFromNow => [
      Math.max(0, hoursFromNow - 1 / MS_PER_HOUR),
      hoursFromNow,
    ]),
  ];
  const points = [...new Set(sampleHours)]
    .sort((a, b) => a - b)
    .map(hoursFromNow => ({
      hoursFromNow,
      remainingFraction: mean(seatBurns.map(seatBurn => remainingAfter(seatBurn, hoursFromNow))),
    }));
  const lowestPoint = points.reduce((lowest, point) =>
    point.remainingFraction < lowest.remainingFraction ? point : lowest,
  );
  return {
    isBurningFast:
      mean(seatBurns.map(({ pace }) => pace.remainingFraction)) <
      mean(seatBurns.map(({ pace }) => pace.evenBurnRemainingFraction)) - PACE_TOLERANCE,
    lowestPoint,
    points,
    refillHours,
    refillsAfterLowestPoint: refillHours.filter(
      hoursFromNow =>
        hoursFromNow > lowestPoint.hoursFromNow && hoursFromNow <= lowestPoint.hoursFromNow + 1,
    ).length,
    uncountedSeats: pooledSeats.length - seatBurns.length,
  };
}

/** One sentence on where the pool bottoms out, plus how many seats the forecast skipped. */
export function describeRunway(runway: ProviderRunway, now: Date): string {
  const { lowestPoint, refillHours, refillsAfterLowestPoint, uncountedSeats } = runway;
  let outlook: string;
  if (lowestPoint.hoursFromNow === 0 && lowestPoint.remainingFraction === 0) {
    outlook =
      refillHours.length === 0
        ? "Out of limit now."
        : `Out of limit now; first seat refills in ${formatDistanceStrict(addHours(now, refillHours[0]), now)}.`;
  } else if (
    lowestPoint.hoursFromNow === 0 ||
    (lowestPoint.hoursFromNow < LOWEST_POINT_IS_NOW_HOURS &&
      lowestPoint.remainingFraction > 0 &&
      runway.points[0].remainingFraction - lowestPoint.remainingFraction <
        NEAR_CURRENT_REMAINING_TOLERANCE)
  ) {
    outlook = `Stays above ~${Math.floor(lowestPoint.remainingFraction * 100)}% all week.`;
  } else {
    const lowestAt = addHours(now, lowestPoint.hoursFromNow);
    const beforeRefills =
      refillsAfterLowestPoint === 0
        ? ""
        : `, before ${`${refillsAfterLowestPoint} ${pluralize(refillsAfterLowestPoint, "seat")}`} ${refillsAfterLowestPoint === 1 ? "refills" : "refill"}`;
    outlook = `Drops to ~${Math.round(lowestPoint.remainingFraction * 100)}% in ${formatDistanceStrict(lowestAt, now)}${beforeRefills}.`;
  }

  if (uncountedSeats === 0) return outlook;
  return `${outlook} ${`${uncountedSeats} ${pluralize(uncountedSeats, "seat")}`} without a current reading ${uncountedSeats === 1 ? "isn't" : "aren't"} counted.`;
}
