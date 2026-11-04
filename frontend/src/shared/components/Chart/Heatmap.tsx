"use client";

import { Tooltip } from "@/shared/components/molecules/Tooltip";
import { ChartColor, ChartContrastColor, ChartContrastColorToken } from "@/shared/tools/chartColors";
import { cn } from "@/shared/tools/cn";

import chroma from "chroma-js";

import { type CSSProperties, type ReactNode } from "react";

import { getSequentialIntensityColor } from "./helpers/getSequentialIntensityColor";

// A calm blue ramp by default; a full grid of solid orange cells reads as an alert.
const DEFAULT_HEATMAP_BASE_COLOR = ChartColor.Scale2;
const LEGEND_STEPS = [0, 0.25, 0.5, 0.75, 1];
// Compact cells fill the card's width; past this height they stretch into wide tiles instead of
// tall squares.
const COMPACT_CELL_MAX_HEIGHT_CLASS = "max-h-9";
const HEATMAP_FILL_CLASS =
  "bg-[color:var(--heatmap-fill)] dark:bg-[color:var(--heatmap-fill-dark)]";
const HEATMAP_INK_CLASS =
  "text-[color:var(--heatmap-ink)] dark:text-[color:var(--heatmap-ink-dark)]";

const HALFTONE_STEPS = 4;
const HALFTONE_SQUARE_SIZES_PX = [2, 3.2, 4.4, 5.2];
const HALFTONE_TILE_PX = 6;
// Keep the four texture levels, but spread their paint farther across the ramp so sparse and
// dense marks do not collapse into nearly the same shade.
const HALFTONE_COLOR_INTENSITIES = [0.02, 0.38, 0.74, 1];
// Paint the mask on a pseudo-element so it does not clip the cell's focus ring.
const HALFTONE_DOT_CLASS =
  "relative [--heatmap-dot:var(--heatmap-fill)] dark:[--heatmap-dot:var(--heatmap-fill-dark)] before:pointer-events-none before:absolute before:inset-0 before:bg-[color:var(--heatmap-dot)] before:[mask-image:var(--heatmap-mask)] before:[mask-repeat:round] before:[mask-size:var(--heatmap-mask-size)] before:content-['']";

export enum HeatmapTexture {
  /** One flat fill per cell, darker for larger values. */
  Solid = "solid",
  /**
   * Squares that grow and deepen with the value in four steps. Compact density only.
   */
  Halftone = "halftone",
}

export enum HeatmapDensity {
  /** Labelled cells showing each value. */
  Comfortable = "comfortable",
  /** Small unlabelled squares; the value shows in a tooltip on hover. */
  Compact = "compact",
}

function heatmapColor(intensity: number, baseColor: string) {
  return getSequentialIntensityColor(intensity, baseColor);
}

function contrastInk(backgroundColor: string) {
  return chroma.contrast(backgroundColor, ChartContrastColor.White) >= 4.5
    ? ChartContrastColorToken.White
    : ChartContrastColorToken.Black;
}

/**
 * Fill and text colors for an intensity, as CSS variables read by `HEATMAP_FILL_CLASS` and
 * `HEATMAP_INK_CLASS`. Low values should sink into the surface: the pale end does that on a light
 * surface, so dark mode runs the ramp the other way and the largest values are the brightest.
 */
function heatmapPaint(intensity: number, baseColor: string) {
  if (intensity === 0) {
    return {
      "--heatmap-fill": "var(--wv-bg-inset)",
      "--heatmap-fill-dark": "var(--wv-bg-inset)",
    } as CSSProperties;
  }
  const fill = heatmapColor(intensity, baseColor);
  const fillDark = heatmapColor(1 - intensity, baseColor);
  return {
    "--heatmap-fill": fill,
    "--heatmap-fill-dark": fillDark,
    "--heatmap-ink": contrastInk(fill),
    "--heatmap-ink-dark": contrastInk(fillDark),
  } as CSSProperties;
}

/** The class and paint for a filled cell or legend swatch at an intensity. */
function heatmapMark(intensity: number, texture: HeatmapTexture, baseColor: string) {
  if (texture === HeatmapTexture.Solid) {
    return { className: HEATMAP_FILL_CLASS, style: heatmapPaint(intensity, baseColor) };
  }
  const step = Math.ceil(intensity * HALFTONE_STEPS);
  // A day with no requests draws nothing; a missing day keeps its dashed outline elsewhere.
  if (step === 0) return { className: "", style: undefined };
  const paint = heatmapPaint(HALFTONE_COLOR_INTENSITIES[step - 1], baseColor);
  const squareSize = HALFTONE_SQUARE_SIZES_PX[step - 1];
  const squareOffset = (HALFTONE_TILE_PX - squareSize) / 2;
  const squareMask = `<svg xmlns="http://www.w3.org/2000/svg" width="${HALFTONE_TILE_PX}" height="${HALFTONE_TILE_PX}"><rect x="${squareOffset}" y="${squareOffset}" width="${squareSize}" height="${squareSize}"/></svg>`;
  return {
    className: HALFTONE_DOT_CLASS,
    style: {
      ...paint,
      // Match the original whole-tile repeat and spacing at each cell's edges.
      "--heatmap-mask": `url("data:image/svg+xml,${encodeURIComponent(squareMask)}")`,
      "--heatmap-mask-size": `${HALFTONE_TILE_PX}px ${HALFTONE_TILE_PX}px`,
    } satisfies CSSProperties,
  };
}

