"use client";

import { Text } from "@/shared/components/atoms/Text";
import { LoadState } from "@/shared/tools/LoadState";

import { useMemo } from "react";

import { Chart } from "./Chart";
import { ChartAxisDomain, ChartAxisType, ChartSeriesConfig, ChartSeriesType } from "./types";

const TIME_KEY = "time" as const;
const VALUE_KEY = "value" as const;
const DEPENDENT_KEYS = LoadState.loaded([VALUE_KEY] as const);
// A time axis without a domain starts at 0 (1970), squeezing every point against the right edge.
const DATA_TIME_DOMAIN: ChartAxisDomain<number> = ["dataMin", "dataMax"];

export enum SmallMultiplesScale {
  /** One value scale for every panel, so panel heights compare across series. */
  Shared = "shared",
  /** Each panel scaled to its own peak, so a small series' shape still shows. */
  Independent = "independent",
}

export interface SmallMultiplesSeries {
  /** Overrides {@link SmallMultiplesProps.color} for this panel. */
  color?: string;
  /** Stable identity, used as the React key. */
  key: string;
  label: string;
  /** Shown beside the label in place of the latest value, e.g. a total and share. */
  summary?: string;
  /** One value per entry of {@link SmallMultiplesProps.times}; null leaves a gap. */
  values: readonly (number | null)[];
}

export interface SmallMultiplesProps {
  color: string;
  formatTime: (time: number) => string;
  formatValue: (value: number) => string;
  /** @default SmallMultiplesScale.Shared */
  scale?: SmallMultiplesScale;
  series: readonly SmallMultiplesSeries[];
  times: readonly number[];
}

/**
 * One compact line per series, so series of different sizes can be compared without tangled
 * overlapping lines. Each panel is labelled with its latest value unless the series has a summary.
 */
export function SmallMultiples({
  color,
  formatTime,
  formatValue,
  scale = SmallMultiplesScale.Shared,
  series,
  times,
}: SmallMultiplesProps) {
  const sharedMax = useMemo(
    () => Math.max(0, ...series.flatMap(({ values }) => values.map(value => value ?? 0))),
    [series],
  );
  const panels = useMemo(
    () =>
      series.map(({ color: seriesColor, key, label, summary, values }) => ({
        config: {
          // An area with a soft fill and an end marker reads at a glance in a short panel.
          [VALUE_KEY]: {
            color: seriesColor ?? color,
            endDot: true,
            fillGradient: true,
            formatValue,
            type: ChartSeriesType.Area,
          },
        } satisfies Partial<Record<typeof VALUE_KEY, ChartSeriesConfig<number, number>>>,
        key,
        label,
        latest: values.findLast(value => value != null),
        max: Math.max(0, ...values.map(value => value ?? 0)),
        summary,
        rows: LoadState.loaded(
          times.map((time, index) => ({ [TIME_KEY]: time, [VALUE_KEY]: values[index] })),
        ),
      })),
    [color, formatValue, series, times],
  );

  return (
    <ul className="grid grid-cols-1 gap-x-6 gap-y-4 @md:grid-cols-2">
      {panels.map(({ config, key, label, latest, max, rows, summary }) => (
        <li key={key} className="flex min-w-0 flex-col gap-1">
          <div className="flex items-baseline justify-between gap-2">
            <Text className="truncate text-body text-wv-secondary" title={label}>
              {label}
            </Text>
            <Text className="shrink-0 text-body tabular-nums">
              {summary ?? (latest == null ? "—" : formatValue(latest))}
            </Text>
          </div>
          <Chart
            className="h-28 min-h-0"
            config={config}
            data={rows}
            dependentDomain={[0, scale === SmallMultiplesScale.Shared ? sharedMax : max]}
            dependentKeys={DEPENDENT_KEYS}
            emptyDataMessage={null}
            formatIndependentValueTooltip={formatTime}
            formatValue={formatValue}
            hideAxes
            independentDomain={DATA_TIME_DOMAIN}
            independentKey={TIME_KEY}
            independentType={ChartAxisType.Time}
            legend={false}
          />
        </li>
      ))}
    </ul>
  );
}
