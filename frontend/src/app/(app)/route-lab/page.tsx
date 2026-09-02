"use client";

import { Text } from "@/components/atoms/Text";
import { Input } from "@/components/Input";
import { Button } from "@/components/molecules/Button";
import { Card } from "@/components/molecules/Card";
import { Page } from "@/components/Page";
import { PageHeader } from "@/components/PageHeader";
import { Appearance, Intent } from "@/components/types";
import { api, type ExternalKey, type ModelValidationResult, type ProviderValidationResult, type RouteTestResult } from "@/lib/api";
import { CheckCircle2, FlaskConical, Loader2, Network, Route, XCircle } from "lucide-react";
import { useEffect, useMemo, useState } from "react";

type ProviderOption = { name: string; keyID?: string; label: string };

function resultTone(status: "valid" | "invalid" | "unknown") {
  if (status === "valid") return "border-success/30 bg-success/5 text-success";
  if (status === "invalid") return "border-danger/30 bg-danger/5 text-danger";
  return "border-warning/30 bg-warning/5 text-warning";
}

function ResultIcon({ status }: { status: "valid" | "invalid" | "unknown" }) {
  if (status === "valid") return <CheckCircle2 className="size-4" />;
  if (status === "invalid") return <XCircle className="size-4" />;
  return <Network className="size-4" />;
}

function providerLabel(provider: string): string {
  return provider.replaceAll("_", " ").replace(/\b\w/g, letter => letter.toUpperCase());
}

