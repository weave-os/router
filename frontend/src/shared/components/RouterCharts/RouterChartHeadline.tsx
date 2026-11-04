"use client";

import { ChangeValue, type ChangeUnit } from "@/shared/components/molecules/ChangeValue";
import { Statistic, type StatisticValue } from "@/shared/components/Statistic";
import { type RouterMetricPoint } from "@/shared/data";
import { LoadState } from "@/shared/tools/LoadState";

// These mirror the group names chmetric/router_cost.go emits. The comparison group keeps the
// "expensive" wording deliberately: renaming it would silently empty charts on any bundle that
// has not reloaded.
export const ROUTER_ACTUAL_COST_GROUP = "Actual cost";
export const ROUTER_COMPARISON_COST_GROUP = "Expensive model cost";

/**
 * Totals of a `router_cost` series grouped by cost type. The comparison total is null when the
 * backend omitted that group because the comparison model couldn't be priced, which is different
 * from traffic that reprices to $0.
 */
export function routerCostTotals(
  values: readonly Pick<RouterMetricPoint, "group" | "value">[],
) {
  let actual = 0;
  let comparison = 0;
  let hasComparison = false;
  for (const { group, value } of values) {
    if (group === ROUTER_ACTUAL_COST_GROUP) actual += value;
    if (group === ROUTER_COMPARISON_COST_GROUP) {
      comparison += value;
      hasComparison = true;
    }
  }
  return { actual, comparison: hasComparison ? comparison : null };
}

/** Percent change from the previous period, or null when there is no baseline to compare to. */
export function periodPercentChange(current: number, previous: number | null | undefined) {
  if (previous == null || previous === 0) return null;
  return ((current - previous) / Math.abs(previous)) * 100;
}

interface RouterChartHeadlineProps {
  /** Overrides the change pill's tooltip, e.g. when the change is in percentage points. */
  changeTooltip?: string;
  changeUnit?: ChangeUnit;
  /** For lower-is-better values such as cost per request, so a decrease reads as good. */
  invertChangeColor?: boolean;
  percentChange: LoadState<number | null>;
  statistic: LoadState<StatisticValue>;
}

/** A chart card's headline with its change from the previous period, laid out as MetricChart's. */
export function RouterChartHeadline({
  changeTooltip,
  changeUnit,
  invertChangeColor,
  percentChange,
  statistic,
}: RouterChartHeadlineProps) {
  // A change reloading after the range moved would compare the new range with the old previous one.
  const change = LoadState.isLoaded(percentChange) ? percentChange.value : null;
  return (
    <Statistic
      change={
        change != null && (
          <ChangeValue
            invertColor={invertChangeColor}
            change={change}
            tooltip={changeTooltip}
            unit={changeUnit}
          />
        )
      }
      statistic={statistic}
    />
  );
}
