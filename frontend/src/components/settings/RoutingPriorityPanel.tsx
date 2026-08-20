"use client";

import { ErrorBanner } from "./shared";
import { Input } from "@/components/Input";
import { Button } from "@/components/molecules/Button";
import { Card } from "@/components/molecules/Card";
import { Appearance, Intent } from "@/components/types";
import { api } from "@/lib/api";
import { useEffect, useState } from "react";

// Routing dials, rendered as proportional weights (sum to 100%) — mirrors the
// metric-weights editor in the main Weave app. Quality is the scorer's Alpha;
// price is the implied remainder.
const ROUTING_DIALS = [
  { key: "quality", label: "Quality" },
  { key: "price", label: "Price" },
] as const;
const DIAL_COLORS = ["bg-primary", "bg-warning"];
const DEFAULT_QUALITY = 70;

// Distributes a changed weight against the others so they always sum to 100,
// using largest-remainder rounding. Carried over from the main app's
// AggregateMetricWeightsSection so both surfaces behave identically.
function adjustWeightsProportionally(
  weights: Record<string, number>,
  changedKey: string,
  newValue: number,
): Record<string, number> {
  const clamped = Math.max(0, Math.min(100, newValue));
  const keys = Object.keys(weights);
  const otherKeys = keys.filter(k => k !== changedKey);

  if (otherKeys.length === 0) {
    return { [changedKey]: 100 };
  }
  if (clamped === 100) {
    const result: Record<string, number> = { [changedKey]: 100 };
    for (const k of otherKeys) {
      result[k] = 0;
    }
    return result;
  }

  const remaining = 100 - clamped;
  const othersSum = otherKeys.reduce((s, k) => s + weights[k], 0);

  const exact: Record<string, number> = {};
  if (othersSum === 0) {
    const each = remaining / otherKeys.length;
    for (const k of otherKeys) {
      exact[k] = each;
    }
  } else {
    for (const k of otherKeys) {
      exact[k] = (weights[k] / othersSum) * remaining;
    }
  }

  const floored: Record<string, number> = {};
  let flooredSum = 0;
  for (const k of otherKeys) {
    floored[k] = Math.floor(exact[k]);
    flooredSum += floored[k];
  }

  let leftover = remaining - flooredSum;
  const remainders = otherKeys
    .map(k => ({ key: k, remainder: exact[k] - floored[k] }))
    .sort((a, b) => b.remainder - a.remainder);
  for (const { key } of remainders) {
    if (leftover <= 0) break;
    floored[key] += 1;
    leftover -= 1;
  }

  const result: Record<string, number> = { [changedKey]: clamped };
  for (const k of otherKeys) {
    result[k] = floored[k];
  }
  return result;
}

function DistributionBar({ weights }: { weights: Record<string, number> }) {
  return (
    <div className="flex h-2 overflow-hidden rounded-full">
      {ROUTING_DIALS.map((dial, i) => (
        <div
          key={dial.key}
          className={`${DIAL_COLORS[i % DIAL_COLORS.length]} transition-all duration-200`}
          style={{ width: `${weights[dial.key] ?? 0}%` }}
        />
      ))}
    </div>
  );
}

