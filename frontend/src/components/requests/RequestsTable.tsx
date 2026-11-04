"use client";

import { RequestDetail } from "@/components/requests/RequestDetail";
import { isFailedRequest, isRerouted } from "@/components/requests/requestFilters";
import {
  formatNumber,
  formatTimestamp,
  formatUSD,
  ProviderBadge,
  UserLabel,
} from "@/components/requests/requestLabels";
import { type MetricsDetailRow } from "@/lib/api";
import { cn } from "@/lib/cn";
import { ChevronDown, ChevronRight } from "lucide-react";
import { Fragment, useState } from "react";

const HEADERS: { label: string; right?: boolean }[] = [
  { label: "" },
  { label: "Time" },
  { label: "Status" },
  { label: "User" },
  { label: "Requested model" },
  { label: "Routed model" },
  { label: "Provider" },
  { label: "Decision" },
  { label: "Tokens in / out", right: true },
  { label: "Actual $", right: true },
  { label: "Latency", right: true },
];

const CELL = "whitespace-nowrap px-3 py-2.5";

export function RequestsTable({ rows }: { rows: MetricsDetailRow[] }) {
  const [expanded, setExpanded] = useState<string | null>(null);

  return (
    <table className="w-full border-collapse text-sm">
      <thead className="sticky top-0 z-10 bg-background shadow-[inset_0_-1px_0_hsl(var(--border))]">
        <tr>
          {HEADERS.map(h => (
            <th
              key={h.label}
              className={cn(CELL, "text-xs font-medium text-muted-foreground", h.right ? "text-right" : "text-left")}
            >
              {h.label}
            </th>
          ))}
        </tr>
      </thead>
      <tbody>
        {rows.map((r, idx) => {
          const rowKey = `${r.request_id}-${idx}`;
          const open = expanded === rowKey;
          return (
            <Fragment key={rowKey}>
              <tr
                className="cursor-pointer border-t border-border/50 hover:bg-muted/30"
                aria-expanded={open}
                onClick={() => setExpanded(open ? null : rowKey)}
              >
                <td className={cn(CELL, "w-6 text-muted-foreground")}>
                  {open ? <ChevronDown size={14} /> : <ChevronRight size={14} />}
                </td>
                <td className={CELL}>{formatTimestamp(r.timestamp)}</td>
                <td className={cn(CELL, "tabular-nums", isFailedRequest(r) && "text-danger")}>
                  {r.upstream_status_code || "—"}
                </td>
                <td className={CELL}><UserLabel email={r.user_email} /></td>
                <td className={CELL}>{r.requested_model || "—"}</td>
                <td className={cn(CELL, isRerouted(r) && "text-success")}>{r.decision_model || "—"}</td>
                <td className={CELL}><ProviderBadge provider={r.decision_provider} /></td>
                <td className={cn(CELL, "max-w-[220px] truncate text-muted-foreground")} title={r.decision_reason}>
                  {r.decision_reason || "—"}
                </td>
                <td className={cn(CELL, "text-right tabular-nums")}>
                  {formatNumber(r.input_tokens)} / {formatNumber(r.output_tokens)}
                </td>
                <td className={cn(CELL, "text-right tabular-nums")}>{formatUSD(r.actual_cost_usd)}</td>
                <td className={cn(CELL, "text-right tabular-nums")}>{formatNumber(r.total_latency_ms)}ms</td>
              </tr>
              {open && (
                <tr className="bg-muted/20">
                  <td colSpan={HEADERS.length}>
                    <RequestDetail row={r} />
                  </td>
                </tr>
              )}
            </Fragment>
          );
        })}
      </tbody>
    </table>
  );
}
