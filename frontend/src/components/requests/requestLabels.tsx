import { cn } from "@/lib/cn";

export function formatUSD(v: number): string {
  if (v === 0) return "$0.00";
  if (Math.abs(v) < 0.01) return `$${v.toFixed(4)}`;
  return `$${v.toFixed(2)}`;
}

export function formatNumber(v: number): string {
  return v.toLocaleString();
}

export function formatTimestamp(iso: string): string {
  const d = new Date(iso);
  const year = d.getFullYear();
  const month = String(d.getMonth() + 1).padStart(2, "0");
  const day = String(d.getDate()).padStart(2, "0");
  let hours = d.getHours();
  const minutes = String(d.getMinutes()).padStart(2, "0");
  const ampm = hours >= 12 ? "pm" : "am";
  hours = hours % 12;
  if (hours === 0) hours = 12;
  return `${year}-${month}-${day} ${hours}:${minutes}${ampm}`;
}

/** Provider name → display label + Tailwind classes for the badge. Unknown providers fall back to a neutral gray. */
const PROVIDER_BADGE: Record<
  string,
  { label: string; className: string }
> = {
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

export function ProviderBadge({ provider }: { provider: string }) {
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

export function ClientAppLabel({ clientApp }: { clientApp: string }) {
  if (clientApp === "") return <>—</>;
  return <>{CLIENT_APP_LABEL[clientApp] ?? clientApp}</>;
}

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

export function TurnTypeLabel({ turnType }: { turnType: string }) {
  if (turnType === "") return <>—</>;
  const label = TURN_TYPE_LABEL[turnType] ?? turnType;
  if (SYSTEM_TURN_TYPES.has(turnType)) {
    return <span className="text-muted-foreground">{label}</span>;
  }
  return <>{label}</>;
}

/** Renders the user identity for a drill-down row: the linked email when the
 *  router resolved one, otherwise an em dash. */
export function UserLabel({ email }: { email: string }) {
  if (email === "") return <>—</>;
  return <span className="font-medium">{email}</span>;
}