/**
 * A generic grid heatmap for the Chart library.
 *
 * Unlike `Chart.tsx` (which wraps recharts), this component renders a
 * CSS grid — a table of colored cells where intensity encodes a numeric
 * value. It exists because no shared heatmap exists in the codebase, and
 * several admin/eval pages need one.
 *
 * Cell coloring uses `getSequentialIntensityColor` so the ramp is a
 * consistent single-hue sequential scale (light to dark, no hue shift)
 * that works in both light and dark themes.
 */

interface HeatmapCellProps {
  baseColor: string;
  children?: ReactNode;
  density: HeatmapDensity;
  /** Whether the cell itself should receive focus instead of a clickable parent. */
  focusable: boolean;
  /**
   * Format the secondary value for display. Defaults to
   * `String(secondaryValue)`.
   */
  formatSecondary?: (v: number) => string;
  /**
   * Format the primary value for display. Defaults to `String(value)`.
   */
  formatValue?: (v: number) => string;
  /** What the cell stands for, e.g. a date. Heads the compact tooltip. */
  label: string;
  /**
   * The maximum value across the entire grid. Cells normalize against this
   * so the color scale is consistent across rows and columns.
   */
  maxValue: number;
  /**
   * Optional secondary value displayed in a smaller font below the main
   * value. Typically a count, percentage, or duration that contextualizes
   * the primary value.
   */
  secondaryValue?: number;
  texture: HeatmapTexture;
  /**
   * The primary numeric value. Cell background intensity is driven by
   * `value / maxValue` (clamped to [0, 1]). Null means no observation, which
   * is drawn as a dashed empty cell rather than as zero.
   */
  value: number | null;
}

function CompactHeatmapCell({
  baseColor,
  focusable,
  intensity,
  label,
  summary,
  texture,
}: {
  baseColor: string;
  focusable: boolean;
  /** Null for a cell with no observation. */
  intensity: number | null;
  label: string;
  summary: string;
  texture: HeatmapTexture;
}) {
  const mark = intensity == null ? null : heatmapMark(intensity, texture, baseColor);
  return (
    <Tooltip
      content={
        <span className="flex flex-col">
          <span className="text-wv-secondary">{label}</span>
          <span className="tabular-nums">{summary}</span>
        </span>
      }
      delayDuration={0}
      disableHoverableContent
      interactiveChild
    >
      <span
        aria-label={`${label}: ${summary}`}
        className={cn(
          "block aspect-square w-full rounded-sm hover:ring-2 hover:ring-wv-focus focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-wv-focus",
          COMPACT_CELL_MAX_HEIGHT_CLASS,
          mark == null ? "border border-dashed border-wv-default" : mark.className,
        )}
        role="img"
        style={mark?.style}
        tabIndex={focusable ? 0 : undefined}
      />
    </Tooltip>
  );
}

