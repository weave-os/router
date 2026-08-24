"use client";

import { EmptyHint, ErrorBanner } from "./shared";
import { Text } from "@/components/atoms/Text";
import { Input } from "@/components/Input";
import { Button } from "@/components/molecules/Button";
import { Card } from "@/components/molecules/Card";
import { Command } from "@/components/molecules/Command";
import { Popover } from "@/components/molecules/Popover";
import { Appearance, Intent } from "@/components/types";
import {
  api,
  type DeployedModel,
  type ExternalKey,
  type ProviderAuthType,
} from "@/lib/api";
import { ChevronDown, Trash2 } from "lucide-react";
import { useEffect, useRef, useState } from "react";

const PROVIDERS = ["anthropic", "openai", "google", "openrouter", "anthropic_gateway", "openai_gateway"] as const;
type Provider = (typeof PROVIDERS)[number];

const PROVIDER_LABEL: Record<Provider, string> = {
  anthropic: "Anthropic",
  openai: "OpenAI",
  google: "Google",
  openrouter: "OpenRouter",
  anthropic_gateway: "Anthropic-compatible gateway",
  openai_gateway: "OpenAI-compatible gateway",
};

const PROVIDER_ENV_VAR: Record<Provider, string> = {
  anthropic: "ANTHROPIC_API_KEY",
  openai: "OPENAI_API_KEY",
  google: "GOOGLE_API_KEY",
  openrouter: "OPENROUTER_API_KEY",
  anthropic_gateway: "ANTHROPIC_GATEWAY_TOKEN",
  openai_gateway: "OPENAI_GATEWAY_TOKEN",
};

// Providers with no vendor endpoint to fall back to: a key without a URL here
// is stored but can never be dispatched, so the form blocks it up front.
const PROVIDERS_REQUIRING_BASE_URL: readonly Provider[] = ["anthropic_gateway", "openai_gateway"];

function providerLabel(p: Provider): string {
  return PROVIDER_LABEL[p];
}

// AliasRow is the editor's working shape: the map is only formed on save, so a
// half-typed or duplicate row can exist without clobbering another entry.
interface AliasRow {
  model: string;
  alias: string;
}

function aliasRowsFrom(aliases: Record<string, string> | undefined): AliasRow[] {
  return Object.entries(aliases ?? {}).map(([model, alias]) => ({ model, alias }));
}

function aliasMapFrom(rows: AliasRow[]): Record<string, string> {
  const out: Record<string, string> = {};
  for (const row of rows) {
    const model = row.model.trim();
    const alias = row.alias.trim();
    if (model !== "" && alias !== "") out[model] = alias;
  }
  return out;
}

// matchesCatalogID reports whether an endpoint-published ID is unambiguously
// the given catalog model under a vendor prefix (e.g. "openai-gpt-5" or
// "openai/gpt-5" for catalog "gpt-5").
function matchesCatalogID(endpointID: string, model: string): boolean {
  const id = endpointID.toLowerCase();
  const m = model.toLowerCase();
  return id === m || id.endsWith(`-${m}`) || id.endsWith(`/${m}`) || id.endsWith(`.${m}`);
}

// autoMatchRows pre-fills alias rows for catalog models the endpoint publishes
// under exactly one recognizable name. Ambiguous (multiple candidates) or
// already-mapped models are left alone rather than guessed.
function autoMatchRows(
  models: DeployedModel[],
  endpointModels: string[],
  existing: AliasRow[],
): AliasRow[] {
  const taken = new Set(existing.map(r => r.model.trim()).filter(m => m !== ""));
  const added: AliasRow[] = [];
  for (const m of models) {
    if (taken.has(m.model)) continue;
    const matches = endpointModels.filter(id => matchesCatalogID(id, m.model));
    if (matches.length === 1) added.push({ model: m.model, alias: matches[0] });
  }
  return added;
}

