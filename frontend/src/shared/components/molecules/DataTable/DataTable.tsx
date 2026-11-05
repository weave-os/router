"use client";

import {
  Cell,
  Column,
  ColumnDef,
  ExpandedState,
  flexRender,
  getCoreRowModel,
  getExpandedRowModel,
  getSortedRowModel,
  Row,
  RowSelectionState,
  SortingState,
  Table as TanstackTable,
  useReactTable,
} from "@tanstack/react-table";
import { defaultRangeExtractor, useVirtualizer, Virtualizer } from "@tanstack/react-virtual";

import { useTheme } from "next-themes";

import React, { type JSX, useEffect, useMemo, useRef, useState } from "react";

import { cn } from "../../../tools/cn";
import { deepEqual } from "../../../tools/deepEqual";
import { Table, TableProps } from "../Table";
import { DataTableEmpty } from "./DataTableEmpty";
import { DefaultExpandButton } from "./DataTableExpandButton";
import { DataTableHeaderCell } from "./DataTableHeaderCell";
import { DataTableLoading } from "./DataTableLoading";
import { columnSizeCss } from "./helpers/columnSizeVar";
import { useTableCSSVariables } from "./hooks/useTableCSSVariables";

const DEFAULT_ROW_HEIGHT = 49; // 48px + 1px border
const DEFAULT_OVERSCAN = 4; // 4 rows above and below the visible range
export interface DataTableProps<TData> extends Pick<
  TableProps,
  "afterContent" | "containerClassName"
