"use client";

import { ErrorBanner } from "./shared";
import { Text } from "@/components/atoms/Text";
import { Button } from "@/components/molecules/Button";
import { Card } from "@/components/molecules/Card";
import { Modal } from "@/components/molecules/Modal";
import { Appearance, Intent } from "@/components/types";
import { api, type CodexOAuthStatus } from "@/lib/api";
import { ExternalLink, KeyRound, RefreshCw } from "lucide-react";
import { useEffect, useState } from "react";

const IDLE_STATUS: CodexOAuthStatus = { state: "idle" };

function statusLabel(status: CodexOAuthStatus): string {
  switch (status.state) {
    case "authenticated":
      return "Connected";
    case "pending":
      return "Waiting for browser sign-in";
    case "failed":
      return "Sign-in failed";
    default:
      return "Not connected";
  }
}

/** CodexOAuthPanel connects the local Codex subscription without exposing its token. */
export function CodexOAuthPanel() {
  const [status, setStatus] = useState<CodexOAuthStatus>(IDLE_STATUS);
  const [loading, setLoading] = useState(true);
  const [starting, setStarting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function refresh() {
    try {
      setStatus(await api.codexOAuth.status());
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : "Failed to read Codex OAuth status.");
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    void refresh();
  }, []);

  useEffect(() => {
    if (status.state !== "pending") return;
    const timer = window.setInterval(() => void refresh(), 2000);
    return () => window.clearInterval(timer);
  }, [status.state]);

  async function start() {
    setStarting(true);
    setError(null);
    try {
      const next = await api.codexOAuth.start();
      setStatus(next);
      if (next.auth_url) {
        const popup = window.open(next.auth_url, "weave-codex-oauth", "popup,width=620,height=760");
        if (popup == null) window.location.assign(next.auth_url);
      }
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : "Failed to start Codex OAuth.");
    } finally {
      setStarting(false);
    }
  }

  async function cancel() {
    setError(null);
    try {
      await api.codexOAuth.cancel();
      setStatus(IDLE_STATUS);
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : "Failed to cancel Codex OAuth.");
    }
  }

  const connected = status.state === "authenticated";
  const pending = status.state === "pending";

  return (
    <Card className="border-brand/30 bg-brand/[0.03]">
      {error && <ErrorBanner>{error}</ErrorBanner>}
      <Card.Header className="flex-row items-start justify-between gap-4">
        <div className="flex items-start gap-3">
          <div className="rounded-md border border-brand/30 bg-brand/10 p-2 text-brand">
            <KeyRound className="size-4" />
          </div>
          <div>
            <Card.Title variant="h4">Codex OAuth</Card.Title>
            <Text className="mt-1 text-2xs text-muted-foreground">
              Use the ChatGPT subscription already signed in by the local Codex CLI.
            </Text>
          </div>
        </div>
        <span className="whitespace-nowrap rounded-full border border-border px-2 py-1 text-[10px] uppercase tracking-wide text-muted-foreground">
          {loading ? "Checking…" : statusLabel(status)}
        </span>
      </Card.Header>
      <Card.Content className="flex flex-col gap-3">
        <Text className="text-2xs text-muted-foreground">
          Router starts <code className="router-mono text-foreground">codex app-server</code> locally;
          OpenAI handles the browser login and the credential remains in Codex&apos;s own store.
        </Text>
        {status.error && <Text className="text-2xs text-danger">{status.error}</Text>}
        {pending && status.auth_url && (
          <a
            className="flex items-center gap-1 text-2xs text-brand underline"
            href={status.auth_url}
            target="_blank"
            rel="noreferrer"
          >
            Open the Codex sign-in page <ExternalLink className="size-3" />
          </a>
        )}
        <div className="flex flex-wrap items-center gap-2">
          <Button
            appearance={Appearance.Filled}
            intent={Intent.Primary}
            className="!border-brand !bg-brand !text-white hover:!bg-brand/90"
            onClick={start}
            disabled={starting || pending}
          >
            <RefreshCw className="size-3.5" />
            {starting ? "Starting…" : connected ? "Reconnect Codex" : "Connect Codex"}
          </Button>
          {pending && (
            <Button appearance={Appearance.Outlined} onClick={cancel}>
              Cancel
            </Button>
          )}
        </div>
      </Card.Content>
    </Card>
  );
}
