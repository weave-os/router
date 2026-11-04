import { cn } from "../../tools/cn";

export interface ResponsiveGridProps extends React.HTMLAttributes<HTMLDivElement> {}

export function ResponsiveGrid({ children, className, ...props }: ResponsiveGridProps) {
  return (
    <div className={cn("grid grid-cols-6 gap-2.5 @container", className)} {...props}>
      {children}
    </div>
  );
}

/** Renders across 2/6 columns on the largest screens. */
ResponsiveGrid.Small = cn("col-span-6 @xl:col-span-3 @4xl:col-span-2");
ResponsiveGrid.OneThird = cn("col-span-6 @xl:col-span-2");
/** Renders across 3/6 columns on the largest screens. */
ResponsiveGrid.Medium = cn("col-span-6 @xl:col-span-3");
ResponsiveGrid.TwoThirds = cn("col-span-6 @xl:col-span-4");
/** Renders across 4/6 columns on the largest screens. */
ResponsiveGrid.Large = cn("col-span-6 @xl:col-span-3 @4xl:col-span-4");
/** Renders across 6/6 columns on every screen size. */
ResponsiveGrid.Full = cn("col-span-6");
