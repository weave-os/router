import React, { useCallback, useDeferredValue, useEffect, useMemo, useRef, useState } from "react";
import * as Recharts from "recharts";

import { cubicBezier, MotionCurve } from "../../design-system/motion";
import { useInView } from "../../hooks/useInView";
import {
  DOT_TEXTURE_RADIUS,
  DOT_TEXTURE_TILE,
  DOTTED_STROKE_DASHARRAY,
  DOTTED_STROKE_LINECAP,
} from "../../tools/chartColors";
import { cn } from "../../tools/cn";
import { LoadState } from "../../tools/LoadState";
import { groupBy } from "../../tools/map";
import { SafeOmit, unreachable } from "../../tools/types";
import { Tooltip } from "../molecules/Tooltip";
import { ChartContainer } from "./ChartContainer";
import { getChartContext } from "./ChartContext";
import { ChartLegend } from "./ChartLegend";
import { ChartMarkDepthFilters, getChartMarkDepthFilterId } from "./ChartMarkDepth";
import { ChartMessageCard } from "./ChartMessageCard";
import { ChartReferenceLineLabel } from "./ChartReferenceLineLabel";
import seriesMotionStyles from "./ChartSeriesMotion.module.css";
import { ChartTick } from "./ChartTick";
import { ChartTooltip } from "./ChartTooltip";
import { X_AXIS_TICK_MARGIN } from "./constants";
import { getDefaultSeriesColorToken } from "./helpers/getDefaultSeriesColor";
import { getRechartsDomain } from "./helpers/getRechartsDomain";
import {
  ChartAxisDomain,
  ChartAxisType,
  ChartBarWhisker,
  ChartConfig,
  ChartData,
  ChartDataKeyType,
  ChartDataValueType,
  ChartDependentAxis,
  ChartFillPattern,
  ChartPointAnnotation,
  ChartReferenceLine,
  ChartSeriesType,
} from "./types";
import { getChartMarkId, useRestoreChartMarkFocus } from "./useRestoreChartMarkFocus";
import {
  SERIES_ENTER_DURATION_MS,
  SERIES_EXIT_DURATION_MS,
  SeriesMotionPhase,
  useLineExitAnimation,
  useSeriesMotion,
} from "./useSeriesMotion";
import { useToggleSeriesVisibility } from "./useToggleSeriesVisibility";

// Left Y-axis numbers start at the axis edge, so they line up in one column with the 0 on the baseline.
const LEFT_ALIGNED_TICK = { textAnchor: "start" } as const;
const STROKE_WIDTH = 2;
// Clearance between a top legend and the plot, so bars and the top tick never touch the legend.
// Recharts centres the top Y tick label on the plot's top edge, so these offsets include the half of
// that label that rises above the plot. Both leave a 32px visible gap above the top tick label: from the
// legend when there is one, otherwise from the card header (which sits 24px above the chart).
const LEGEND_TO_PLOT_GAP_PX = 38;
const HEADER_TO_PLOT_GAP_PX = 16;
const AREA_GRADIENT_MAX_OPACITY = 0.2;
const Y_AXIS_TICK_COUNT = 5;
const MIN_BAR_HEIGHT_PX = 4;
// Matches the pie slice corner radius so bars and donuts read as one family.
const BAR_CORNER_RADIUS_PX = 4;
// Cut from the segment rather than stroked: a surface-coloured stroke would ring the whole bar and
// fight the highlight and lowlight on its edges.
const STACKED_BAR_SEAM_PX = 1;
// Extra Y-axis width reserved for the rotated dependentAxisLabel. The label is
// rendered inside the axis area ("insideLeft"), so without this reservation it
// would overlap the tick text; a "left" position would land outside the SVG
// canvas entirely (the chart has no left margin) and be clipped invisible.
const DEPENDENT_AXIS_LABEL_WIDTH_PX = 16;
// Fixed Y-axis gutter for fullBleed charts. Measuring the summed series values reserves far more
// space than the whole-number ticks need.
const FULL_BLEED_AXIS_WIDTH_PX = 40;
// Recharts' default 10% gap on each side of a category leaves bars at 80% of their slot.
const FULL_BLEED_BAR_CATEGORY_GAP = "4%";
// Custom category tick renderers opt out of Recharts' automatic collision handling. Charts can opt
// into skipping ticks once they have enough data points while retaining their custom renderer.
const DENSE_X_AXIS_DATA_POINT_THRESHOLD = 12;
/** A portaled legend leaves the chart container, so it restates the chart's caption text size. */
const PORTALED_LEGEND_CLASS = "flex flex-wrap items-center gap-x-1 text-caption";

/**
 * Custom bar shape that enforces a minimum pixel height for non-zero values.
 *
 * Recharts' built-in `minPointSize` doesn't work for stacked bars because it receives
 * the cumulative stack value, not the individual bar's contribution. A 0-value bar
 * stacked on a 1000-value bar gets value[1]=1000, so minPointSize treats it as non-zero.
 *
 * This shape computes the actual bar contribution (value[1] - value[0]) and only
 * enforces the minimum height when the contribution is genuinely > 0.
 */
interface MinHeightBarProps extends Recharts.RectangleProps {
  chartId: string;
  index?: number;
  stackEdges: StackEdges;
  /** `performance.now()` when the current enter/exit phase began (see useSeriesMotion). */
  phaseStartedAt?: number;
  /** Delay between consecutive bars in the domino sequence. */
  staggerStepMs?: number;
  value?: [number, number] | number;
  whisker?: ChartBarWhisker;
}

/**
 * Places a bar in the domino sequence: it starts after the bars before it and scales about the zero
 * line of its axis, so every segment of a stacked column falls together onto the baseline.
 * `elapsedMs` is how long the phase had already run when the bar mounted, so remounts skip ahead
 * instead of replaying.
 */
function getBarMotionStyle(
  { height = 0, index = 0, value, y = 0 }: MinHeightBarProps,
  staggerStepMs: number,
  elapsedMs: number,
): React.CSSProperties {
  const [startValue, endValue] = Array.isArray(value) ? value : [0, value ?? 0];
  // Recharts draws the bar from `endValue` at `y` to `startValue` at `y + height`, linearly.
  const zeroLineY =
    startValue === endValue ? y + height : y + (endValue * height) / (endValue - startValue);
  return {
    animationDelay: `${index * staggerStepMs - elapsedMs}ms`,
    transformBox: "view-box",
    transformOrigin: `0 ${zeroLineY}px`,
  };
}

interface YAxisDomainRecorderProps {
  allowDecimals: boolean;
  axis: ChartDependentAxis;
  /** Whether the axis uses Recharts' default `[0, "auto"]` domain rather than a caller's domain. */
  isAutoDomain: boolean;
  onDomainChange: (axis: ChartDependentAxis, domain: Recharts.NumberDomain) => void;
}

/** Reports a numeric Y axis's drawn domain so it can be held fixed while the series exit. */
function YAxisDomainRecorder({
  allowDecimals,
  axis,
  isAutoDomain,
  onDomainChange,
}: YAxisDomainRecorderProps) {
  const domain = Recharts.useYAxisDomain(axis);

  useEffect(() => {
    const [min, max] = domain ?? [];
    if (typeof min !== "number" || typeof max !== "number") return;
    if (!isAutoDomain) {
      onDomainChange(axis, [min, max]);
      return;
    }

    // The hook returns the data extent, but an "auto" axis draws out to its rounded ticks. Recharts
    // does not round an explicit domain, so freezing the extent alone would shrink the scale.
    const niceTicks = Recharts.getNiceTickValues([min, max], Y_AXIS_TICK_COUNT, allowDecimals);
    onDomainChange(axis, [
      Math.min(min, niceTicks[0]),
      Math.max(max, niceTicks[niceTicks.length - 1]),
    ]);
  }, [allowDecimals, axis, domain, isAutoDomain, onDomainChange]);

  return null;
}

/** Where a bar segment sits in its stack. A bar outside any stack is both its start and its end. */
interface StackEdges {
  isEnd: boolean;
  isStart: boolean;
}

const UNSTACKED_BAR_EDGES = { isEnd: true, isStart: true };

/**
 * Finds a bar's place in its stack at one data point. Recharts stacks positive and negative values
 * apart, so only segments on the same side of zero share the stack.
 */
function getStackEdges<TKey extends ChartDataKeyType>(
  key: TKey,
  stackKeys: readonly TKey[] | undefined,
  row: Record<string, unknown> | undefined,
): StackEdges {
  const value = row?.[String(key)];
  if (row == null || stackKeys == null || typeof value !== "number" || value === 0) {
    return UNSTACKED_BAR_EDGES;
  }

  const segmentKeys = stackKeys.filter(stackKey => {
    const segmentValue = row[String(stackKey)];
    return typeof segmentValue === "number" && Math.sign(segmentValue) === Math.sign(value);
  });
  return { isEnd: segmentKeys.at(-1) === key, isStart: segmentKeys[0] === key };
}

