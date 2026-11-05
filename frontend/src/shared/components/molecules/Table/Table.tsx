import React from "react";

import { cn } from "../../../tools/cn";

type TableType = React.ForwardRefExoticComponent<TableProps> & {
  /**
   * Renders a styled `thead` element.
   */
  Header: typeof TableHeader;

  /**
   * Renders a styled `tbody` element.
   */
  Body: typeof TableBody;

  /**
   * Renders a styled `tfoot` element.
   */
  Footer: typeof TableFooter;

  /**
   * Renders a styled `tr` element.
   */
  Row: typeof TableRow;

  /**
   * Renders a styled `th` element.
   */
  Head: typeof TableHead;

  /**
   * Renders a styled `td` element.
   */
  Cell: typeof TableCell;
};

export interface TableProps extends React.HTMLAttributes<HTMLTableElement> {
  /**
   * Content rendered after the table element but inside the scroll container.
   * Useful for banners or notices that should appear at the bottom of scrollable content.
   */
  afterContent?: React.ReactNode;
  containerClassName?: string;
  containerRef?: React.RefObject<HTMLDivElement | null>;
}

/**
 * Renders a styled HTML table.
 *
 * @example
 * <Table>
 *   <Table.Header>
 *     <Table.Row>
 *       <Table.Head>Header 1</Table.Head>
 *       <Table.Head>Header 2</Table.Head>
 *     </Table.Row>
 *   </Table.Header>
 *   <Table.Body>
 *     <Table.Row>
 *       <Table.Cell>Cell 1</Table.Cell>
 *       <Table.Cell>Cell 2</Table.Cell>
 *     </Table.Row>
 *   </Table.Body>
 *   <Table.Footer>
 *     <Table.Row>
 *       <Table.Cell>Footer 1</Table.Cell>
 *       <Table.Cell>Footer 2</Table.Cell>
 *     </Table.Row>
 *   </Table.Footer>
 * </Table>
 */
export const Table = React.forwardRef<HTMLTableElement, TableProps>(
  ({ afterContent, className, containerClassName, containerRef, ...props }, ref) => (
    <div className={cn("relative w-full overflow-auto", containerClassName)} ref={containerRef}>
      <table ref={ref} className={cn("w-full caption-bottom text-body", className)} {...props} />
      {afterContent}
    </div>
  ),
) as TableType;
Table.displayName = "Table";

const TableHeader = React.forwardRef<
  HTMLTableSectionElement,
  React.HTMLAttributes<HTMLTableSectionElement>
>(({ className, ...props }, ref) => (
  <thead
    ref={ref}
    className={cn("[&_tr_th]:shadow-[inset_0_-1px_0_var(--wv-border-subtle)]", className)}
    {...props}
  />
));
TableHeader.displayName = "Table.Header";

const TableBody = React.forwardRef<
  HTMLTableSectionElement,
  React.HTMLAttributes<HTMLTableSectionElement>
>(({ className, ...props }, ref) => (
  <tbody ref={ref} className={cn("[&_tr:last-child]:border-0", className)} {...props} />
));
TableBody.displayName = "Table.Body";

const TableFooter = React.forwardRef<
  HTMLTableSectionElement,
  React.HTMLAttributes<HTMLTableSectionElement>
>(({ className, ...props }, ref) => (
  <tfoot
    ref={ref}
    className={cn(
      "border-t border-wv-subtle bg-wv-inset font-medium [&>tr]:last:border-b-0",
      className,
    )}
    {...props}
  />
));
TableFooter.displayName = "Table.Footer";

interface TableRowProps extends React.HTMLAttributes<HTMLTableRowElement> {
  /**
   * Whether the row is interactive. If false, the row will not have hover effects.
   *
   * @default true
   */
  interactive?: boolean;
}

// A row can't round its corners, so its cells paint the hover and focus fill and the end cells
// round it. The row control's full-row overlay draws the focus ring with the same radius.
/** For a row whose whole width opens something; pair with {@link TABLE_ROW_CONTROL_CLASS_NAME}. */
export const TABLE_LINK_ROW_CLASS_NAME =
  "[&:has(:focus-visible)>td]:bg-wv-hover [&:hover>td]:bg-wv-hover [&>td:first-child]:rounded-l-md [&>td:last-child]:rounded-r-md [&>td]:transition-colors";

/** Stretches a row's link or button over the whole row, so the full row is the click target. */
export const TABLE_ROW_CONTROL_CLASS_NAME =
  "outline-none after:absolute after:inset-0 after:rounded-md after:content-[''] focus-visible:after:outline focus-visible:after:outline-2 focus-visible:after:-outline-offset-2 focus-visible:after:outline-wv-focus";

const TableRow = React.forwardRef<HTMLTableRowElement, TableRowProps>(
  ({ className, interactive = true, ...props }, ref) => (
    <tr
      ref={ref}
      className={cn(
        "border-b border-wv-subtle transition-colors data-[state=selected]:bg-wv-selected",
        { "hover:bg-wv-hover": interactive },
        className,
      )}
      {...props}
    />
  ),
);
TableRow.displayName = "Table.Row";

const TableHead = React.forwardRef<
  HTMLTableCellElement,
  React.ThHTMLAttributes<HTMLTableCellElement>
>(({ className, ...props }, ref) => (
  <th
    ref={ref}
    className={cn(
      "h-12 px-4 text-left align-middle font-medium text-wv-secondary [&:has([role=checkbox])]:pr-0",
      className,
    )}
    {...props}
  />
));
TableHead.displayName = "Table.Head";

const TableCell = React.forwardRef<
  HTMLTableCellElement,
  React.TdHTMLAttributes<HTMLTableCellElement>
>(({ className, ...props }, ref) => (
  <td
    ref={ref}
    className={cn("h-12 px-4 align-middle [&:has([role=checkbox])]:pr-0", className)}
    {...props}
  />
));
TableCell.displayName = "Table.Cell";

// const TableCaption = React.forwardRef<
//   HTMLTableCaptionElement,
//   React.HTMLAttributes<HTMLTableCaptionElement>
// >(({ className, ...props }, ref) => (
//   <caption ref={ref} className={cn("mt-4 text-body text-wv-secondary", className)} {...props} />
// ));
// TableCaption.displayName = "Table.Caption";

Table.Header = TableHeader;
Table.Body = TableBody;
Table.Footer = TableFooter;
Table.Row = TableRow;
Table.Head = TableHead;
Table.Cell = TableCell;
// Table.Caption = TableCaption;
