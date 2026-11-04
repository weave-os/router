"use client";

import { Text } from "@/components/atoms/Text";
import { DateRangePill, useDashboardFilters } from "@/components/DashboardPageFilters";
import { Page } from "@/components/Page";
import { PageHeader } from "@/components/PageHeader";
import { RequestFilterBar } from "@/components/requests/RequestFilterBar";
import {
  EMPTY_REQUEST_FILTERS,
  filterRequests,
} from "@/components/requests/requestFilters";
import { RequestsTable } from "@/components/requests/RequestsTable";
import { api, type MetricsDetailRow } from "@/lib/api";
import { useEffect, useMemo, useState } from "react";

// The details endpoint caps a page at 1000 rows; the log shows the newest
// requests in the window up to that cap.
const REQUEST_LIMIT = 1000;

function RequestsBody({
  rows,
  error,
  filteredRows,
}: {
  rows: MetricsDetailRow[] | null;
  error: string | null;
  filteredRows: MetricsDetailRow[];
}) {
  if (error != null) {
    return (
      <div className="m-4 rounded-md border border-danger/30 bg-danger/5 p-3 text-sm text-danger">{error}</div>
    );
  }
  if (rows == null) return <div className="py-12 text-center text-sm text-muted-foreground">Loading…</div>;
  if (rows.length === 0) {
    return <div className="py-12 text-center text-sm text-muted-foreground">No requests in this window.</div>;
  }
  if (filteredRows.length === 0) {
    return <div className="py-12 text-center text-sm text-muted-foreground">No requests match these filters.</div>;
  }
  return <RequestsTable rows={filteredRows} />;
}

export default function RequestsPage() {
  const dashboardFilters = useDashboardFilters("this-week");
  const { fromISO, toISO } = dashboardFilters.filters;
  const [rows, setRows] = useState<MetricsDetailRow[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [filters, setFilters] = useState(EMPTY_REQUEST_FILTERS);

  useEffect(() => {
    let cancelled = false;
    setRows(null);
    setError(null);
    api.metrics
      .details(fromISO, toISO, REQUEST_LIMIT)
      .then(res => {
        if (!cancelled) setRows(res.rows);
      })
      .catch((err: unknown) => {
        if (!cancelled) setError(err instanceof Error ? err.message : "Failed to load requests.");
      });
    return () => {
      cancelled = true;
    };
  }, [fromISO, toISO]);

  const filteredRows = useMemo(() => filterRequests(rows ?? [], filters), [rows, filters]);

  return (
    <Page
      header={
        <PageHeader
          left={
            <Text variant="h4" as="h2" className="whitespace-nowrap">
              Requests
            </Text>
          }
        />
      }
    >
      <Page.Section>
        <div className="flex flex-row flex-wrap items-center gap-3">
          <DateRangePill result={dashboardFilters} />
          <RequestFilterBar rows={rows ?? []} filters={filters} onChange={setFilters} />
        </div>
        {rows != null && rows.length > 0 && (
          <p className="text-xs text-muted-foreground">
            {filteredRows.length.toLocaleString()} of {rows.length.toLocaleString()} requests
            {rows.length >= REQUEST_LIMIT ? ` (newest ${REQUEST_LIMIT.toLocaleString()} in this window)` : ""}
          </p>
        )}
        <div className="overflow-auto rounded-lg border border-border">
          <RequestsBody rows={rows} error={error} filteredRows={filteredRows} />
        </div>
      </Page.Section>
    </Page>
  );
}
