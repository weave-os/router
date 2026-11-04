import { useLayoutEffect, useState } from "react";
import * as Recharts from "recharts";

import { cn } from "../../tools/cn";
import { Button } from "../molecules/Button";
import { Drawer } from "../molecules/Drawer";
import { Tooltip } from "../molecules/Tooltip";
import { Appearance } from "../types";
import { useChartContext } from "./ChartContext";
import { ChartSeriesSwatch, isDottedSeries, isHatchedBarSeries } from "./ChartSeriesSwatch";
import { ChartDataKeyType, ChartSeriesType } from "./types";

/** Maximum character length for legend labels before truncation */
const LEGEND_TEXT_MAX_LENGTH = 40;

/** Height of one legend row: the 20px line plus the item's vertical padding. */
const LEGEND_ROW_HEIGHT_PX = 24;

function truncateText(text: string, maxLength: number): string {
  if (text.length <= maxLength) return text;
  return text.slice(0, maxLength - 3) + "...";
}

export interface ChartLegendProps extends React.HTMLAttributes<HTMLDivElement> {
  payload?: ReadonlyArray<Recharts.LegendPayload>;
}

/** Fixed item count used where the legend can't be measured (no ResizeObserver, e.g. jsdom). */
const LEGEND_FALLBACK_VISIBLE_COUNT = 6;

/**
 * Renders a legend for the chart. Items beyond one row collapse into a "+N more" button
 * that opens the full list in a drawer, so the legend never changes the chart's height.
 */
export function ChartLegend({ className, onMouseLeave, payload }: ChartLegendProps) {
  const { hiddenSeries, hideHiddenSeriesFromLegend, setActiveSeries } = useChartContext();
  const [isListOpen, setIsListOpen] = useState(false);
  const [rowsElement, setRowsElement] = useState<HTMLDivElement | null>(null);
  const [visibleCount, setVisibleCount] = useState<number | null>(null);

  const handleMouseLeave = (event: React.MouseEvent<HTMLDivElement>) => {
    onMouseLeave?.(event);
    setActiveSeries(undefined);
  };

  const legendSeries = (payload ?? [])
    .filter(series => series.type !== "none")
    .filter(
      series =>
        series.value == null ||
        hideHiddenSeriesFromLegend !== true ||
        !(hiddenSeries?.has(series.value) ?? false),
    );

  const legendSeriesKey = legendSeries.map(series => String(series.value)).join("\u0000");

  // Every item stays rendered, so the measurement can grow back when the card widens; the ones
  // past the cutoff are only made invisible. Items are observed too, so a label that changes width
  // re-measures. The "+N more" button sits outside the rows, so its width is already excluded.
  useLayoutEffect(() => {
    if (rowsElement == null || typeof ResizeObserver === "undefined") return;
    const measure = () => {
      const items = Array.from(rowsElement.children) as HTMLElement[];
      const firstRowTop = items[0]?.offsetTop;
      setVisibleCount(items.filter(item => item.offsetTop === firstRowTop).length);
    };
    const observer = new ResizeObserver(measure);
    observer.observe(rowsElement);
    for (const item of rowsElement.children) observer.observe(item);
    measure();
    return () => observer.disconnect();
  }, [rowsElement, legendSeriesKey]);

  const shownCount = Math.max(
    0,
    visibleCount ?? Math.min(legendSeries.length, LEGEND_FALLBACK_VISIBLE_COUNT),
  );
  const hiddenCount = legendSeries.length - shownCount;

  return (
    <>
      <div
        className={cn("flex w-full items-start justify-start gap-x-1", className)}
        onMouseLeave={handleMouseLeave}
      >
        <div
          className="flex min-w-0 flex-1 flex-wrap items-center justify-start gap-x-1 overflow-hidden"
          ref={setRowsElement}
          style={{ maxHeight: `${LEGEND_ROW_HEIGHT_PX}px` }}
        >
          {legendSeries.map((series, index) => (
            <div
              key={`${series.value}-${series.type}`}
              className={cn("max-w-full", index >= shownCount && "invisible")}
            >
              <ChartLegendSeries series={series} />
            </div>
          ))}
        </div>
        {hiddenCount > 0 && (
          <Button
            appearance={Appearance.Tinted}
            className="shrink-0"
            onClick={() => setIsListOpen(true)}
            size="sm"
          >
            +{hiddenCount} more
          </Button>
        )}
      </div>
      <Drawer open={isListOpen} onOpenChange={setIsListOpen}>
        <Drawer.Content aria-describedby={undefined}>
          <Drawer.Toolbar closeLabel="Close legend" />
          <Drawer.Body>
            <Drawer.Header title="Series" />
            <div className="flex flex-col gap-1">
              {legendSeries.map(series => (
                <ChartLegendSeries key={`${series.value}-${series.type}`} series={series} />
              ))}
            </div>
          </Drawer.Body>
        </Drawer.Content>
      </Drawer>
    </>
  );
}