function HeatmapCell({
  baseColor,
  children,
  density,
  focusable,
  formatSecondary,
  formatValue,
  label,
  maxValue,
  secondaryValue,
  texture,
  value,
}: HeatmapCellProps) {
  if (density === HeatmapDensity.Compact) {
    if (value == null) {
      return (
        <CompactHeatmapCell
          baseColor={baseColor}
          focusable={focusable}
          intensity={null}
          label={label}
          summary="No data"
          texture={texture}
        />
      );
    }
    const intensity = maxValue <= 0 ? 0 : Math.min(1, Math.max(0, value / maxValue));
    const formattedValue = formatValue != null ? formatValue(value) : String(value);
    return (
      <CompactHeatmapCell
        baseColor={baseColor}
        focusable={focusable}
        intensity={intensity}
        label={label}
        texture={texture}
        summary={
          secondaryValue != null
            ? `${formattedValue} (${formatSecondary?.(secondaryValue) ?? secondaryValue})`
            : formattedValue
        }
      />
    );
  }

  if (value == null) {
    return (
      <span
        className="flex h-full min-h-9 flex-col items-center justify-center rounded-md border border-dashed border-wv-default p-1.5 text-center text-wv-secondary"
        title="No data"
      >
        <span className="text-body">—</span>
        <span className="sr-only">No data</span>
        {children}
      </span>
    );
  }

  // Clamp intensity to [0, 1] — negative values and values above max both
  // clamp to an endpoint so the color scale never extrapolates.
  const intensity = maxValue <= 0 ? 0 : Math.min(1, Math.max(0, value / maxValue));

  const formattedValue = formatValue != null ? formatValue(value) : String(value);
  const formattedSecondary =
    secondaryValue != null
      ? formatSecondary != null
        ? formatSecondary(secondaryValue)
        : String(secondaryValue)
      : null;

  return (
    <span
      className={cn(
        "flex h-full min-h-9 flex-col items-center justify-center rounded-md p-1.5 text-center",
        HEATMAP_FILL_CLASS,
        intensity === 0 ? "text-wv-secondary" : [HEATMAP_INK_CLASS, "shadow-wv-chart-mark-depth"],
      )}
      style={heatmapPaint(intensity, baseColor)}
      title={secondaryValue != null ? `${formattedValue} (${formattedSecondary})` : formattedValue}
    >
      <span className="text-body tabular-nums">{formattedValue}</span>
      {formattedSecondary != null && (
        <span className="text-body tabular-nums opacity-80">{formattedSecondary}</span>
      )}
      {children}
    </span>
  );
}

export interface HeatmapProps {
  /**
   * The ramp's hue as a hex color; only lightness varies across it. Defaults to the chart blue.
   */
  baseColor?: string;
  /**
   * Cell data, one array per row, matching `rows` order. Each inner array
   * must have exactly `columns.length` elements.
   */
  cells: { label?: string; secondaryValue?: number; value: number | null }[][];
  /**
   * Column header labels, one per column.
   */
  columns: string[];
  /**
   * Compact draws small unlabelled squares that fit the container width, with each value in a
   * hover tooltip. Defaults to comfortable.
   */
  density?: HeatmapDensity;
  /**
   * Format the secondary value. Defaults to `String(secondaryValue)`.
   */
  formatSecondary?: (v: number) => string;
  /**
   * Format the primary value. Defaults to `String(value)`.
   */
  formatValue?: (v: number) => string;
  /**
   * Optional legend text shown below the grid, e.g. "Cell color = solve rate".
   * Pass `null` to hide the legend entirely.
   */
  legendLabel?: string | null;
  /**
   * How compact cells draw their value. Comfortable cells are always solid, so their numbers stay
   * readable. Defaults to solid.
   */
  texture?: HeatmapTexture;
  /**
   * Maximum value for normalization. If omitted, computed as `max(cells)`.
   */
  maxValue?: number;
  onCellClick?: (rowIndex: number, columnIndex: number) => void;
  onColumnClick?: (columnIndex: number) => void;
  renderCellDetails?: (rowIndex: number, columnIndex: number) => ReactNode;
  renderColumnHeader?: (columnIndex: number) => ReactNode;
  /**
   * Row header column width in CSS pixels. Defaults to 80.
   */
  rowHeaderWidth?: number;
  /**
   * Row header labels, one per row.
   */
  rows: string[];
}

/**
 * A grid heatmap with row and column headers.
 *
 * The cell background is driven by `value / maxValue` through a
 * single-hue sequential color scale. Row and column headers are rendered
 * with a muted background.
 *
 * Usage:
 * ```tsx
 * <Heatmap
 *   columns={["opus", "sonnet", "fable"]}
 *   rows={["swebench", "terminalbench", "livecodebench"]}
 *   cells={[
 *     [{ value: 42.5, secondaryValue: 100 }, { value: 38.1 }, ...],
 *     ...
 *   ]}
 *   formatValue={v => `${v.toFixed(1)}%`}
 *   legendLabel="Cell color = solve rate"
 * />
 * ```
 */
