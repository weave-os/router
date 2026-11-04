import { type RouterMetricPoint } from "@/shared/data";

export const TIME_KEY = "time" as const;

export type PivotRow = { [TIME_KEY]: number } & Record<string, number>;

export interface PivotedSeries {
  /** Groups ordered by total, largest first. */
  keys: readonly string[];
  rows: readonly PivotRow[];
  /** Sum of every group per bucket, in `rows` order. */
  totals: readonly number[];
}

/** Turns grouped points into one row per bucket with a column per group, zero-filling gaps. */
export function pivotSeries(points: readonly RouterMetricPoint[]): PivotedSeries {
  const byTime = new Map<number, Map<string, number>>();
  const groupTotals = new Map<string, number>();
  for (const { group, time, value } of points) {
    if (time == null) continue;
    const key = group ?? "";
    const bucket = byTime.get(time) ?? new Map<string, number>();
    bucket.set(key, (bucket.get(key) ?? 0) + value);
    byTime.set(time, bucket);
    groupTotals.set(key, (groupTotals.get(key) ?? 0) + value);
  }
  const keys = [...groupTotals.entries()].sort((a, b) => b[1] - a[1]).map(([key]) => key);
  const times = [...byTime.keys()].sort((a, b) => a - b);
  const rows = times.map(time => {
    const bucket = byTime.get(time);
    const row: PivotRow = { [TIME_KEY]: time } as PivotRow;
    for (const key of keys) row[key] = bucket?.get(key) ?? 0;
    return row;
  });
  const totals = rows.map(row => keys.reduce((sum, key) => sum + row[key], 0));
  return { keys, rows, totals };
}

/** Each row's groups as a percent of that row's total. */
export function toShare({ keys, rows, totals }: PivotedSeries): PivotedSeries {
  return {
    keys,
    rows: rows.map((row, index) => {
      const total = totals[index];
      const shared: PivotRow = { [TIME_KEY]: row[TIME_KEY] } as PivotRow;
      for (const key of keys) shared[key] = total > 0 ? (row[key] / total) * 100 : 0;
      return shared;
    }),
    totals: totals.map(total => (total > 0 ? 100 : 0)),
  };
}