// ModelAliasEditor maps catalog model IDs to the names a custom endpoint
// publishes them under. Routing, pricing, and analytics stay keyed by the
// catalog ID; only the outbound request carries the alias.
function ModelAliasEditor({
  rows,
  onChange,
  models,
  idPrefix,
  endpointModels,
  onFetchModels,
  fetching,
  fetchHint,
}: {
  rows: AliasRow[];
  onChange: (rows: AliasRow[]) => void;
  models: DeployedModel[];
  idPrefix: string;
  // Endpoint-published model IDs, once fetched; null until then.
  endpointModels: string[] | null;
  onFetchModels?: () => void;
  fetching?: boolean;
  // Why fetching is unavailable right now (e.g. missing key/URL); shown next
  // to a disabled button.
  fetchHint?: string | null;
}) {
  const listID = `${idPrefix}-catalog-models`;
  const endpointListID = `${idPrefix}-endpoint-models`;
  return (
    <div className="flex flex-col gap-2">
      <div className="flex items-center justify-between">
        <span className="text-xs font-medium text-foreground">Model aliases (optional)</span>
        {onFetchModels != null && (
          <div className="flex items-center gap-2">
            {fetchHint != null && (
              <Text className="text-2xs text-muted-foreground">{fetchHint}</Text>
            )}
            <Button
              type="button"
              appearance={Appearance.Outlined}
              size="sm"
              onClick={onFetchModels}
              disabled={fetching || fetchHint != null}
            >
              {fetching ? "Fetching…" : "Fetch models from endpoint"}
            </Button>
          </div>
        )}
      </div>
      <datalist id={listID}>
        {models.map(m => (
          <option key={m.model} value={m.model} />
        ))}
      </datalist>
      {endpointModels != null && (
        <datalist id={endpointListID}>
          {endpointModels.map(id => (
            <option key={id} value={id} />
          ))}
        </datalist>
      )}
      {endpointModels != null && endpointModels.length === 0 && (
        <Text className="text-2xs text-muted-foreground">
          The endpoint returned no models; enter aliases manually.
        </Text>
      )}
      {rows.map((row, i) => {
        const alias = row.alias.trim();
        const unlisted =
          endpointModels != null &&
          endpointModels.length > 0 &&
          alias !== "" &&
          !endpointModels.includes(alias);
        return (
          <div key={i} className="flex flex-col gap-1">
            <div className="grid grid-cols-[1fr_1fr_auto] items-center gap-2">
              <Input
                id={`${idPrefix}-model-${i}`}
                list={listID}
                autoComplete="off"
                placeholder="gpt-5"
                value={row.model}
                onChange={e =>
                  onChange(rows.map((r, j) => (i === j ? { ...r, model: e.target.value } : r)))
                }
              />
              <Input
                id={`${idPrefix}-alias-${i}`}
                list={endpointModels != null && endpointModels.length > 0 ? endpointListID : undefined}
                autoComplete="off"
                placeholder="openai-gpt-5"
                value={row.alias}
                onChange={e =>
                  onChange(rows.map((r, j) => (i === j ? { ...r, alias: e.target.value } : r)))
                }
              />
              <Button
                type="button"
                appearance={Appearance.Hollow}
                intent={Intent.Danger}
                size="icon"
                onClick={() => onChange(rows.filter((_, j) => j !== i))}
                title="Remove alias."
              >
                <Trash2 className="size-3.5" />
              </Button>
            </div>
            {unlisted && (
              <Text className="text-2xs text-amber-600">
                “{alias}” is not in the endpoint&apos;s published model list.
              </Text>
            )}
          </div>
        );
      })}
      <div>
        <Button
          type="button"
          appearance={Appearance.Outlined}
          onClick={() => onChange([...rows, { model: "", alias: "" }])}
        >
          Add alias
        </Button>
      </div>
      <Text className="text-2xs text-muted-foreground">
        Left: the catalog model the router routes to. Right: the ID this endpoint publishes it
        under. Only the outbound request is rewritten — routing and billing stay on the catalog ID.
      </Text>
    </div>
  );
}

