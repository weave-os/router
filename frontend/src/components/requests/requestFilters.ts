import { type MetricsDetailRow } from "@/lib/api";

export type RequestOutcome = "all" | "ok" | "error";

export interface RequestFilters {
  outcome: RequestOutcome;
  provider: string;
  model: string;
  search: string;
}

export const EMPTY_REQUEST_FILTERS: RequestFilters = {
  outcome: "all",
  provider: "",
  model: "",
  search: "",
};

export function isFailedRequest(row: MetricsDetailRow): boolean {
  return row.upstream_status_code >= 400;
}

export function isRerouted(row: MetricsDetailRow): boolean {
  return row.decision_model !== "" && row.requested_model !== row.decision_model;
}

function matchesSearch(row: MetricsDetailRow, needle: string): boolean {
  return [row.request_id, row.user_email, row.requested_model, row.decision_model, row.decision_reason]
    .some(field => field.toLowerCase().includes(needle));
}

export function filterRequests(rows: MetricsDetailRow[], filters: RequestFilters): MetricsDetailRow[] {
  const needle = filters.search.trim().toLowerCase();
  return rows.filter(row => {
    if (filters.outcome === "ok" && isFailedRequest(row)) return false;
    if (filters.outcome === "error" && !isFailedRequest(row)) return false;
    if (filters.provider !== "" && row.decision_provider !== filters.provider) return false;
    if (filters.model !== "" && row.decision_model !== filters.model) return false;
    return needle === "" || matchesSearch(row, needle);
  });
}

// The active selection stays listed even when a new date window has no rows
// for it, so the dropdown never hides the filter that is emptying the table.
export function distinctValues(
  rows: MetricsDetailRow[],
  pick: (row: MetricsDetailRow) => string,
  selected: string,
): string[] {
  return [...new Set([...rows.map(pick), selected].filter(v => v !== ""))].sort();
}
