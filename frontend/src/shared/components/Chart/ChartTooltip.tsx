import type { ChartProps } from "./Chart";
import type { TooltipSeriesEntry } from "./helpers/capTooltipSeries";

import { useLayoutEffect, useRef } from "react";
import * as Recharts from "recharts";

import { cn } from "../../tools/cn";
import { useChartContext } from "./ChartContext";
import { ChartSeriesSwatch, isDottedSeries, isHatchedBarSeries } from "./ChartSeriesSwatch";
import { TOOLTIP_DATA_KEY } from "./constants";
import { capTooltipSeries, MAX_TOOLTIP_SERIES_ROWS } from "./helpers/capTooltipSeries";
import { ChartDataKeyType, ChartDataPoint, ChartDataValueType, ChartSeriesType } from "./types";

export interface ChartTooltipProps<
  TIndependentKey extends ChartDataKeyType,
  TDependentKey extends ChartDataKeyType,
  TIndependentValue extends ChartDataValueType,
  TDependentValue extends ChartDataValueType,
  TTooltipData,
>
  extends
    Partial<Recharts.TooltipContentProps<TDependentValue, TDependentKey>>,
    Pick<React.ComponentProps<"div">, "className">,
    Pick<
      ChartProps<TIndependentKey, TDependentKey, TIndependentValue, TDependentValue>,
      "formatValue"
    > {
  /**
   * Formatter for the independent values in the chart tooltip.
   * Can return a React node to display rich content like avatars.
   */
  formatIndependentValue?: (value: TIndependentValue) => React.ReactNode;
  /**
   * Formatter for the independent values that takes the active series into account.
   * If provided, this is used instead of formatIndependentValue.
   * Useful for showing different dates based on which series is being hovered.
   */
  formatIndependentValueForSeries?: (
    value: TIndependentValue,
    activeSeriesKey: ChartDataKeyType | null,
  ) => React.ReactNode;
  /**
   * If true, the label will not be shown.
   *
   * @default false
   */
  hideLabel?: boolean;
  /**
   * If true, the per-series rows are not rendered — only the label (unless also
   * hidden) and `renderTooltipData`. Use when the consumer renders the entire
   * tooltip body itself and the auto-generated series/axis rows would duplicate it.
   *
   * @default false
   */
  hideSeriesRows?: boolean;
  /**
   * If true, the tooltip is locked in place and will not be hidden when moving the mouse away from
   * the given data point.
   *
   * @default false
   */
  locked?: boolean;
  /**
   * If provided, used to render additional data in the tooltip.
   * Receives the tooltip data and the independent value (label) for the hovered data point.
   */
  renderTooltipData?: (
    data: TTooltipData,
    independentValue: TIndependentValue | null,
    activeSeriesKey: ChartDataKeyType | null,
  ) => React.ReactNode;

  /**
   * If true, inactive series will be faded out.
   *
   * @default true
   */
  fadeInactiveSeries?: boolean;
}

export function ChartTooltip<
  TIndependentKey extends ChartDataKeyType,
  TDependentKey extends ChartDataKeyType,
  TIndependentValue extends ChartDataValueType,
  TDependentValue extends ChartDataValueType,
  TTooltipData,
