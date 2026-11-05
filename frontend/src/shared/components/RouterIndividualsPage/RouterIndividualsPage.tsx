"use client";

import { type ExpandedState, type SortingState } from "@tanstack/react-table";

import { useMemo, useState } from "react";

import { isUnavailable, useRouterDashboardDataSource, useRouterDashboardQuery } from "../../data";
import { LoadState } from "../../tools/LoadState";
import { Text } from "../atoms/Text";
import { DashboardPageFilters, useDashboardFilters } from "../DashboardPageFilters";
import { ErrorCard } from "../ErrorCard";
import { LoadStateWrapper } from "../LoadStateWrapper";
import { DataTable } from "../molecules/DataTable";
import { Page } from "../Page";
import { RouterUnavailableCard } from "../RouterCharts/RouterUnavailableCard";
import { buildRouterIndividualRows } from "./buildRouterIndividualRows";
import { RouterIndividualsColumn } from "./constants";
import { IndividualTableSearchBar } from "./search/IndividualTableSearchBar";
import {
  IndividualTableSearchProvider,
  useIndividualTableSearch,
} from "./search/IndividualTableSearchContext";
import { type RouterIndividualRow, RouterIndividualRowKind } from "./types";
import { useRouterIndividualsColumns } from "./useRouterIndividualsColumns";

const DEFAULT_SORTING: SortingState = [{ desc: true, id: RouterIndividualsColumn.Requests }];

function RouterIndividualsContent() {
  const { searchQuery } = useIndividualTableSearch();
  const filters = useDashboardFilters();
  const range = useMemo(
    () => ({ from: filters.filters.fromISO, to: filters.filters.toISO }),
    [filters.filters.fromISO, filters.filters.toISO],
  );
  // The interval control is hidden, so sparklines bucket daily unless the range is a single day.
  const granularity = filters.filters.granularity === "hour" ? "hour" : "day";
  const [sorting, setSorting] = useState(DEFAULT_SORTING);
  const [expanded, setExpanded] = useState<ExpandedState>(true);
  const individuals = useRouterDashboardQuery(
    dataSource =>
      isUnavailable(dataSource.individuals)
        ? Promise.reject(new Error(dataSource.individuals.message))
        : dataSource.individuals.list({ granularity, range }),
    [granularity, range],
  );
  const rows = useMemo(
    () => LoadState.map(individuals, people => buildRouterIndividualRows(people, searchQuery)),
    [individuals, searchQuery],
  );
  const columns = useRouterIndividualsColumns({
    individualSortDescending: sorting.some(
      sort => sort.id === RouterIndividualsColumn.Individual && sort.desc,
    ),
    range,
    searchQuery,
  });

  return (
    <>
      <div className="flex w-full items-center">
        <DashboardPageFilters hideGranularity result={filters} />
      </div>

      <div className="relative h-full w-full overflow-hidden">
        <LoadStateWrapper
          state={rows}
          loading={<DataTable.Loading containerClassName="h-full" columns={columns.length} />}
          loadingDelayMs={0}
          error={error => (
            <ErrorCard title="Could not load individuals" description={`Error: ${error.message}`} />
          )}
        >
          {tableRows => (
            <DataTable<RouterIndividualRow>
              columns={columns}
              containerClassName="h-full"
              data={tableRows}
              expanded={expanded}
              expansion={{ enabled: true, minSubRowsForExpansion: 1 }}
              getRowId={row => row.id}
              getSubRows={row =>
                row.kind === RouterIndividualRowKind.Group ? [...row.subRows] : undefined
              }
              onExpandedChange={setExpanded}
              onSortingChange={setSorting}
              pinnedColumns={[RouterIndividualsColumn.Individual]}
              sorting={sorting}
            />
          )}
        </LoadStateWrapper>
      </div>
    </>
  );
}

/**
 * Router usage per person, modeled on the main app's Individuals page: one row per person who can
 * route, with request, token, and cost totals over the range.
 */
export function RouterIndividualsPage() {
  const { individuals } = useRouterDashboardDataSource();

  return (
    <IndividualTableSearchProvider>
      <Page
        header={
          <div className="flex w-full min-w-0 items-center gap-6">
            <div className="min-w-0 flex-[0_1_auto] overflow-hidden pr-1">
              <Text variant="h4" as="h2" className="truncate">
                Individuals
              </Text>
            </div>

            {!isUnavailable(individuals) && (
              <div className="min-w-0 flex-1 basis-0 pl-1">
                <IndividualTableSearchBar />
              </div>
            )}
          </div>
        }
      >
        {isUnavailable(individuals) ? (
          <Page.Section>
            <RouterUnavailableCard title="Individuals" unavailable={individuals} />
          </Page.Section>
        ) : (
          <RouterIndividualsContent />
        )}
      </Page>
    </IndividualTableSearchProvider>
  );
}
