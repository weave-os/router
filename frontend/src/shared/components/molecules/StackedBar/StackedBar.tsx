import { type HTMLAttributes } from "react";

import { cn } from "../../../tools/cn";

export interface StackedBarSegment {
  amount: number;
  /** Styles the segment when a plain color isn't enough, e.g. a hatched "not scored" segment. */
  className?: string;
  color?: string;
  id: string;
}

export interface StackedBarProps extends HTMLAttributes<HTMLDivElement> {
  segments: readonly StackedBarSegment[];
  /** `sm` fits a table cell; `md` sits above a legend. */
  size?: "md" | "sm";
  /** The bar's full width; whatever the segments don't fill shows as track. */
  total: number;
}

export function StackedBar({ className, segments, size = "md", total, ...props }: StackedBarProps) {
  const visibleSegments = segments.filter(segment => segment.amount > 0);
  const filledAmount = visibleSegments.reduce((sum, segment) => sum + segment.amount, 0);
  const toPercent = (amount: number) => `${total === 0 ? 0 : (amount / total) * 100}%`;

  return (
    <div
      className={cn(
        "relative flex w-full shrink-0 gap-px overflow-hidden",
        size === "md" ? "h-5 rounded" : "h-2 rounded-sm",
        className,
      )}
      {...props}
    >
      <div
        aria-hidden="true"
        className="absolute inset-y-0 right-0 bg-wv-track"
        style={{ left: toPercent(filledAmount) }}
      />
      {visibleSegments.map(segment => (
        <div
          key={segment.id}
          className={cn("h-full min-w-[2px]", segment.className)}
          style={{ backgroundColor: segment.color, width: toPercent(segment.amount) }}
        />
      ))}
    </div>
  );
}