>({
  active,
  className,
  fadeInactiveSeries = true,
  formatIndependentValue,
  formatIndependentValueForSeries,
  formatValue,
  hideLabel = false,
  hideSeriesRows = false,
  label,
  locked = false,
  payload,
  renderTooltipData,
}: ChartTooltipProps<
  TIndependentKey,
  TDependentKey,
  TIndependentValue,
  TDependentValue,
  TTooltipData
>) {
  const { activeSeries, config } = useChartContext();
  const tooltipRef = useRef<HTMLDivElement>(null);

  useLayoutEffect(() => {
    const el = tooltipRef.current;
    if (el == null) return;

    el.style.transform = "";

    const rect = el.getBoundingClientRect();
    const chartWrapper = el.closest(".recharts-wrapper");
    if (chartWrapper == null) return;
    const chartRect = chartWrapper.getBoundingClientRect();

    let offsetX = 0;
    let offsetY = 0;

    if (rect.right > chartRect.right) {
      offsetX = chartRect.right - rect.right - 8;
    }
    if (rect.left + offsetX < chartRect.left) {
      offsetX = chartRect.left - rect.left + 8;
    }

    if (rect.bottom > chartRect.bottom) {
      offsetY = chartRect.bottom - rect.bottom - 8;
    }
    if (rect.top + offsetY < chartRect.top) {
      offsetY = chartRect.top - rect.top + 8;
    }

    const transforms: string[] = [];
    if (offsetX !== 0) transforms.push(`translateX(${offsetX}px)`);
    if (offsetY !== 0) transforms.push(`translateY(${offsetY}px)`);

    if (transforms.length > 0) {
      el.style.transform = transforms.join(" ");
    }
    // Only re-measure when tooltip content or lock state changes. Without this, every parent
    // re-render (e.g. on every mousemove) would force a synchronous layout reflow via
    // getBoundingClientRect, causing jank on complex pages.
  }, [active, payload, label, locked]);

  if (!locked && (active === false || payload == null || payload.length === 0)) return null;
  if (payload == null || payload.length === 0) return null;
  const independentValue = label as TIndependentValue | null | undefined;

  // Use series-aware formatter if provided, otherwise fall back to basic formatter
  const title =
    independentValue != null && formatIndependentValueForSeries != null
      ? formatIndependentValueForSeries(independentValue, activeSeries ?? null)
      : independentValue != null && formatIndependentValue != null
        ? formatIndependentValue(independentValue)
        : independentValue != null
          ? String(independentValue)
          : undefined;

  let tooltipData: TTooltipData | undefined;

  type PayloadItem = RechartsTooltipPayloadItem<TDependentKey, TDependentValue>;
  const typedPayload = payload as ReadonlyArray<PayloadItem>;

  if (renderTooltipData != null) {
    tooltipData = typedPayload
      .map(
        p =>
          p.payload as ChartDataPoint<
            TIndependentKey,
            TDependentKey,
            TIndependentValue,
            TDependentValue,
            TTooltipData
          >,
      )
      .find(p => p[TOOLTIP_DATA_KEY] != null)?.[TOOLTIP_DATA_KEY];
  }

  // Filter out entries that won't render before capping so the count is accurate.
  const renderablePayload: PayloadItem[] = [];
  const renderableEntries: TooltipSeriesEntry<TDependentKey>[] = [];
  for (const series of typedPayload) {
    if (series.type === "none") continue;
    if (series.name == null) continue;
    const seriesConfig = config?.[series.name];
    const tooltip = seriesConfig?.showTooltip;
    if (tooltip === false) continue;
    if (typeof tooltip === "function" && independentValue != null && !tooltip(independentValue)) {
      continue;
    }
    renderablePayload.push(series);
    renderableEntries.push({
      name: series.name,
      sortValue: series.value != null ? Number(series.value) : null,
    });
  }

  // Cap to at most MAX_TOOLTIP_SERIES_ROWS visible rows.
  const capped = capTooltipSeries<TDependentKey>({
    activeSeries: (activeSeries as TDependentKey | undefined) ?? null,
    entries: renderableEntries,
    maxRows: MAX_TOOLTIP_SERIES_ROWS,
  });
  const payloadByName = new Map(renderablePayload.map(series => [series.name, series]));
  const sortedPayload = capped.visible.flatMap(series => {
    const payload = payloadByName.get(series.name);
    return payload != null ? [payload] : [];
  });

  return (
    <div
      ref={tooltipRef}
      data-chart-tooltip
      className={cn(
        "surface-float grid max-h-96 min-w-[8rem] items-start gap-3 overflow-auto rounded-lg border p-2 text-caption shadow-xl",
        { "border-border-darker": locked },
        className,
      )}
      onClick={locked ? e => e.stopPropagation() : undefined}
    >
      {!hideLabel && title != null && <div className="font-medium">{title}</div>}

      {!hideSeriesRows && (
        <div className="flex w-full flex-col gap-1.5">
          {sortedPayload.map(series => (
            <ChartTooltipSeries
              key={`${String(series.dataKey)}-${series.type}`}
              formatValue={formatValue}
              independentValue={independentValue}
              series={series}
              fadeInactiveSeries={fadeInactiveSeries}
            />
          ))}
          {capped.hiddenCount > 0 && (
            <div className="flex items-center gap-2 text-caption text-wv-secondary">
              +{capped.hiddenCount} more
            </div>
          )}
        </div>
      )}

      {tooltipData != null &&
        renderTooltipData?.(tooltipData, independentValue ?? null, activeSeries ?? null)}
    </div>
  );
}

