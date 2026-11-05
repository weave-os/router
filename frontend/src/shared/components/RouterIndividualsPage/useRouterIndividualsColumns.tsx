"use client";

import { createColumnHelper } from "@tanstack/react-table";

import { useMemo } from "react";

import { type DateRange } from "../../data";
import { COLLATOR } from "../../tools/collator";
import { formatBigDecimal, formatRouterModelName, formatUSD } from "../../tools/format";
import { totalTokens } from "./buildRouterIndividualRows";
import {
  RouterExplainedEmptyCell,
  RouterIndividualNameCell,
  RouterIndividualsHeaderCell,
  RouterLastRequestCell,
  RouterModelMixCell,
  RouterModelMixLegend,
  RouterTotalCell,
} from "./cells/RouterIndividualsCells";
import { RouterSessionDrillDownCell } from "./cells/RouterSessionDrillDownCell";
import {
  CODE_OUTPUT_UNAVAILABLE_INFO,
  ROUTING_GROUP_ORDER,
  RouterIndividualsColumn,
  RouterRankedMetric,
} from "./constants";
import { type RouterIndividualRow, RouterIndividualRowKind } from "./types";

// Column widths match the main Individuals table (account, sparkline, and plain data columns).
const NAME_COLUMN_SIZE = 275;
const SPARKLINE_COLUMN_SIZE = 200;
const DATA_COLUMN_SIZE = 150;
const MODEL_MIX_COLUMN_SIZE = 240;

function personOf(row: RouterIndividualRow) {
  return row.kind === RouterIndividualRowKind.Person ? row : undefined;
}

export interface UseRouterIndividualsColumnsArgs {
  /** Whether the Individual column is sorted descending; routing sections ignore direction. */
  individualSortDescending: boolean;
  range: DateRange;
  searchQuery: string;
}

export function useRouterIndividualsColumns({
  individualSortDescending,
  range,
  searchQuery,
}: UseRouterIndividualsColumnsArgs) {
  return useMemo(() => {
    const columnHelper = createColumnHelper<RouterIndividualRow>();

    return [
      columnHelper.accessor(row => row.name, {
        cell: props => (
          <RouterIndividualNameCell row={props.row.original} searchQuery={searchQuery} />
        ),
        header: RouterIndividualsHeaderCell,
        id: RouterIndividualsColumn.Individual,
        size: NAME_COLUMN_SIZE,
        sortingFn: (rowA, rowB) => {
          const a = rowA.original;
          const b = rowB.original;
          if (
            a.kind === RouterIndividualRowKind.Group &&
            b.kind === RouterIndividualRowKind.Group
          ) {
            // TanStack negates comparator results for descending sorts; pre-negate so
            // sections keep their fixed order in either direction.
            const sectionOrder =
              ROUTING_GROUP_ORDER.indexOf(a.group) - ROUTING_GROUP_ORDER.indexOf(b.group);
            return individualSortDescending ? -sectionOrder : sectionOrder;
          }
          return COLLATOR.compare(a.name, b.name);
        },
      }),
      columnHelper.accessor(row => row.usage.requests, {
        cell: props => (
          <RouterSessionDrillDownCell range={range} row={props.row.original}>
            <RouterTotalCell
              rank={personOf(props.row.original)?.ranking[RouterRankedMetric.Requests]}
              sparkline={{
                label: "Requests per period",
                points: props.row.original.usage.requestActivity,
                passthroughBands: personOf(props.row.original)?.passthroughBands,
                passthroughPeriods: personOf(props.row.original)?.passthroughPeriods,
              }}
              value={props.getValue()}
            />
          </RouterSessionDrillDownCell>
        ),
        header: RouterIndividualsHeaderCell,
        id: RouterIndividualsColumn.Requests,
        size: SPARKLINE_COLUMN_SIZE,
        sortDescFirst: true,
      }),
      columnHelper.accessor(row => row.usage.cost, {
        cell: props => (
          <RouterSessionDrillDownCell range={range} row={props.row.original}>
            <RouterTotalCell
              formatValue={formatUSD}
              rank={personOf(props.row.original)?.ranking[RouterRankedMetric.Cost]}
              sparkline={{
                label: "Cost per period",
                points: props.row.original.usage.costActivity,
                passthroughBands: personOf(props.row.original)?.passthroughBands,
                passthroughPeriods: personOf(props.row.original)?.passthroughPeriods,
              }}
              value={props.getValue()}
            />
          </RouterSessionDrillDownCell>
        ),
        header: RouterIndividualsHeaderCell,
        id: RouterIndividualsColumn.Cost,
        size: SPARKLINE_COLUMN_SIZE,
        sortDescFirst: true,
      }),
      columnHelper.accessor(row => totalTokens(row.usage), {
        cell: props => (
          <RouterSessionDrillDownCell
            range={range}
            row={props.row.original}
            tooltip={
              <dl className="grid grid-cols-[auto_auto] gap-x-3 text-caption">
                {(
                  [
                    ["Input", props.row.original.usage.inputTokens],
                    ["Output", props.row.original.usage.outputTokens],
                    ["Cache read", props.row.original.usage.cacheReadTokens],
                    ["Cache write", props.row.original.usage.cacheCreationTokens],
                  ] as const
                ).map(([label, tokens]) => (
                  <div key={label} className="contents">
                    <dt className="text-wv-secondary">{label}</dt>
                    <dd className="text-right tabular-nums">{formatBigDecimal(tokens)}</dd>
                  </div>
                ))}
              </dl>
            }
          >
            <RouterTotalCell value={props.getValue()} />
          </RouterSessionDrillDownCell>
        ),
        header: RouterIndividualsHeaderCell,
        id: RouterIndividualsColumn.Tokens,
        size: DATA_COLUMN_SIZE,
        sortDescFirst: true,
      }),
      columnHelper.display({
        cell: () => <RouterExplainedEmptyCell explanation={CODE_OUTPUT_UNAVAILABLE_INFO} />,
        enableSorting: false,
        header: RouterIndividualsHeaderCell,
        id: RouterIndividualsColumn.CodeOutput,
        size: DATA_COLUMN_SIZE,
      }),
      columnHelper.accessor(
        row => {
          const topModel = personOf(row)?.person.modelMix?.models[0]?.model;
          return topModel == null ? undefined : formatRouterModelName(topModel);
        },
        {
          cell: props => {
            const modelMix = personOf(props.row.original)?.person.modelMix ?? null;
            return (
              <RouterSessionDrillDownCell
                range={range}
                row={props.row.original}
                tooltip={
                  modelMix == null ? undefined : <RouterModelMixLegend modelMix={modelMix} />
                }
              >
                <RouterModelMixCell modelMix={modelMix} />
              </RouterSessionDrillDownCell>
            );
          },
          header: RouterIndividualsHeaderCell,
          id: RouterIndividualsColumn.RoutedModels,
          size: MODEL_MIX_COLUMN_SIZE,
          sortUndefined: "last",
        },
      ),
      columnHelper.accessor(row => personOf(row)?.person.lastRequestAt ?? undefined, {
        cell: props => {
          const lastRequestAt = props.getValue();
          return (
            <RouterLastRequestCell
              lastRequestAt={lastRequestAt == null ? null : new Date(lastRequestAt)}
            />
          );
        },
        header: RouterIndividualsHeaderCell,
        id: RouterIndividualsColumn.LastRequest,
        size: DATA_COLUMN_SIZE,
        // Least recent first, to surface who has gone quiet; never-seen people sort last.
        sortDescFirst: false,
        sortUndefined: "last",
      }),
    ];
  }, [individualSortDescending, range, searchQuery]);
}
