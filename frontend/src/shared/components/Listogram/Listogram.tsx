import { cn } from "../../tools/cn";
import { Skeleton } from "../atoms/Skeleton";
import { ListogramRow } from "./ListogramRow";
import { ListogramSelector } from "./ListogramSelector";

export interface ListogramProps extends React.ComponentProps<"div"> {}

/**
 * Renders a listogram.
 *
 * @example
 * <Listogram>
 *   <Listogram.Row {...row} />
 * </Listogram>
 */
export function Listogram({ children, className, ...props }: ListogramProps) {
  return (
    <div
      className={cn("flex h-full w-full flex-col gap-1 overflow-auto p-1", className)}
      {...props}
    >
      {children}
    </div>
  );
}

Listogram.Row = ListogramRow;
Listogram.Selector = ListogramSelector;

/**
 * Renders a loading skeleton for a listogram.
 */
Listogram.Loading = function ListogramLoading({ rows = 5 }: { rows?: number }) {
  return (
    <Listogram className="min-h-0 flex-1">
      {Array.from({ length: rows }, (_, i) => (
        <ListogramRow.Loading key={i}>
          <Skeleton.Text size="sm" className="w-32" />
        </ListogramRow.Loading>
      ))}
    </Listogram>
  );
};