/** RouteLabPage is the operator workspace for safe provider/model checks and route previews. */
export default function RouteLabPage() {
  const [keys, setKeys] = useState<ExternalKey[]>([]);
  const [envProviders, setEnvProviders] = useState<string[]>([]);
  const [models, setModels] = useState<{ model: string; provider: string }[]>([]);
  const [provider, setProvider] = useState("");
  const [model, setModel] = useState("");
  const [prompt, setPrompt] = useState("Help me refactor this function and explain the trade-offs.");
  const [providerResult, setProviderResult] = useState<ProviderValidationResult | null>(null);
  const [modelResult, setModelResult] = useState<ModelValidationResult | null>(null);
  const [routeResult, setRouteResult] = useState<RouteTestResult | null>(null);
  const [loading, setLoading] = useState(true);
  const [checkingProvider, setCheckingProvider] = useState(false);
  const [checkingModel, setCheckingModel] = useState(false);
  const [testingRoute, setTestingRoute] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    Promise.all([api.providerKeys.list(), api.config.get(), api.excludedModels.get()])
      .then(([keyResponse, config, modelResponse]) => {
        const nextKeys = keyResponse.keys ?? [];
        const nextEnv = config.env_provider_keys ?? [];
        const nextModels = modelResponse.available ?? [];
        setKeys(nextKeys);
        setEnvProviders(nextEnv);
        setModels(nextModels);
        const firstProvider = nextKeys[0]?.provider ?? nextEnv[0] ?? nextModels[0]?.provider ?? "";
        setProvider(firstProvider);
        setModel(nextModels.find(item => item.provider === firstProvider)?.model ?? nextModels[0]?.model ?? "");
      })
      .catch((err: unknown) => setError(err instanceof Error ? err.message : "Failed to load routing inventory."))
      .finally(() => setLoading(false));
  }, []);

  const providers = useMemo<ProviderOption[]>(() => {
    const byProvider = new Map<string, ProviderOption>();
    for (const name of envProviders) byProvider.set(name, { name, label: `${providerLabel(name)} · environment` });
    for (const key of keys) byProvider.set(key.provider, { name: key.provider, keyID: key.id, label: `${providerLabel(key.provider)} · saved key` });
    for (const item of models) if (!byProvider.has(item.provider)) byProvider.set(item.provider, { name: item.provider, label: providerLabel(item.provider) });
    return Array.from(byProvider.values()).sort((a, b) => a.label.localeCompare(b.label));
  }, [envProviders, keys, models]);

  const selectedProvider = providers.find(item => item.name === provider);

  function chooseProvider(next: string) {
    setProvider(next);
    setProviderResult(null);
    setModelResult(null);
    setModel(models.find(item => item.provider === next)?.model ?? "");
  }

  async function validateProvider() {
    if (!provider) return;
    setCheckingProvider(true);
    setError(null);
    try {
      setProviderResult(await api.validation.provider(provider, selectedProvider?.keyID));
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : "Provider validation failed.");
    } finally {
      setCheckingProvider(false);
    }
  }

  async function validateModel() {
    if (!provider || !model) return;
    setCheckingModel(true);
    setError(null);
    try {
      setModelResult(await api.validation.model(provider, model, selectedProvider?.keyID));
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : "Model validation failed.");
    } finally {
      setCheckingModel(false);
    }
  }

  async function testRoute() {
    if (!prompt.trim()) return;
    setTestingRoute(true);
    setError(null);
    setRouteResult(null);
    try {
      setRouteResult(await api.validation.route(prompt.trim()));
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : "Route preview failed.");
    } finally {
      setTestingRoute(false);
    }
  }

  return (
    <Page
      header={
        <PageHeader
          left={<Text variant="h4" as="h2" className="flex items-center gap-2"><FlaskConical className="size-4 text-brand" />Route Lab</Text>}
          right={<span className="router-mono text-[10px] uppercase tracking-wider text-muted-foreground">safe preview</span>}
        />
      }
    >
      <div className="flex w-full max-w-content-width flex-col gap-6 p-6">
        {error && <div className="rounded-md border border-danger/30 bg-danger/5 px-4 py-3 text-xs text-danger">{error}</div>}

        <section className="grid gap-4 lg:grid-cols-[minmax(0,1.3fr)_minmax(300px,0.7fr)]">
          <Card className="border-brand/30 bg-brand/[0.03]">
            <Card.Header>
              <div className="flex items-center gap-2 text-brand"><Route className="size-4" /><span className="router-eyebrow">Prompt experiment</span></div>
              <Card.Title variant="h3">Which model would handle this?</Card.Title>
              <Card.Description>Preview the routing decision without sending the prompt to an upstream model.</Card.Description>
            </Card.Header>
            <Card.Content className="flex flex-col gap-3">
              <textarea
                aria-label="Prompt to test"
                className="min-h-36 w-full resize-y rounded-md border border-input bg-background px-3 py-3 text-sm shadow-sm outline-none transition-colors placeholder:text-muted-foreground focus-visible:border-brand focus-visible:ring-2 focus-visible:ring-brand/20"
                value={prompt}
                onChange={event => setPrompt(event.target.value)}
                placeholder="Describe the task you want the router to classify…"
              />
              <div className="flex items-center justify-between gap-3">
                <Text className="text-2xs text-muted-foreground">This is a dry run: no provider call and no inference charge.</Text>
                <Button appearance={Appearance.Filled} intent={Intent.Primary} className="!border-brand !bg-brand !text-white hover:!bg-brand/90" onClick={testRoute} disabled={testingRoute || !prompt.trim()}>
                  {testingRoute && <Loader2 className="size-3.5 animate-spin" />}
                  {testingRoute ? "Analyzing…" : "Test routing"}
                </Button>
              </div>
              {routeResult && (
                <div className="grid gap-2 rounded-md border border-brand/30 bg-background/70 p-4 sm:grid-cols-3">
                  <div><Text className="router-eyebrow">Selected model</Text><p className="mt-1 break-all font-mono text-sm text-foreground">{routeResult.model}</p></div>
                  <div><Text className="router-eyebrow">Provider</Text><p className="mt-1 font-mono text-sm text-foreground">{routeResult.provider}</p></div>
                  <div><Text className="router-eyebrow">Credential</Text><p className="mt-1 font-mono text-sm text-foreground">{routeResult.credential_source || "policy only"}</p></div>
                  {routeResult.reason && <p className="sm:col-span-3 border-t border-border pt-2 text-2xs text-muted-foreground">{routeResult.reason}</p>}
                  {routeResult.candidates && routeResult.candidates.length > 0 && <p className="sm:col-span-3 text-2xs text-muted-foreground">Candidate pool: {routeResult.candidates.join(" · ")}</p>}
                </div>
              )}
            </Card.Content>
          </Card>

          <Card>
            <Card.Header>
              <Text className="router-eyebrow">How to read this page</Text>
              <Card.Title variant="h4">Three checks, one control surface</Card.Title>
            </Card.Header>
            <Card.Content className="flex flex-col gap-3 text-xs text-muted-foreground">
              <p><span className="font-medium text-foreground">Provider check</span> calls its safe catalog endpoint and checks credentials without inference.</p>
              <p><span className="font-medium text-foreground">Model check</span> compares a catalog model (including aliases) with the provider&apos;s published models.</p>
              <p><span className="font-medium text-foreground">Prompt experiment</span> runs the same selection logic used by the router, but never dispatches.</p>
            </Card.Content>
          </Card>
        </section>

        <section className="grid gap-4 lg:grid-cols-2">
          <Card>
            <Card.Header>
              <Card.Title variant="h4">Provider validity</Card.Title>
              <Card.Description>Check whether a configured provider can answer a safe catalog request.</Card.Description>
            </Card.Header>
            <Card.Content className="flex flex-col gap-3">
              <select aria-label="Provider to validate" className="h-9 rounded-md border border-input bg-background px-3 text-sm" value={provider} onChange={event => chooseProvider(event.target.value)} disabled={loading}>
                <option value="">Select provider</option>
                {providers.map(item => <option key={`${item.name}-${item.keyID ?? "env"}`} value={item.name}>{item.label}</option>)}
              </select>
              <div className="flex items-center gap-2">
                <Button appearance={Appearance.Outlined} onClick={validateProvider} disabled={checkingProvider || !provider}>
                  {checkingProvider && <Loader2 className="size-3.5 animate-spin" />}
                  {checkingProvider ? "Checking…" : "Validate provider"}
                </Button>
                {providerResult && <div className={`flex items-center gap-1 rounded-md border px-2 py-1 text-2xs ${resultTone(providerResult.status)}`}><ResultIcon status={providerResult.status} />{providerResult.status}{providerResult.model_count != null && ` · ${providerResult.model_count} models`}</div>}
              </div>
              {providerResult && <Text className="text-2xs text-muted-foreground">{providerResult.message}</Text>}
            </Card.Content>
          </Card>

          <Card>
            <Card.Header>
              <Card.Title variant="h4">Model validity</Card.Title>
              <Card.Description>Verify that the selected provider publishes the model you intend to route.</Card.Description>
            </Card.Header>
            <Card.Content className="flex flex-col gap-3">
              <div>
                <Input
                  aria-label="Model to validate"
                  list="route-lab-models"
                  className="font-mono text-xs"
                  placeholder="Type a catalog or upstream model ID"
                  value={model}
                  onChange={event => { setModel(event.target.value); setModelResult(null); }}
                  disabled={!provider || loading}
                />
                <datalist id="route-lab-models">
                  {models.map(item => <option key={`${item.provider}-${item.model}`} value={item.model}>{item.provider}</option>)}
                </datalist>
              </div>
              <div className="flex items-center gap-2">
                <Button appearance={Appearance.Outlined} onClick={validateModel} disabled={checkingModel || !provider || !model}>
                  {checkingModel && <Loader2 className="size-3.5 animate-spin" />}
                  {checkingModel ? "Checking…" : "Validate model"}
                </Button>
                {modelResult && <div className={`flex items-center gap-1 rounded-md border px-2 py-1 text-2xs ${resultTone(modelResult.status)}`}><ResultIcon status={modelResult.status} />{modelResult.status}</div>}
              </div>
              {modelResult && <Text className="text-2xs text-muted-foreground">{modelResult.message}{modelResult.upstream_model !== modelResult.model && ` Upstream ID: ${modelResult.upstream_model}`}</Text>}
            </Card.Content>
          </Card>
        </section>
      </div>
    </Page>
  );
}
