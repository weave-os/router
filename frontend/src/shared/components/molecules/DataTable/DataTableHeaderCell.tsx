import { HeaderContext } from "@tanstack/react-table";

import React from "react";

import { cn } from "../../../tools/cn";
import { Icon } from "../../atoms/Icon";
import { Appearance } from "../../types";
import { Button } from "../Button";

export interface DataTableHeaderCellProps<TData, TValue>
  extends HeaderContext<TData, TValue>, React.PropsWithChildren {
  /** Optional className to apply to the header cell wrapper */
  className?: string;
}

export function DataTableHeaderCell<TData, TValue>({
  children,
  className,
  column,
  header,
}: DataTableHeaderCellProps<TData, TValue>) {
  const sort = column.getIsSorted();
  const resizeHandler = header.getResizeHandler();

  return (
    <div className={cn("group/header relative h-full w-full p-1", className)}>
      {column.getCanSort() ? (
        <Button
          onClick={column.getToggleSortingHandler()}
          appearance={Appearance.Hollow}
          size="sm"
          className="h-full w-full justify-start"
        >
          {children}

          {sort === "asc" && <Icon type="SortAsc" className="size-4" />}
          {sort === "desc" && <Icon type="SortDesc" className="size-4" />}
        </Button>
      ) : (
        <div className="flex h-full min-h-8 w-full items-center gap-2 whitespace-nowrap px-3 text-sm font-medium">
          {children}
        </div>
      )}

      {column.getCanResize() && (
        <div
          className={cn(
            "absolute bottom-2 right-0 top-2 w-0.5 cursor-col-resize touch-none select-none rounded-sm bg-wv-status-neutral-solid opacity-10 transition-all",
            { "opacity-80": column.getIsResizing() },
          )}
          onTouchStart={resizeHandler}
          onMouseDown={resizeHandler}
          onDoubleClick={() => column.resetSize()}
          title="Drag to resize column. Double click to reset size."
        />
      )}
    </div>
  );
}
