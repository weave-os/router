import { Tooltip } from "@/shared/components/molecules/Tooltip";
import { cn } from "@/shared/tools/cn";
import { formatHours, formatInteger } from "@/shared/tools/format";

const DEFAULT_TOOLTIP = "Compared to the previous period";
const DEFAULT_EMPTY_TOOLTIP = "Not enough data to compare";

/**
 * "%" for a relative change; "pp" for a change between two percentages; "score" for a 0–10 score
 * (one decimal); "points" for a whole-number score; "count" for a change in a count;
 * "h" for a change in hours, shown in minutes, hours or days like the durations beside it.
 */
export type ChangeUnit = "%" | "pp" | "score" | "points" | "count" | "h";

export interface ChangeValueProps {
  /** Null when there is nothing to compare against; shows an em dash. */
  change: number | null;
  className?: string;
  /** Shown on hover over the em dash. */
  emptyTooltip?: string;
  /**
   * Swaps the success/danger colors. Use for "lower is better" metrics (e.g. bug reports), where
   * a decrease should read as positive (green).
   */
  invertColor?: boolean;
  /** Never colors the change, for values with no good or bad direction (e.g. spend). */
  neutral?: boolean;
  /** Tooltip override; null for none. Defaults to "Compared to the previous period". */
  tooltip?: string | null;
  /** @default "%" */
  unit?: ChangeUnit;
}

const CHANGE_UNIT_SUFFIX: Record<Exclude<ChangeUnit, "score" | "h">, string> = {
  "%": "%",
  count: "",
  points: "",
  pp: " pp",
};

/**
 * A signed change, colored by whether it is good (success) or bad (danger). A colored change sits
 * in a tinted cell so it stands out from the figures around it.
 */
export function ChangeValue({
  change,
  className,
  emptyTooltip = DEFAULT_EMPTY_TOOLTIP,
  invertColor = false,
  neutral = false,
  tooltip = DEFAULT_TOOLTIP,
  unit = "%",
}: ChangeValueProps) {
  const baseClassName = cn("whitespace-nowrap text-body font-medium tabular-nums", className);
  if (change == null) {
    return (
      <Tooltip content={emptyTooltip} interactiveChild>
        <span className={cn(baseClassName, "text-wv-secondary")}>—</span>
      </Tooltip>
    );
  }

  const { direction, formattedChange } = formatChange(change, unit);
  // For "lower is better" metrics, a positive change is bad (danger) and a negative one good.
  const goodDirection = invertColor ? -1 : 1;
  const isColored = !neutral && direction !== 0;
  const changeElement = (
    <span
      className={cn(baseClassName, "text-wv-secondary", {
        "rounded-md px-1.5 py-0.5": isColored,
        "bg-wv-status-danger-subtle text-wv-status-danger":
          isColored && direction !== goodDirection,
        "bg-wv-status-success-subtle text-wv-status-success":
          isColored && direction === goodDirection,
      })}
    >
      {formattedChange}
    </span>
  );
  return tooltip == null ? (
    changeElement
  ) : (
    <Tooltip
      content={tooltip}
      className="rounded-md focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-wv-focus"
    >
      {changeElement}
    </Tooltip>
  );
}

/** Rounds to the shown precision first, so the sign and color always match the shown number. */
function formatChange(change: number, unit: ChangeUnit) {
  if (unit === "score") {
    const rounded = Math.round(change * 10) / 10;
    const direction = Math.sign(rounded);
    if (direction > 0) return { direction, formattedChange: `+${rounded.toFixed(1)}` };
    if (direction < 0) return { direction, formattedChange: `−${(-rounded).toFixed(1)}` };
    return { direction: 0, formattedChange: "±0.0" };
  }
  if (unit === "h") {
    const magnitude = formatHours(Math.abs(change));
    // "0m" and "0.0h" read as no change, so they take no sign or color.
    const direction = /[1-9]/.test(magnitude) ? Math.sign(change) : 0;
    if (direction > 0) return { direction, formattedChange: `+${magnitude}` };
    if (direction < 0) return { direction, formattedChange: `-${magnitude}` };
    return { direction: 0, formattedChange: "0m" };
  }
  const raw = Math.round(change);
  const rounded = Object.is(raw, -0) ? 0 : raw;
  const direction = Math.sign(rounded);
  const suffix = CHANGE_UNIT_SUFFIX[unit];
  return {
    direction,
    formattedChange: `${direction > 0 ? "+" : ""}${formatInteger(rounded)}${suffix}`,
  };
}