function MinHeightBar(props: MinHeightBarProps) {
  const {
    chartId,
    height = 0,
    phaseStartedAt,
    stackEdges,
    staggerStepMs,
    style,
    value,
    whisker,
    y = 0,
    ...rest
  } = props;

  // Recharts keys bars by position, so a layout shift mid-sequence remounts them. A bar mounted
  // after its phase began skips ahead by that much instead of replaying its domino step.
  const [mountedAt] = useState(() => performance.now());

  const motionStyle =
    phaseStartedAt != null && staggerStepMs != null
      ? getBarMotionStyle(props, staggerStepMs, Math.max(0, mountedAt - phaseStartedAt))
      : undefined;
  const mergedStyle = motionStyle != null ? { ...motionStyle, ...style } : style;

  const barContribution = Array.isArray(value)
    ? Math.abs(value[1] - value[0])
    : typeof value === "number"
      ? Math.abs(value)
      : 0;

  if (barContribution === 0) {
    return <Recharts.Rectangle {...rest} height={0} style={mergedStyle} y={y} />;
  }

  // Recharts draws from the end value at `y` to the start value at `y + height`, so `height` is
  // negative below zero and the first two corner radii always sit on the end side.
  const sign = height >= 0 ? 1 : -1;
  const startY = y + height;
  // Only extend the zero-adjacent segment. Growing later segments past their allocation makes
  // them overlap their neighbors, so their seam and fill stay within the original height.
  const seam = stackEdges.isStart ? 0 : Math.min(STACKED_BAR_SEAM_PX, Math.abs(height));
  const minimumLength = stackEdges.isStart ? MIN_BAR_HEIGHT_PX : 0;
  const length = Math.max(Math.abs(height) - seam, minimumLength);
  const endRadius = stackEdges.isEnd ? BAR_CORNER_RADIUS_PX : 0;
  const startRadius = stackEdges.isStart ? BAR_CORNER_RADIUS_PX : 0;
  // Light falls from above: only an upper edge no other segment covers is lit, and only an
  // uncovered lower edge is shaded.
  const depthFilterId = getChartMarkDepthFilterId(chartId, {
    isBottomShaded: sign > 0 ? stackEdges.isStart : stackEdges.isEnd,
    isTopLit: sign > 0 ? stackEdges.isEnd : stackEdges.isStart,
  });

  const rect = (
    <Recharts.Rectangle
      {...rest}
      filter={depthFilterId != null ? `url(#${depthFilterId})` : undefined}
      height={sign * length}
      radius={[endRadius, endRadius, startRadius, startRadius]}
      style={mergedStyle}
      y={startY - sign * (seam + length)}
    />
  );

  if (whisker == null || barContribution <= 0 || height <= 0) {
    return rect;
  }

  const scale = height / barContribution;
  const yLow = startY - Math.max(0, whisker.low) * scale;
  const yMedian = startY - Math.max(0, whisker.median) * scale;
  const yHigh = startY - Math.max(0, whisker.high) * scale;
  const barWidth = rest.width ?? 0;
  const cx = (rest.x ?? 0) + barWidth / 2;
  const medianHalfWidth = Math.min(9, Math.max(6, barWidth * 0.2));
  const whiskerColor = whisker.color ?? "var(--wv-text-primary)";
  const whiskerPath = `M ${cx} ${yHigh} V ${yLow} M ${cx - medianHalfWidth} ${yMedian} H ${cx + medianHalfWidth}`;

  return (
    <g>
      {rect}
      <g aria-hidden className="pointer-events-none" data-chart-distribution="" style={motionStyle}>
        <path
          d={whiskerPath}
          fill="none"
          stroke="var(--wv-bg-surface)"
          strokeLinecap="round"
          strokeLinejoin="round"
          strokeWidth={3.5}
        />
        <path
          d={whiskerPath}
          fill="none"
          stroke={whiskerColor}
          strokeLinecap="round"
          strokeLinejoin="round"
          strokeWidth={1.5}
        />
      </g>
    </g>
  );
}

export interface ChartProps<
  TIndependentKey extends ChartDataKeyType,
  TDependentKey extends ChartDataKeyType,
  TIndependentValue extends ChartDataValueType,
  TDependentValue extends ChartDataValueType,
  TTooltipData = unknown,
> extends SafeOmit<React.HTMLAttributes<HTMLDivElement>, "children"> {
  /**
   * The data for the chart. See the {@link ChartData} type for more.
   */
  data: LoadState<ChartData<TIndependentKey, TDependentKey, TIndependentValue, TDependentValue>>;
  /**
   * The keys in the chart data for the dependent values. The order of these keys determines the
   * order of the series in the chart.
   */
  dependentKeys: LoadState<readonly TDependentKey[]>;
  /**
   * The key in the chart data for the independent value. For example, in a time series chart, this
   * could be `"time"`.
   */
  independentKey: TIndependentKey;

  /**
   * Let a click anywhere in a category's column drill into that category, not
   * just a click that lands on the bar itself.
   *
   * Needed because a bar of value zero has no rectangle to hit, so the one
   * category whose answer is "none" is also the one you cannot open — and a
   * zero group still has rows behind it ("no bugs still open" is a list of
   * closed bugs). Also helps when there are too many categories for every tick
   * label to render.
   *
   * Only takes effect on charts with a single interactive series, since a
   * column-wide click cannot say which series was meant. Suppresses
   * `onClickEmpty` inside the plot area, which is why it is opt-in.
   *
   * @default false
   */
  allowCategoryClickDrillDown?: boolean;
  /**
   * Allow the ticks of the dependent axis to be decimals.
   *
   * @default false
   */
  allowDependentTickDecimals?: boolean;
  /**
   * Configuration for specific chart series.
   */
  config?: ChartConfig<TDependentKey, TIndependentValue, TDependentValue>;
  /**
   * A ref to pass to the container.
   */
  containerRef?: React.Ref<HTMLDivElement>;
  /**
   * Number of independent-axis ticks to skip when a custom category tick renderer is used on dense
   * data. Recharts' interval is zero-based, so `1` renders every other tick and `2` every third
   * tick. Time axes use Recharts' collision handling instead.
   */
  denseXAxisTickInterval?: number;
  /**
   * Label for the dependent (Y) axis.
   */
  dependentAxisLabel?: string;
  /**
   * The domain of the dependent axis. If not provided, the domain will be automatically calculated.
   */
  dependentDomain?: ChartAxisDomain<TDependentValue>;
  /**
   * The type of the dependent values.
   *
   * @default ChartAxisType.Number
   */
  dependentType?: ChartAxisType;
  /**
   * If provided, this data will be shown when the chart has no data. This can be useful to ensure
   * the chart doesn't jump around too much when data is loading.
   */
  emptyData?: ChartData<TIndependentKey, TDependentKey, TIndependentValue, TDependentValue>;
  /**
   * The message to show when the chart has no data.
   *
   * @default
   * <Card.Header className="text-center">
   *   <Card.Title>No data yet</Card.Title>
   * </Card.Header>
   */
  emptyDataMessage?: React.ReactNode;
  /**
   * Formatter for the independent values in the chart. For example, if the values are Unix
   * timestamps, this could be a function that formats them into a human-readable date string.
   *
   * @default String
   */
  formatIndependentValue?: (value: TIndependentValue) => string;
  /**
   * Formatter for the independent values in the chart, only used in the tooltip.
   * Can return a React node to display rich content like avatars.
   *
   * @default formatIndependentValue
   */
  formatIndependentValueTooltip?: (value: TIndependentValue) => React.ReactNode;
  /**
   * Formatter for the independent values in the tooltip that takes the active series into account.
   * If provided, this is used instead of formatIndependentValueTooltip.
   * Useful for showing different dates based on which series is being hovered.
   */
  formatIndependentValueTooltipForSeries?: (
    value: TIndependentValue,
    activeSeriesKey: ChartDataKeyType | null,
  ) => React.ReactNode;
  /**
   * Formatter for the dependent values in the chart. For example, if the values are in
   * milliseconds, this could be a function that formats them into a human-readable duration string.
   * To control how individual series are rendered in their tooltip, use the `formatValue` property
   * of the series config items.
   *
   * @default String
   */
  formatValue?: (value: TDependentValue) => string;
  /**
   * Formatter for the right Y-axis tick labels. When a chart uses dual axes
   * (left + right), the right axis may need different formatting (e.g. currency).
   *
   * @default formatValue
   */
  formatValueRight?: (value: TDependentValue) => string;
  /**
   * Formatter for the dependent values in the chart, only used in the tooltip. See {@link formatValue}.
   *
   * @default formatValue
   */
  formatValueTooltip?: (value: TDependentValue) => string;
  /**
   * Series that should be grouped together on the chart. For bar charts, grouped series will be
   * shown in a stacked bar. For line charts, there is no visual difference between grouped and
   * ungrouped series.
   */
  groups?: LoadState<readonly (readonly TDependentKey[])[]>;
  /**
   * The set of series hidden by legend clicks. If not provided, the chart will manage its own
   * state internally.
   */
  hiddenSeries?: ReadonlySet<TDependentKey>;
  /**
   * Hides the axes and ticks.
   *
   * @default false
   */
  hideAxes?: boolean;
  /**
   * Hides the left value axis and its gutter but keeps the time axis, for charts whose scale is
   * implied, such as shares that always fill 0–100%.
   *
   * @default false
   */
  hideDependentAxis?: boolean;
  /**
   * When true, a hidden series is removed from the legend entirely instead of
   * rendering dimmed. Only safe when another control (e.g. a series visibility
   * dropdown) remains available to re-show it — otherwise the legend's own
   * click-to-toggle is the only way back.
   *
   * @default false
   */
  hideHiddenSeriesFromLegend?: boolean;
  /**
   * Explicit pixel height for the independent (X) axis. Useful with a custom
   * {@link independentTick} that needs more vertical room than Recharts' default (e.g. to
   * wrap long labels onto multiple lines). When omitted, Recharts' default height is used.
   */
  independentAxisHeight?: number;
  /**
   * Label for the independent (X) axis.
   */
  independentAxisLabel?: string;
  /**
   * The domain of the independent axis. If not provided, the domain will be automatically calculated.
   */
  independentDomain?: ChartAxisDomain<TIndependentValue>;
  /**
   * A custom renderer for the independent axis ticks.
   */
  independentTick?: (value: TIndependentValue) => React.ReactNode;
  /**
   * Tick interval to use with a custom independent tick renderer. Category axes render every custom
   * tick by default; time axes let Recharts skip colliding labels by default.
   */
  independentTickInterval?: Recharts.XAxisProps["interval"];
  /**
   * The type of the independent values.
   *
   * @default ChartAxisType.Category
   */
  independentType?: ChartAxisType;
  /**
   * Whether to show the legend for the chart.
   *
   * @default false
   */
  legend?: boolean;
  /** External legend host; keeps the chart's series payload and interaction context. */
  legendPortal?: HTMLElement | null;
  /** Overrides the portaled legend's layout; only applies with {@link legendPortal}. */
  legendPortalClassName?: string;
  /**
   * Trims the plot to the content edges: a fixed-width left Y-axis gutter instead of one measured
   * from the summed series values, no reserved right Y-axis, narrower gaps between bars, and a
   * small outer margin for end dots.
   *
   * @default false
   */
  fullBleed?: boolean;
  /**
   * Reference lines to draw on the chart.
   */
  referenceLines?: LoadState<readonly ChartReferenceLine<TIndependentValue, TDependentValue>[]>;
  /**
   * If provided, used to render additional data in the tooltip.
   */
  renderTooltipData?: (
    data: TTooltipData,
    independentValue: TIndependentValue | null,
    activeSeriesKey: ChartDataKeyType | null,
  ) => React.ReactNode;
  /**
   * Style to apply to the chart.
   */
  renderDataPointTooltip?: (value: TIndependentValue, seriesKey: TDependentKey) => React.ReactNode;
  style?: React.CSSProperties;
  /**
   * Whether to show the tooltip.
   *
   * @default true
   */
  tooltip?: boolean;
  /**
   * Whether the tooltip should draw its independent-axis guide through the plot.
   *
   * @default true
   */
  tooltipCursor?: boolean;

  /**
   * The series that the user is currently hovering over, if any. If not provided, the chart will
   * manage its own state internally. If provided, {@link setActiveSeries} must also be provided.
   */
  activeSeries?: TDependentKey;
  /**
   * If provided, highlights the bar(s) with this independent value by dimming all other bars.
   * Similar to hover behavior but for a specific independent value.
   */
  highlightedIndependentValue?: TIndependentValue;
  /**
   * With {@link highlightedIndependentValue}, paints the other bars this color at full opacity
   * instead of fading them, so the highlighted bucket stands out against a neutral field.
   */
  mutedBarColor?: string;
  /** Labelled markers on individual data points, such as a bucket that breaks from the pattern. */
  annotations?: readonly ChartPointAnnotation<
    Extract<TIndependentValue, ChartDataKeyType>,
    TDependentKey
  >[];
  /**
   * If provided, this callback will be called when the user clicks on a data point in the chart
   * (a bar, line, or area).
   */
  onClickDataPoint?: (value: TIndependentValue, seriesKey: TDependentKey) => void;
  /** Restricts drilldown to actual marks instead of the nearest bucket in the plot. */
  pointDrillDownOnly?: boolean;
  /**
   * Reports the independent value of the bar currently hovered or keyboard-focused.
   * Clears with `undefined` when focus or the pointer leaves that bar.
   */
  onHoverIndependentValue?: (value: TIndependentValue | undefined) => void;
  /**
   * If provided, this callback will be called when the user clicks on an empty area of the chart
   * (not on a bar or line).
   */
  onClickEmpty?: () => void;
  /**
   * The setter for the active series. If not provided, the chart will manage its own state
   * internally. If provided, {@link activeSeries} must also be provided.
   */
  setActiveSeries?: React.Dispatch<React.SetStateAction<TDependentKey | undefined>>;
  /**
   * The setter for the hidden series. If not provided, the chart will manage its own state
   * internally. If provided, {@link hiddenSeries} must also be provided.
   */
  setHiddenSeries?: React.Dispatch<React.SetStateAction<ReadonlySet<TDependentKey>>>;
  /**
   * Optional function to sort the chart data. Called whenever the data or hidden series change,
   * which lets sort logic react to series visibility (e.g. excluding hidden series from totals).
   * Receives a mutable copy of the data array — the function may sort in place or return a new
   * array.
   */
  sortData?: (
    data: ChartData<TIndependentKey, TDependentKey, TIndependentValue, TDependentValue>,
    hiddenSeries: ReadonlySet<TDependentKey>,
  ) => ChartData<TIndependentKey, TDependentKey, TIndependentValue, TDependentValue>;
}

