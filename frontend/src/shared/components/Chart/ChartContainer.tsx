import { forwardRef, useId } from "react";

import { cn } from "../../tools/cn";

export interface ChartContainerProps extends React.HTMLAttributes<HTMLDivElement> {}

export const ChartContainer = forwardRef<HTMLDivElement, ChartContainerProps>(
  ({ children, className, id, ...props }, ref) => {
    const uniqueId = useId();
    const chartId = `chart-${id ?? uniqueId.replace(/:/g, "")}`;

    return (
      <div
        data-chart={chartId}
        ref={ref}
        className={cn(
          "relative flex aspect-auto h-full min-h-48 w-full min-w-56 justify-center text-caption [&_.recharts-cartesian-axis-tick_text]:fill-[var(--wv-text-secondary)] [&_.recharts-cartesian-grid_line[stroke='#ccc']]:stroke-[var(--wv-border-default)] [&_.recharts-curve.recharts-tooltip-cursor]:stroke-[var(--wv-border-default)] [&_.recharts-dot[stroke='#fff']]:stroke-transparent [&_.recharts-layer]:outline-none [&_.recharts-polar-grid_[stroke='#ccc']]:stroke-[var(--wv-border-default)] [&_.recharts-radial-bar-background-sector]:fill-[var(--wv-bg-track)] [&_.recharts-rectangle.recharts-tooltip-cursor]:fill-[var(--wv-bg-hover)] [&_.recharts-reference-line_[stroke='#ccc']]:stroke-[var(--wv-border-default)] [&_.recharts-sector[stroke='#fff']]:stroke-transparent [&_.recharts-sector]:outline-none [&_.recharts-surface:focus-visible]:outline-offset-1 [&_.recharts-surface:focus-visible]:outline-wv-focus [&_.recharts-surface:has(.recharts-layer.recharts-pie:focus-visible)]:outline-offset-1 [&_.recharts-surface:has(.recharts-layer.recharts-pie:focus-visible)]:outline-wv-focus [&_.recharts-surface]:rounded [&_.recharts-surface]:outline-none [&_g[tabindex]]:outline-none",
          className,
        )}
        {...props}
      >
        {children}
      </div>
    );
  },
);
ChartContainer.displayName = "ChartContainer";
