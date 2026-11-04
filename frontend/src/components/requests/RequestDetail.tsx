import {
  ClientAppLabel,
  formatNumber,
  formatUSD,
  TurnTypeLabel,
} from "@/components/requests/requestLabels";
import { type MetricsDetailRow } from "@/lib/api";
import { type ReactNode } from "react";

interface DetailField {
  label: string;
  value: ReactNode;
}

function optionalTokens(v: number | null): string {
  return v == null ? "—" : formatNumber(v);
}

function detailFields(row: MetricsDetailRow): DetailField[] {
  const saved = row.requested_cost_usd - row.actual_cost_usd;
  return [
    { label: "Request ID", value: <span className="font-mono">{row.request_id}</span> },
    { label: "Requested model", value: row.requested_model || "—" },
    { label: "Routed model", value: row.decision_model || "—" },
    { label: "Provider", value: row.decision_provider || "—" },
    { label: "Decision", value: row.decision_reason || "—" },
    { label: "AI tool", value: <ClientAppLabel clientApp={row.client_app} /> },
    { label: "Turn type", value: <TurnTypeLabel turnType={row.turn_type} /> },
    { label: "Session pin", value: row.sticky_hit ? "Reused pinned model" : "Fresh decision" },
    { label: "Upstream status", value: row.upstream_status_code || "—" },
    { label: "Latency", value: `${formatNumber(row.total_latency_ms)}ms` },
    { label: "Input / output tokens", value: `${formatNumber(row.input_tokens)} / ${formatNumber(row.output_tokens)}` },
    {
      label: "Cache write / read",
      value: `${optionalTokens(row.cache_creation_tokens)} / ${optionalTokens(row.cache_read_tokens)}`,
    },
    { label: "Requested cost", value: formatUSD(row.requested_cost_usd) },
    { label: "Actual cost", value: formatUSD(row.actual_cost_usd) },
    { label: "Saved", value: formatUSD(saved) },
    { label: "Router user", value: row.router_user_id ? <span className="font-mono">{row.router_user_id}</span> : "—" },
  ];
}

export function RequestDetail({ row }: { row: MetricsDetailRow }) {
  return (
    <dl className="grid grid-cols-2 gap-x-6 gap-y-3 px-4 py-4 text-xs md:grid-cols-4">
      {detailFields(row).map(field => (
        <div className="min-w-0" key={field.label}>
          <dt className="text-muted-foreground">{field.label}</dt>
          <dd className="mt-0.5 break-words text-foreground">{field.value}</dd>
        </div>
      ))}
    </dl>
  );
}