export function Heatmap({
  baseColor = DEFAULT_HEATMAP_BASE_COLOR,
  cells,
  columns,
  density = HeatmapDensity.Comfortable,
  formatSecondary,
  formatValue,
  legendLabel,
  maxValue,
  onCellClick,
  onColumnClick,
  renderCellDetails,
  renderColumnHeader,
  rowHeaderWidth = 80,
  rows,
  texture: requestedTexture = HeatmapTexture.Solid,
}: HeatmapProps) {
  const computedMax = maxValue ?? Math.max(0, ...cells.flat().map(c => c.value ?? 0));
  const isCompact = density === HeatmapDensity.Compact;
  const texture = isCompact ? requestedTexture : HeatmapTexture.Solid;
  const hasMissingCells = isCompact && cells.some(row => row.some(cell => cell.value == null));

  return (
    <div className={cn(!isCompact && "overflow-x-auto")}>
      <div
        className={cn("grid gap-1", !isCompact && "min-w-max")}
        role="table"
        aria-label="Heatmap"
        style={{
          gridTemplateColumns: isCompact
            ? `${rowHeaderWidth}px repeat(${columns.length}, minmax(0, 1fr))`
            : `${rowHeaderWidth}px repeat(${columns.length}, 1fr)`,
        }}
      >
        {/* Header row — wrapped in role="row" so assistive tech sees the
            header hierarchy. `contents` keeps the CSS grid flat. */}
        <div className="contents" role="row">
          {/* Corner cell */}
          <div aria-hidden="true" />
          {columns.map((col, ci) => (
            <div
              key={ci}
              className={cn(
                "pb-1 text-body text-wv-secondary",
                // Compact columns are narrower than a label, so sparse labels overflow rightwards.
                isCompact ? "whitespace-nowrap" : "px-1 text-center",
              )}
              role="columnheader"
            >
              {onColumnClick != null ? (
                <button
                  type="button"
                  className="rounded p-1 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-wv-focus"
                  onClick={() => onColumnClick(ci)}
                >
                  <span className="sr-only">View </span>
                  {renderColumnHeader?.(ci) ?? col}
                </button>
              ) : (
                (renderColumnHeader?.(ci) ?? col)
              )}
            </div>
          ))}
        </div>
        {rows.map((row, ri) => (
          <div key={ri} className="contents" role="row">
            <div
              className="flex items-center break-words pr-2 text-body text-wv-secondary"
              role="rowheader"
            >
              {row}
            </div>
            {cells[ri].map((cell, ci) => {
              const cellContent = (
                <HeatmapCell
                  baseColor={baseColor}
                  density={density}
                  focusable={onCellClick == null}
                  texture={texture}
                  label={cell.label ?? `${row}, ${columns[ci]}`}
                  value={cell.value}
                  secondaryValue={cell.secondaryValue}
                  formatValue={formatValue}
                  formatSecondary={formatSecondary}
                  maxValue={computedMax}
                >
                  {renderCellDetails?.(ri, ci)}
                </HeatmapCell>
              );
              return (
                <div role="cell" key={ci}>
                  {onCellClick != null ? (
                    <button
                      type="button"
                      className="h-full w-full rounded-md focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-wv-focus"
                      onClick={() => onCellClick(ri, ci)}
                    >
                      <span className="sr-only">
                        {row}: {columns[ci]}{" "}
                      </span>
                      {cellContent}
                    </button>
                  ) : (
                    cellContent
                  )}
                </div>
              );
            })}
          </div>
        ))}
      </div>
      {legendLabel !== null && (
        <HeatmapLegend
          baseColor={baseColor}
          className="mt-3"
          label={legendLabel}
          showMissing={hasMissingCells}
          texture={texture}
        />
      )}
    </div>
  );
}

export interface HeatmapLegendProps {
  /** Match the heatmap's own `baseColor`. */
  baseColor?: string;
  className?: string;
  /** Text before the scale, e.g. "Requests per day". */
  label?: string;
  /** Adds a dashed "No data" key, for grids with missing cells. */
  showMissing?: boolean;
  /** Match the heatmap's own `texture`. */
  texture?: HeatmapTexture;
}

/**
 * The heatmap's "Less → More" key. The heatmap draws one under its grid; render this directly
 * (with the heatmap's `legendLabel={null}`) to place the key elsewhere, such as under a card title.
 */
export function HeatmapLegend({
  baseColor = DEFAULT_HEATMAP_BASE_COLOR,
  className,
  label,
  showMissing = false,
  texture = HeatmapTexture.Solid,
}: HeatmapLegendProps) {
  return (
    <div className={cn("flex flex-wrap items-center gap-2 text-body text-wv-secondary", className)}>
      {label != null && <span>{label}</span>}
      <span>Less</span>
      <span aria-hidden="true" className="flex items-center gap-0.5">
        {LEGEND_STEPS.filter(
          // An empty halftone swatch would read as a gap, not a level.
          step => texture === HeatmapTexture.Solid || step > 0,
        ).map(step => {
          const mark = heatmapMark(step, texture, baseColor);
          return (
            <span
              key={step}
              className={cn("size-3 rounded-sm", mark.className)}
              style={mark.style}
            />
          );
        })}
      </span>
      <span>More</span>
      {showMissing && (
        <span className="ml-2 flex items-center gap-1.5">
          <span
            aria-hidden="true"
            className="size-3 rounded-sm border border-dashed border-wv-default"
          />
          No data
        </span>
      )}
    </div>
  );
}