export function ProviderKeysPanel() {
  const [keys, setKeys] = useState<ExternalKey[]>([]);
  const [envKeyed, setEnvKeyed] = useState<Provider[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [pickedProvider, setPickedProvider] = useState<Provider | null>(null);
  const [keyValue, setKeyValue] = useState("");
  const [name, setName] = useState("");
  const [baseURL, setBaseURL] = useState("");
  const [authType, setAuthType] = useState<ProviderAuthType>("bearer");
  const [authAccount, setAuthAccount] = useState("");
  const [authUser, setAuthUser] = useState("");
  const [aliasRows, setAliasRows] = useState<AliasRow[]>([]);
  const [catalogModels, setCatalogModels] = useState<DeployedModel[]>([]);
  const [editingAliases, setEditingAliases] = useState<string | null>(null);
  const [editAliasRows, setEditAliasRows] = useState<AliasRow[]>([]);
  const [savingAliases, setSavingAliases] = useState(false);
  const [saving, setSaving] = useState(false);
  const [deleting, setDeleting] = useState<string | null>(null);
  const [newEndpointModels, setNewEndpointModels] = useState<string[] | null>(null);
  const [fetchingNewModels, setFetchingNewModels] = useState(false);
  const [editEndpointModels, setEditEndpointModels] = useState<string[] | null>(null);
  const [fetchingEditModels, setFetchingEditModels] = useState(false);
  // Signature of the last auto-fetched (provider, key, URL) tuple, so typing
  // pauses trigger one discovery call rather than one per keystroke burst.
  const autoFetchedRef = useRef<string | null>(null);
  // Bumped whenever the form inputs change so a slower in-flight response
  // for a previous endpoint can't land as the current one's inventory.
  const newFetchSeqRef = useRef(0);
  // The key ID an edit-side listing is for; a response for any other key
  // (switched or closed editor) is discarded.
  const editFetchIDRef = useRef<string | null>(null);

  function load() {
    api.providerKeys
      .list()
      .then(r => setKeys(r.keys ?? []))
      .catch((err: unknown) =>
        setError(err instanceof Error ? err.message : "Failed to load keys."),
      );
  }

  useEffect(load, []);

  useEffect(() => {
    api.config
      .get()
      .then(cfg => {
        const set = (cfg.env_provider_keys ?? []).filter((p): p is Provider =>
          (PROVIDERS as readonly string[]).includes(p),
        );
        setEnvKeyed(set);
      })
      .catch(() => {
        // Non-fatal: the panel still works, env-keyed providers just won't
        // be flagged as read-only.
        setEnvKeyed([]);
      });
  }, []);

  useEffect(() => {
    api.excludedModels
      .get()
      // Non-fatal: aliases stay typeable, just without catalog suggestions.
      .then(r => setCatalogModels(r.available ?? []))
      .catch(() => setCatalogModels([]));
  }, []);

  const taken = new Set<string>([...keys.map(k => k.provider), ...envKeyed]);
  const available: Provider[] = PROVIDERS.filter(p => !taken.has(p));
  const provider: Provider | null =
    pickedProvider != null && available.includes(pickedProvider)
      ? pickedProvider
      : (available[0] ?? null);

  const baseURLRequired = provider != null && PROVIDERS_REQUIRING_BASE_URL.includes(provider);
  // Mirrors the server's normalization: a slash-only value normalizes away to nothing.
  const baseURLMissing = baseURLRequired && baseURL.trim().replace(/\/+$/, "") === "";
  // Key-pair, workload-identity, and Entra auth are gateway-only credential shapes; the
  // vendor providers authenticate with their own API keys.
  const authTypeOffered = baseURLRequired;
  const usingKeypair = authTypeOffered && authType === "keypair_jwt";
  const usingWIF = authTypeOffered && authType === "wif";
  const usingEntra = authTypeOffered && authType === "azure_entra";
  const keypairIncomplete = usingKeypair && (authAccount.trim() === "" || authUser.trim() === "");
  const entraIncomplete = usingEntra && (authAccount.trim() === "" || authUser.trim() === "");
  // A WIF key carries no secret, so an empty key field is the expected state.
  const keyMissing = !usingWIF && keyValue.trim() === "";
  const saveDisabled = saving || keyMissing || baseURLMissing || keypairIncomplete || entraIncomplete;
  // Pre-save discovery can only authenticate with a token sent as-is; derived
  // auth (key pair, WIF) needs the stored key, so those save first and fetch
  // from the saved entry.
  const fetchHint =
    authTypeOffered && authType !== "bearer"
      ? "Save the key first, then fetch from the saved entry."
      : keyMissing
        ? "Enter the API key first."
        : baseURLMissing
          ? "Enter the endpoint URL first."
          : null;

  const canFetchNew = provider != null && authTypeOffered && authType === "bearer" && !keyMissing && !baseURLMissing;

  // Discover models as soon as a usable key + URL are entered, so endpoint IDs
  // and auto-matched aliases appear without an extra click.
  useEffect(() => {
    const signature = `${provider}\n${keyValue.trim()}\n${baseURL.trim()}`;
    if (autoFetchedRef.current === signature) return;
    // Inputs changed: any in-flight or previously fetched inventory belongs
    // to a different endpoint.
    autoFetchedRef.current = null;
    newFetchSeqRef.current++;
    setNewEndpointModels(null);
    setFetchingNewModels(false);
    if (!canFetchNew) return;
    const timer = setTimeout(() => {
      autoFetchedRef.current = signature;
      void handleFetchNewModels();
    }, 800);
    return () => clearTimeout(timer);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [canFetchNew, provider, keyValue, baseURL]);

  async function handleFetchNewModels() {
    if (provider == null) return;
    const seq = ++newFetchSeqRef.current;
    setFetchingNewModels(true);
    setError(null);
    try {
      const r = await api.providerKeys.discoverModels(
        provider,
        keyValue.trim(),
        baseURL.trim() || undefined,
      );
      if (seq !== newFetchSeqRef.current) return;
      const models = r.models ?? [];
      setNewEndpointModels(models);
      setAliasRows(rows => [...rows, ...autoMatchRows(catalogModels, models, rows)]);
    } catch (err: unknown) {
      if (seq !== newFetchSeqRef.current) return;
      setError(err instanceof Error ? err.message : "Failed to fetch models from the endpoint.");
    } finally {
      if (seq === newFetchSeqRef.current) setFetchingNewModels(false);
    }
  }

  async function handleFetchEditModels(id: string) {
    editFetchIDRef.current = id;
    setFetchingEditModels(true);
    setError(null);
    try {
      const r = await api.providerKeys.listUpstreamModels(id);
      if (editFetchIDRef.current !== id) return;
      const models = r.models ?? [];
      setEditEndpointModels(models);
      setEditAliasRows(rows => [...rows, ...autoMatchRows(catalogModels, models, rows)]);
    } catch (err: unknown) {
      if (editFetchIDRef.current !== id) return;
      setError(err instanceof Error ? err.message : "Failed to fetch models from the endpoint.");
    } finally {
      if (editFetchIDRef.current === id) setFetchingEditModels(false);
    }
  }

  async function handleSave(e: React.FormEvent) {
    e.preventDefault();
    if (provider == null || saveDisabled) return;
    setSaving(true);
    try {
      await api.providerKeys.upsert(
        provider,
        usingWIF ? "" : keyValue.trim(),
        name.trim() || undefined,
        baseURL.trim() || undefined,
        aliasMapFrom(aliasRows),
        usingKeypair || usingEntra
          ? { type: authType, account: authAccount.trim(), user: authUser.trim() }
          : { type: authTypeOffered ? authType : "bearer" },
      );
      setKeyValue("");
      setName("");
      setBaseURL("");
      setAuthType("bearer");
      setAuthAccount("");
      setAuthUser("");
      setAliasRows([]);
      setNewEndpointModels(null);
      load();
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : "Failed to save key");
    } finally {
      setSaving(false);
    }
  }

  async function handleSaveAliases(id: string) {
    setSavingAliases(true);
    setError(null);
    try {
      await api.providerKeys.updateModelAliases(id, aliasMapFrom(editAliasRows));
      setEditingAliases(null);
      load();
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : "Failed to save model aliases.");
    } finally {
      setSavingAliases(false);
    }
  }

  async function handleDelete(id: string) {
    setDeleting(id);
    try {
      await api.providerKeys.delete(id);
      load();
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : "Failed to delete key.");
    } finally {
      setDeleting(null);
    }
  }

  const hasAnyKey = keys.length > 0 || envKeyed.length > 0;
  return (
    <>
      {error && <ErrorBanner>{error}</ErrorBanner>}

      {available.length > 0 && provider != null ? (
        <Card>
          <Card.Header>
            <Card.Title variant="h4">Add a key</Card.Title>
          </Card.Header>
          <Card.Content>
            <form onSubmit={handleSave} className="space-y-3" autoComplete="off">
              <div className="grid grid-cols-1 gap-3 sm:grid-cols-[200px_1fr]">
                <ProviderPicker value={provider} onChange={setPickedProvider} options={available} />
                {usingWIF ? null : usingKeypair ? (
                  <div className="flex flex-col gap-1.5">
                    <label htmlFor="provider-private-key" className="text-xs font-medium text-foreground">
                      Private key (PEM)
                    </label>
                    <textarea
                      id="provider-private-key"
                      name="provider-private-key"
                      autoComplete="off"
                      data-1p-ignore
                      data-lpignore="true"
                      data-form-type="other"
                      spellCheck={false}
                      rows={4}
                      className="w-full rounded-md border border-input bg-background px-3 py-2 font-mono text-2xs shadow-sm transition-colors placeholder:text-muted-foreground focus-visible:border-foreground/40 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/30"
                      placeholder="-----BEGIN PRIVATE KEY-----"
                      value={keyValue}
                      onChange={e => setKeyValue(e.target.value)}
                      required
                    />
                  </div>
                ) : (
                  <Input
                    label={usingEntra ? "Client secret" : "API key"}
                    type="password"
                    name="provider-api-key"
                    autoComplete="new-password"
                    data-1p-ignore
                    data-lpignore="true"
                    data-form-type="other"
                    placeholder={usingEntra ? "Azure Entra client secret" : "sk-..."}
                    value={keyValue}
                    onChange={e => setKeyValue(e.target.value)}
                    required
                  />
                )}
              </div>
              {authTypeOffered ? (
                <div className="flex flex-col gap-1.5">
                  <label htmlFor="provider-auth-type" className="text-xs font-medium text-foreground">
                    Authentication
                  </label>
                  <select
                    id="provider-auth-type"
                    name="provider-auth-type"
                    className="flex h-9 w-full rounded-md border border-input bg-background px-3 py-1 text-sm shadow-sm focus-visible:border-foreground/40 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/30"
                    value={authType}
                    onChange={e => {
                      setAuthType(e.target.value as ProviderAuthType);
                      setKeyValue("");
                    }}
                  >
                    <option value="bearer">Token (sent as-is)</option>
                    <option value="keypair_jwt">Key pair (router signs a short-lived token)</option>
                    <option value="wif">Workload identity (router attests its own identity)</option>
                    <option value="azure_entra">Microsoft Entra (router mints a short-lived token)</option>
                  </select>
                  {usingWIF ? (
                    <Text className="text-2xs text-muted-foreground">
                      No credential to enter. Grant this router&apos;s workload identity access to the
                      endpoint&apos;s service user.
                    </Text>
                  ) : null}
                </div>
              ) : null}
              {usingKeypair || usingEntra ? (
                <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
                  <Input
                    label={usingEntra ? "Tenant ID" : "Account identifier"}
                    name="provider-auth-account"
                    autoComplete="off"
                    placeholder={usingEntra ? "00000000-0000-0000-0000-000000000000" : "MYORG-MYACCOUNT"}
                    value={authAccount}
                    onChange={e => setAuthAccount(e.target.value)}
                    required
                  />
                  <Input
                    label={usingEntra ? "Client ID" : "User"}
                    name="provider-auth-user"
                    autoComplete="off"
                    placeholder={usingEntra ? "00000000-0000-0000-0000-000000000000" : "SERVICE_USER"}
                    value={authUser}
                    onChange={e => setAuthUser(e.target.value)}
                    required
                  />
                </div>
              ) : null}
              {usingEntra ? (
                <Text className="text-2xs text-muted-foreground">
                  The router exchanges the client secret for a short-lived Entra token and sends only
                  that token to Azure.
                </Text>
              ) : null}
              <Input
                label={baseURLRequired ? "Endpoint URL" : "Endpoint URL (optional)"}
                name="provider-base-url"
                autoComplete="off"
                data-1p-ignore
                data-lpignore="true"
                data-form-type="other"
                placeholder="https://gateway.example.com/api"
                value={baseURL}
                onChange={e => setBaseURL(e.target.value)}
                required={baseURLRequired}
              />
              <Text className="text-2xs text-muted-foreground">
                {baseURLRequired
                  ? "This provider has no default endpoint. Give the full base URL; the router appends the API path (e.g. /v1/messages)."
                  : "Leave blank to use the provider's default endpoint. The router appends the API path (e.g. /v1/messages)."}
              </Text>
              <Input
                label="Name (optional)"
                name="provider-key-label"
                autoComplete="off"
                data-1p-ignore
                data-lpignore="true"
                data-form-type="other"
                placeholder="My Anthropic key"
                value={name}
                onChange={e => setName(e.target.value)}
              />
              <ModelAliasEditor
                rows={aliasRows}
                onChange={setAliasRows}
                models={catalogModels}
                idPrefix="new-key"
                endpointModels={newEndpointModels}
                onFetchModels={authTypeOffered ? handleFetchNewModels : undefined}
                fetching={fetchingNewModels}
                fetchHint={fetchHint}
              />
              <div>
                <Button
                  type="submit"
                  appearance={Appearance.Filled}
                  intent={Intent.Primary}
                  className="!border-brand !bg-brand !text-white hover:!bg-brand/90"
                  disabled={saveDisabled}
                >
                  {saving ? "Saving…" : "Save key"}
                </Button>
              </div>
            </form>
          </Card.Content>
        </Card>
      ) : null}

      {hasAnyKey ? (
        <Card className="p-0">
          <Card.Header className="border-b border-border px-5 py-3">
            <Card.Title variant="h4">Active provider keys</Card.Title>
          </Card.Header>
          <Card.Content>
            <ul className="divide-y divide-border">
              {envKeyed.map(p => (
                <li
                  key={`env-${p}`}
                  className="flex items-center justify-between px-5 py-3"
                >
                  <div>
                    <div className="flex items-center gap-2">
                      <span className="text-xs font-medium text-foreground">
                        {PROVIDER_LABEL[p]}
                      </span>
                      <span className="rounded border border-border bg-muted px-1.5 py-0.5 text-[10px] uppercase tracking-wide text-muted-foreground">
                        env
                      </span>
                    </div>
                    <p className="mt-0.5 font-mono text-2xs text-muted-foreground">
                      Set via {PROVIDER_ENV_VAR[p]}
                    </p>
                  </div>
                  <Button
                    appearance={Appearance.Hollow}
                    intent={Intent.Danger}
                    size="icon"
                    disabled
                    title="Unset the env var and restart the router to remove."
                  >
                    <Trash2 className="size-3.5" />
                  </Button>
                </li>
              ))}
              {keys.map(k => (
                <li key={k.id} className="px-5 py-3">
                  <div className="flex items-center justify-between">
                    <div>
                      <div className="flex items-center gap-2">
                        <span className="text-xs font-medium text-foreground">
                          {PROVIDER_LABEL[k.provider as Provider] ?? k.provider}
                        </span>
                        {k.name != null && (
                          <span className="text-2xs text-muted-foreground">· {k.name}</span>
                        )}
                      </div>
                      <p className="mt-0.5 font-mono text-2xs text-muted-foreground">
                        {k.key_prefix}…{k.key_suffix}
                        {k.base_url != null && k.base_url !== "" && (
                          <span className="ml-2 font-sans">· {k.base_url}</span>
                        )}
                      </p>
                      <p className="mt-0.5 text-2xs text-muted-foreground">
                        {Object.keys(k.model_aliases ?? {}).length === 0
                          ? "No model aliases"
                          : Object.entries(k.model_aliases ?? {})
                              .map(([model, alias]) => `${model} → ${alias}`)
                              .join(", ")}
                      </p>
                    </div>
                    <div className="flex items-center gap-2">
                      <Button
                        appearance={Appearance.Outlined}
                        size="sm"
                        onClick={() => {
                          if (editingAliases === k.id) {
                            setEditingAliases(null);
                            editFetchIDRef.current = null;
                            setFetchingEditModels(false);
                            return;
                          }
                          setEditingAliases(k.id);
                          setEditAliasRows(aliasRowsFrom(k.model_aliases));
                          setEditEndpointModels(null);
                          editFetchIDRef.current = null;
                          setFetchingEditModels(false);
                          // Gateways publish their inventory; pull it right
                          // away so aliases are linkable without a click.
                          if ((PROVIDERS_REQUIRING_BASE_URL as readonly string[]).includes(k.provider)) {
                            void handleFetchEditModels(k.id);
                          }
                        }}
                      >
                        {editingAliases === k.id ? "Cancel" : "Edit aliases"}
                      </Button>
                      <Button
                        appearance={Appearance.Hollow}
                        intent={Intent.Danger}
                        size="icon"
                        onClick={() => handleDelete(k.id)}
                        disabled={deleting === k.id}
                        title="Revoke key."
                      >
                        <Trash2 className="size-3.5" />
                      </Button>
                    </div>
                  </div>
                  {editingAliases === k.id && (
                    <div className="mt-3 flex flex-col gap-2 border-t border-border pt-3">
                      <ModelAliasEditor
                        rows={editAliasRows}
                        onChange={setEditAliasRows}
                        models={catalogModels}
                        idPrefix={`key-${k.id}`}
                        endpointModels={editEndpointModels}
                        onFetchModels={() => handleFetchEditModels(k.id)}
                        fetching={fetchingEditModels}
                      />
                      <div>
                        <Button
                          appearance={Appearance.Filled}
                          intent={Intent.Primary}
                          className="!border-brand !bg-brand !text-white hover:!bg-brand/90"
                          onClick={() => handleSaveAliases(k.id)}
                          disabled={savingAliases}
                        >
                          {savingAliases ? "Saving…" : "Save aliases"}
                        </Button>
                      </div>
                    </div>
                  )}
                </li>
              ))}
            </ul>
          </Card.Content>
        </Card>
      ) : (
        <EmptyHint>No provider keys configured.</EmptyHint>
      )}
    </>
  );
}

function ProviderPicker({
  value,
  onChange,
  options,
}: {
  value: Provider;
  onChange: (p: Provider) => void;
  options: readonly Provider[];
}) {
  const [open, setOpen] = useState(false);
  return (
    <div className="flex flex-col gap-1.5">
      <label htmlFor="provider-picker" className="text-xs font-medium text-foreground">
        Provider
      </label>
      <Popover open={open} onOpenChange={setOpen}>
        <Popover.Trigger>
          <Button
            id="provider-picker"
            type="button"
            appearance={Appearance.Outlined}
            className="h-9 w-full justify-between border-input px-3 text-sm font-normal"
          >
            <span>{providerLabel(value)}</span>
            <ChevronDown className="size-3.5 text-muted-foreground" />
          </Button>
        </Popover.Trigger>
        <Popover.Content className="w-56 p-1" align="start">
          <Command>
            <Command.List>
              {options.map(p => (
                <Command.Item
                  key={p}
                  onSelect={() => {
                    onChange(p);
                    setOpen(false);
                  }}
                >
                  {providerLabel(p)}
                </Command.Item>
              ))}
            </Command.List>
          </Command>
        </Popover.Content>
      </Popover>
    </div>
  );
}
