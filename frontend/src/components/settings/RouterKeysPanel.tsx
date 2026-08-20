"use client";

import { EmptyHint, ErrorBanner, formatDate } from "./shared";
import { Text } from "@/components/atoms/Text";
import { Input } from "@/components/Input";
import { InstallCommandPicker } from "@/components/InstallCommandPicker";
import { Button } from "@/components/molecules/Button";
import { Card } from "@/components/molecules/Card";
import { Appearance, Intent } from "@/components/types";
import { api, type APIKey, type APIKeyScope, type IssueAPIKeyResponse } from "@/lib/api";
import { Copy, RotateCw, Search, Trash2 } from "lucide-react";
import { useEffect, useState } from "react";

// Show the search box only once the list is long enough that scanning it by eye
// gets tedious; for a couple of keys a search bar is just noise.
const KEY_SEARCH_THRESHOLD = 5;

// Shown in place of a name for keys created without one; kept in one place so the
// list row and the search haystack stay in sync.
const UNNAMED_KEY_LABEL = "Unnamed key";

// Alphabetical by name, with unnamed keys pushed to the bottom.
function compareKeysByName(a: APIKey, b: APIKey): number {
  if (a.name == null && b.name == null) return 0;
  if (a.name == null) return 1;
  if (b.name == null) return -1;
  return a.name.localeCompare(b.name);
}

const SCOPE_OPTIONS: { value: APIKeyScope; label: string; hint: string }[] = [
  { value: "routing", label: "Routing", hint: "Proxies inference through the router." },
  {
    value: "analytics_read",
    label: "Analytics (read-only)",
    hint: "Reads the analytics export. Cannot route requests or spend.",
  },
];

function scopeLabel(scope: APIKeyScope): string {
  return SCOPE_OPTIONS.find(o => o.value === scope)?.label ?? scope;
}

// Case-insensitive substring match over what the row actually shows: the label
// ("Unnamed key" when there's no name), the scope badge, the raw prefix/suffix
// (so the last few characters of a token match), and the exact "prefix…suffix"
// fingerprint, so pasting the visible fingerprint verbatim also finds it.
function keyMatchesQuery(k: APIKey, query: string): boolean {
  if (query === "") return true;
  const label = k.name ?? UNNAMED_KEY_LABEL;
  const badge = k.scope === "analytics_read" ? scopeLabel(k.scope) : "";
  const haystack =
    `${label} ${badge} ${k.key_prefix} ${k.key_suffix} ${k.key_prefix}…${k.key_suffix}`.toLowerCase();
  return haystack.includes(query);
}

