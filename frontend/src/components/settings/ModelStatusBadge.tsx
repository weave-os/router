"use client";

import { Popover } from "@/components/molecules/Popover";
import { Tooltip } from "@/components/molecules/Tooltip";
import { cn } from "@/lib/cn";
import { type ModelStatus, type ModelStatusUpdate } from "@/lib/api";
import { Check, LoaderCircle, RotateCcw } from "lucide-react";
import { useState } from "react";

const STATUS_STYLES: Record<ModelStatus, string> = {
  online: "bg-success",
  offline: "bg-muted-foreground",
  rate_limited: "bg-warning",
  maintenance: "bg-blue-500",
  error: "bg-danger",
};

const STATUS_LABELS: Record<ModelStatus, string> = {
  online: "Online",
  offline: "Offline",
  rate_limited: "Rate limited",
  maintenance: "Maintenance",
  error: "Error",
};

const ACTIONS: Array<{ status: ModelStatusUpdate; label: string }> = [
  { status: "online", label: "Set online" },
  { status: "offline", label: "Set offline" },
  { status: "maintenance", label: "Set maintenance" },
  { status: "auto", label: "Reset to auto" },
];

export interface ModelStatusBadgeProps {
  status: ModelStatus;
  reason?: string;
  updatedAt?: string;
  source?: string;
  adminPinned?: boolean;
  interactive?: boolean;
  pending?: boolean;
  onStatusChange?: (status: ModelStatusUpdate) => Promise<void>;
}

function formatTimestamp(value?: string) {
  if (!value) return "Not recorded";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleString();
}

export function ModelStatusBadge({
  adminPinned,
  interactive = false,
  onStatusChange,
  pending = false,
  reason,
  source,
  status,
  updatedAt,
}: ModelStatusBadgeProps) {
  const [open, setOpen] = useState(false);
  const label = STATUS_LABELS[status];
  const tooltip = (
    <div className="space-y-1">
      <p className="font-medium">{label}</p>
      <p>{reason || "No reason reported"}</p>
      <p className="opacity-75">Updated {formatTimestamp(updatedAt)}</p>
    </div>
  );
  const badgeContent = (
    <>
      {pending ? (
        <LoaderCircle className="size-2.5 animate-spin" />
      ) : (
        <span className={cn("size-2 rounded-full", STATUS_STYLES[status])} aria-hidden="true" />
      )}
      {label}
    </>
  );
  const badgeClassName = cn(
    "inline-flex items-center gap-1.5 rounded-full border border-border px-2 py-1 text-2xs font-medium",
    interactive
      ? "transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      : "cursor-default",
  );
  const badge = interactive ? (
    <button
      type="button"
      disabled={pending}
      aria-label={`${label}; change model status`}
      className={badgeClassName}
    >
      {badgeContent}
    </button>
  ) : (
    <span className={badgeClassName}>{badgeContent}</span>
  );

  if (!interactive || onStatusChange == null) {
    return (
      <Tooltip content={tooltip} interactiveChild>
        {badge}
      </Tooltip>
    );
  }

  async function select(next: ModelStatusUpdate) {
    if (onStatusChange == null) return;
    try {
      await onStatusChange(next);
      setOpen(false);
    } catch {
      // The parent surfaces the request error while the menu remains open for retry.
    }
  }

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <Tooltip content={tooltip} interactiveChild>
        <Popover.Trigger>{badge}</Popover.Trigger>
      </Tooltip>
      <Popover.Content align="end" className="w-52 p-1.5">
        <p className="px-2 py-1.5 text-2xs font-medium text-muted-foreground">
          Override status
        </p>
        {ACTIONS.map(action => {
          const selected = action.status === status && adminPinned === true;
          return (
            <button
              key={action.status}
              type="button"
              disabled={pending}
              onClick={() => void select(action.status)}
              className="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-xs hover:bg-muted disabled:opacity-50"
            >
              {action.status === "auto" ? (
                <RotateCcw className="size-3.5 text-muted-foreground" />
              ) : (
                <span className={cn("size-2 rounded-full", STATUS_STYLES[action.status])} />
              )}
              <span className="flex-1">{action.label}</span>
              {selected && <Check className="size-3.5" />}
            </button>
          );
        })}
        <p className="border-t border-border px-2 pt-2 text-2xs text-muted-foreground">
          {adminPinned ? "Admin override active" : `Automatic · ${source || "unknown source"}`}
        </p>
      </Popover.Content>
    </Popover>
  );
}
