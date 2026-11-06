import "@tanstack/react-table";

// See https://tanstack.com/table/v8/docs/api/core/column-def#meta for reference.

declare module "@tanstack/react-table" {
  interface ColumnMeta<TData, TValue> {
    /** Whether this column is the last submetric in its health score group */
    isLastInGroup?: boolean;
    /** Whether this column is a submetric (nested under a health score parent column) */
    isSubmetric?: boolean;
    /** In a `fillWidth` table, only `grow` columns take the spare width (all do when none set it). */
    grow?: boolean;
    /** Plain-text column label, when the rendered header is a component. */
    label?: string;
  }
}