> {
  /**
   * Cell selection handlers and state
   */
  cellSelection?: {
    getCellSelectionState?: (
      cell: Cell<TData, unknown>,
      rowIndex?: number,
      colIndex?: number,
    ) => {
      isSelected: boolean;
      isSelectionBottom: boolean;
      isSelectionLeft: boolean;
      isSelectionRight: boolean;
      isSelectionTop: boolean;
    };
    handleCellMouseDown: (e: React.MouseEvent<HTMLElement>, cell: Cell<TData, unknown>) => void;
    handleCellMouseOver: (e: React.MouseEvent<HTMLElement>, cell: Cell<TData, unknown>) => void;
    handleCellMouseUp: (e: React.MouseEvent<HTMLElement>, cell: Cell<TData, unknown>) => void;
    isCellSelected: (cell: Cell<TData, unknown>) => boolean;
  };
  className?: string;
  /**
   * Column order state for drag-and-drop column reordering.
   * Array of column IDs in the desired order.
   */
  columnOrder?: string[];
  // We use any here because it's entirely the ColumnDef's responsibility to define the type of the
  // data and handle it correctly.
  // oxlint-disable-next-line typescript/no-explicit-any
  columns: ColumnDef<TData, any>[];
  data: TData[];
  /**
   * Whether to enable row selection (default: false)
   */
  enableSelection?: boolean;
  /**
   * State for expanded rows
   */
  expanded?: ExpandedState;
  /**
   * Configuration for built-in row expansion
   */
  expansion?: {
    /**
     * Base indentation in pixels (default: 12)
     */
    baseIndentation?: number;
    /**
     * Indentation per depth level in pixels (default: 12)
     */
    depthIndentation?: number;
    /**
     * Whether to show expansion UI (chevron buttons)
     */
    enabled: boolean;
    /**
     * Whether to highlight parent/child rows with background colors (default: false)
     */
    highlightRows?: boolean;
    /**
     * Minimum number of subRows to show expand button (default: 2)
     */
    minSubRowsForExpansion?: number;
  };
  /**
   * Whether the table should stretch to fill its container width.
   * When true (default), columns will grow proportionally to fill available space.
   * When false, columns use their defined sizes and table scrolls horizontally if needed.
   * @default true
   */
  fillWidth?: boolean;
  /**
   * Optional function to compute a background color for each row based on its data.
   * Returns a CSS color string to apply as an inline style to cells.
   * Useful for conditional row highlighting (e.g., highlighting qualifying R&D PRs).
   *
   * @param row - The row data
   * @param isDarkMode - Whether the app is in dark mode
   * @returns A CSS color string or undefined
   *
   * @example
   * getRowBackgroundColor={row =>
   *   row.isQualifying ? "var(--wv-bg-status-success-subtle)" : undefined
   * }
   */
  getRowBackgroundColor?: (row: TData, isDarkMode: boolean) => string | undefined;
  getRowCanExpand?: (row: Row<TData>) => boolean;
  /**
   * Optional function to compute a CSS class name for each row based on its data.
   * Useful for conditional row styling beyond background colors.
   *
   * @example
   * getRowClassName={(row) => row.isSpecial ? "font-bold" : undefined}
   */
  getRowClassName?: (row: TData) => string | undefined;
  /**
   * Function to derive a unique ID for each row.
   * When not provided, the row index is used, which can cause stale UI
   * when rows are removed or reordered.
   */
  getRowId?: (originalRow: TData, index: number) => string;
  /**
   * Function to get sub rows for expanding functionality
   */
  getSubRows?: (originalRow: TData) => TData[] | undefined;
  /**
   * Column IDs to hide from the table
   */
  hiddenColumns?: string[];
  /**
   * Callback for when expanded state changes
   */
  onExpandedChange?: (
    updaterOrValue: ((old: ExpandedState) => ExpandedState) | ExpandedState,
  ) => void;
  /**
   * Makes rows clickable with the mouse. Rows are not focusable, so give each row a real control
   * (e.g. a button in its first cell) whose click bubbles up here for keyboard users.
   */
  onRowClick?: (row: TData) => void;
  /**
   * Callback for when row selection state changes
   */
  onRowSelectionChange?: (
    updaterOrValue: ((old: RowSelectionState) => RowSelectionState) | RowSelectionState,
  ) => void;
  /**
   * Callback for when sorting state changes
   */
  onSortingChange?: (updaterOrValue: ((old: SortingState) => SortingState) | SortingState) => void;
  /**
   * Callback to receive the virtualizer instance for external scroll control.
   * May be called more than once if the virtualizer instance changes (e.g. due to data updates).
   */
  onVirtualizerReady?: (virtualizer: Virtualizer<HTMLDivElement, Element>) => void;
  /**
   * IDs of columns that should be pinned to the left.
   */
  pinnedColumns?: string[];
  /**
   * Optional action overlaid on the trailing edge of the first visible cell. It is revealed on row
   * hover or focus and consumes no layout width, so the cell's content keeps the full column. The
   * reveal needs rows marked with `group` through {@link DataTableProps.getRowClassName}.
   */
  renderRowAction?: (row: TData) => React.ReactNode;
  /**
   * State for row selection
   */
  rowSelection?: RowSelectionState;
  /**
   * Row indices that should always be rendered even when outside the visible range.
   * Useful for keeping selected cells in the DOM for copy operations.
   */
  selectedRowIndices?: Set<number>;
  /**
   * State for sorted columns
   */
  sorting?: SortingState;
  /**
   * External table instance. If provided, DataTable will use this instead of creating its own.
   * Useful when you need to share table state with external hooks (e.g., cell selection).
   */
  table?: TanstackTable<TData>;
  /**
   * Configuration for row virtualization.
   */
  virtualization?: {
    /** Number of rows to render above and below the visible range (default: 4) */
    overscan?: number;
    /** Height of each row in pixels (default: 49) */
    rowHeight?: number;
  };
}

function fillWidthFlex<TData>(
  column: Column<TData>,
  columns: readonly Column<TData>[],
  sizeVar: string,
) {
  const grows =
    column.columnDef.meta?.grow === true ||
    !columns.some(other => other.columnDef.meta?.grow === true);
  return `${grows ? 1 : 0} 0 ${sizeVar}`;
}

