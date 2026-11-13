import {
  DOT_TEXTURE_CLASS_NAME,
  DOTTED_STROKE_DASHARRAY,
  DOTTED_STROKE_LINECAP,
  HATCHED_TEXTURE_CLASS_NAME,
} from "../../tools/chartColors";
import { cn } from "../../tools/cn";
import { ChartDataValueType, ChartFillPattern, ChartSeriesConfig, ChartSeriesType } from "./types";

/** Whether a series fills its area with dots, so its swatch should be dotted to match. */
export function isDottedSeries(
  seriesConfig:
    | Pick<ChartSeriesConfig<ChartDataValueType, ChartDataValueType>, "fillPattern">
    | undefined,
) {
  return seriesConfig?.fillPattern === ChartFillPattern.Dots;
}

/**
 * Whether a series draws hatched bars, so its swatch should be hatched to match. Dashed lines and
 * areas keep a solid swatch.
 */
export function isHatchedBarSeries(
  seriesConfig:
    | Pick<ChartSeriesConfig<ChartDataValueType, ChartDataValueType>, "dashed" | "type">
    | undefined,
) {
  return (
    seriesConfig?.dashed === true &&
    (seriesConfig.type ?? ChartSeriesType.Bar) === ChartSeriesType.Bar
  );
}

export interface ChartSeriesSwatchProps {
  className?: string;
  color: string;
  dashed?: boolean;
  /** Pass `isDottedSeries` so the swatch matches a dotted area. */
  dotted?: boolean;
  hatched: boolean;
  line?: boolean;
}

/**
 * Pass `hatched` from `isHatchedBarSeries` so bar keys match their rendered fills, and `dotted` from
 * `isDottedSeries` for dotted fills; `line` selects a line sample and `dashed` mirrors its stroke
 * pattern.
 */
export function ChartSeriesSwatch({
  className,
  color,
  dashed = false,
  dotted = false,
  hatched,
  line = false,
}: ChartSeriesSwatchProps) {
  if (line) {
    return (
      <svg aria-hidden="true" className={cn("h-2.5 w-4 shrink-0", className)} viewBox="0 0 16 10">
        <line
          stroke={color}
          strokeDasharray={dashed ? DOTTED_STROKE_DASHARRAY : undefined}
          strokeLinecap={DOTTED_STROKE_LINECAP}
          strokeWidth="2"
          x1="0"
          x2="16"
          y1="5"
          y2="5"
        />
      </svg>
    );
  }

  return (
    <div
      className={cn(
        "size-2.5 shrink-0 rounded-sm",
        hatched ? HATCHED_TEXTURE_CLASS_NAME : dotted ? DOT_TEXTURE_CLASS_NAME : "bg-[--color-bg]",
        className,
      )}
      style={{ "--color-bg": color }}
    />
  );
}
