"use client";

import { EmptyHint, ErrorBanner } from "./shared";
import { Button } from "@/components/molecules/Button";
import { Card } from "@/components/molecules/Card";
import { Appearance, Intent } from "@/components/types";
import { api, type DeployedModel } from "@/lib/api";
import { useEffect, useState } from "react";

export function ModelSelectionPanel() {
  const [available, setAvailable] = useState<DeployedModel[] | null>(null);
  const [excluded, setExcluded] = useState<Set<string>>(new Set());
  const [savedExcluded, setSavedExcluded] = useState<Set<string>>(new Set());
  const [envOverrideActive, setEnvOverrideActive] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    api.excludedModels
      .get()
      .then(res => {
        setAvailable(res.available);
        const ex = new Set(res.excluded);
        setExcluded(ex);
        setSavedExcluded(ex);
        setEnvOverrideActive(res.env_override_active);
      })
      .catch((err: unknown) =>
        setError(err instanceof Error ? err.message : "Failed to load models."),
      );
  }, []);

  const dirty =
    excluded.size !== savedExcluded.size ||
    Array.from(excluded).some(m => !savedExcluded.has(m));

  function toggle(model: string) {
    if (envOverrideActive) return;
    setExcluded(prev => {
      const next = new Set(prev);
      if (next.has(model)) next.delete(model);
      else next.add(model);
      return next;
    });
  }

  async function save() {
    setSaving(true);
    setError(null);
    try {
      const res = await api.excludedModels.update(Array.from(excluded));
      const ex = new Set(res.excluded);
      setExcluded(ex);
      setSavedExcluded(ex);
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : "Failed to save excluded models.");
    } finally {
      setSaving(false);
    }
  }

  if (available == null) {
    return (
      <Card className="p-0">
        <Card.Content>
          <div className="px-5 py-8 text-center text-2xs text-muted-foreground">
            {error != null ? "Failed to load" : "Loading…"}
          </div>
        </Card.Content>
      </Card>
    );
  }

  const grouped = new Map<string, DeployedModel[]>();
  for (const m of available) {
    const arr = grouped.get(m.provider) ?? [];
    arr.push(m);
    grouped.set(m.provider, arr);
  }

  return (
    <>
      {error && <ErrorBanner>{error}</ErrorBanner>}
      {envOverrideActive && (
        <div className="rounded-md border border-border bg-muted/30 p-3 text-2xs text-muted-foreground">
          Exclusion list is pinned by <code className="font-mono">ROUTER_EXCLUDED_MODELS</code>;
          clear the env var to edit here.
        </div>
      )}
      <Card className="p-0">
        <Card.Content>
          {available.length === 0 ? (
            <EmptyHint>No deployed models. Check ROUTER_CLUSTER_VERSION and provider keys.</EmptyHint>
          ) : (
            <div className="divide-y divide-border">
              {Array.from(grouped.entries()).map(([provider, models]) => (
                <div key={provider} className="px-5 py-3">
                  <p className="mb-2 text-2xs font-medium uppercase tracking-wide text-muted-foreground">
                    {provider}
                  </p>
                  <ul className="space-y-1">
                    {models.map(m => {
                      const isExcluded = excluded.has(m.model);
                      return (
                        <li key={m.model} className="flex items-center gap-2">
                          <input
                            type="checkbox"
                            id={`model-${m.model}`}
                            checked={!isExcluded}
                            onChange={() => toggle(m.model)}
                            disabled={envOverrideActive}
                            className="size-3.5"
                          />
                          <label
                            htmlFor={`model-${m.model}`}
                            className="cursor-pointer font-mono text-xs text-foreground"
                          >
                            {m.model}
                          </label>
                        </li>
                      );
                    })}
                  </ul>
                </div>
              ))}
            </div>
          )}
        </Card.Content>
        <Card.Footer className="border-t border-border px-5 py-3">
          <Button
            onClick={save}
            disabled={!dirty || saving || envOverrideActive}
            intent={Intent.Primary}
            appearance={Appearance.Filled}
          >
            {saving ? "Saving…" : "Save"}
          </Button>
        </Card.Footer>
      </Card>
    </>
  );
}