function getCommonPinningStyles<TData>(
  column: Column<TData>,
  options?: { backgroundColor?: string; hasRowClassName?: boolean },
): React.CSSProperties {
  const isPinned = column.getIsPinned();

  // Only set backgroundColor if:
  // 1. An explicit backgroundColor is provided, OR
  // 2. The column is pinned (needs background to prevent content showing through), AND no custom row class
  const shouldSetBackground =
    options?.backgroundColor != null || (isPinned !== false && options?.hasRowClassName !== true);

  return {
    backgroundColor: shouldSetBackground
      ? (options?.backgroundColor ?? "var(--wv-bg-surface)")
      : undefined,
    left: isPinned === "left" ? `${column.getStart("left")}px` : undefined,
    position: isPinned !== false ? "sticky" : "relative",
    right: isPinned === "right" ? `${column.getAfter("right")}px` : undefined,
    zIndex: isPinned !== false ? 1 : 0,
  };
}

/**
 * Sticky left/right styles for pinned header cells. Vertical stickiness (keeping the
 * header row itself pinned to the top of the scroll container) lives on `<thead>`
 * instead (see the `sticky top-0` class on `Table.Header` below) — putting `position:
 * sticky; top: 0` on the `<th>` looks equivalent but isn't: the virtualized header row
 * (`<tr>` set to `display: flex`; `<th>` set to `display: inline-flex`) still respects
 * `<thead>` stickiness fine, but a flex-row `<th>` cannot itself stick vertically once
 * its owning `<tr>` is no longer laid out as a row in a table — only `<thead>`'s
 * `table-header-group` box participates in the table's sticky-positioning chain.
 * Inline `position` is required so unstyled non-pinned cells aren't overridden by
 * body-cell pinning helpers that default them to `position: relative`.
 */
function getHeaderStickyStyles<TData>(column: Column<TData>): React.CSSProperties {
  const isPinned = column.getIsPinned();
  if (isPinned === false) return {};

  return {
    backgroundColor: "var(--wv-bg-surface)",
    left: isPinned === "left" ? `${column.getStart("left")}px` : undefined,
    position: "sticky",
    right: isPinned === "right" ? `${column.getAfter("right")}px` : undefined,
    // Above body cells (z-index 1) so rows don't paint over the header.
    zIndex: 12,
  };
}

interface MemoizedTableRowProps<TData> {
  cellSelection?: {
    getCellSelectionState?: (
      cell: Cell<TData, unknown>,
      rowIndex?: number,
      colIndex?: number,
    ) => {
      isSelected: boolean;
      isSelectionBottom: boolean;
      isSelectionLeft: boolean;
      isSelectionRight: boolean;
      isSelectionTop: boolean;
    };
    handleCellMouseDown: (e: React.MouseEvent<HTMLElement>, cell: Cell<TData, unknown>) => void;
    handleCellMouseOver: (e: React.MouseEvent<HTMLElement>, cell: Cell<TData, unknown>) => void;
    handleCellMouseUp: (e: React.MouseEvent<HTMLElement>, cell: Cell<TData, unknown>) => void;
    isCellSelected: (cell: Cell<TData, unknown>) => boolean;
  };
  /** Column order for drag-and-drop reordering - changes trigger re-render */
  columnOrder?: string[];
  expansion?: {
    baseIndentation?: number;
    depthIndentation?: number;
    enabled: boolean;
    highlightRows?: boolean;
    minSubRowsForExpansion?: number;
  };
  /** Whether cells should use flex to fill available width */
  fillWidth?: boolean;
  hasAnyExpandableRows: boolean;
  // Pass expansion state as props to enable proper memoization
  isExpanded: boolean;
  isSelected: boolean;
  onRowClick?: (row: TData) => void;
  parentIsExpanded: boolean | undefined;
  renderRowAction?: (row: TData) => React.ReactNode;
  row: Row<TData>;
  /**
   * Optional background color to apply to this row's cells (from getRowBackgroundColor prop).
   */
  rowBackgroundColor?: string;
  /**
   * Optional CSS class name to apply to this row (from getRowClassName prop).
   */
  rowClassName?: string;
  rowIndex: number;
  /** Style for virtualized rows with absolute positioning */
  style?: React.CSSProperties;
}

