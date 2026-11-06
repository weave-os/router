"use client";

import { Modal } from "@weave-os/ui/components/molecules/Modal";
import { cn } from "@weave-os/ui/tools/cn";
import { formatInteger } from "@weave-os/ui/tools/format";
import { LoadState } from "@weave-os/ui/tools/LoadState";
import { format } from "date-fns";

import { isUnavailable } from "../data/availability";
import {
  useRouterDashboardDataSource,
  useRouterDashboardQuery,
} from "../data/RouterDashboardDataProvider";

export interface DrillDownModalProps {
  /** ISO start of the bucket window (inclusive). */
  fromISO: string;
  /** ISO end of the bucket window (exclusive). */
  toISO: string;
  /** Human-readable title — used as the (sr-only) accessible name and on the footer. */
  title: string;
  /** Subtitle for screen readers / metric descriptor. */
  subtitle?: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

function formatUSD(v: number) {
  if (v === 0) return "$0.00";
  if (Math.abs(v) < 0.01) return `$${v.toFixed(4)}`;
  return `$${v.toFixed(2)}`;
}

/** Provider name → display label + Tailwind classes for the badge. Mirrors
 *  `internal/providers` constants on the Go side: anthropic, openai, google,
 *  openrouter. Unknown providers fall back to a neutral gray. */
const PROVIDER_BADGE: Record<string, { label: string; className: string }> = {
  anthropic: {
    label: "Anthropic",
    className: "bg-brand/10 text-brand ring-brand/20",
  },
  openai: {
    label: "OpenAI",
    className: "bg-success/10 text-success ring-success/20",
  },
  google: {
    label: "Google",
    className: "bg-primary/10 text-primary ring-primary/20",
  },
  openrouter: {
    label: "OpenRouter",
    className: "bg-accent text-foreground ring-border",
  },
};

function ProviderBadge({ provider }: { provider: string }) {
  if (provider === "") return <>—</>;
  const meta = PROVIDER_BADGE[provider] ?? {
    label: provider,
    className: "bg-muted text-muted-foreground ring-border",
  };
  return (
    <span
      className={cn(
        "inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium ring-1 ring-inset",
        meta.className,
      )}
    >
      {meta.label}
    </span>
  );
}

/** Client-app value → display label. Mirrors `NormalizeClientApp` on the Go
 *  side: claude-code, codex, cursor, gemini-cli. Unknown values render as-is. */
const CLIENT_APP_LABEL: Record<string, string> = {
  "claude-code": "Claude Code",
  codex: "Codex",
  cursor: "Cursor",
  "gemini-cli": "Gemini CLI",
};

/** Turn-type value → display label. Mirrors `internal/router/turntype` on the
 *  Go side. System turn types (probe, title_gen, compaction, classifier, recap) are
 *  automated traffic — render them muted so user turns stand out. */
const TURN_TYPE_LABEL: Record<string, string> = {
  main_loop: "Main loop",
  tool_result: "Tool result",
  sub_agent_dispatch: "Subagent",
  compaction: "Compaction",
  probe: "Probe",
  title_gen: "Title gen",
  classifier: "Classifier",
  recap: "Recap",
};

const SYSTEM_TURN_TYPES = new Set(["probe", "title_gen", "compaction", "classifier", "recap"]);

function TurnTypeLabel({ turnType }: { turnType: string }) {
  if (turnType === "") return <>—</>;
  const label = TURN_TYPE_LABEL[turnType] ?? turnType;
  if (SYSTEM_TURN_TYPES.has(turnType)) {
    return <span className="text-muted-foreground">{label}</span>;
  }
  return <>{label}</>;
}

interface ColumnDef {
  key: string;
  label: string;
  align?: "left" | "right";
  className?: string;
}

const COLUMNS: ColumnDef[] = [
  { key: "time", label: "Time", className: "w-[160px]" },
  { key: "user", label: "User" },
  { key: "tool", label: "AI tool" },
  { key: "turn_type", label: "Turn type" },
  { key: "requested", label: "Requested model" },
  { key: "decision", label: "Decision model" },
  { key: "provider", label: "Provider" },
  { key: "input_tokens", label: "Input tokens", align: "right" },
  { key: "output_tokens", label: "Output tokens", align: "right" },
  { key: "cache_write", label: "Cache write", align: "right" },
  { key: "cache_read", label: "Cache read", align: "right" },
  { key: "requested_cost_usd", label: "Requested $", align: "right" },
  { key: "actual_cost_usd", label: "Actual $", align: "right" },
  { key: "latency", label: "Latency", align: "right" },
  { key: "status", label: "Status", align: "right" },
];

export function DrillDownModal({
  fromISO,
  toISO,
  title,
  subtitle,
  open,
  onOpenChange,
}: DrillDownModalProps) {
  const { requests } = useRouterDashboardDataSource();
  const requestsState = useRouterDashboardQuery(
    open && !isUnavailable(requests)
      ? () => requests.list({ limit: 200, range: { from: fromISO, to: toISO } })
      : null,
    [open, fromISO, toISO, requests],
  );
  const rows = LoadState.isLoaded(requestsState) ? requestsState.value.rows : null;
  const loading = LoadState.isLoading(requestsState) || LoadState.isReloading(requestsState);
  const error = isUnavailable(requests)
    ? requests.message
    : LoadState.isError(requestsState)
      ? requestsState.error.message
      : null;

  return (
    <Modal open={open} onOpenChange={onOpenChange}>
      <Modal.Content className="h-[80dvh] w-[90dvw] max-w-screen-2xl gap-0 p-0">
        <Modal.Title className="sr-only">{title}</Modal.Title>
        {subtitle != null && <Modal.Description className="sr-only">{subtitle}</Modal.Description>}

        <div className="h-full overflow-auto">
          {loading && (
            <div className="py-12 text-center text-sm text-muted-foreground">Loading…</div>
          )}
          {error != null && (
            <div className="m-6 rounded-md border border-danger/30 bg-danger/5 p-3 text-sm text-danger">
              {error}
            </div>
          )}
          {!loading && error == null && rows != null && rows.length === 0 && (
            <div className="py-12 text-center text-sm text-muted-foreground">
              No requests in this window.
            </div>
          )}
          {!loading && error == null && rows != null && rows.length > 0 && (
            <table className="w-full border-collapse text-sm">
              <thead className="sticky top-0 z-10 bg-background shadow-[inset_0_-1px_0_hsl(var(--border))]">
                <tr>
                  {COLUMNS.map(col => (
                    <th
                      key={col.key}
                      className={cn(
                        "px-4 py-3 text-sm font-medium text-foreground",
                        col.align === "right" ? "text-right" : "text-left",
                        col.className,
                      )}
                    >
                      {col.label}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {rows.map((r, idx) => {
                  const isError = r.upstream_status_code >= 400;
                  const downgraded =
                    r.requested_model !== r.decision_model && r.decision_model !== "";
                  return (
                    <tr
                      key={`${r.request_id}-${idx}`}
                      className="border-border/50 border-t hover:bg-muted/30"
                    >
                      <td className="whitespace-nowrap px-4 py-3">
                        {format(new Date(r.timestamp), "yyyy-MM-dd h:mmaaa")}
                      </td>
                      <td className="whitespace-nowrap px-4 py-3">
                        {r.user_email === "" ? (
                          "—"
                        ) : (
                          <span className="font-medium">{r.user_email}</span>
                        )}
                      </td>
                      <td className="whitespace-nowrap px-4 py-3">
                        {r.client_app === ""
                          ? "—"
                          : (CLIENT_APP_LABEL[r.client_app] ?? r.client_app)}
                      </td>
                      <td className="whitespace-nowrap px-4 py-3">
                        <TurnTypeLabel turnType={r.turn_type} />
                      </td>
                      <td className="whitespace-nowrap px-4 py-3">{r.requested_model || "—"}</td>
                      <td
                        className={cn("whitespace-nowrap px-4 py-3", downgraded && "text-success")}
                      >
                        {r.decision_model || "—"}
                      </td>
                      <td className="whitespace-nowrap px-4 py-3">
                        <ProviderBadge provider={r.decision_provider} />
                      </td>
                      <td className="whitespace-nowrap px-4 py-3 text-right tabular-nums">
                        {formatInteger(r.input_tokens)}
                      </td>
                      <td className="whitespace-nowrap px-4 py-3 text-right tabular-nums">
                        {formatInteger(r.output_tokens)}
                      </td>
                      <td className="whitespace-nowrap px-4 py-3 text-right tabular-nums text-muted-foreground">
                        {r.cache_creation_tokens == null
                          ? "—"
                          : formatInteger(r.cache_creation_tokens)}
                      </td>
                      <td className="whitespace-nowrap px-4 py-3 text-right tabular-nums text-muted-foreground">
                        {r.cache_read_tokens == null ? "—" : formatInteger(r.cache_read_tokens)}
                      </td>
                      <td className="whitespace-nowrap px-4 py-3 text-right tabular-nums">
                        {formatUSD(r.requested_cost_usd)}
                      </td>
                      <td className="whitespace-nowrap px-4 py-3 text-right tabular-nums">
                        {formatUSD(r.actual_cost_usd)}
                      </td>
                      <td className="whitespace-nowrap px-4 py-3 text-right tabular-nums">
                        {r.total_latency_ms}ms
                      </td>
                      <td
                        className={cn(
                          "whitespace-nowrap px-4 py-3 text-right tabular-nums",
                          isError && "text-danger",
                        )}
                      >
                        {r.upstream_status_code || "—"}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          )}
        </div>
      </Modal.Content>
    </Modal>
  );
}
