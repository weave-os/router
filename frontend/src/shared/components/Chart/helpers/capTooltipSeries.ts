/** Maximum number of series rows shown in a chart tooltip before capping with "+N more". */
export const MAX_TOOLTIP_SERIES_ROWS = 10;

export interface TooltipSeriesEntry<T> {
  name: T;
  /** Numeric value used to sort entries when capping. Null/undefined entries sort last. */
  sortValue?: number | null;
}

export interface CapTooltipSeriesResult<T> {
  hiddenCount: number;
  visible: TooltipSeriesEntry<T>[];
}

/**
 * Caps tooltip series entries to at most `maxRows`.
 *
 * When entries fit within the cap they are returned in their original order.
 * When they exceed the cap the active series (if any) is pinned first,
 * followed by the highest-value remaining entries in descending order.
 * Entries with null/undefined sortValue sort last.
 */
export function capTooltipSeries<T>({
  activeSeries,
  entries,
  maxRows,
}: {
  activeSeries?: T | null;
  entries: TooltipSeriesEntry<T>[];
  maxRows: number;
}): CapTooltipSeriesResult<T> {
  if (maxRows <= 0) return { hiddenCount: entries.length, visible: [] };
  if (entries.length <= maxRows) return { hiddenCount: 0, visible: [...entries] };

  const byValueDesc = [...entries].sort((a, b) => {
    const aVal = a.sortValue;
    const bVal = b.sortValue;
    if (aVal == null && bVal == null) return 0;
    if (aVal == null) return 1;
    if (bVal == null) return -1;
    return bVal - aVal;
  });

  // Pin active series first if present.
  const active = byValueDesc.find(e => activeSeries != null && e.name === activeSeries);

  const rest = active != null ? byValueDesc.filter(e => e.name !== activeSeries) : byValueDesc;
  const available = active != null ? maxRows - 1 : maxRows;

  const visible = active != null ? [active, ...rest.slice(0, available)] : rest.slice(0, available);
  return { hiddenCount: entries.length - visible.length, visible };
}
