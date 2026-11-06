"use client";

import { ChangeValue, type ChangeUnit } from "@weave-os/ui/components/molecules/ChangeValue";
import { LoadState } from "@weave-os/ui/tools/LoadState";

import { Statistic, type StatisticValue } from "../Statistic";

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
