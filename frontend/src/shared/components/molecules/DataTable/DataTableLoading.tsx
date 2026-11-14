import { range } from "../../../tools/array/range";
import { Skeleton } from "../../atoms/Skeleton";
import { Table, TableProps } from "../Table";

const NUM_ROWS = 10;
const COL_WIDTHS = [250, 200, 150, 150, 150, 200];

interface DataTableLoadingProps extends Pick<TableProps, "containerClassName"> {
  /**
   * The number of columns to render.
   *
   * @default 6
   */
  columns?: number;
}

/**
 * Renders a fake table loading state.
 */
export function DataTableLoading({
  columns = COL_WIDTHS.length,
  containerClassName,
}: DataTableLoadingProps) {
  return (
    <Table className="pointer-events-none table-fixed" containerClassName={containerClassName}>
      <Table.Header>
        <Table.Row className="border-none">
          {range(columns).map(i => (
            <Table.Head
              key={i}
              className="sticky top-0 z-10 bg-wv-surface"
              style={{ width: `${COL_WIDTHS[i % COL_WIDTHS.length]}px` }}
            >
              <Skeleton.Text size="sm" className="w-24" />
            </Table.Head>
          ))}
        </Table.Row>
      </Table.Header>
      <Table.Body>
        {range(NUM_ROWS).map(i => (
          <Table.Row key={i}>
            {range(columns).map(j => (
              <Table.Cell key={j} style={{ width: `${COL_WIDTHS[j % COL_WIDTHS.length]}px` }}>
                <Skeleton.Text size="sm" className="w-16" />
              </Table.Cell>
            ))}
          </Table.Row>
        ))}
      </Table.Body>
    </Table>
  );
}
