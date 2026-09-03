"use client";

import { Card } from "@/components/molecules/Card";
import { ProviderInventoryCard } from "@/components/settings/ProviderInventoryCard";
import { SettingsPage } from "@/components/settings/SettingsPage";
import { ErrorBanner } from "@/components/settings/shared";
import {
  api,
  type ModelStatusEntry,
  type ModelStatusUpdate,
  type ProviderInventory,
} from "@/lib/api";
import { Activity, Boxes, Layers3 } from "lucide-react";
import { type ReactNode, useEffect, useMemo, useState } from "react";

function SummaryCard({
  icon,
  label,
  value,
}: {
  icon: ReactNode;
  label: string;
  value: string | number;
}) {
  return (
    <Card size="sm" className="gap-2">
      <div className="flex items-center gap-2 text-muted-foreground">
        {icon}
        <span className="text-2xs font-medium uppercase tracking-wide">{label}</span>
      </div>
      <p className="text-2xl font-semibold tracking-tight">{value}</p>
    </Card>
  );
}

function applyStatus(entry: ModelStatusEntry, providers: ProviderInventory[]) {
  return providers.map(provider => {
    if (provider.provider !== entry.provider) return provider;
    return {
      ...provider,
      bindings: provider.bindings.map(binding =>
        binding.model_id === entry.model_id
          ? {
              ...binding,
              status: entry.status,
              status_reason: entry.reason,
              status_source: entry.source,
              status_updated_at: entry.updated_at,
              status_expires_at: entry.expires_at,
              admin_pinned: entry.admin_pinned,
            }
          : binding,
      ),
    };
  });
}

export default function ProviderInventoryPage() {
  const [providers, setProviders] = useState<ProviderInventory[] | null>(null);
  const [pendingBinding, setPendingBinding] = useState<string>();
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    api.providerInventory
      .get()
      .then(response => setProviders(response.providers))
      .catch((err: unknown) =>
        setError(err instanceof Error ? err.message : "Failed to load provider inventory."),
      );
  }, []);

  const summary = useMemo(() => {
    const bindings = providers?.flatMap(provider => provider.bindings) ?? [];
    const models = new Set(bindings.map(binding => binding.model_id));
    const online = bindings.filter(binding => binding.status === "online").length;
    return {
      models: models.size,
      onlineRate:
        bindings.length === 0 ? "—" : `${Math.round((online / bindings.length) * 100)}%`,
    };
  }, [providers]);

  async function updateStatus(modelID: string, provider: string, status: ModelStatusUpdate) {
    const key = `${provider}:${modelID}`;
    setPendingBinding(key);
    setError(null);
    try {
      const entry = await api.modelStatus.update(modelID, provider, status);
      setProviders(current => (current == null ? current : applyStatus(entry, current)));
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : "Failed to update model status.");
      throw err;
    } finally {
      setPendingBinding(undefined);
    }
  }

  return (
    <SettingsPage href="/settings/inventory" contentClassName="max-w-none">
      <div className="flex flex-col gap-5 p-4 sm:p-6">
        <div>
          <h3 className="text-lg font-semibold">Provider and model inventory</h3>
          <p className="mt-1 text-xs text-muted-foreground">
            Inspect every catalog binding and control its live routing status.
          </p>
        </div>

        {error && <ErrorBanner>{error}</ErrorBanner>}

        <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
          <SummaryCard
            icon={<Boxes className="size-4" />}
            label="Providers"
            value={providers?.length ?? "—"}
          />
          <SummaryCard
            icon={<Layers3 className="size-4" />}
            label="Models"
            value={providers == null ? "—" : summary.models}
          />
          <SummaryCard
            icon={<Activity className="size-4" />}
            label="Online rate"
            value={providers == null ? "—" : summary.onlineRate}
          />
        </div>

        {providers == null ? (
          <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
            {Array.from({ length: 4 }, (_, index) => (
              <Card.Loading key={index} className="min-h-48" />
            ))}
          </div>
        ) : (
          <div className="grid grid-cols-1 items-start gap-4 lg:grid-cols-2">
            {providers.map(provider => (
              <ProviderInventoryCard
                key={provider.provider}
                provider={provider}
                pendingBinding={pendingBinding}
                onStatusChange={updateStatus}
              />
            ))}
          </div>
        )}
      </div>
    </SettingsPage>
  );
}
