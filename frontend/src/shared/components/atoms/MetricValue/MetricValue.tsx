import type { ReactNode } from "react";

import { cn } from "../../../tools/cn";
import { TypographyRole } from "../../../tools/typography";

export type MetricValueSize = TypographyRole.PageTitle | TypographyRole.Display;

const SIZE_CLASS_NAMES: Record<MetricValueSize, string> = {
  [TypographyRole.PageTitle]: "text-page-title",
  [TypographyRole.Display]: "text-display",
};

export interface MetricValueProps {
  /** Extra classes for the number, e.g. container-query size steps or color. */
  className?: string;
  /** @default TypographyRole.PageTitle */
  size?: MetricValueSize;
  /** Supporting text on the number's baseline, e.g. "output" or "/week". */
  unit?: ReactNode;
  /** Already formatted by the caller (e.g. `formatNumber`); this component only sets type. */
  value: ReactNode;
}

/**
 * A headline number above a chart or in a stat. It sits on a type-scale step but at the UI
 * weight: the roles' emphasis weight reads heavy at numeral sizes.
 */
export function MetricValue({
  className,
  size = TypographyRole.PageTitle,
  unit,
  value,
}: MetricValueProps) {
  // Important: role size utilities also set the role's weight, and container-query steps such as
  // `@2xl:text-display` are emitted after `font-normal`, so a plain weight class would lose there.
  const valueElement = (
    <span className={cn(SIZE_CLASS_NAMES[size], "!font-normal tabular-nums", className)}>
      {value}
    </span>
  );
  if (unit == null) return valueElement;
  return (
    <span className="inline-flex items-baseline gap-1.5">
      {valueElement}
      {/* Flex drops this space visually but keeps it in the accessible name. */}{" "}
      <span className="text-body font-normal text-wv-secondary">{unit}</span>
    </span>
  );
}