/** A single item in the payload Recharts passes to the legend. */
type RechartsLegendPayloadItem = Recharts.LegendPayload;

interface ChartLegendSeriesProps {
  series: Pick<RechartsLegendPayloadItem, "color" | "type" | "value">;
}

function ChartLegendSeries<TDependentKey extends ChartDataKeyType>({
  series,
}: ChartLegendSeriesProps) {
  const { activeSeries, config, hiddenSeries, setActiveSeries, toggleSeriesVisibility } =
    useChartContext();

  const seriesName = series.value as TDependentKey | undefined;
  if (seriesName == null) return null;

  const seriesConfig = config?.[seriesName];

  const color = seriesConfig?.color ?? series.color;
  const canToggleSeriesVisibility = toggleSeriesVisibility != null;
  const otherSeriesActive = activeSeries != null && activeSeries !== seriesName;
  const isHidden = hiddenSeries?.has(seriesName) ?? false;

  const setSeriesActive = (active: boolean) => {
    if (isHidden) return;
    if (active) {
      setActiveSeries(seriesName);
    } else {
      setActiveSeries(curr => (curr === seriesName ? undefined : curr));
    }
  };

  const handleClick = (e: React.MouseEvent) => {
    if (!canToggleSeriesVisibility) return;

    toggleSeriesVisibility(seriesName, e.metaKey || e.ctrlKey);
    // Clear the active (hovered) series so other items aren't left dimmed after clicking
    setActiveSeries(undefined);
  };

  const name = seriesConfig?.label ?? String(seriesName);
  const displayName = truncateText(name, LEGEND_TEXT_MAX_LENGTH);
  const tooltipContent = seriesConfig?.tooltipContent;

  const legendItem = (
    <div
      className={cn("flex max-w-full items-center gap-2 px-2 py-0.5", {
        "cursor-pointer": canToggleSeriesVisibility,
        "opacity-30": !isHidden && otherSeriesActive,
        "opacity-50": isHidden,
      })}
      onClick={canToggleSeriesVisibility ? handleClick : undefined}
      onMouseEnter={() => setSeriesActive(true)}
      onMouseLeave={() => setSeriesActive(false)}
    >
      {color != null && (
        <ChartSeriesSwatch
          color={color}
          dashed={seriesConfig?.dashed}
          dotted={isDottedSeries(seriesConfig)}
          hatched={isHatchedBarSeries(seriesConfig)}
          line={seriesConfig?.type === ChartSeriesType.Line}
        />
      )}

      <span className="truncate" title={name}>
        {seriesConfig?.labelNode ?? displayName}
      </span>
    </div>
  );

  if (tooltipContent != null) {
    return (
      <Tooltip content={tooltipContent} interactiveChild>
        {legendItem}
      </Tooltip>
    );
  }

  return legendItem;
}
