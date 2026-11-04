import React from "react";

import { cn } from "../../tools/cn";
import { formatInteger } from "../../tools/format";
import { Text } from "../atoms/Text";
import { Button, type ButtonProps } from "../molecules/Button";
import { Tooltip } from "../molecules/Tooltip";
import { Appearance } from "../types";

export interface ListogramRowProps<TValue extends number>
  extends Pick<ButtonProps, "href" | "onClick">, React.PropsWithChildren {
  /** Additional CSS class names for the row container. */
  className?: string;
  /** The maximum value across the entire listogram. */
  maxValue: number;
  /** The value for a row in a listogram. */
  value: TValue | null;

  /**
   * Formats the value to display.
   *
   * @default formatInteger
   */
  formatValue?: (value: TValue) => string;

  /**
   * When set, shown inside the bar on hover (e.g. a percentage).
   */
  formatBarHoverLabel?: (value: TValue) => string;

  /**
   * Optional full-width content rendered on its own line beneath the row's main
   * content (label + bar). Used e.g. to show a secondary breakdown of the bar's
   * value. The row container is flex-wrap, so a full-width element wraps below.
   */
  subContent?: React.ReactNode;

  /**
   * Positive slices that fill the bar left-to-right. Amounts are relative weights:
   * widths are each slice's share of their sum, and the whole stack is scaled to
   * {@link value} / {@link maxValue}. Slice labels report `value × share`, so the
   * weights may be in a different unit from the displayed value (e.g. window sums
   * behind a per-period average) without the tooltip disagreeing with the row.
   * When omitted, the bar is a single brand fill.
   */
  barSegments?: readonly ListogramBarSegment[];
}

export interface ListogramBarSegment {
  amount: number;
  /** CSS color for the slice fill (hex or any `background-color` value). */
  color: string;
  key: string;
  /** Human-readable slice name; becomes the segment's `title` and accessible label. */
  label: string;
}

/** Renders a single row in a listogram. */
export function ListogramRow<TValue extends number>({
  children,
  className,
  href,
  onClick,
  subContent,
  ...props
}: ListogramRowProps<TValue>) {
  const { value } = props;

  const row = (
    <div
      className={cn(
        "flex w-full flex-row flex-wrap items-center gap-4 gap-y-1 p-1.5",
        { "opacity-60": value === 0 },
        className,
      )}
    >
      {children}

      <ListogramBar className="ml-auto" {...props} />

      {subContent != null && <div className="w-full">{subContent}</div>}
    </div>
  );

  if (onClick != null || href != null) {
    return (
      <Button
        className="h-min w-full p-0 font-normal"
        appearance={Appearance.Hollow}
        onClick={onClick}
        href={href}
        prefetch={false} // avoid prefetching too many pages at once when rapidly mousing over listogram rows
      >
        {row}
      </Button>
    );
  }

  return row;
}

/**
 * Renders a loading row. If the children are not provided, a reasonable default (simulating an
 * avatar and some text) will be used.
 */
ListogramRow.Loading = function ListogramRowLoading({ children }: React.PropsWithChildren) {
  return (
    <div className="flex flex-row flex-wrap items-center gap-4 gap-y-1 p-1.5">
      {children}

      <div className="relative ml-auto h-5 w-24 rounded bg-wv-placeholder" />
    </div>
  );
};

interface ListogramBarProps<TValue extends number> extends Pick<
  ListogramRowProps<TValue>,
  "barSegments" | "formatBarHoverLabel" | "formatValue" | "maxValue" | "value"
> {
  className?: string;
  /** Hides the value beside the bar, for layouts where a column already shows it. */
  showValue?: boolean;
  trackClassName?: string;
}

/** Renders a single bar in a listogram row. */
export function ListogramBar<TValue extends number>({
  barSegments,
  className,
  formatBarHoverLabel,
  formatValue = formatInteger,
  maxValue,
  showValue = true,
  trackClassName = "w-24",
  value,
}: ListogramBarProps<TValue>) {
  const fillWidthPercent =
    value == null || maxValue <= 0 ? 0 : Math.max((value / maxValue) * 100, 0);
  const hoverLabel = value != null && value > 0 ? formatBarHoverLabel?.(value) : undefined;
  const positiveSegments = (barSegments ?? []).filter(segment => segment.amount > 0);
  const stackedTotal = positiveSegments.reduce((sum, segment) => sum + segment.amount, 0);
  // A non-positive row draws no fill, so it must not stack either: slice labels are
  // value × share and would otherwise read as negative amounts for positive slices.
  const useStack = value != null && value > 0 && stackedTotal > 0;

  return (
    <div className={cn("flex flex-row items-center gap-3", className)}>
      {showValue && (
        <Text
          variant="p"
          className={cn("text-body font-medium", {
            "text-wv-secondary": value == null || value === 0,
          })}
        >
          {value == null ? "Unavailable" : formatValue(value)}
        </Text>
      )}

      <div
        className={cn(
          "group/bar relative h-5 overflow-hidden rounded",
          !useStack && "bg-wv-track",
          trackClassName,
        )}
      >
        {/* Track color covers only the unfilled part so stack gaps show the surface behind the bar. */}
        {useStack && (
          <div
            className="absolute inset-y-0 right-0 bg-wv-track"
            style={{ left: `${fillWidthPercent}%` }}
          />
        )}
        {value != null && (
          <div
            className={cn(
              "absolute left-0 flex h-full min-w-0 items-stretch",
              useStack && "gap-px",
              !useStack &&
                "items-center justify-center rounded bg-brand px-1 shadow-wv-chart-mark-depth",
            )}
            style={{ width: `${fillWidthPercent}%` }}
          >
            {useStack ? (
              positiveSegments.map((segment, index) => {
                // Segment amounts are relative weights; label with the slice's share of the
                // displayed row value so the tooltip agrees with the number beside the bar
                // (which may be a per-period average while the weights are window sums).
                const share = segment.amount / stackedTotal;
                const segmentLabel = `${segment.label}: ${formatValue((value * share) as TValue)}`;
                return (
                  <Tooltip
                    key={segment.key}
                    content={segmentLabel}
                    delayDuration={350}
                    disableHoverableContent
                    interactiveChild
                  >
                    <div
                      role="img"
                      aria-label={segmentLabel}
                      className={cn(
                        "h-full min-w-0 shadow-wv-chart-mark-depth transition-[filter] hover:brightness-110",
                        index === 0 && "rounded-l",
                        index === positiveSegments.length - 1 && "rounded-r",
                      )}
                      style={{ backgroundColor: segment.color, flexBasis: 0, flexGrow: share }}
                    />
                  </Tooltip>
                );
              })
            ) : hoverLabel != null ? (
              <span className="text-caption font-medium text-wv-on-action opacity-0 transition-opacity group-hover/bar:opacity-100">
                {hoverLabel}
              </span>
            ) : null}
          </div>
        )}
      </div>
    </div>
  );
}
