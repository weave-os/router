"use client";

import {
  distinctValues,
  type RequestFilters,
  type RequestOutcome,
} from "@/components/requests/requestFilters";
import { type MetricsDetailRow } from "@/lib/api";

const CONTROL =
  "h-8 rounded-md border border-input bg-background px-2 text-xs shadow-sm focus-visible:border-foreground/40 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/30";

const OUTCOMES: { value: RequestOutcome; label: string }[] = [
  { value: "all", label: "All statuses" },
  { value: "ok", label: "Succeeded" },
  { value: "error", label: "Failed" },
];

interface RequestFilterBarProps {
  rows: MetricsDetailRow[];
  filters: RequestFilters;
  onChange: (filters: RequestFilters) => void;
}

export function RequestFilterBar({ rows, filters, onChange }: RequestFilterBarProps) {
  const providers = distinctValues(rows, r => r.decision_provider);
  const models = distinctValues(rows, r => r.decision_model);

  return (
    <>
      <select
        aria-label="Status"
        className={CONTROL}
        value={filters.outcome}
        onChange={e => onChange({ ...filters, outcome: e.target.value as RequestOutcome })}
      >
        {OUTCOMES.map(o => (
          <option key={o.value} value={o.value}>{o.label}</option>
        ))}
      </select>
      <select
        aria-label="Provider"
        className={CONTROL}
        value={filters.provider}
        onChange={e => onChange({ ...filters, provider: e.target.value })}
      >
        <option value="">All providers</option>
        {providers.map(p => <option key={p} value={p}>{p}</option>)}
      </select>
      <select
        aria-label="Routed model"
        className={CONTROL}
        value={filters.model}
        onChange={e => onChange({ ...filters, model: e.target.value })}
      >
        <option value="">All routed models</option>
        {models.map(m => <option key={m} value={m}>{m}</option>)}
      </select>
      <input
        type="search"
        aria-label="Search requests"
        placeholder="Search request ID, user, model, decision"
        className={`${CONTROL} w-72`}
        value={filters.search}
        onChange={e => onChange({ ...filters, search: e.target.value })}
      />
    </>
  );
}
