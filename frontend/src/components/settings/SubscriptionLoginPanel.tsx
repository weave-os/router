"use client";

import { EmptyHint, ErrorBanner } from "./shared";
import { Text } from "@/components/atoms/Text";
import { Input } from "@/components/Input";
import { Button } from "@/components/molecules/Button";
import { Card } from "@/components/molecules/Card";
import { Appearance, Intent } from "@/components/types";
import {
  api,
  type LinkedSubscriptionAccount,
  type StartedSubscriptionLogin,
  type SubscriptionLoginProvider,
  type SubscriptionOwner,
  type SubscriptionSeat,
  type SubscriptionOwnersResponse,
} from "@/lib/api";
import { ExternalLink } from "lucide-react";
import { useEffect, useState } from "react";

const PROVIDER_LABEL: Record<SubscriptionLoginProvider, string> = {
  claude: "Claude",
  codex: "Codex (ChatGPT)",
};

interface ActiveLogin {
  provider: SubscriptionLoginProvider;
  subscriberID: string;
  started: StartedSubscriptionLogin;
}

// The key identity disambiguates owners whose keys share a name or have none.
function subscriptionOwnerLabel(owner: SubscriptionOwner): string {
  const key = `${owner.key_prefix}…${owner.key_suffix}`;
  if (owner.display_name === "" && owner.email === "") {
    return `Unnamed key ${key}`;
  }
  const name =
    owner.email !== "" && owner.display_name !== "" && owner.email !== owner.display_name
      ? `${owner.display_name} (${owner.email})`
      : owner.display_name || owner.email;
  return `${name} · ${key}`;
}

export function SubscriptionLoginPanel({ onLinked }: { onLinked?: () => void }) {
  const [owners, setOwners] = useState<SubscriptionOwnersResponse | null>(null);
  const [subscriberID, setSubscriberID] = useState("");
  const [login, setLogin] = useState<ActiveLogin | null>(null);
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [linked, setLinked] = useState<LinkedSubscriptionAccount | null>(null);

  useEffect(() => {
    api.subscriptionLogins
      .owners()
      .then(response => {
        setOwners(response);
        setSubscriberID(response.owners[0]?.subscriber_id ?? "");
      })
      .catch((err: unknown) => setError(String(err)));
  }, []);

  // Codex uses a device code: poll until the person approves it on OpenAI's page.
  useEffect(() => {
    if (login == null || login.provider !== "codex") return;
    let cancelled = false;
    const intervalMs = Math.max(login.started.poll_interval_seconds ?? 5, 1) * 1000;
    const poll = async () => {
      try {
        const account = await api.subscriptionLogins.complete(
          login.subscriberID,
          login.started.login_token,
        );
        if (cancelled) return;
        if (account == null) {
          timer = setTimeout(poll, intervalMs);
          return;
        }
        setLinked(account);
        setLogin(null);
        onLinked?.();
      } catch (err) {
        if (cancelled) return;
        setError(String(err));
        setLogin(null);
      }
    };
    let timer = setTimeout(poll, intervalMs);
    return () => {
      cancelled = true;
      clearTimeout(timer);
    };
  }, [login, onLinked]);

  async function start(provider: SubscriptionLoginProvider) {
    setBusy(true);
    setError(null);
    setLinked(null);
    setCode("");
    try {
      const started = await api.subscriptionLogins.start(subscriberID, provider);
      setLogin({ provider, started, subscriberID });
      window.open(started.verification_url, "_blank", "noopener,noreferrer");
    } catch (err) {
      setError(String(err));
    } finally {
      setBusy(false);
    }
  }

  async function submitClaudeCode() {
    if (login == null) return;
    setBusy(true);
    setError(null);
    try {
      const account = await api.subscriptionLogins.complete(
        login.subscriberID,
        login.started.login_token,
        code.trim(),
      );
      setLinked(account);
      setLogin(null);
      onLinked?.();
    } catch (err) {
      setError(String(err));
    } finally {
      setBusy(false);
    }
  }

  if (owners == null) {
    return error != null ? <ErrorBanner>{error}</ErrorBanner> : null;
  }
  if (!owners.enabled) {
    return (
      <EmptyHint>
        Linking subscriptions is off. Set ROUTER_SUBSCRIPTION_POOLS_ENABLED=true and
        EXTERNAL_KEY_ENCRYPTION_KEY (a Tink AES-256-GCM keyset) on the router, then restart it.
      </EmptyHint>
    );
  }
  if (owners.owners.length === 0) {
    return (
      <EmptyHint>
        Nobody has a personal routing key yet. A subscription is linked to a person, so have them
        install the router with their own key first.
      </EmptyHint>
    );
  }

  return (
    <Card className="flex flex-col gap-4 p-4">
      {error != null && <ErrorBanner>{error}</ErrorBanner>}
      {linked != null && (
        <Text className="text-xs text-foreground">
          Linked {PROVIDER_LABEL[linked.provider]}
          {linked.display_name != null && linked.display_name !== ""
            ? ` (${linked.display_name})`
            : ""}
          . It shows up on the dashboard&apos;s Subscriptions tab once it serves a request.
        </Text>
      )}
      <div className="flex flex-col gap-1.5">
        <label htmlFor="subscription-owner" className="text-xs font-medium text-foreground">
          Link for
        </label>
        <select
          id="subscription-owner"
          className="h-9 rounded-md border border-input bg-background px-3 text-sm"
          value={subscriberID}
          disabled={login != null}
          onChange={event => setSubscriberID(event.target.value)}
        >
          {owners.owners.map(owner => (
            <option key={owner.subscriber_id} value={owner.subscriber_id}>
              {subscriptionOwnerLabel(owner)}
            </option>
          ))}
        </select>
      </div>
      {login == null && (
        <div className="flex flex-row gap-2">
          {(Object.keys(PROVIDER_LABEL) as SubscriptionLoginProvider[]).map(provider => (
            <Button
              key={provider}
              appearance={Appearance.Filled}
              intent={Intent.Primary}
              size="sm"
              disabled={busy || subscriberID === ""}
              onClick={() => void start(provider)}
            >
              Link {PROVIDER_LABEL[provider]}
            </Button>
          ))}
        </div>
      )}
      {login != null && (
        <div className="flex flex-col gap-3">
          <a
            href={login.started.verification_url}
            target="_blank"
            rel="noopener noreferrer"
            className="flex items-center gap-1 text-xs text-primary underline"
          >
            Open the {PROVIDER_LABEL[login.provider]} sign-in page
            <ExternalLink className="size-3" />
          </a>
          {login.provider === "codex" ? (
            <Text className="text-xs text-muted-foreground">
              Enter code{" "}
              <span className="font-mono text-foreground">{login.started.user_code}</span> on that
              page. This finishes on its own once it&apos;s approved.
            </Text>
          ) : (
            <div className="flex flex-row items-end gap-2">
              <Input
                id="subscription-login-code"
                label="Paste the code Claude shows after you sign in"
                autoComplete="off"
                value={code}
                onChange={event => setCode(event.target.value)}
              />
              <Button
                appearance={Appearance.Filled}
                intent={Intent.Primary}
                size="sm"
                disabled={busy || code.trim() === ""}
                onClick={() => void submitClaudeCode()}
              >
                Link
              </Button>
            </div>
          )}
          <Button
            appearance={Appearance.Outlined}
            size="sm"
            className="self-start"
            onClick={() => setLogin(null)}
          >
            Cancel
          </Button>
        </div>
      )}
    </Card>
  );
}

