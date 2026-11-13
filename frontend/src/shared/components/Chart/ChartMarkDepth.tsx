/**
 * Which edges of a chart mark show depth. An edge that touches another mark (inside a stack) stays
 * flat so the stack reads as one object.
 */
export interface ChartMarkDepthEdges {
  isBottomShaded: boolean;
  isTopLit: boolean;
}

export const FULL_CHART_MARK_DEPTH: ChartMarkDepthEdges = { isBottomShaded: true, isTopLit: true };

/** Returns the filter id for one combination of edges, or undefined when the mark stays flat. */
export function getChartMarkDepthFilterId(
  scopeId: string,
  { isBottomShaded, isTopLit }: ChartMarkDepthEdges,
) {
  if (!isBottomShaded && !isTopLit) return undefined;
  return `${scopeId}-mark-depth${isTopLit ? "-lit" : ""}${isBottomShaded ? "-shaded" : ""}`;
}

/**
 * The part of the mark that a shifted, blurred copy of itself leaves uncovered, painted in `color`.
 * This is SVG's equivalent of an inset shadow and never draws outside the mark.
 */
function InnerEdge({
  blur,
  color,
  name,
  offsetY,
}: {
  blur: number;
  color: string;
  name: string;
  offsetY: number;
}) {
  return (
    <>
      <feOffset in="SourceAlpha" dy={offsetY} result={`${name}-offset`} />
      <feGaussianBlur in={`${name}-offset`} stdDeviation={blur} result={`${name}-blur`} />
      <feComposite in="SourceAlpha" in2={`${name}-blur`} operator="out" result={`${name}-edge`} />
      <feFlood style={{ floodColor: color }} />
      <feComposite in2={`${name}-edge`} operator="in" result={name} />
    </>
  );
}

function ChartMarkDepthFilter({ edges, scopeId }: { edges: ChartMarkDepthEdges; scopeId: string }) {
  return (
    <filter
      id={getChartMarkDepthFilterId(scopeId, edges)}
      x="-10%"
      y="-10%"
      width="120%"
      height="120%"
      colorInterpolationFilters="sRGB"
    >
      {edges.isTopLit && (
        <InnerEdge
          name="highlight"
          offsetY={1.5}
          blur={0.5}
          color="var(--wv-chart-mark-highlight)"
        />
      )}
      {edges.isBottomShaded && (
        <InnerEdge name="lowlight" offsetY={-2} blur={1} color="var(--wv-chart-mark-lowlight)" />
      )}
      <feMerge>
        <feMergeNode in="SourceGraphic" />
        {edges.isTopLit && <feMergeNode in="highlight" />}
        {edges.isBottomShaded && <feMergeNode in="lowlight" />}
      </feMerge>
    </filter>
  );
}

/**
 * Defines the highlight and lowlight filters for one chart; render inside its `<defs>` and point
 * marks at {@link getChartMarkDepthFilterId}. HTML marks use the `shadow-wv-chart-mark-depth`
 * utility, which paints the same two edges.
 */
export function ChartMarkDepthFilters({ scopeId }: { scopeId: string }) {
  const edgeCombinations = [
    FULL_CHART_MARK_DEPTH,
    { isBottomShaded: false, isTopLit: true },
    { isBottomShaded: true, isTopLit: false },
  ];

  return (
    <>
      {edgeCombinations.map(edges => (
        <ChartMarkDepthFilter
          key={getChartMarkDepthFilterId(scopeId, edges)}
          scopeId={scopeId}
          edges={edges}
        />
      ))}
    </>
  );
}
