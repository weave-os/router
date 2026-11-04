import { cn } from "../../../tools/cn";
import { formatPercentage } from "../../../tools/format";
import { Icon } from "../../atoms/Icon";
import { StackedBar } from "../StackedBar";
import { Tooltip } from "../Tooltip";

export interface BreakdownSlice {
  amount: number;
  color: string;
  id: string;
  /** A catch-all such as "Other"; drawn after every named slice whatever its size. */
  isOther?: boolean;
  label: string;
}

/** Largest slice first, catch-alls last; ties keep the caller's order. */
function orderBreakdownSlices(slices: readonly BreakdownSlice[]) {
  const isOther = (slice: BreakdownSlice) => slice.isOther === true;
  return [...slices].sort((a, b) => {
    if (isOther(a) !== isOther(b)) return isOther(a) ? 1 : -1;
    return b.amount - a.amount;
  });
}

export interface BreakdownBarSliceActions {
  /** Names a clickable row for screen readers, e.g. "Select Platform in spend flow". */
  getActionLabel: (slice: BreakdownSlice) => string;
  isSliceInteractive: (sliceId: string) => boolean;
  onSliceClick: (sliceId: string) => void;
}

export interface BreakdownBarProps {
  formatAmount: (amount: number) => string;
  /** Makes rows clickable; without it every row is static. */
  sliceActions?: BreakdownBarSliceActions;
  /** Greys the label of rows that aren't clickable, so they read as static. */
  dimInactiveSlices?: boolean;
  /** Allows the breakdown rows to scroll when the surrounding card has a fixed height. */
  scrollable?: boolean;
  /** Explains a row on hover, e.g. what a cost source means. */
  getSliceTooltip?: (slice: BreakdownSlice) => string | undefined;
  slices: readonly BreakdownSlice[];
  /** Class for tooltip bubbles, e.g. to allow wrapping long explanations. */
  tooltipContentClassName?: string;
  total: number;
}

/**
 * Horizontal stacked bar plus legend rows: each row shows the slice's swatch, label, share of the
 * total and amount, with a hover chevron when clickable. Slices are drawn largest first with any
 * catch-all last, whatever order the caller passes.
 */
export function BreakdownBar({
  dimInactiveSlices = false,
  formatAmount,
  getSliceTooltip,
  scrollable = true,
  sliceActions,
  slices,
  tooltipContentClassName,
  total,
}: BreakdownBarProps) {
  const orderedSlices = orderBreakdownSlices(slices);
  return (
    <div className={cn("flex w-full flex-col gap-4", scrollable && "h-full min-h-0")}>
      <div className={cn("flex flex-col gap-3", scrollable && "min-h-0")}>
        <StackedBar segments={orderedSlices} total={total} />
        <ul
          className={cn(
            "flex flex-col divide-y divide-wv-default",
            scrollable && "min-h-0 overflow-auto",
          )}
        >
          {orderedSlices.map(slice => {
            const interactiveActions =
              sliceActions?.isSliceInteractive(slice.id) === true ? sliceActions : undefined;
            const isInteractive = interactiveActions != null;
            const row = (
              <>
                <span
                  className="size-2.5 shrink-0 rounded-sm"
                  style={{ backgroundColor: slice.color }}
                />
                <span
                  className={cn(
                    "min-w-0 truncate",
                    dimInactiveSlices && !isInteractive && "text-wv-secondary",
                  )}
                >
                  {slice.label}
                </span>
                <span className="ml-auto tabular-nums text-wv-secondary">
                  {total === 0 || slice.amount === 0 ? "—" : formatPercentage(slice.amount / total)}
                </span>
                <span className="w-24 text-right font-medium tabular-nums">
                  {formatAmount(slice.amount)}
                </span>
              </>
            );

            const tooltip = getSliceTooltip?.(slice);
            if (interactiveActions == null) {
              return (
                <li key={slice.id} className="flex w-full items-center gap-2 py-2 pr-5 text-body">
                  {tooltip == null ? (
                    row
                  ) : (
                    <Tooltip
                      className="flex w-full min-w-0 items-center gap-2 text-left"
                      content={tooltip}
                      contentClassName={tooltipContentClassName}
                    >
                      {row}
                    </Tooltip>
                  )}
                </li>
              );
            }

            const button = (
              <button
                type="button"
                aria-label={`${interactiveActions.getActionLabel(slice)}, ${
                  total === 0 || slice.amount === 0
                    ? "— of total"
                    : `${formatPercentage(slice.amount / total)} of total`
                }, ${formatAmount(slice.amount)}`}
                onClick={() => interactiveActions.onSliceClick(slice.id)}
                className="group/slice flex w-full items-center gap-2 rounded px-1 py-2 text-left text-body transition-colors hover:bg-wv-hover focus-visible:outline focus-visible:outline-2 focus-visible:outline-wv-focus [@media(pointer:coarse)]:min-h-11"
              >
                {row}
                <Icon
                  type="ChevronRight"
                  className="size-3.5 shrink-0 text-wv-secondary opacity-0 transition-opacity group-hover/slice:opacity-100"
                />
              </button>
            );
            return (
              <li key={slice.id}>
                {tooltip == null ? (
                  button
                ) : (
                  <Tooltip
                    content={tooltip}
                    contentClassName={tooltipContentClassName}
                    interactiveChild
                  >
                    {button}
                  </Tooltip>
                )}
              </li>
            );
          })}
        </ul>
      </div>
    </div>
  );
}
