import { ChartReferenceLine } from "./types";

const LABEL_OFFSET = 4;

export interface ChartReferenceLineLabelProps extends Pick<
  ChartReferenceLine<number, number>,
  "side"
> {
  label?: string;
  viewBox?: { height?: number; width?: number; x?: number; y?: number };
}

const TEXT_ANCHOR: Readonly<
  Record<Required<ChartReferenceLine<number, number>>["side"], "end" | "middle" | "start">
> = {
  center: "middle",
  left: "end",
  right: "start",
};

const HORIZONTAL_TEXT_ANCHOR: Readonly<
  Record<Required<ChartReferenceLine<number, number>>["side"], "end" | "middle" | "start">
> = {
  center: "middle",
  left: "start",
  right: "end",
};

export function ChartReferenceLineLabel({
  label,
  side = "center",
  viewBox,
}: ChartReferenceLineLabelProps) {
  if (label == null) return null;

  // A horizontal line's box has no height; its label sits just above the line at the chosen end.
  if (viewBox?.height === 0 && viewBox.x != null && viewBox.y != null && viewBox.width != null) {
    const horizontalX =
      side === "right"
        ? viewBox.x + viewBox.width
        : side === "left"
          ? viewBox.x
          : viewBox.x + viewBox.width / 2;
    return (
      <text
        x={horizontalX}
        y={viewBox.y - LABEL_OFFSET}
        className="fill-[var(--wv-text-secondary)]"
        textAnchor={HORIZONTAL_TEXT_ANCHOR[side]}
      >
        {label}
      </text>
    );
  }

  let x = viewBox?.x;
  if (x != null && side === "left") {
    x = x - LABEL_OFFSET;
  } else if (x != null && side === "right") {
    x = x + LABEL_OFFSET;
  }

  return (
    <text
      x={x}
      y={-8}
      className="fill-[var(--wv-text-secondary)] px-1"
      textAnchor={TEXT_ANCHOR[side]}
    >
      {label}
    </text>
  );
}