// Lists every linked seat, including ones whose owner lost their last routing
// key, so an admin can always unlink a seat and let it be linked again.
export function SubscriptionSeatsPanel({ refreshKey }: { refreshKey: number }) {
  const [seats, setSeats] = useState<SubscriptionSeat[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [unlinking, setUnlinking] = useState<string | null>(null);

  useEffect(() => {
    api.subscriptionLogins
      .seats()
      .then(response => setSeats(response.seats))
      .catch((err: unknown) => setError(String(err)));
  }, [refreshKey]);

  async function unlink(seat: SubscriptionSeat) {
    setUnlinking(seat.account_id);
    setError(null);
    try {
      await api.subscriptionLogins.unlink(seat.subscriber_id, seat.account_id);
      setSeats(current => current?.filter(other => other.account_id !== seat.account_id) ?? null);
    } catch (err) {
      setError(String(err));
    } finally {
      setUnlinking(null);
    }
  }

  if (seats == null) {
    return error != null ? <ErrorBanner>{error}</ErrorBanner> : null;
  }
  if (seats.length === 0) {
    return <EmptyHint>No subscriptions are linked yet.</EmptyHint>;
  }
  return (
    <Card className="flex flex-col divide-y divide-border">
      {error != null && <ErrorBanner>{error}</ErrorBanner>}
      {seats.map(seat => (
        <div key={seat.account_id} className="flex flex-row items-center gap-3 p-3">
          <div className="flex min-w-0 flex-1 flex-col">
            <Text className="truncate text-sm">
              {PROVIDER_LABEL[seat.provider]}
              {seat.display_name !== "" ? ` · ${seat.display_name}` : ""}
            </Text>
            <Text className="truncate text-xs text-muted-foreground">
              {seat.owner_display_name || seat.owner_email || "Unknown owner"}
              {seat.owner_has_routing_key ? "" : " · no routing key, not serving requests"}
            </Text>
          </div>
          <Button
            appearance={Appearance.Outlined}
            intent={Intent.Danger}
            size="sm"
            disabled={unlinking != null}
            onClick={() => void unlink(seat)}
          >
            Unlink
          </Button>
        </div>
      ))}
    </Card>
  );
}
