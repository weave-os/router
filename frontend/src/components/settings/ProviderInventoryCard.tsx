"use client";

import { Card } from "@/components/molecules/Card";
import { ModelStatusBadge } from "@/components/settings/ModelStatusBadge";
import { cn } from "@/lib/cn";
import {
  type ModelStatusUpdate,
  type ProviderInventory,
  type ProviderInventoryBinding,
} from "@/lib/api";
import { ChevronDown, KeyRound, Layers3 } from "lucide-react";
import { useState } from "react";

const TIER_ORDER: ProviderInventoryBinding["tier"][] = ["high", "mid", "low"];

export interface ProviderInventoryCardProps {
  provider: ProviderInventory;
  pendingBinding?: string;
  onStatusChange: (
    modelID: string,
    provider: string,
    status: ModelStatusUpdate,
  ) => Promise<void>;
}

function formatContextWindow(tokens: number) {
  if (tokens >= 1_000_000) {
    return `${(tokens / 1_000_000).toFixed(tokens % 1_000_000 === 0 ? 0 : 1)}M`;
  }
  if (tokens >= 1_000) return `${Math.round(tokens / 1_000)}K`;
  return String(tokens);
}

function formatPrice(value: number) {
  return value.toLocaleString(undefined, { maximumFractionDigits: 3 });
}

function ModelRow({
  binding,
  onStatusChange,
  pending,
  provider,
}: {
  binding: ProviderInventoryBinding;
  onStatusChange: ProviderInventoryCardProps["onStatusChange"];
  pending: boolean;
  provider: string;
}) {
  return (
    <li className="grid grid-cols-[minmax(0,1fr)_auto] gap-x-3 gap-y-2 border-t border-border/70 px-4 py-3 first:border-t-0">
      <div className="min-w-0">
        <p
          className="truncate font-mono text-xs font-medium text-foreground"
          title={binding.model_id}
        >
          {binding.model_id}
        </p>
        {binding.upstream_id !== binding.model_id && (
          <p
            className="truncate font-mono text-2xs text-muted-foreground"
            title={binding.upstream_id}
          >
            {binding.upstream_id}
          </p>
        )}
      </div>
      <ModelStatusBadge
        status={binding.status}
        reason={binding.status_reason}
        source={binding.status_source}
        updatedAt={binding.status_updated_at}
        adminPinned={binding.admin_pinned}
        interactive
        pending={pending}
        onStatusChange={status => onStatusChange(binding.model_id, provider, status)}
      />
      <div className="col-span-2 flex flex-wrap items-center gap-2 text-2xs text-muted-foreground">
        <span
          className={cn(
            "rounded px-1.5 py-0.5 font-medium capitalize",
            binding.tier === "high" && "bg-primary/10 text-primary",
            binding.tier === "mid" && "bg-warning/10 text-warning",
            binding.tier === "low" && "bg-muted text-muted-foreground",
          )}
        >
          {binding.tier}
        </span>
        <span>{formatContextWindow(binding.context_window)} context</span>
        <span aria-hidden="true">·</span>
        <span>
          ${formatPrice(binding.price_input_per_1m_usd)} in / $
          {formatPrice(binding.price_output_per_1m_usd)} out per 1M
        </span>
      </div>
    </li>
  );
}

export function ProviderInventoryCard({
  onStatusChange,
  pendingBinding,
  provider,
}: ProviderInventoryCardProps) {
  const [expanded, setExpanded] = useState(false);

  return (
    <Card className="gap-0 overflow-hidden p-0">
      <Card.Header className="gap-3 border-b border-border p-4">
        <div className="flex items-start justify-between gap-3">
          <div className="min-w-0">
            <Card.Title className="truncate text-base" title={provider.provider}>
              {provider.provider}
            </Card.Title>
            <div className="mt-1 flex flex-wrap gap-1.5">
              <span className="rounded bg-muted px-1.5 py-0.5 text-2xs font-medium capitalize text-muted-foreground">
                {provider.family}
              </span>
              {provider.is_gateway && (
                <span className="rounded bg-muted px-1.5 py-0.5 text-2xs text-muted-foreground">
                  Gateway
                </span>
              )}
              {provider.is_credential_only && (
                <span className="rounded bg-muted px-1.5 py-0.5 text-2xs text-muted-foreground">
                  Credential only
                </span>
              )}
            </div>
          </div>
          <div
            className={cn(
              "flex shrink-0 items-center gap-1.5 rounded-full border px-2 py-1 text-2xs",
              provider.deployment_key_present
                ? "border-success/30 bg-success/5 text-success"
                : "border-border text-muted-foreground",
            )}
            title={provider.api_key_env || "No deployment key environment variable"}
          >
            <KeyRound className="size-3" />
            {provider.deployment_key_present ? "API key ready" : "No API key"}
          </div>
        </div>
        <button
          type="button"
          onClick={() => setExpanded(value => !value)}
          aria-expanded={expanded}
          className="flex items-center justify-between rounded-md bg-muted/40 px-3 py-2 text-xs text-muted-foreground md:hidden"
        >
          <span className="flex items-center gap-2">
            <Layers3 className="size-3.5" />
            {provider.bindings.length} model bindings
          </span>
          <ChevronDown className={cn("size-4 transition-transform", expanded && "rotate-180")} />
        </button>
      </Card.Header>

      <Card.Content className={cn(expanded ? "block" : "hidden", "md:block")}>
        {provider.bindings.length === 0 ? (
          <p className="px-4 py-8 text-center text-xs text-muted-foreground">
            No catalog bindings
          </p>
        ) : (
          TIER_ORDER.map(tier => {
            const bindings = provider.bindings.filter(binding => binding.tier === tier);
            if (bindings.length === 0) return null;
            return (
              <section key={tier} className="border-t border-border first:border-t-0">
                <div className="flex items-center justify-between bg-muted/30 px-4 py-2">
                  <p className="text-2xs font-semibold uppercase tracking-wide text-muted-foreground">
                    {tier} tier
                  </p>
                  <span className="text-2xs text-muted-foreground">{bindings.length}</span>
                </div>
                <ul>
                  {bindings.map(binding => (
                    <ModelRow
                      key={binding.model_id}
                      binding={binding}
                      provider={provider.provider}
                      pending={pendingBinding === `${provider.provider}:${binding.model_id}`}
                      onStatusChange={onStatusChange}
                    />
                  ))}
                </ul>
              </section>
            );
          })
        )}
      </Card.Content>
    </Card>
  );
}