/**
 * Memoized table row component that only re-renders when this specific row is affected by expansion changes.
 * This prevents unnecessary re-renders of all rows when only a subset should update.
 */
function MemoizedTableRowInner<TData>({
  cellSelection,
  expansion,
  fillWidth,
  hasAnyExpandableRows,
  isExpanded,
  isSelected,
  onRowClick,
  parentIsExpanded: _parentIsExpanded,
  renderRowAction,
  row,
  rowBackgroundColor: customRowBackgroundColor,
  rowClassName,
  rowIndex,
  style,
}: MemoizedTableRowProps<TData>) {
  const hasChildren = Boolean(
    expansion?.enabled === true &&
    row.getCanExpand() &&
    row.subRows.length >= (expansion.minSubRowsForExpansion ?? 2),
  );

  const isChildRow = row.depth > 0;
  const isExpandedParent = hasChildren && isExpanded;

  // Determine the background color for this row
  const rowBackgroundColor = useMemo(() => {
    // Apply custom row background color if provided (e.g., for R&D qualifying rows or wasted licenses)
    if (customRowBackgroundColor != null) {
      return customRowBackgroundColor;
    }

    // Only apply highlighting if explicitly enabled
    if (expansion?.highlightRows === true) {
      // Only apply backgrounds to expanded parents and their descendants
      if (isExpandedParent || isChildRow) {
        // Named interaction tints step by depth and adapt to light and dark on their own
        if (row.depth === 0) {
          return "var(--wv-bg-selected)";
        }
        return row.depth === 1 ? "var(--wv-bg-hover)" : "var(--wv-bg-active)";
      }
      // Collapsed rows have no special background
    }
    return undefined;
  }, [customRowBackgroundColor, expansion?.highlightRows, row.depth, isExpandedParent, isChildRow]);

  const cellBackgroundColor = isSelected ? "var(--wv-bg-action-subtle-active)" : rowBackgroundColor;

  return (
    <Table.Row
      key={row.id}
      data-row-id={row.id}
      data-state={isSelected ? "selected" : undefined}
      data-depth={row.depth}
      data-expandable={hasChildren}
      data-expanded={isExpandedParent}
      interactive={onRowClick != null}
      onClick={onRowClick == null ? undefined : () => onRowClick(row.original)}
      className={cn(
        onRowClick != null && "cursor-pointer",
        isChildRow && "data-table-child-row",
        hasChildren && "data-table-expandable-row",
        isExpandedParent && "data-table-expanded-parent",
        // Remove border for highlighted rows to create seamless shaded sections
        expansion?.highlightRows === true && (isExpandedParent || isChildRow) && "border-b-0",
        // Change border color to match selection color for selected rows
        isSelected && "border-b-wv-action",
        // The row action overlay is revealed with group-hover/group-focus-within
        renderRowAction != null && "group",
        // Custom row class from getRowClassName prop
        rowClassName,
      )}
      style={style}
    >
      {row.getVisibleCells().map((cell, cellIndex) => {
        const isExpandColumn = expansion?.enabled === true && cellIndex === 0;
        const isFirstCell = cellIndex === 0;
        const selectionState = cellSelection?.getCellSelectionState?.(cell, rowIndex, cellIndex);
        const isCellSelected =
          selectionState?.isSelected ?? cellSelection?.isCellSelected(cell) ?? false;
        // Use the same CSS variable as headers to ensure alignment
        const cellSizeVar = columnSizeCss(cell.column.id);
        const paintedCellBackground = isCellSelected
          ? "var(--wv-bg-action-subtle)"
          : cellBackgroundColor;
        const renderedRowAction = isFirstCell ? renderRowAction?.(row.original) : null;

        return (
          <Table.Cell
            key={cell.id}
            className={cn(
              "relative inline-flex items-center overflow-hidden",
              isExpandColumn && "data-table-expand-cell",
            )}
            onMouseDown={e => cellSelection?.handleCellMouseDown(e, cell)}
            onMouseUp={e => cellSelection?.handleCellMouseUp(e, cell)}
            onMouseOver={e => cellSelection?.handleCellMouseOver(e, cell)}
            style={{
              ...getCommonPinningStyles(cell.column, {
                backgroundColor: paintedCellBackground,
                hasRowClassName: rowClassName != null,
              }),
              paddingLeft:
                isExpandColumn === true
                  ? `${(expansion.baseIndentation ?? 12) + row.depth * (expansion.depthIndentation ?? 12)}px`
                  : undefined,
              ...(fillWidth === true
                ? {
                    flex: fillWidthFlex(
                      cell.column,
                      cell.getContext().table.getVisibleLeafColumns(),
                      cellSizeVar,
                    ),
                  }
                : { width: cellSizeVar }),
            }}
          >
            {isCellSelected && (
              <div
                className={cn(
                  "pointer-events-none absolute inset-0 border-wv-action",
                  selectionState != null
                    ? cn(
                        selectionState.isSelectionTop && "border-t",
                        selectionState.isSelectionBottom && "border-b",
                        selectionState.isSelectionLeft && "border-l",
                        selectionState.isSelectionRight && "border-r",
                      )
                    : "border",
                )}
              />
            )}
            {isExpandColumn === true ? (
              <div className="flex min-w-0 flex-1 items-center gap-2">
                {hasAnyExpandableRows === true && (
                  <div className="relative flex w-6 shrink-0">
                    {hasChildren === true && (
                      <DefaultExpandButton
                        isExpanded={isExpanded}
                        onToggle={row.getToggleExpandedHandler()}
                      />
                    )}
                  </div>
                )}
                <div className="flex min-w-0 flex-1 items-center truncate">
                  {flexRender(cell.column.columnDef.cell, cell.getContext())}
                </div>
              </div>
            ) : (
              // Gives cell content the column's width in both layouts: width-relative content
              // (w-full bars, text-right, truncate) collapses or overflows without it.
              <div className="min-w-0 flex-1">
                {flexRender(cell.column.columnDef.cell, cell.getContext())}
              </div>
            )}
            {renderedRowAction != null && (
              <div className="pointer-events-none absolute inset-y-0 right-0 flex items-center pl-6 pr-2 opacity-0 transition-opacity group-focus-within:opacity-100 group-hover:opacity-100 [@media(hover:none)]:opacity-100">
                <div className="absolute inset-0 bg-gradient-to-l from-wv-surface from-[calc(100%-1.5rem)] to-transparent" />
                {/*
                  Translucent tints (selection, custom row colors) composite over the page
                  background, so the fade needs the same two layers or it shows as a pale block.
                  Opaque interaction tokens already carry their own surface color, so a second
                  underlay would show as a visible band.
                */}
                {paintedCellBackground != null &&
                  paintedCellBackground !== "var(--wv-bg-selected)" &&
                  paintedCellBackground !== "var(--wv-bg-hover)" &&
                  paintedCellBackground !== "var(--wv-bg-active)" && (
                    <div
                      className="absolute inset-0"
                      style={{
                        background: `linear-gradient(to left, ${paintedCellBackground} calc(100% - 1.5rem), transparent)`,
                      }}
                    />
                  )}
                {/*
                  Pointer events stay off until the action is visible, otherwise the invisible
                  control sits over the tail of the cell's content and swallows clicks meant for it.
                */}
                <div className="pointer-events-none relative group-focus-within:pointer-events-auto group-hover:pointer-events-auto [@media(hover:none)]:pointer-events-auto">
                  {renderedRowAction}
                </div>
              </div>
            )}
          </Table.Cell>
        );
      })}
    </Table.Row>
  );
}