export function RouterKeysPanel() {
  const [keys, setKeys] = useState<APIKey[]>([]);
  const [query, setQuery] = useState("");
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [name, setName] = useState("");
  const [scope, setScope] = useState<APIKeyScope>("routing");
  const [creating, setCreating] = useState(false);
  const [rotating, setRotating] = useState<string | null>(null);
  const [deleting, setDeleting] = useState<string | null>(null);
  // Holds the issued token together with the scope it was minted for: only a
  // routing key has an installer to offer, and rotate carries forward the
  // rotated key's scope rather than whatever the create form currently shows.
  const [issued, setIssued] = useState<IssueAPIKeyResponse | null>(null);
  const [copied, setCopied] = useState(false);

  const hasKey = keys.length > 0;
  const showSearch = keys.length >= KEY_SEARCH_THRESHOLD;
  const normalizedQuery = query.trim().toLowerCase();
  // Only filter while the search box is actually shown; otherwise a stale query
  // from before the list shrank below the threshold would hide keys with no
  // visible input to clear it.
  const activeQuery = showSearch ? normalizedQuery : "";
  const visibleKeys = keys
    .slice()
    .sort(compareKeysByName)
    .filter(k => keyMatchesQuery(k, activeQuery));

  function load() {
    api.keys
      .list()
      .then(r => {
        setKeys(r.keys ?? []);
        setLoaded(true);
      })
      .catch((err: unknown) => {
        setError(err instanceof Error ? err.message : "Failed to load keys.");
        setLoaded(true);
      });
  }

  useEffect(load, []);

  async function handleCreate(e: React.FormEvent) {
    e.preventDefault();
    setCreating(true);
    try {
      const res = await api.keys.issue(name.trim() || undefined, scope);
      setIssued(res);
      setName("");
      load();
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : "Failed to create key.");
    } finally {
      setCreating(false);
    }
  }

  async function handleRotate(id: string) {
    const confirmed = window.confirm(
      "Rotate this API key?\n\nThe current token will stop working immediately. A new token will be shown once.",
    );
    if (!confirmed) return;
    setRotating(id);
    try {
      const res = await api.keys.rotate(id);
      setIssued(res);
      load();
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : "Failed to rotate key.");
    } finally {
      setRotating(null);
    }
  }

  async function handleDelete(id: string) {
    const confirmed = window.confirm(
      "Revoke this API key?\n\nThe token will stop working immediately. You will need to issue a new key before clients can authenticate again.",
    );
    if (!confirmed) return;
    setDeleting(id);
    try {
      await api.keys.delete(id);
      load();
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : "Failed to delete key.");
    } finally {
      setDeleting(null);
    }
  }

  function handleCopy() {
    if (issued == null) return;
    navigator.clipboard.writeText(issued.token).then(() => {
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    });
  }

  return (
    <>
      {error && <ErrorBanner>{error}</ErrorBanner>}

      {issued != null && (
        <div className="flex flex-col gap-3 rounded-lg border border-success/30 bg-success/5 p-4">
          <div className="flex flex-col gap-2">
            <Text className="text-xs font-medium text-success">
              Key created. Copy it; it won&apos;t be shown again.
            </Text>
            <div className="flex items-center gap-2">
              <code className="flex-1 rounded bg-muted px-3 py-1.5 font-mono text-2xs text-foreground">
                {issued.token}
              </code>
              <Button appearance={Appearance.Outlined} size="sm" onClick={handleCopy}>
                <Copy className="size-3.5" />
                {copied ? "Copied" : "Copy"}
              </Button>
            </div>
          </div>

          {/* A routing key is only useful once a harness points at it, so the
              installer command ships with the token rather than leaving the
              user to hunt for it. Analytics keys have nothing to install. */}
          {issued.key.scope === "routing" && (
            <div className="flex flex-col gap-2 border-t border-success/30 pt-3">
              <Text className="text-xs font-medium text-foreground">
                Point a harness at this router
              </Text>
              <InstallCommandPicker token={issued.token} />
            </div>
          )}

          <button
            className="self-start text-2xs text-muted-foreground underline"
            onClick={() => setIssued(null)}
          >
            Dismiss
          </button>
        </div>
      )}

      {loaded && (
        <Card>
          <Card.Header>
            <Card.Title variant="h4">Issue a new key</Card.Title>
          </Card.Header>
          <Card.Content>
            <form onSubmit={handleCreate} className="flex flex-col gap-3" autoComplete="off">
              {/* Ahead of the submit button in DOM order: picking the wrong scope
                  mints a credential with the wrong authority, so the choice has to
                  come before the button that acts on it. */}
              <div className="flex flex-col gap-1.5">
                <div role="radiogroup" aria-label="Key scope" className="flex flex-wrap gap-2">
                  {SCOPE_OPTIONS.map(o => (
                    <Button
                      key={o.value}
                      type="button"
                      role="radio"
                      aria-checked={scope === o.value}
                      size="sm"
                      appearance={scope === o.value ? Appearance.Filled : Appearance.Outlined}
                      className={
                        scope === o.value ? "!border-brand !bg-brand !text-white hover:!bg-brand/90" : undefined
                      }
                      title={o.hint}
                      onClick={() => setScope(o.value)}
                    >
                      {o.label}
                    </Button>
                  ))}
                </div>
                <Text className="text-2xs text-muted-foreground">
                  {SCOPE_OPTIONS.find(o => o.value === scope)?.hint}
                </Text>
              </div>
              <div className="flex items-end gap-3">
                <div className="flex-1">
                  <Input
                    label="Name (optional)"
                    name="router-key-label"
                    autoComplete="off"
                    data-1p-ignore
                    data-lpignore="true"
                    data-form-type="other"
                    placeholder="My API key"
                    value={name}
                    onChange={e => setName(e.target.value)}
                  />
                </div>
                <Button
                  type="submit"
                  appearance={Appearance.Filled}
                  intent={Intent.Primary}
                  className="!border-brand !bg-brand !text-white hover:!bg-brand/90"
                  disabled={creating}
                >
                  {creating ? "Creating…" : "Create key"}
                </Button>
              </div>
            </form>
          </Card.Content>
        </Card>
      )}

      {hasKey ? (
        <Card className="p-0">
          <Card.Header className="flex-row items-center justify-between gap-3 border-b border-border px-5 py-3">
            <Card.Title variant="h4">Active router keys</Card.Title>
            {showSearch && (
              <div className="relative w-48">
                <Search className="pointer-events-none absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
                <Input
                  type="search"
                  aria-label="Search router keys"
                  placeholder="Search keys"
                  className="h-8 pl-8 text-xs"
                  value={query}
                  onChange={e => setQuery(e.target.value)}
                />
              </div>
            )}
          </Card.Header>
          <Card.Content>
            {visibleKeys.length === 0 ? (
              <div className="px-5 py-8 text-center text-2xs text-muted-foreground">
                No keys match “{query.trim()}”.
              </div>
            ) : (
            <ul className="divide-y divide-border">
              {visibleKeys.map(k => (
                <li key={k.id} className="flex items-center justify-between gap-3 px-5 py-3">
                  <div className="min-w-0 flex-1">
                    <div className="flex items-center gap-2 text-xs font-medium text-foreground">
                      {k.name ?? UNNAMED_KEY_LABEL}
                      {k.scope === "analytics_read" && (
                        <span className="rounded bg-muted px-1.5 py-0.5 text-2xs font-normal text-muted-foreground">
                          {scopeLabel(k.scope)}
                        </span>
                      )}
                    </div>
                    <p className="mt-0.5 truncate font-mono text-2xs text-muted-foreground">
                      {k.key_prefix}…{k.key_suffix}
                      <span className="ml-2 font-sans">
                        · created {formatDate(k.created_at)}
                        {k.last_used_at != null && ` · last used ${formatDate(k.last_used_at)}`}
                      </span>
                    </p>
                  </div>
                  <div className="flex items-center gap-2">
                    <Button
                      appearance={Appearance.Outlined}
                      size="sm"
                      onClick={() => handleRotate(k.id)}
                      disabled={rotating != null || deleting != null}
                    >
                      <RotateCw className="size-3.5" />
                      {rotating === k.id ? "Rotating…" : "Rotate"}
                    </Button>
                    <Button
                      appearance={Appearance.Hollow}
                      intent={Intent.Danger}
                      size="icon"
                      onClick={() => handleDelete(k.id)}
                      disabled={deleting === k.id || rotating != null}
                      title="Revoke key."
                    >
                      <Trash2 className="size-3.5" />
                    </Button>
                  </div>
                </li>
              ))}
            </ul>
            )}
          </Card.Content>
        </Card>
      ) : loaded ? (
        <EmptyHint>No router keys yet.</EmptyHint>
      ) : null}
    </>
  );
}