export function Chart<
  TIndependentKey extends ChartDataKeyType,
  TDependentKey extends ChartDataKeyType,
  TIndependentValue extends ChartDataValueType,
  TDependentValue extends ChartDataValueType,
  TTooltipData = unknown,
>({
  activeSeries: activeSeriesExternal,
  allowCategoryClickDrillDown = false,
  allowDependentTickDecimals = false,
  config,
  containerRef,
  data,
  denseXAxisTickInterval,
  dependentAxisLabel,
  dependentDomain,
  dependentKeys,
  dependentType = ChartAxisType.Number,
  emptyData = [],
  emptyDataMessage,
  formatIndependentValue = String,
  formatIndependentValueTooltip,
  formatIndependentValueTooltipForSeries,
  formatValue = String,
  formatValueRight,
  formatValueTooltip = formatValue,
  groups,
  hiddenSeries: hiddenSeriesExternal,
  hideAxes = false,
  hideDependentAxis = false,
  hideHiddenSeriesFromLegend = false,
  highlightedIndependentValue,
  mutedBarColor,
  annotations,
  independentAxisHeight,
  independentAxisLabel,
  independentDomain,
  independentKey,
  independentTick,
  independentTickInterval,
  independentType = ChartAxisType.Category,
  fullBleed = false,
  legend = false,
  legendPortal,
  legendPortalClassName = PORTALED_LEGEND_CLASS,
  onClickDataPoint,
  pointDrillDownOnly = false,
  onClickEmpty,
  onHoverIndependentValue,
  onMouseLeave,
  referenceLines,
  renderTooltipData,
  renderDataPointTooltip,
  setActiveSeries: setActiveSeriesExternal,
  setHiddenSeries: setHiddenSeriesExternal,
  sortData,
  style,
  tooltip = true,
  tooltipCursor = true,
  ...props
}: ChartProps<TIndependentKey, TDependentKey, TIndependentValue, TDependentValue, TTooltipData>) {
  const [activePointTooltipId, setActivePointTooltipId] = useState<string>();
  const chartId = React.useId().replaceAll(":", "");
  const [activeSeriesInternal, setActiveSeriesInternal] = useState<TDependentKey>();

  const activeSeries = activeSeriesExternal ?? activeSeriesInternal;
  const setActiveSeries = setActiveSeriesExternal ?? setActiveSeriesInternal;

  const { hiddenSeries, toggleSeriesVisibility } = useToggleSeriesVisibility({
    dependentKeys,
    hiddenSeriesExternal,
    setHiddenSeriesExternal,
  });

  // Recharts mounts, and the series enter, only once the chart is near the viewport: charts below
  // the fold neither pay for the first paint nor finish their entrance before anyone sees it. The
  // deferral keeps the mount off the critical path of the scroll that triggered it.
  const [inViewRef, isInView] = useInView();
  const isPlotMounted = useDeferredValue(isInView);
  const seriesMotion = useSeriesMotion(data, isPlotMounted);
  const isSeriesExiting = seriesMotion.phase === SeriesMotionPhase.Exiting;
  // The exit animates away from the rows that were loaded, even once a reload has replaced them.
  const displayedData = useMemo(
    () => (isSeriesExiting ? LoadState.loaded(seriesMotion.exitingRows) : data),
    [data, isSeriesExiting, seriesMotion.exitingRows],
  );

  // Apply the optional sort callback. Recomputing when hiddenSeries changes lets sort logic
  // react to users hiding series via the legend (e.g. so bars sorted by total stay sorted by the
  // total of only the visible series).
  const sortedData = useMemo(() => {
    if (sortData == null) return displayedData;
    return LoadState.map(displayedData, value => sortData([...value], hiddenSeries));
  }, [displayedData, hiddenSeries, sortData]);

  // An empty reload waits for the old series to finish exiting before the empty card replaces them.
  const isEmpty = !isSeriesExiting && LoadState.isLoaded(data) && data.value.length === 0;
  const ChartContext = getChartContext<TDependentKey, TIndependentValue, TDependentValue>();
  const groupIndices = useMemo(() => {
    const result = new Map<TDependentKey, number>();
    if (groups == null) return result;
    if (!LoadState.isReady(groups)) return result;

    const count = groups.value.length;
    for (const [i, group] of groups.value.entries()) {
      for (const key of group) {
        // We need to make sure that stacks in different layers are not grouped together - Recharts
        // can't handle it.
        const layer = config?.[key]?.layer ?? 0;
        result.set(key, count * layer + i);
      }
    }

    return result;
  }, [config, groups]);

  /** Maps each dependent key to its resolved color using the full unfiltered key list,
   *  so that hiding a series doesn't shift other series' colors. */
  const colorByKey = useMemo(() => {
    const map = new Map<TDependentKey, string>();
    if (!LoadState.isReady(dependentKeys)) return map;
    dependentKeys.value.forEach((key, i) => {
      map.set(key, config?.[key]?.color ?? getDefaultSeriesColorToken(i));
    });
    return map;
  }, [config, dependentKeys]);

  const areaGradientIdByKey = useMemo(() => {
    const gradientIds = new Map<TDependentKey, string>();
    if (!LoadState.isReady(dependentKeys)) return gradientIds;

    dependentKeys.value.forEach((key, index) => {
      if (config?.[key]?.fillGradient === true) {
        gradientIds.set(key, `${chartId}-area-gradient-${index}`);
      }
    });
    return gradientIds;
  }, [chartId, config, dependentKeys]);

  const dotPatternIdByKey = useMemo(() => {
    const patternIds = new Map<TDependentKey, string>();
    if (!LoadState.isReady(dependentKeys)) return patternIds;

    dependentKeys.value.forEach((key, index) => {
      if (config?.[key]?.fillPattern === ChartFillPattern.Dots) {
        patternIds.set(key, `${chartId}-dots-${index}`);
      }
    });
    return patternIds;
  }, [chartId, config, dependentKeys]);

  /** Orders dependent keys by layer. */
  const orderedDependentKeys = useMemo(
    () =>
      LoadState.map(dependentKeys, dependentKeys => {
        const byLayer = groupBy(dependentKeys, key => config?.[key]?.layer ?? 0);
        const layers = [...byLayer.keys()].sort((a, b) => b - a);
        const result: TDependentKey[][] = [];
        for (const layer of layers) {
          result.push(byLayer.get(layer) ?? []);
        }

        return result;
      }),
    [config, dependentKeys],
  );

  /** Maps each stacked bar series to the visible bar series in its stack, in drawing order. */
  const stackKeysByKey = useMemo(() => {
    const keyStacks = new Map<TDependentKey, TDependentKey[]>();
    const stacks = new Map<string, TDependentKey[]>();
    for (const key of LoadState.unwrap(orderedDependentKeys)?.flat() ?? []) {
      const keyConfig = config?.[key];
      const stackId = groups != null ? groupIndices.get(key) : keyConfig?.stackId;
      if (stackId == null || hiddenSeries.has(key)) continue;
      if ((keyConfig?.type ?? ChartSeriesType.Bar) !== ChartSeriesType.Bar) continue;

      // Recharts stacks per axis pair, so a shared stack ID on another layer or axis is a new stack.
      const stackKey = [
        stackId,
        keyConfig?.layer ?? 0,
        keyConfig?.dependentAxis ?? ChartDependentAxis.Left,
      ].join("|");
      const stackKeys = stacks.get(stackKey) ?? [];
      stackKeys.push(key);
      stacks.set(stackKey, stackKeys);
      keyStacks.set(key, stackKeys);
    }
    return keyStacks;
  }, [config, groupIndices, groups, hiddenSeries, orderedDependentKeys]);

  const isSeriesEntering = seriesMotion.phase === SeriesMotionPhase.Entering;
  const isAnimatingUpdates =
    seriesMotion.phase === SeriesMotionPhase.Shown && seriesMotion.isAnimationActive;
  // Line and Area type `animationEasing` as the CSS keywords only, but every series hands it to the
  // same Recharts easing parser as Bar, which also accepts cubic-bezier().
  const updateAnimationProps = {
    animationDuration: SERIES_ENTER_DURATION_MS,
    animationEasing: cubicBezier(MotionCurve.EaseOutExpo) as Recharts.LineProps["animationEasing"],
    isAnimationActive: isAnimatingUpdates,
  };
  // Lines draw at a constant rate across the whole domino so their tip keeps pace with the bars. They
  // stay still while exiting: a Recharts redraw would overwrite the erase's inherited dash pattern.
  const lineAnimationProps = isSeriesEntering
    ? {
        animationDuration: SERIES_ENTER_DURATION_MS + seriesMotion.staggerSpanMs,
        animationEasing: "linear" as const,
        isAnimationActive: true,
      }
    : updateAnimationProps;
  const areaAnimationProps = isSeriesExiting
    ? {
        animationDuration: SERIES_EXIT_DURATION_MS + seriesMotion.staggerSpanMs,
        animationEasing: cubicBezier(
          MotionCurve.EaseInOutCubic,
        ) as Recharts.AreaProps["animationEasing"],
        isAnimationActive: true,
      }
    : { ...updateAnimationProps, isAnimationActive: isSeriesEntering || isAnimatingUpdates };

  const areaKeys = useMemo(
    () =>
      LoadState.unwrap(dependentKeys, []).filter(
        key => config?.[key]?.type === ChartSeriesType.Area,
      ),
    [config, dependentKeys],
  );
  const isLoweringAreas = isSeriesExiting && areaKeys.length > 0;

  const plottedRows = useMemo(() => {
    const rows = LoadState.unwrap(sortedData, emptyData);
    if (!isLoweringAreas) return rows;

    // Zeroing area values lets Recharts' data animation lower them to the baseline, while the
    // frozen Y-axis domains keep the scale from collapsing along with them.
    return rows.map(row => ({
      ...row,
      ...Object.fromEntries(
        areaKeys.filter(key => typeof row[key] === "number").map(key => [key, 0]),
      ),
    }));
  }, [areaKeys, emptyData, isLoweringAreas, sortedData]);

  const [yAxisDomains, setYAxisDomains] = useState<
    Partial<Record<ChartDependentAxis, Recharts.NumberDomain>>
  >({});
  const recordYAxisDomain = useCallback(
    (axis: ChartDependentAxis, domain: Recharts.NumberDomain) =>
      setYAxisDomains(previous => {
        const [previousMin, previousMax] = previous[axis] ?? [];
        if (previousMin === domain[0] && previousMax === domain[1]) return previous;
        return { ...previous, [axis]: domain };
      }),
    [],
  );
  // Freezing only when areas lower: a domain change recomputes every bar, and Recharts remounts the
  // recomputed bars, which would restart their exit.
  const getYAxisDomain = (axis: ChartDependentAxis) =>
    (isLoweringAreas ? yAxisDomains[axis] : undefined) ?? getRechartsDomain(dependentDomain);

  const hasCustomTick = independentTick != null;
  const customTickInterval =
    independentType === ChartAxisType.Category
      ? denseXAxisTickInterval != null &&
        LoadState.unwrap(sortedData, emptyData).length >= DENSE_X_AXIS_DATA_POINT_THRESHOLD
        ? denseXAxisTickInterval
        : 0
      : undefined;

  const maxAbsDataValueByAxis = useMemo((): Record<ChartDependentAxis, number> => {
    const maxValues = {
      [ChartDependentAxis.Left]: 0,
      [ChartDependentAxis.Right]: 0,
    };
    const unwrappedData = LoadState.unwrap(displayedData);
    const unwrappedKeys = LoadState.unwrap(dependentKeys);
    if (unwrappedData == null || unwrappedData.length === 0) return maxValues;
    if (unwrappedKeys == null || unwrappedKeys.length === 0) return maxValues;

    for (const point of unwrappedData) {
      const positiveSums = {
        [ChartDependentAxis.Left]: 0,
        [ChartDependentAxis.Right]: 0,
      };
      const negativeSums = {
        [ChartDependentAxis.Left]: 0,
        [ChartDependentAxis.Right]: 0,
      };

      for (const key of unwrappedKeys) {
        const value = (point as Record<string, unknown>)[key as string];
        if (typeof value === "number" && !isNaN(value)) {
          const axis = config?.[key]?.dependentAxis ?? ChartDependentAxis.Left;
          if (value >= 0) positiveSums[axis] += value;
          else negativeSums[axis] += Math.abs(value);
        }
      }

      maxValues[ChartDependentAxis.Left] = Math.max(
        maxValues[ChartDependentAxis.Left],
        positiveSums[ChartDependentAxis.Left],
        negativeSums[ChartDependentAxis.Left],
      );
      maxValues[ChartDependentAxis.Right] = Math.max(
        maxValues[ChartDependentAxis.Right],
        positiveSums[ChartDependentAxis.Right],
        negativeSums[ChartDependentAxis.Right],
      );
    }
    return maxValues;
  }, [config, dependentKeys, displayedData]);

  const computeAxisWidth = useCallback(
    (formatter: (v: TDependentValue) => string, maxAbsDataValue: number): number => {
      if (hideAxes) return 0;
      if (maxAbsDataValue === 0) return 40;

      // Size the gutter to the tick labels Recharts draws, not the raw maximum, which formats
      // with more digits than the rounded ticks.
      const ticks = Recharts.getNiceTickValues(
        [0, maxAbsDataValue],
        Y_AXIS_TICK_COUNT,
        allowDependentTickDecimals,
      );
      const maxLen = Math.max(
        ...ticks.flatMap(tick => [
          formatter(tick as TDependentValue).length,
          formatter(-tick as TDependentValue).length,
        ]),
      );

      const CHAR_WIDTH_PX = 7;
      const PADDING_PX = 12;
      return maxLen * CHAR_WIDTH_PX + PADDING_PX;
    },
    [allowDependentTickDecimals, hideAxes],
  );

  const dependentAxisWidth = useMemo(() => {
    if (hideDependentAxis) return 0;
    if (fullBleed && !hideAxes) return FULL_BLEED_AXIS_WIDTH_PX;
    return (
      computeAxisWidth(formatValue, maxAbsDataValueByAxis[ChartDependentAxis.Left]) +
      (dependentAxisLabel != null && !hideAxes ? DEPENDENT_AXIS_LABEL_WIDTH_PX : 0)
    );
  }, [
    computeAxisWidth,
    dependentAxisLabel,
    fullBleed,
    formatValue,
    hideAxes,
    hideDependentAxis,
    maxAbsDataValueByAxis,
  ]);

  // Callers that pass independentDomain already pad it, so extra padding would stretch the baseline.
  const independentAxisPadding: Recharts.XAxisProps["padding"] =
    independentType !== ChartAxisType.Time
      ? "gap"
      : independentDomain == null
        ? { left: 20, right: 20 }
        : { left: 0, right: 0 };

  const usesRightAxis = (LoadState.unwrap(dependentKeys) ?? []).some(
    key => config?.[key]?.dependentAxis === ChartDependentAxis.Right,
  );

  const dependentAxisWidthRight = useMemo(
    () =>
      computeAxisWidth(
        formatValueRight ?? formatValue,
        maxAbsDataValueByAxis[ChartDependentAxis.Right],
      ),
    [computeAxisWidth, formatValue, formatValueRight, maxAbsDataValueByAxis],
  );

  // Track whether a data point (bar/line) was just clicked in this event cycle.
  // Used to prevent the chart-level onClick from firing onClickEmpty incorrectly.
  const dataPointClickedRef = useRef(false);

  // Clear hover state when leaving the chart; recharts v3 can miss per-series leave events.
  const handleChartMouseLeave = useCallback(
    (event: React.MouseEvent<HTMLDivElement>) => {
      onMouseLeave?.(event);
      setActiveSeries(undefined);
      onHoverIndependentValue?.(undefined);
    },
    [onHoverIndependentValue, onMouseLeave, setActiveSeries],
  );

  const { hasInteractiveBarSeries, interactiveVisibleSeriesKeys } = useMemo(() => {
    const keys = LoadState.unwrap(orderedDependentKeys)?.flat() ?? [];
    const interactiveVisible: TDependentKey[] = [];
    let hasInteractiveBar = false;

    if (onClickDataPoint == null) {
      return { hasInteractiveBarSeries: false, interactiveVisibleSeriesKeys: interactiveVisible };
    }

    for (const key of keys) {
      if (hiddenSeries.has(key)) continue;

      const keyConfig = config?.[key];
      const seriesInteractive = keyConfig?.interactive ?? true;
      if (!seriesInteractive) continue;

      const type = keyConfig?.type ?? ChartSeriesType.Bar;
      interactiveVisible.push(key);
      if (type === ChartSeriesType.Bar) {
        hasInteractiveBar = true;
      }
    }

    return {
      hasInteractiveBarSeries: hasInteractiveBar,
      interactiveVisibleSeriesKeys: interactiveVisible,
    };
  }, [config, hiddenSeries, onClickDataPoint, orderedDependentKeys]);

  const handleSeriesDotClick = useCallback(
    (
      payload: Record<string, unknown> | null | undefined,
      seriesKey: TDependentKey,
      suppressBubbledChartClick = true,
    ) => {
      if (onClickDataPoint == null) return;
      if (payload == null || typeof payload !== "object") return;

      const independentValue = (payload as Record<TIndependentKey, TIndependentValue | undefined>)[
        independentKey
      ];
      if (independentValue == null) return;

      if (suppressBubbledChartClick) {
        dataPointClickedRef.current = true;
      }
      onClickDataPoint(independentValue, seriesKey);
    },
    [independentKey, onClickDataPoint],
  );

  // Whole-plot drill-down needs a single interactive series: with several we can't pick a
  // series key, and consumers (e.g. OutputBreakdownChart) require one — those rely on the
  // per-point dot click targets instead.
  //
  // Bar charts are excluded by default because their bars are already click targets, and
  // taking over the whole plot would swallow onClickEmpty. `allowCategoryClickDrillDown`
  // opts a bar chart in anyway, for the case the bars can't cover: a zero-valued bar has no
  // rectangle to hit.
  const wholePlotDrillDownKey =
    onClickDataPoint != null &&
    !pointDrillDownOnly &&
    (!hasInteractiveBarSeries || allowCategoryClickDrillDown) &&
    interactiveVisibleSeriesKeys.length === 1
      ? interactiveVisibleSeriesKeys[0]
      : undefined;

  const handleChartClick = useCallback(
    (state: unknown) => {
      // A data-point click (bar/line/area dot) already fired its own handler this event cycle;
      // skip so we don't double-handle or fire onClickEmpty.
      if (dataPointClickedRef.current) {
        dataPointClickedRef.current = false;
        return;
      }

      if (onClickDataPoint != null && wholePlotDrillDownKey != null) {
        const chartState = state as { activeTooltipIndex?: number };

        if (chartState.activeTooltipIndex != null) {
          const chartData = LoadState.unwrap(sortedData, emptyData);
          const payload = chartData[chartState.activeTooltipIndex];
          const independentValue = payload?.[independentKey];
          if (independentValue != null) {
            // No need to set dataPointClickedRef here: this handler is the chart-level onClick
            // itself, so nothing else runs after it in this event.
            onClickDataPoint(independentValue, wholePlotDrillDownKey);
            return;
          }
        }
      }

      if (onClickEmpty != null) {
        onClickEmpty();
      }
    },
    [emptyData, independentKey, onClickDataPoint, onClickEmpty, sortedData, wholePlotDrillDownKey],
  );

  // Measure the container before mounting Recharts' ResponsiveContainer. Rendering it with an
  // unmeasured (0/-1) size makes Recharts log a "width(-1) and height(-1)" warning on every server
  // render and initial client paint. By waiting for a positive measurement and seeding it via
  // `initialDimension`, the ResponsiveContainer's first render is already valid — no warning and no
  // wrong-size flash. Recharts' own ResizeObserver takes over for subsequent resizes.
  const internalContainerRef = useRef<HTMLDivElement | null>(null);
  useLineExitAnimation({
    containerRef: internalContainerRef,
    dashedLineClassName: seriesMotionStyles.dashedLine,
    phase: seriesMotion.phase,
    staggerSpanMs: seriesMotion.staggerSpanMs,
  });
  const [containerSize, setContainerSize] = useState<{ height: number; width: number } | null>(
    null,
  );
  const { handleBlur: handleMarkBlur, handleFocus: handleMarkFocus } =
    useRestoreChartMarkFocus(internalContainerRef);

  const setContainerRef = useCallback(
    (node: HTMLDivElement | null) => {
      internalContainerRef.current = node;
      inViewRef(node);
      if (typeof containerRef === "function") {
        containerRef(node);
      } else if (containerRef != null) {
        // oxlint-disable-next-line react/immutability -- existing warning baseline
        containerRef.current = node;
      }
    },
    [containerRef, inViewRef],
  );

  useEffect(() => {
    const node = internalContainerRef.current;
    if (node == null || typeof ResizeObserver === "undefined") return;

    const observer = new ResizeObserver(entries => {
      const entry = entries[0];
      if (entry == null) return;
      const { height, width } = entry.contentRect;
      if (width > 0 && height > 0) {
        setContainerSize({ height: Math.round(height), width: Math.round(width) });
        // We only need the first valid measurement to seed ResponsiveContainer, which then manages
        // its own resize observation.
        observer.disconnect();
      }
    });
    observer.observe(node);

    return () => observer.disconnect();
  }, []);

  // Element-scoped mouseleave is unreliable for clearing the hover highlight: recharts v3
  // re-renders on hover (swapping opacities, the active-bar layer, and the tooltip portal), and
  // a browser does NOT fire mouseleave on an element that is removed/replaced under the pointer.
  // Fast movement across SVG clip-path boundaries and exits through the tooltip portal drop the
  // event too. As a safety net, while a series is active we listen for the pointer entering any
  // element outside this chart and clear then.
  //
  // Cost is bounded: the listener only exists while a series is active, and only one chart can be
  // hovered at a time — so at most one listener is attached page-wide regardless of chart count.
  // We use `pointerover` (fires only when the pointer crosses into a new element, not on every
  // move) and `node.contains` (a reflow-free tree check that correctly treats the tooltip portal,
  // a DOM child of the container, as "inside").
  useEffect(() => {
    if (activeSeries == null) return;
    if (typeof document === "undefined") return;

    const handlePointerOver = (event: PointerEvent) => {
      const node = internalContainerRef.current;
      if (node == null) return;

      const target = event.target;
      // A portaled legend lives outside the container but still drives the active series.
      if (
        target instanceof Node &&
        (node.contains(target) || legendPortal?.contains(target) === true)
      ) {
        return;
      }

      setActiveSeries(undefined);
    };

    document.addEventListener("pointerover", handlePointerOver);
    return () => document.removeEventListener("pointerover", handlePointerOver);
  }, [activeSeries, legendPortal, setActiveSeries]);

  return (
    <ChartContext.Provider
      value={{
        activeSeries,
        config,
        hiddenSeries,
        hideHiddenSeriesFromLegend,
        setActiveSeries,
        toggleSeriesVisibility,
      }}
    >
      <ChartContainer
        ref={setContainerRef}
        data-series-motion={seriesMotion.phase}
        onMouseLeave={handleChartMouseLeave}
        {...props}
        className={cn(seriesMotionStyles.chart, props.className)}
        onBlur={event => {
          handleMarkBlur(event);
          props.onBlur?.(event);
        }}
        onFocus={event => {
          handleMarkFocus(event);
          props.onFocus?.(event);
        }}
        style={{
          "--series-sweep-duration": `${
            (isSeriesExiting ? SERIES_EXIT_DURATION_MS : SERIES_ENTER_DURATION_MS) +
            seriesMotion.staggerSpanMs
          }ms`,
        }}
      >
        {containerSize != null && isPlotMounted && (
          <Recharts.ResponsiveContainer debounce={200} initialDimension={containerSize}>
            <Recharts.ComposedChart
              accessibilityLayer
              data={isEmpty ? emptyData : plottedRows}
              margin={
                fullBleed
                  ? { bottom: 0, left: 0, right: 8, top: 8 }
                  : { top: legend ? 0 : HEADER_TO_PLOT_GAP_PX }
              }
              barCategoryGap={fullBleed ? FULL_BLEED_BAR_CATEGORY_GAP : undefined}
              style={{
                ...style,
                cursor: wholePlotDrillDownKey != null ? "pointer" : style?.cursor,
              }}
              stackOffset="sign"
              onClick={
                onClickDataPoint != null || onClickEmpty != null ? handleChartClick : undefined
              }
            >
              <defs>
                {/* Black/white are luminance-mask inputs; theming them would change stripe opacity. */}
                <pattern
                  id="diagonalLines"
                  width="10"
                  height="10"
                  patternTransform="rotate(130 0 0) scale(0.8)"
                  patternUnits="userSpaceOnUse"
                >
                  <rect x="0" y="0" width="10" height="10" style={{ fill: "white" }} />
                  <line
                    x1="0"
                    y1="0"
                    x2="0"
                    y2="10"
                    style={{ opacity: 0.6, stroke: "black", strokeWidth: 10 }}
                  />
                </pattern>

                <mask id="diagonalLinesMask" x="0" y="0" width="1" height="1">
                  <rect x="0" y="0" width="4096" height="4096" fill="url(#diagonalLines)" />
                </mask>

                <ChartMarkDepthFilters scopeId={chartId} />

                {[...dotPatternIdByKey.entries()].map(([key, patternId]) => (
                  <pattern
                    key={patternId}
                    id={patternId}
                    width={DOT_TEXTURE_TILE}
                    height={DOT_TEXTURE_TILE}
                    patternUnits="userSpaceOnUse"
                  >
                    {/* Full-strength dots overpower the plot on a dark surface. */}
                    <circle
                      cx="1"
                      cy="1"
                      r={DOT_TEXTURE_RADIUS}
                      className="[fill-opacity:0.85] dark:[fill-opacity:0.45]"
                      fill={colorByKey.get(key)}
                    />
                  </pattern>
                ))}

                {[...areaGradientIdByKey.entries()].map(([key, gradientId]) => {
                  const color = colorByKey.get(key);
                  const fillOpacity = config?.[key]?.fillOpacity ?? AREA_GRADIENT_MAX_OPACITY;
                  const topOpacity = Math.min(AREA_GRADIENT_MAX_OPACITY, fillOpacity * 2);
                  return (
                    <linearGradient key={gradientId} id={gradientId} x1="0" x2="0" y1="0" y2="1">
                      <stop offset="0%" stopColor={color} stopOpacity={topOpacity} />
                      <stop offset="65%" stopColor={color} stopOpacity={fillOpacity} />
                      <stop offset="100%" stopColor={color} stopOpacity="0" />
                    </linearGradient>
                  );
                })}
              </defs>

              <Recharts.XAxis
                xAxisId={0}
                dataKey={independentKey}
                type={independentType === ChartAxisType.Category ? "category" : "number"}
                scale={independentType === ChartAxisType.Time ? "time" : "auto"}
                domain={getRechartsDomain(independentDomain)}
                minTickGap={20}
                tick={
                  hideAxes ? (
                    false
                  ) : hasCustomTick ? (
                    <ChartTick renderTick={independentTick} />
                  ) : undefined
                }
                axisLine={hideAxes ? false : undefined}
                height={hideAxes ? 0 : independentAxisHeight}
                interval={
                  !hasCustomTick ? undefined : (independentTickInterval ?? customTickInterval)
                }
                tickMargin={X_AXIS_TICK_MARGIN}
                tickLine={false}
                // Recharts offsets ticks by the tick-line length even with the line hidden; ChartTick
                // sizes its box to the axis height less the margin, so that offset would push it past
                // the bottom edge (clipping avatars on fullBleed charts).
                tickSize={hasCustomTick ? 0 : undefined}
                tickFormatter={formatIndependentValue}
                padding={independentAxisPadding}
                label={
                  independentAxisLabel != null && !hideAxes
                    ? { offset: 5, position: "bottom", value: independentAxisLabel }
                    : undefined
                }
              />

              {/* This X axis is not rendered, but it allows us to support the `layer` series configuration.
                Used for projected values that overlay on top of the main bars. */}
              <Recharts.XAxis
                xAxisId={1}
                dataKey={independentKey}
                type={independentType === ChartAxisType.Category ? "category" : "number"}
                scale={independentType === ChartAxisType.Time ? "time" : "auto"}
                domain={getRechartsDomain(independentDomain)}
                minTickGap={20}
                tick={false}
                axisLine={false}
                height={0}
                tickMargin={X_AXIS_TICK_MARGIN}
                tickLine={false}
                padding={independentAxisPadding}
              />

              <Recharts.YAxis
                yAxisId={ChartDependentAxis.Left}
                orientation="left"
                tickLine={false}
                type={dependentType === ChartAxisType.Category ? "category" : "number"}
                scale={dependentType === ChartAxisType.Time ? "time" : "auto"}
                tickFormatter={formatValue}
                tick={hideAxes || hideDependentAxis ? false : LEFT_ALIGNED_TICK}
                tickSize={0}
                tickMargin={
                  dependentAxisWidth -
                  (dependentAxisLabel != null && !hideAxes ? DEPENDENT_AXIS_LABEL_WIDTH_PX : 0)
                }
                axisLine={false}
                width={dependentAxisWidth}
                allowDecimals={allowDependentTickDecimals}
                tickCount={Y_AXIS_TICK_COUNT}
                domain={getYAxisDomain(ChartDependentAxis.Left)}
                label={
                  dependentAxisLabel != null && !hideAxes
                    ? {
                        angle: -90,
                        offset: DEPENDENT_AXIS_LABEL_WIDTH_PX / 2,
                        position: "insideLeft",
                        textAnchor: "middle",
                        value: dependentAxisLabel,
                      }
                    : undefined
                }
              />

              <Recharts.YAxis
                yAxisId={ChartDependentAxis.Right}
                orientation="right"
                hide={fullBleed || !usesRightAxis}
                tickLine={false}
                type={dependentType === ChartAxisType.Category ? "category" : "number"}
                scale={dependentType === ChartAxisType.Time ? "time" : "auto"}
                tickFormatter={formatValueRight ?? formatValue}
                tick={hideAxes ? false : undefined}
                axisLine={false}
                width={dependentAxisWidthRight}
                allowDecimals={allowDependentTickDecimals}
                tickCount={Y_AXIS_TICK_COUNT}
                domain={getYAxisDomain(ChartDependentAxis.Right)}
              />

              <Recharts.CartesianGrid
                vertical={false}
                horizontal={({ x1, x2, y1, y2, y, stroke, strokeWidth }) =>
                  y1 === y ? (
                    <g />
                  ) : (
                    <line
                      x1={x1}
                      x2={x2}
                      y1={y1}
                      y2={y2}
                      stroke={stroke}
                      strokeWidth={strokeWidth}
                      fill="none"
                    />
                  )
                }
              />

              {dependentType !== ChartAxisType.Category && (
                <>
                  <YAxisDomainRecorder
                    allowDecimals={allowDependentTickDecimals}
                    axis={ChartDependentAxis.Left}
                    isAutoDomain={dependentDomain == null}
                    onDomainChange={recordYAxisDomain}
                  />
                  <YAxisDomainRecorder
                    allowDecimals={allowDependentTickDecimals}
                    axis={ChartDependentAxis.Right}
                    isAutoDomain={dependentDomain == null}
                    onDomainChange={recordYAxisDomain}
                  />
                </>
              )}

              {seriesMotion.phase !== SeriesMotionPhase.Hidden &&
                LoadState.unwrap(orderedDependentKeys)?.flatMap(keys =>
                  keys.map((key, i) => {
                    const keyConfig = config?.[key];
                    const areaGradientId = areaGradientIdByKey.get(key);
                    const dotPatternId = dotPatternIdByKey.get(key);

                    const type = keyConfig?.type ?? ChartSeriesType.Bar;

                    const color =
                      colorByKey.get(key) ?? keyConfig?.color ?? getDefaultSeriesColorToken(i);
                    const opacity = keyConfig?.opacity ?? 1;
                    const inactiveOpacity = keyConfig?.inactiveOpacity ?? 0.3 * opacity;
                    const dashed = keyConfig?.dashed ?? false;
                    const dot = keyConfig?.dot ?? true;
                    const legend = keyConfig?.legend ?? true;
                    const seriesInteractive = keyConfig?.interactive ?? true;
                    const layer = keyConfig?.layer ?? 0;
                    const dependentAxis = keyConfig?.dependentAxis ?? ChartDependentAxis.Left;
                    const interactive = onClickDataPoint != null && seriesInteractive;
                    const connectNulls = keyConfig?.connectNulls ?? false;

                    const isHidden = hiddenSeries.has(key);
                    const otherSeriesActive = activeSeries != null && activeSeries !== key;

                    const setSeriesActive = (active: boolean) => {
                      if (active) {
                        setActiveSeries(key);
                      } else {
                        setActiveSeries(curr => (curr === key ? undefined : curr));
                      }
                    };

                    switch (type) {
                      case ChartSeriesType.Area:
                        return (
                          <Recharts.Area
                            key={key}
                            dataKey={key}
                            hide={isHidden}
                            {...areaAnimationProps}
                            xAxisId={layer}
                            yAxisId={dependentAxis}
                            opacity={otherSeriesActive ? inactiveOpacity : opacity}
                            legendType={legend ? undefined : "none"}
                            fill={
                              dotPatternId != null
                                ? `url(#${dotPatternId})`
                                : areaGradientId != null
                                  ? `url(#${areaGradientId})`
                                  : color
                            }
                            fillOpacity={
                              dotPatternId != null || areaGradientId != null
                                ? 1
                                : keyConfig?.fillOpacity
                            }
                            stroke={keyConfig?.stroke ?? color}
                            strokeDasharray={dashed ? DOTTED_STROKE_DASHARRAY : undefined}
                            strokeLinecap={DOTTED_STROKE_LINECAP}
                            mask={dashed ? "url(#diagonalLinesMask)" : undefined}
                            stackId={groups != null ? groupIndices.get(key) : keyConfig?.stackId}
                            connectNulls={connectNulls}
                            type="monotoneX"
                            strokeWidth={STROKE_WIDTH}
                            dot={
                              keyConfig?.endDot === true
                                ? dotProps =>
                                    dotProps.index ===
                                    LoadState.unwrap(sortedData, emptyData).findLastIndex(
                                      row => row[key] != null,
                                    ) ? (
                                      <Recharts.Dot
                                        cx={dotProps.cx}
                                        cy={dotProps.cy}
                                        fill={color}
                                        mask=""
                                        r={3.5}
                                        stroke="hsl(var(--card))"
                                        strokeWidth={1.5}
                                      />
                                    ) : (
                                      <g />
                                    )
                                : dot
                                  ? { fill: color, mask: "", r: 1.2 }
                                  : false
                            }
                            // A clickable active dot (shown on hover) gives a per-point drill-down
                            // target. We reuse the visible Area (no separate overlay) so stacked
                            // areas aren't double-counted.
                            activeDot={
                              interactive
                                ? activeDotProps => {
                                    const openAreaPointDrillDown = () => {
                                      const payload = activeDotProps.payload as unknown;
                                      handleSeriesDotClick(
                                        payload != null && typeof payload === "object"
                                          ? (payload as Record<string, unknown>)
                                          : undefined,
                                        key,
                                      );
                                    };
                                    return (
                                      <Recharts.Dot
                                        {...activeDotProps}
                                        aria-label="View data point details"
                                        data-chart-mark={getChartMarkId(key, activeDotProps.index)}
                                        fill={color}
                                        onClick={openAreaPointDrillDown}
                                        onKeyDown={(_, event) => {
                                          if (event.key !== "Enter" && event.key !== " ") return;
                                          event.preventDefault();
                                          openAreaPointDrillDown();
                                        }}
                                        r={5}
                                        role="button"
                                        style={{ cursor: "pointer" }}
                                        tabIndex={0}
                                      />
                                    );
                                  }
                                : false
                            }
                          />
                        );

                      case ChartSeriesType.Bar: {
                        // Calculate per-bar opacity when an independent value is highlighted
                        const chartData = LoadState.unwrap(sortedData, emptyData);
                        const hasHighlight = highlightedIndependentValue != null;

                        return (
                          <Recharts.Bar
                            key={key}
                            dataKey={key}
                            hide={isHidden}
                            // Bars enter and exit through ChartSeriesMotion.module.css, which can
                            // stagger them per point. Recharts' own tween stays off: it restarts
                            // on every re-render (hover included), so in-place updates such as
                            // legend toggles snap instead.
                            isAnimationActive={false}
                            xAxisId={layer}
                            yAxisId={dependentAxis}
                            opacity={
                              hasHighlight
                                ? undefined
                                : otherSeriesActive
                                  ? inactiveOpacity
                                  : opacity
                            }
                            legendType={legend ? undefined : "none"}
                            fill={dotPatternId != null ? `url(#${dotPatternId})` : color}
                            mask={dashed ? "url(#diagonalLinesMask)" : undefined}
                            stackId={groups != null ? groupIndices.get(key) : keyConfig?.stackId}
                            maxBarSize={100} // without this, bars aren't rendered when there's only 1 data point
                            shape={barProps => {
                              const payload =
                                barProps.payload != null && typeof barProps.payload === "object"
                                  ? (barProps.payload as Record<string, unknown>)
                                  : undefined;
                              const independentValue = payload?.[independentKey as string] as
                                | TIndependentValue
                                | undefined;
                              const pointWhisker =
                                independentValue != null
                                  ? keyConfig?.pointWhisker?.(independentValue)
                                  : undefined;
                              return (
                                <MinHeightBar
                                  {...barProps}
                                  whisker={pointWhisker}
                                  chartId={chartId}
                                  className={seriesMotionStyles.bar}
                                  stackEdges={getStackEdges(key, stackKeysByKey.get(key), payload)}
                                  phaseStartedAt={seriesMotion.phaseStartedAt}
                                  data-chart-mark={
                                    interactive ? getChartMarkId(key, barProps.index) : undefined
                                  }
                                  staggerStepMs={seriesMotion.staggerStepMs}
                                  aria-label={
                                    interactive
                                      ? `View ${String(key)} data point details`
                                      : undefined
                                  }
                                  onClick={
                                    interactive
                                      ? () => handleSeriesDotClick(payload, key)
                                      : undefined
                                  }
                                  onMouseEnter={() => onHoverIndependentValue?.(independentValue)}
                                  onMouseLeave={() => onHoverIndependentValue?.(undefined)}
                                  onFocus={() => onHoverIndependentValue?.(independentValue)}
                                  onBlur={() => onHoverIndependentValue?.(undefined)}
                                  onKeyDown={
                                    interactive
                                      ? event => {
                                          if (event.key !== "Enter" && event.key !== " ") return;
                                          event.preventDefault();
                                          handleSeriesDotClick(payload, key, false);
                                        }
                                      : undefined
                                  }
                                  role={interactive ? "button" : undefined}
                                  tabIndex={interactive ? 0 : undefined}
                                />
                              );
                            }} // ensure small non-zero values remain visible and clickable
                            onFocus={() => setSeriesActive(seriesInteractive && true)}
                            onMouseEnter={() => setSeriesActive(seriesInteractive && true)}
                            onBlur={() => setSeriesActive(false)}
                            onMouseLeave={() => setSeriesActive(false)}
                            style={interactive ? { cursor: "pointer" } : {}}
                          >
                            {hasHighlight &&
                              chartData.map((entry, index) => {
                                const entryIndependentValue = entry[independentKey];
                                const isHighlighted =
                                  entryIndependentValue === highlightedIndependentValue;
                                const cellOpacity =
                                  isHighlighted || mutedBarColor != null
                                    ? opacity
                                    : inactiveOpacity;
                                return (
                                  <Recharts.Cell
                                    key={`cell-${index}`}
                                    // An explicit undefined fill would paint the cell black.
                                    fill={
                                      isHighlighted || mutedBarColor == null
                                        ? dotPatternId != null
                                          ? `url(#${dotPatternId})`
                                          : color
                                        : mutedBarColor
                                    }
                                    opacity={otherSeriesActive ? inactiveOpacity : cellOpacity}
                                  />
                                );
                              })}
                          </Recharts.Bar>
                        );
                      }

                      case ChartSeriesType.Line:
                        return (
                          <React.Fragment key={key}>
                            <Recharts.Line
                              xAxisId={layer}
                              yAxisId={dependentAxis}
                              key={key}
                              dataKey={key}
                              hide={isHidden}
                              {...lineAnimationProps}
                              className={dashed ? seriesMotionStyles.dashedLine : undefined}
                              opacity={otherSeriesActive ? inactiveOpacity : opacity}
                              connectNulls={connectNulls}
                              type="monotoneX"
                              legendType={legend ? undefined : "none"}
                              stroke={color}
                              dot={
                                dot &&
                                (keyConfig?.dotRadius != null ||
                                  keyConfig?.dotStroke != null ||
                                  keyConfig?.dotStrokeWidth != null)
                                  ? {
                                      fill: color,
                                      mask: "",
                                      r: keyConfig.dotRadius ?? 3,
                                      stroke: keyConfig.dotStroke ?? color,
                                      strokeWidth: keyConfig.dotStrokeWidth ?? 1,
                                    }
                                  : dot
                              }
                              fill={color}
                              activeDot={false}
                              strokeWidth={keyConfig?.strokeWidth ?? STROKE_WIDTH}
                              strokeDasharray={dashed ? DOTTED_STROKE_DASHARRAY : undefined}
                              strokeLinecap={DOTTED_STROKE_LINECAP}
                            />

                            {/* This line allows the user to hover over a wider target to set a line as active. */}
                            {seriesInteractive && !isHidden && (
                              <Recharts.Line
                                xAxisId={layer}
                                yAxisId={dependentAxis}
                                key={`${key}-mouse-target`}
                                dataKey={key}
                                data-series-key={key}
                                animationDuration={0}
                                opacity={renderDataPointTooltip == null ? 0 : undefined}
                                stroke={renderDataPointTooltip == null ? undefined : "transparent"}
                                connectNulls={connectNulls}
                                type="monotoneX"
                                legendType="none"
                                tooltipType="none"
                                dot={
                                  interactive
                                    ? dotProps => {
                                        const payload =
                                          dotProps.payload != null &&
                                          typeof dotProps.payload === "object"
                                            ? (dotProps.payload as Record<string, unknown>)
                                            : undefined;
                                        const independentValue = payload?.[
                                          independentKey as string
                                        ] as TIndependentValue | undefined;
                                        if (renderDataPointTooltip != null) {
                                          if (
                                            independentValue == null ||
                                            payload?.[String(key)] == null
                                          ) {
                                            return <g />;
                                          }
                                          const markId = getChartMarkId(key, dotProps.index);
                                          return (
                                            <Tooltip
                                              content={renderDataPointTooltip(
                                                independentValue,
                                                key,
                                              )}
                                              delayDuration={0}
                                              interactiveChild
                                              onOpenChange={open =>
                                                setActivePointTooltipId(current =>
                                                  open
                                                    ? markId
                                                    : current === markId
                                                      ? undefined
                                                      : current,
                                                )
                                              }
                                              open={activePointTooltipId === markId}
                                            >
                                              <circle
                                                aria-label={`View ${keyConfig?.label ?? String(key)}: ${formatIndependentValue(independentValue)} details`}
                                                className="cursor-pointer outline-none hover:stroke-[var(--wv-border-focus)] focus-visible:stroke-[var(--wv-border-focus)]"
                                                cx={dotProps.cx}
                                                cy={dotProps.cy}
                                                data-chart-mark={markId}
                                                fill="transparent"
                                                onClick={() => handleSeriesDotClick(payload, key)}
                                                onFocus={() => setActivePointTooltipId(markId)}
                                                onBlur={() =>
                                                  setActivePointTooltipId(current =>
                                                    current === markId ? undefined : current,
                                                  )
                                                }
                                                onKeyDown={event => {
                                                  if (event.key !== "Enter" && event.key !== " ")
                                                    return;
                                                  event.preventDefault();
                                                  event.stopPropagation();
                                                  handleSeriesDotClick(payload, key, false);
                                                }}
                                                r={10}
                                                role="button"
                                                stroke="transparent"
                                                strokeOpacity={1}
                                                strokeWidth={2}
                                                tabIndex={0}
                                              />
                                            </Tooltip>
                                          );
                                        }
                                        return (
                                          <Recharts.Dot
                                            aria-label={`View ${String(key)} data point details`}
                                            data-chart-mark={getChartMarkId(key, dotProps.index)}
                                            cx={dotProps.cx}
                                            cy={dotProps.cy}
                                            fill={dotProps.fill}
                                            onClick={() => handleSeriesDotClick(payload, key)}
                                            onKeyDown={(_, event) => {
                                              if (event.key !== "Enter" && event.key !== " ")
                                                return;
                                              event.preventDefault();
                                              handleSeriesDotClick(payload, key, false);
                                            }}
                                            onFocus={() => setSeriesActive(true)}
                                            onBlur={() => setSeriesActive(false)}
                                            onMouseEnter={() => setSeriesActive(true)}
                                            onMouseLeave={() => setSeriesActive(false)}
                                            opacity={renderDataPointTooltip == null ? 0 : undefined}
                                            r={10}
                                            role="button"
                                            style={{ cursor: "pointer" }}
                                            tabIndex={0}
                                          />
                                        );
                                      }
                                    : false
                                }
                                activeDot={false}
                                strokeWidth={20}
                                onFocus={() => setSeriesActive(interactive && true)}
                                onMouseEnter={() => setSeriesActive(interactive && true)}
                                onBlur={() => setSeriesActive(false)}
                                onMouseLeave={() => setSeriesActive(false)}
                              />
                            )}
                          </React.Fragment>
                        );

                      default:
                        unreachable(type);
                    }
                  }),
                )}

              {referenceLines != null &&
                LoadState.unwrap(referenceLines)?.map(line => {
                  // Extract scalar value for Recharts (handles both single values and tuples)
                  const getValue = (val: ChartDataValueType): number | string =>
                    Array.isArray(val) ? val[0] : val;

                  return (
                    <Recharts.ReferenceLine
                      key={line.label ?? `${line.type}-${String(line.value)}`}
                      xAxisId={0}
                      yAxisId={ChartDependentAxis.Left}
                      label={<ChartReferenceLineLabel label={line.label} side={line.side} />}
                      x={line.type === "independent" ? getValue(line.value) : undefined}
                      y={line.type === "dependent" ? getValue(line.value) : undefined}
                      strokeWidth={1.5}
                      stroke={line.color ?? "var(--wv-text-secondary)"}
                      strokeOpacity={line.color == null ? 0.3 : undefined}
                      ifOverflow="visible"
                      strokeDasharray={line.dashed === true ? DOTTED_STROKE_DASHARRAY : undefined}
                      strokeLinecap={DOTTED_STROKE_LINECAP}
                    />
                  );
                })}

              {annotations?.map(annotation => {
                const row = LoadState.unwrap(sortedData, emptyData).find(
                  entry => entry[independentKey] === annotation.independentValue,
                );
                const value = row?.[annotation.dependentKey];
                if (typeof value !== "number") return null;
                return (
                  <Recharts.ReferenceDot
                    key={`${String(annotation.dependentKey)}-${String(annotation.independentValue)}`}
                    xAxisId={0}
                    yAxisId={
                      config?.[annotation.dependentKey]?.dependentAxis ?? ChartDependentAxis.Left
                    }
                    x={annotation.independentValue}
                    y={value}
                    r={4}
                    fill="var(--wv-bg-surface)"
                    stroke="var(--wv-border-status-warning)"
                    strokeWidth={1.5}
                    ifOverflow="extendDomain"
                    label={
                      <Recharts.Label
                        value={annotation.label}
                        position="top"
                        offset={8}
                        fill="var(--wv-text-status-warning)"
                      />
                    }
                  />
                );
              })}

              {tooltip && (
                <Recharts.Tooltip
                  active={activePointTooltipId != null ? false : undefined}
                  cursor={tooltipCursor}
                  wrapperStyle={{ zIndex: 10 }}
                  content={
                    <ChartTooltip
                      formatValue={formatValueTooltip}
                      formatIndependentValue={
                        formatIndependentValueTooltip ?? formatIndependentValue
                      }
                      formatIndependentValueForSeries={formatIndependentValueTooltipForSeries}
                      renderTooltipData={renderTooltipData}
                    />
                  }
                />
              )}

              {legend && (
                <Recharts.Legend
                  itemSorter={null} // sorts legend items by the order they appear in the data, instead of alphabetically (default behavior)
                  portal={legendPortal}
                  verticalAlign="top"
                  align="left"
                  wrapperStyle={
                    legendPortal != null
                      ? undefined
                      : { paddingBottom: LEGEND_TO_PLOT_GAP_PX, top: 0 }
                  }
                  content={props => (
                    <ChartLegend
                      payload={props.payload}
                      className={cn(
                        legendPortal != null ? legendPortalClassName : undefined,
                        legendPortal == null && "justify-start",
                      )}
                    />
                  )}
                />
              )}
            </Recharts.ComposedChart>
          </Recharts.ResponsiveContainer>
        )}

        {isEmpty && <ChartMessageCard.Empty emptyMessage={emptyDataMessage} />}
        {/* Dimming only helps over a stale chart that stays plotted (reduced motion); otherwise the
            series have exited and there is nothing left to dim. */}
        {LoadState.isReloading(data) && (
          <ChartMessageCard.Loading overlay={seriesMotion.phase === SeriesMotionPhase.Shown} />
        )}
        {LoadState.isError(data) && <ChartMessageCard.Error error={data.error} />}
      </ChartContainer>
    </ChartContext.Provider>
  );
}