/** A single item in the payload for a Recharts tooltip. */
type RechartsTooltipPayloadItem<
  TDependentKey extends ChartDataKeyType,
  TDependentValue extends ChartDataValueType,
> = Omit<Recharts.TooltipPayloadEntry<TDependentValue, TDependentKey>, "name" | "value"> & {
  name?: TDependentKey;
  value?: TDependentValue;
};

interface ChartTooltipSeriesProps<
  TIndependentKey extends ChartDataKeyType,
  TDependentKey extends ChartDataKeyType,
  TIndependentValue extends ChartDataValueType,
  TDependentValue extends ChartDataValueType,
>
  extends
    Pick<
      ChartProps<TIndependentKey, TDependentKey, TIndependentValue, TDependentValue>,
      "formatValue"
    >,
    Pick<
      ChartTooltipProps<
        TIndependentKey,
        TDependentKey,
        TIndependentValue,
        TDependentValue,
        unknown
      >,
      "fadeInactiveSeries"
    > {
  independentValue: TIndependentValue | null | undefined;
  series: Pick<
    RechartsTooltipPayloadItem<TDependentKey, TDependentValue>,
    "color" | "name" | "type" | "value"
  >;
}

function ChartTooltipSeries<
  TIndependentKey extends ChartDataKeyType,
  TDependentKey extends ChartDataKeyType,
  TIndependentValue extends ChartDataValueType,
  TDependentValue extends ChartDataValueType,
>({
  fadeInactiveSeries = true,
  formatValue: globalFormatValue = String,
  independentValue,
  series,
}: ChartTooltipSeriesProps<TIndependentKey, TDependentKey, TIndependentValue, TDependentValue>) {
  const { activeSeries, config } = useChartContext();

  if (series.name == null) return null;
  if (series.type === "none") return null;

  const seriesConfig = config?.[series.name];

  const tooltip = seriesConfig?.showTooltip;
  if (tooltip === false) return null;
  if (typeof tooltip === "function" && independentValue != null && !tooltip(independentValue)) {
    return null;
  }

  const icon = seriesConfig?.icon;
  const color = seriesConfig?.color ?? series.color;
  const formatValue = seriesConfig?.formatValue ?? globalFormatValue;
  const otherSeriesActive = activeSeries != null && activeSeries !== series.name;
  const showSwatch = seriesConfig?.tooltipSwatch !== false;

  return (
    <div
      className={cn("flex w-full flex-wrap items-center gap-2", {
        "opacity-30": fadeInactiveSeries && otherSeriesActive,
      })}
    >
      {showSwatch &&
        (icon != null ? (
          <span className="text-wv-secondary">{icon}</span>
        ) : color != null ? (
          <ChartSeriesSwatch
            color={color}
            dashed={seriesConfig?.dashed}
            dotted={isDottedSeries(seriesConfig)}
            hatched={isHatchedBarSeries(seriesConfig)}
            line={seriesConfig?.type === ChartSeriesType.Line}
          />
        ) : null)}

      <div className="flex flex-1 items-center gap-4">
        <span className="text-wv-secondary">
          {seriesConfig?.labelNode ?? seriesConfig?.label ?? series.name}
        </span>

        <span
          className={cn(
            "ml-auto font-mono font-medium tabular-nums",
            series.value != null ? "text-wv-primary" : "text-wv-secondary",
          )}
        >
          {series.value != null ? formatValue(series.value) : "-"}
        </span>
      </div>
    </div>
  );
}