export function RoutingPriorityPanel() {
  const [weights, setWeights] = useState<Record<string, number>>({
    quality: DEFAULT_QUALITY,
    price: 100 - DEFAULT_QUALITY,
  });
  const [saved, setSaved] = useState<Record<string, number>>({
    quality: DEFAULT_QUALITY,
    price: 100 - DEFAULT_QUALITY,
  });
  const [focusedKey, setFocusedKey] = useState<string | null>(null);
  const [focusedValue, setFocusedValue] = useState("");
  const [isDefault, setIsDefault] = useState(true);
  const [loaded, setLoaded] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  function apply(res: { quality: number; is_default: boolean }) {
    const quality = Math.round(res.quality);
    const next = { quality, price: 100 - quality };
    setWeights(next);
    setSaved(next);
    setFocusedKey(null);
    setIsDefault(res.is_default);
  }

  useEffect(() => {
    api.routingPreferences
      .get()
      .then(res => {
        apply(res);
        setLoaded(true);
      })
      .catch((err: unknown) => {
        setError(err instanceof Error ? err.message : "Failed to load routing priority");
        setLoaded(true);
      });
  }, []);

  const dirty = weights.quality !== saved.quality;

  function applyProportionalAdjustment(key: string, rawValue: string) {
    const val = parseInt(rawValue, 10);
    if (isNaN(val)) {
      setFocusedKey(null);
      return;
    }
    setWeights(prev => adjustWeightsProportionally(prev, key, val));
    setFocusedKey(null);
  }

  async function save() {
    setSaving(true);
    setError(null);
    try {
      apply(await api.routingPreferences.update({ quality: weights.quality, price: weights.price }));
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : "Failed to save");
    } finally {
      setSaving(false);
    }
  }

  async function reset() {
    setSaving(true);
    setError(null);
    try {
      apply(await api.routingPreferences.reset());
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : "Failed to reset");
    } finally {
      setSaving(false);
    }
  }

  if (!loaded) {
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
      <Card className="p-0">
        <Card.Content className="flex flex-col gap-4 p-5">
          {isDefault && (
            <div className="rounded-md border border-border bg-muted/30 p-3 text-2xs text-muted-foreground">
              Using the router&apos;s default routing. Adjust a weight and save to set a preference.
            </div>
          )}

          <DistributionBar weights={weights} />

          {ROUTING_DIALS.map((dial, i) => (
            <div key={dial.key} className="flex items-center justify-between gap-2">
              <div className="flex items-center gap-1.5">
                <div className={`size-2 rounded-full ${DIAL_COLORS[i % DIAL_COLORS.length]}`} />
                <span className="text-sm text-foreground">{dial.label}</span>
              </div>
              <div className="flex items-center gap-1">
                <Input
                  type="number"
                  min={0}
                  max={100}
                  step={1}
                  className="w-16 text-right text-sm"
                  disabled={saving}
                  value={focusedKey === dial.key ? focusedValue : (weights[dial.key] ?? 0)}
                  onFocus={() => {
                    setFocusedKey(dial.key);
                    setFocusedValue(String(weights[dial.key] ?? 0));
                  }}
                  onChange={e => {
                    const newVal = e.target.value;
                    setFocusedValue(newVal);
                    const parsed = parseInt(newVal, 10);
                    const current = weights[dial.key] ?? 0;
                    if (!isNaN(parsed) && Math.abs(parsed - current) === 1) {
                      setWeights(prev => {
                        const adjusted = adjustWeightsProportionally(prev, dial.key, parsed);
                        setFocusedValue(String(adjusted[dial.key] ?? parsed));
                        return adjusted;
                      });
                    }
                  }}
                  onBlur={() => {
                    if (focusedKey === dial.key) {
                      applyProportionalAdjustment(dial.key, focusedValue);
                    }
                  }}
                  onKeyDown={e => {
                    if (e.key === "ArrowUp" || e.key === "ArrowDown") {
                      e.preventDefault();
                      const delta = e.key === "ArrowUp" ? 1 : -1;
                      setWeights(prev => {
                        const current = prev[dial.key] ?? 0;
                        const next = Math.max(0, Math.min(100, current + delta));
                        const adjusted = adjustWeightsProportionally(prev, dial.key, next);
                        setFocusedValue(String(adjusted[dial.key] ?? next));
                        return adjusted;
                      });
                    }
                    if (e.key === "Enter") {
                      applyProportionalAdjustment(dial.key, focusedValue);
                      (e.target as HTMLInputElement).blur();
                    }
                  }}
                />
                <span className="text-xs text-muted-foreground">%</span>
              </div>
            </div>
          ))}
        </Card.Content>
        <Card.Footer className="flex items-center justify-between border-t border-border px-5 py-3">
          <span className="text-2xs text-muted-foreground">
            Higher quality favors stronger models; higher price favors cheaper ones.
          </span>
          <div className="flex items-center gap-2">
            {!isDefault && (
              <Button appearance={Appearance.Outlined} onClick={reset} disabled={saving}>
                Reset to default
              </Button>
            )}
            <Button
              onClick={save}
              disabled={!dirty || saving}
              intent={Intent.Primary}
              appearance={Appearance.Filled}
            >
              {saving ? "Saving…" : "Save"}
            </Button>
          </div>
        </Card.Footer>
      </Card>
    </>
  );
}
