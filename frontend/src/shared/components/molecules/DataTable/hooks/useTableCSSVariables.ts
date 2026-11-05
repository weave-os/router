import { Table } from "@tanstack/react-table";

import React, { useMemo } from "react";

import { DataTableProps } from "../DataTable";
import { columnSizeVarName, headerSizeVarName } from "../helpers/columnSizeVar";

/**
 * Returns CSS variables to set on the table element.
 */
export function useTableCSSVariables<TData>(
  table: Table<TData>,
  expansion?: DataTableProps<TData>["expansion"],
): React.CSSProperties {
  const columnSizingInfo = table.getState().columnSizingInfo;
  const columnSizing = table.getState().columnSizing;
  // Track column visibility changes to recalculate CSS variables when columns are shown/hidden
  const columnVisibility = table.getState().columnVisibility;
  // Track the number of visible columns to detect when columns are added/removed
  const visibleColumnCount = table.getVisibleFlatColumns().length;

  return useMemo(() => {
    const headers = table.getFlatHeaders();
    const cssVars: React.CSSProperties = {};

    for (const header of headers) {
      // Use header.column.id consistently for both header and cell lookups
      cssVars[headerSizeVarName(header.column.id)] = header.getSize();
      cssVars[columnSizeVarName(header.column.id)] = header.column.getSize();
    }

    // Add indentation variables
    if (expansion?.enabled === true) {
      cssVars["--data-table-base-indent"] = `${expansion.baseIndentation ?? 24}px`;
      cssVars["--data-table-depth-indent"] = `${expansion.depthIndentation ?? 20}px`;
    }

    return cssVars;
    // oxlint-disable-next-line react/exhaustive-deps
  }, [table, expansion, columnSizingInfo, columnSizing, columnVisibility, visibleColumnCount]);
}