function areRowPropsEqual<TData>(
  prevProps: MemoizedTableRowProps<TData>,
  nextProps: MemoizedTableRowProps<TData>,
): boolean {
  const prevRow = prevProps.row;
  const nextRow = nextProps.row;

  // 0. If row index changed
  if (prevProps.rowIndex !== nextProps.rowIndex) {
    return false;
  }

  // 1. If this row's expansion state changed (most important for parent rows)
  if (prevProps.isExpanded !== nextProps.isExpanded) {
    return false; // Re-render needed
  }

  // 2. If this row became visible/hidden (parent expansion changed)
  if (prevProps.parentIsExpanded !== nextProps.parentIsExpanded) {
    return false; // Re-render needed
  }

  // 3. If the row's expandable state changed (children added/removed)
  const prevCanExpand = prevRow.getCanExpand();
  const nextCanExpand = nextRow.getCanExpand();
  if (prevCanExpand !== nextCanExpand) {
    return false; // Re-render needed
  }

  // 4. If the row's selection state changed
  if (prevProps.isSelected !== nextProps.isSelected) {
    return false; // Re-render needed
  }

  // 5. If expansion settings changed
  if (
    prevProps.expansion?.enabled !== nextProps.expansion?.enabled ||
    prevProps.expansion?.highlightRows !== nextProps.expansion?.highlightRows ||
    prevProps.hasAnyExpandableRows !== nextProps.hasAnyExpandableRows
  ) {
    return false; // Re-render needed
  }

  // 6. If cell selection changed (force re-render to update cell highlighting)
  if (prevProps.cellSelection !== nextProps.cellSelection) {
    return false; // Re-render needed
  }

  // 7. If row className changed (from getRowClassName)
  if (prevProps.rowClassName !== nextProps.rowClassName) {
    return false; // Re-render needed
  }

  if (prevProps.renderRowAction !== nextProps.renderRowAction) {
    return false;
  }

  if (prevProps.onRowClick !== nextProps.onRowClick) {
    return false;
  }

  // 8. If row background color changed (from getRowBackgroundColor)
  if (prevProps.rowBackgroundColor !== nextProps.rowBackgroundColor) {
    return false; // Re-render needed
  }

  // 9. If column order changed (for drag-and-drop reordering)
  if (prevProps.columnOrder !== nextProps.columnOrder) {
    return false; // Re-render needed
  }

  // Check if the row object itself changed (new reference)
  if (prevRow !== nextRow) {
    return false; // Re-render needed
  }

  // 7. If the style changed (for virtualized rows)
  if (prevProps.style !== nextProps.style && !deepEqual(prevProps.style, nextProps.style)) {
    return false; // Re-render needed
  }

  // Otherwise, no re-render needed
  return true; // Skip re-render
}

