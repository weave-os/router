"use client";

import { EmptyHint, ErrorBanner } from "./shared";
import { Button } from "@/components/molecules/Button";
import { Card } from "@/components/molecules/Card";
import { Appearance, Intent } from "@/components/types";
import { api } from "@/lib/api";
import { useEffect, useState } from "react";

export function ProviderSelectionPanel() {
  const [available, setAvailable] = useState<string[] | null>(null);
  const [excluded, setExcluded] = useState<Set<string>>(new Set());
  const [savedExcluded, setSavedExcluded] = useState<Set<string>>(new Set());
  const [envOverrideActive, setEnvOverrideActive] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    api.excludedProviders
      .get()
      .then(res => {
        setAvailable(res.available);
        const ex = new Set(res.excluded);
        setExcluded(ex);
        setSavedExcluded(ex);
        setEnvOverrideActive(res.env_override_active);
      })
      .catch((err: unknown) =>
        setError(err instanceof Error ? err.message : "Failed to load providers."),
      );
  }, []);

  const dirty =
    excluded.size !== savedExcluded.size ||
    Array.from(excluded).some(p => !savedExcluded.has(p));

  function toggle(provider: string) {
    if (envOverrideActive) return;
    setExcluded(prev => {
      const next = new Set(prev);
      if (next.has(provider)) next.delete(provider);
      else next.add(provider);
      return next;
    });
  }

  async function save() {
    setSaving(true);
    setError(null);
    try {
      const res = await api.excludedProviders.update(Array.from(excluded));
      const ex = new Set(res.excluded);
      setExcluded(ex);
      setSavedExcluded(ex);
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : "Failed to save excluded providers.");
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

  return (
    <>
      {error && <ErrorBanner>{error}</ErrorBanner>}
      {envOverrideActive && (
        <div className="rounded-md border border-border bg-muted/30 p-3 text-2xs text-muted-foreground">
          Exclusion list is pinned by <code className="font-mono">ROUTER_EXCLUDED_PROVIDERS</code>;
          clear the env var to edit here.
        </div>
      )}
      <Card className="p-0">
        <Card.Content>
          {available.length === 0 ? (
            <EmptyHint>No deployed providers. Check ROUTER_CLUSTER_VERSION and provider keys.</EmptyHint>
          ) : (
            <ul className="space-y-1 px-5 py-3">
              {available.map(p => {
                const isExcluded = excluded.has(p);
                return (
                  <li key={p} className="flex items-center gap-2">
                    <input
                      type="checkbox"
                      id={`provider-${p}`}
                      checked={!isExcluded}
                      onChange={() => toggle(p)}
                      disabled={envOverrideActive}
                      className="size-3.5"
                    />
                    <label
                      htmlFor={`provider-${p}`}
                      className="cursor-pointer font-mono text-xs text-foreground"
                    >
                      {p}
                    </label>
                  </li>
                );
              })}
            </ul>
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
