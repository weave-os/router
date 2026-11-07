"use client";

import {
  Chart,
  ChartAxisType,
  type ChartConfig,
  ChartSeriesType,
} from "@weave-os/ui/components/Chart";
import { formatPercentage } from "@weave-os/ui/tools/format";
import { LoadState } from "@weave-os/ui/tools/LoadState";
import { DateFormat } from "@weave-os/ui/tools/time";
import { addHours, format } from "date-fns";

import { useMemo } from "react";

import {
  HOURS_PER_DAY,
  type ProviderRunway,
  RUNWAY_HORIZON_HOURS,
} from "./subscriptionPoolDisplay";

const REMAINING_KEY = "remainingFraction";
const SERIES_LABEL = "Limit left";
const HOURS_KEY = "hoursFromNow";
const NEUTRAL_RUNWAY_COLOR = "var(--wv-bg-neutral-solid)";
const BURNING_FAST_RUNWAY_COLOR = "var(--wv-bg-status-warning-solid)";

type IndependentKey = typeof HOURS_KEY;
type DependentKey = typeof REMAINING_KEY;
type AxisValue = number;

const DEPENDENT_KEYS = LoadState.loaded([REMAINING_KEY] as const);

function formatHoursFromNow(hoursFromNow: number) {
  if (hoursFromNow === 0) return "Now";
  return `+${Math.round(hoursFromNow / HOURS_PER_DAY)}d`;
}

/**
 * The pool's weekly limit left over the next 7 days. The line is neutral ink and only takes the
 * warning color when the pool is burning faster than an even pace.
 */
export function ProviderRunwayChart({
  label,
  now,
  runway,
}: {
  label: string;
  now: Date;
  runway: ProviderRunway;
}) {
  const color = runway.isBurningFast ? BURNING_FAST_RUNWAY_COLOR : NEUTRAL_RUNWAY_COLOR;
  const config = useMemo<ChartConfig<DependentKey, AxisValue, AxisValue>>(
    () => ({
      [REMAINING_KEY]: {
        color,
        dashed: true,
        fillOpacity: 0.5,
        label: SERIES_LABEL,
        type: ChartSeriesType.Area,
      },
    }),
    [color],
  );
  const runwayPoints = useMemo(() => LoadState.loaded(runway.points), [runway.points]);
  const referenceLines = useMemo(
    () =>
      LoadState.loaded(
        [...new Set(runway.refillHours)].map(hoursFromNow => ({
          dashed: true,
          type: "independent" as const,
          value: hoursFromNow,
        })),
      ),
    [runway.refillHours],
  );

  return (
    <div role="img" aria-label={label} className="mt-4 h-48">
      <Chart<IndependentKey, DependentKey, AxisValue, AxisValue>
        allowDependentTickDecimals
        data={runwayPoints}
        config={config}
        dependentDomain={[0, 1]}
        dependentKeys={DEPENDENT_KEYS}
        formatIndependentValue={formatHoursFromNow}
        formatIndependentValueTooltip={hoursFromNow => {
          const at = addHours(now, hoursFromNow);
          return `${format(at, DateFormat.DayOfWeekFull)} ${format(at, DateFormat.Hour)}`;
        }}
        formatValue={formatPercentage}
        independentDomain={[0, RUNWAY_HORIZON_HOURS]}
        independentKey={HOURS_KEY}
        independentType={ChartAxisType.Number}
        referenceLines={referenceLines}
      />
    </div>
  );
}