const MemoizedTableRow = React.memo(MemoizedTableRowInner, areRowPropsEqual) as <TData>(
  props: MemoizedTableRowProps<TData>,
) => JSX.Element;

/**
 * Renders a table with data.
 */
export function DataTable<TData>({
  afterContent,
  cellSelection,
  className,
  columnOrder,
  columns,
  containerClassName,
  data,
  enableSelection,
  expanded,
  expansion,
  fillWidth = true,
  getRowBackgroundColor,
  getRowCanExpand,
  getRowClassName,
  getRowId,
  getSubRows,
  hiddenColumns,
  onExpandedChange,
  onRowClick,
  onRowSelectionChange,
  onSortingChange,
  onVirtualizerReady,
  pinnedColumns,
  renderRowAction,
  rowSelection,
  selectedRowIndices,
  sorting,
  table: externalTable,
  virtualization,
}: DataTableProps<TData>) {
  // oxlint-disable-next-line react/incompatible-library -- existing warning baseline
  const internalTable = useReactTable({
    columnResizeMode: "onChange",
    columns,
    data,
    enableRowSelection: enableSelection ?? false,
    getCoreRowModel: getCoreRowModel(),
    getExpandedRowModel: getExpandedRowModel(),
    getRowCanExpand,
    getRowId,
    getSortedRowModel: getSortedRowModel(),
    getSubRows,
    initialState: {
      columnPinning: { left: pinnedColumns },
      columnVisibility: hiddenColumns?.reduce(
        (acc, columnId) => {
          acc[columnId] = false;
          return acc;
        },
        {} as Record<string, boolean>,
      ),
    },
    onExpandedChange,
    ...(onRowSelectionChange != null ? { onRowSelectionChange } : {}),
    ...(onSortingChange != null ? { onSortingChange } : {}),
    state: {
      ...(columnOrder != null ? { columnOrder } : {}),
      expanded,
      ...(rowSelection != null ? { rowSelection } : {}),
      ...(sorting != null ? { sorting } : {}),
    },
  });

  // Use external table if provided, otherwise use internal
  const table = externalTable ?? internalTable;

  const rows = table.getRowModel().rows;
  const cssVars = useTableCSSVariables(table, expansion);

  // Get theme for row background color callback
  const { resolvedTheme } = useTheme();
  const isDarkMode = resolvedTheme === "dark";

  // Check if table has any expandable rows to determine if we need expand button space
  const hasAnyExpandableRows =
    expansion?.enabled === true &&
    rows.some(
      row => row.getCanExpand() && row.subRows.length >= (expansion.minSubRowsForExpansion ?? 2),
    );

  // Virtualization setup
  const parentRef = useRef<HTMLDivElement>(null);

  const rowVirtualizer = useVirtualizer({
    count: rows.length,
    estimateSize: () => virtualization?.rowHeight ?? DEFAULT_ROW_HEIGHT,
    getScrollElement: () => parentRef.current,
    overscan: virtualization?.overscan ?? DEFAULT_OVERSCAN,
    rangeExtractor: range => {
      const defaultRange = defaultRangeExtractor(range);

      // Always include rows that have selected cells
      if (selectedRowIndices == null || selectedRowIndices.size === 0) {
        return defaultRange;
      }

      const values = new Set([...defaultRange, ...selectedRowIndices]);
      return Array.from(values).sort((a, b) => a - b);
    },
  });

  useEffect(() => {
    onVirtualizerReady?.(rowVirtualizer);
  }, [onVirtualizerReady, rowVirtualizer]);

  // The empty state spans every column, so in a horizontally scrollable table the cell is as
  // wide as the full content and centers off-screen. Measure the visible scroll viewport (only
  // while empty) so the empty state can be sized to it and centered in view on both axes.
  const isEmpty = rows.length === 0;
  const [emptyStateSize, setEmptyStateSize] = useState<{ height: number; width: number }>({
    height: 0,
    width: 0,
  });
  useEffect(() => {
    const container = parentRef.current;
    if (container == null || !isEmpty) return;

    const updateSize = () => {
      setEmptyStateSize({ height: container.clientHeight, width: container.clientWidth });
    };
    updateSize();

    const observer = new ResizeObserver(updateSize);
    observer.observe(container);
    return () => observer.disconnect();
  }, [isEmpty]);

  const virtualItems = rowVirtualizer.getVirtualItems();

  // When fillWidth is true, use minWidth so table can stretch to fill container
  const tableWidth = fillWidth === true ? undefined : table.getTotalSize();
  const tableMinWidth = fillWidth === true ? table.getTotalSize() : undefined;

  return (
    <Table
      afterContent={afterContent}
      className={cn(className, "table-fixed")}
      containerClassName={containerClassName}
      containerRef={parentRef}
      style={{ ...cssVars, minWidth: tableMinWidth, width: tableWidth }}
    >
      <Table.Header className="sticky top-0 z-10 bg-wv-surface shadow-fake-border">
        {table.getHeaderGroups().map(headerGroup => (
          <Table.Row
            key={headerGroup.id}
            className={fillWidth === true ? "flex border-none" : "block border-none"}
            interactive={false}
          >
            {headerGroup.headers.map(header => {
              // Use header.column.id to match how CSS variables are set in useTableCSSVariables
              const headerSizeVar = columnSizeCss(header.column.id);
              return (
                <Table.Head
                  key={header.id}
                  className="inline-flex overflow-hidden p-0 shadow-fake-border"
                  colSpan={header.colSpan}
                  data-column-id={header.column.id}
                  style={{
                    ...getHeaderStickyStyles(header.column),
                    ...(fillWidth === true
                      ? {
                          flex: fillWidthFlex(
                            header.column,
                            table.getVisibleLeafColumns(),
                            headerSizeVar,
                          ),
                        }
                      : { width: headerSizeVar }),
                  }}
                >
                  {header.isPlaceholder
                    ? null
                    : flexRender(header.column.columnDef.header, header.getContext())}
                </Table.Head>
              );
            })}
          </Table.Row>
        ))}
      </Table.Header>

      <Table.Body
        className="relative"
        style={{
          // The virtualized rows are all absolutely positioned and contribute no
          // intrinsic height, so the body needs an explicit height for the table
          // (and its scroll container, and the sticky `<thead>`'s range of travel
          // within it) to size correctly. Only applied when there are rows so the
          // content-sized empty state keeps its default table layout.
          ...(rows.length > 0 && { height: `${rowVirtualizer.getTotalSize()}px` }),
          // Safari additionally ignores that specified height (and position:relative)
          // on a `table-row-group`, so `display: block` is needed there to make the
          // height apply; translateZ(0) forces it to act as a containing block for the
          // absolutely-positioned rows. Without it, internal-scroll (`h-full`) tables in
          // Safari only scroll as far as the rendered rows reach and the sticky
          // `<thead>` scrolls away with them.
          ...(rows.length > 0 && { display: "block" }),
          transform: "translateZ(0)",
        }}
      >
        {rows.length > 0 ? (
          virtualItems.map(({ index, size, start }) => {
            const row = rows[index];
            if (row == null) return null;

            return (
              <MemoizedTableRow
                key={row.id}
                cellSelection={cellSelection}
                columnOrder={columnOrder}
                expansion={expansion}
                fillWidth={fillWidth}
                hasAnyExpandableRows={hasAnyExpandableRows}
                isExpanded={row.getIsExpanded()}
                isSelected={row.getIsSelected()}
                onRowClick={onRowClick}
                parentIsExpanded={row.getParentRow()?.getIsExpanded()}
                row={row}
                rowBackgroundColor={getRowBackgroundColor?.(row.original, isDarkMode)}
                rowClassName={getRowClassName?.(row.original)}
                rowIndex={index}
                renderRowAction={renderRowAction}
                style={{
                  ...(fillWidth === true ? { display: "flex" } : {}),
                  height: `${size}px`,
                  position: "absolute",
                  top: 0,
                  transform: `translateY(${start}px)`,
                  width: "100%",
                }}
              />
            );
          })
        ) : (
          <Table.Row interactive={false}>
            <Table.Cell colSpan={columns.length} className="p-0">
              <div
                className="sticky left-0 flex min-h-64 w-full items-center justify-center"
                style={{
                  height: emptyStateSize.height > 0 ? emptyStateSize.height : undefined,
                  width: emptyStateSize.width > 0 ? emptyStateSize.width : undefined,
                }}
              >
                <DataTableEmpty />
              </div>
            </Table.Cell>
          </Table.Row>
        )}
      </Table.Body>
    </Table>
  );
}

/**
 * Renders a header cell for the table. Handles clicking to sort and resizing.
 */
DataTable.HeaderCell = DataTableHeaderCell;

/**
 * Renders a loading state for a table.
 */
DataTable.Loading = DataTableLoading;
