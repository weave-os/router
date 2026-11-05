import { area, curveMonotoneX, line } from "d3-shape";

import { useId, useMemo } from "react";

import {
  DOTTED_STROKE_DASHARRAY,
  DOTTED_STROKE_LINECAP,
  FINE_DOT_TEXTURE_CLASS_NAME,
} from "../../../tools/chartColors";
import { cn } from "../../../tools/cn";

const PADDING = 2; // Pixels of padding around edges
const DOT_RADIUS = 2; // Radius of the end point dot
const VIEW_BOX_HEIGHT = 30;
const VIEW_BOX_WIDTH = 100;

export interface Point<Value extends number | null = number> {
  x: number;
  y: Value;
}

/** An inclusive range of x values, from the first point it covers to the last. */
export interface SparklineBand {
  end: number;
  start: number;
}

export interface SparklineProps {
  /** Ranges to shade behind the line, e.g. periods the series was in a different regime. */
  bands?: readonly SparklineBand[];
  className?: string;
  /** Null values leave gaps in the line and fill while retaining their time positions. */
  data: readonly Point<number | null>[];
  /** Draws a dotted, unlabeled line across the plot at this value, e.g. the period median. */
  median?: number;
  /** Puts 0 at the bottom so a low value reads as low, not as nothing. */
  startAtZero?: boolean;
}

/**
 * Renders a sparkline showing a trend of data over time.
 */
export function Sparkline({ bands, className, data, median, startAtZero = false }: SparklineProps) {
  const gradientId = useId();

  const { areaPath, bandRects, lastPoint, medianY, path, viewBox } = useMemo(() => {
    const scoredPoints = data.filter((point): point is Point => point.y != null);
    if (scoredPoints.length < 2) {
      return {
        areaPath: "",
        bandRects: [],
        lastPoint: null,
        medianY: null,
        path: "",
        viewBox: `0 0 ${VIEW_BOX_WIDTH} ${VIEW_BOX_HEIGHT}`,
      };
    }

    const innerWidth = VIEW_BOX_WIDTH - 2 * PADDING;
    const innerHeight = VIEW_BOX_HEIGHT - 2 * PADDING;

    let minX = data[0].x;
    let maxX = data[0].x;
    let minY = scoredPoints[0].y;
    let maxY = scoredPoints[0].y;

    for (let i = 0; i < data.length; i++) {
      const point = data[i];
      if (point.x < minX) minX = point.x;
      if (point.x > maxX) maxX = point.x;
      if (point.y == null) continue;
      if (point.y < minY) minY = point.y;
      if (point.y > maxY) maxY = point.y;
    }

    if (startAtZero) minY = Math.min(minY, 0);
    if (median != null) {
      minY = Math.min(minY, median);
      maxY = Math.max(maxY, median);
    }

    // Handle edge case where all values are the same
    if (minX === maxX) maxX = minX + 1;
    if (minY === maxY) maxY = minY + 1;

    // Create scaling functions that account for padding
    const xScale = (x: number) => PADDING + ((x - minX) / (maxX - minX)) * innerWidth;
    const yScale = (y: number) =>
      VIEW_BOX_HEIGHT - PADDING - ((y - minY) / (maxY - minY)) * innerHeight;

    // Generate the line path
    const lineGenerator = line<Point<number | null>>()
      .defined(point => point.y != null)
      .x(d => xScale(d.x))
      .y(d => yScale(d.y!))
      .curve(curveMonotoneX);

    // Generate the area path
    const areaGenerator = area<Point<number | null>>()
      .defined(point => point.y != null)
      .x(d => xScale(d.x))
      .y0(VIEW_BOX_HEIGHT - PADDING)
      .y1(d => yScale(d.y!))
      .curve(curveMonotoneX);

    // Get the coordinates of the last point
    const lastDataPoint = scoredPoints[scoredPoints.length - 1];
    const lastPoint = {
      x: xScale(lastDataPoint.x),
      y: yScale(lastDataPoint.y),
    };

    // A band reaches half a bucket past its first and last points so it lines up with the points
    // it covers. Series can skip empty buckets, so the bucket is the smallest gap between points,
    // not the average one.
    const sortedXs = data.map(point => point.x).sort((a, b) => a - b);
    const bucket = sortedXs.slice(1).reduce((smallest, x, index) => {
      const gap = x - sortedXs[index];
      return gap > 0 && gap < smallest ? gap : smallest;
    }, maxX - minX);
    const halfBucket = bucket / 2;
    const bandRects = (bands ?? []).flatMap(({ end, start }) => {
      const left = Math.max(xScale(start - halfBucket), 0);
      const right = Math.min(xScale(end + halfBucket), VIEW_BOX_WIDTH);
      return right > left ? [{ width: right - left, x: left }] : [];
    });

    return {
      areaPath: areaGenerator(data) ?? "",
      bandRects,
      lastPoint,
      medianY: median == null ? null : yScale(median),
      path: lineGenerator(data) ?? "",
      viewBox: `0 0 ${VIEW_BOX_WIDTH} ${VIEW_BOX_HEIGHT}`,
    };
  }, [bands, data, median, startAtZero]);

  if (lastPoint == null) return null;

  return (
    <div className={cn("relative h-full w-full", className)}>
      {/* HTML, not SVG: the stretched viewBox would squash the dots into dashes. */}
      {bandRects.map(({ width, x }) => (
        <div
          key={x}
          className={cn("absolute inset-y-0", FINE_DOT_TEXTURE_CLASS_NAME)}
          style={{
            "--color-bg": "currentColor",
            left: `${(x / VIEW_BOX_WIDTH) * 100}%`,
            width: `${(width / VIEW_BOX_WIDTH) * 100}%`,
          }}
        />
      ))}

      <svg
        width="100%"
        height="100%"
        viewBox={viewBox}
        preserveAspectRatio="none"
        className="relative block"
      >
        <defs>
          <linearGradient id={gradientId} x1="0" x2="0" y1="0" y2="1">
            <stop offset="0%" stopColor="currentColor" stopOpacity="0.2" />
            <stop offset="100%" stopColor="currentColor" stopOpacity="0" />
          </linearGradient>
        </defs>

        <path d={areaPath} fill={`url(#${gradientId})`} vectorEffect="non-scaling-stroke" />

        <path
          d={path}
          fill="none"
          stroke="currentColor"
          strokeWidth={1.5}
          vectorEffect="non-scaling-stroke"
        />

        {medianY != null && (
          <line
            stroke="currentColor"
            strokeDasharray={DOTTED_STROKE_DASHARRAY}
            strokeLinecap={DOTTED_STROKE_LINECAP}
            strokeOpacity={0.5}
            strokeWidth={1.5}
            vectorEffect="non-scaling-stroke"
            x1={0}
            x2={VIEW_BOX_WIDTH}
            y1={medianY}
            y2={medianY}
          />
        )}
      </svg>

      {lastPoint != null && (
        <div
          style={{
            backgroundColor: "currentColor",
            borderRadius: "50%",
            height: DOT_RADIUS * 2,
            left: `${(lastPoint.x / VIEW_BOX_WIDTH) * 100}%`,
            position: "absolute",
            top: `${(lastPoint.y / VIEW_BOX_HEIGHT) * 100}%`,
            transform: "translate(-50%, -50%)",
            width: DOT_RADIUS * 2,
          }}
        />
      )}
    </div>
  );
}
