"use client";

import { CopyBlock } from "@/components/CopyBlock";
import { Text } from "@/components/atoms/Text";
import { Button } from "@/components/molecules/Button";
import { Appearance } from "@/components/types";
import { api, type APIKey, type IssueAPIKeyResponse } from "@/lib/api";
import {
  HARNESSES,
  type HarnessID,
  type InstallScope,
  harness,
  installCommand,
  routerOrigin,
} from "@/lib/installCommands";
import { cn } from "@/lib/cn";
import { ArrowRight, ChevronRight, GitBranch, Loader2, User, Zap } from "lucide-react";
import { useEffect, useState } from "react";

const POLL_INTERVAL_MS = 3000;

type StepID = "setup" | "harness" | "scope" | "install";

const STEPS: StepID[] = ["setup", "harness", "scope", "install"];

interface RouterOnboardingProps {
  /** Called once the router has served its first request through any key. */
  onComplete: () => void;
}

/**
 * First-run onboarding for a self-hosted router: one decision per screen —
 * issue a key → pick a harness → personal vs team → copy the command — then
 * idle until the router serves its first request and hand off to the
 * dashboard.
 *
 * Deliberately mirrors the hosted flow at router.workweave.ai so the two
 * surfaces teach the same mental model, but the base URL comes from this
 * deployment's own origin rather than the hosted endpoint.
 */
export function RouterOnboarding({ onComplete }: RouterOnboardingProps) {
  const [step, setStep] = useState<StepID>("setup");
  const [harnessID, setHarnessID] = useState<HarnessID | null>(null);
  const [scope, setScope] = useState<InstallScope>("user");
  const [issued, setIssued] = useState<IssueAPIKeyResponse | null>(null);
  const [creating, setCreating] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function handleGetStarted() {
    setCreating(true);
    setError(null);
    try {
      const res = await api.keys.issue("Onboarding key");
      setIssued(res);
      setStep("harness");
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : "Failed to create key.");
    } finally {
      setCreating(false);
    }
  }

  const currentIndex = Math.max(0, STEPS.indexOf(step));

  return (
    <div className="flex min-h-full w-full flex-col items-center justify-center p-8">
      {error != null && (
        <div className="mb-4 rounded-md border border-danger/30 bg-danger/5 p-3 text-xs text-danger">
          {error}
        </div>
      )}

      {step === "setup" && (
        <SetupStep creating={creating} onGetStarted={() => void handleGetStarted()} />
      )}
      {step === "harness" && (
        <HarnessStep
          onSelect={id => {
            setHarnessID(id);
            setStep("scope");
          }}
        />
      )}
      {step === "scope" && harnessID != null && (
        <ScopeStep
          harnessID={harnessID}
          onBack={() => setStep("harness")}
          onSelect={s => {
            setScope(s);
            setStep("install");
          }}
        />
      )}
      {step === "install" && harnessID != null && issued != null && (
        <InstallStep
          harnessID={harnessID}
          scope={scope}
          token={issued.token}
          onBack={() => setStep("scope")}
          onComplete={onComplete}
        />
      )}

      {step !== "setup" && <StepDots currentIndex={currentIndex} />}
    </div>
  );
}

function StepDots({ currentIndex }: { currentIndex: number }) {
  return (
    <div className="mt-10 flex items-center gap-3">
      {STEPS.map((id, index) => (
        <div
          key={id}
          aria-label={id}
          title={id}
          className={cn(
            "rounded-full transition-all duration-300",
            index === currentIndex
              ? "h-3 w-8 bg-brand"
              : index < currentIndex
                ? "size-2.5 bg-muted-foreground/50"
                : "size-2.5 bg-muted-foreground/20",
          )}
        />
      ))}
    </div>
  );
}

function SetupStep({
  creating,
  onGetStarted,
}: {
  creating: boolean;
  onGetStarted: () => void;
}) {
  return (
    <div className="flex w-full max-w-sm flex-col items-center gap-6 rounded-2xl bg-background p-10 shadow-lg ring-1 ring-border">
      <div className="flex size-14 items-center justify-center rounded-2xl bg-brand/10">
        <Zap className="size-7 text-brand" />
      </div>
      <div className="flex flex-col items-center gap-2 text-center">
        <Text variant="h3" className="text-lg font-semibold">
          Set up your router key
        </Text>
        <Text className="text-xs text-muted-foreground">
          Issue a bearer token to start routing your AI requests through this router.
        </Text>
      </div>
      <Button
        appearance={Appearance.Filled}
        onClick={onGetStarted}
        disabled={creating}
        className="w-full justify-center !border-brand !bg-brand !text-white hover:!bg-brand/90"
      >
        {creating ? "Creating key…" : "Get started"}
        <ArrowRight className="size-4" />
      </Button>
    </div>
  );
}

function HarnessStep({ onSelect }: { onSelect: (id: HarnessID) => void }) {
  return (
    <div className="flex w-full max-w-lg flex-col items-center gap-6">
      <div className="flex flex-col items-center gap-2 text-center">
        <Text variant="h3" className="text-lg font-semibold">
          Choose your harness
        </Text>
        <Text className="text-xs text-muted-foreground">
          Which coding agent do you want to route through this router?
        </Text>
      </div>
      <div className="flex w-full flex-col gap-3">
        {HARNESSES.map(h => (
          <button
            key={h.id}
            type="button"
            onClick={() => onSelect(h.id)}
            className="flex items-center gap-4 rounded-xl border border-border-darker bg-background p-4 text-left transition-colors hover:border-brand"
          >
            <div className="flex min-w-0 flex-col gap-0.5">
              <Text className="text-xs font-medium">{h.label}</Text>
              <Text className="text-2xs text-muted-foreground">{h.detail}</Text>
            </div>
            <ChevronRight className="ml-auto size-4 shrink-0 text-muted-foreground" />
          </button>
        ))}
      </div>
    </div>
  );
}

function ScopeStep({
  harnessID,
  onBack,
  onSelect,
}: {
  harnessID: HarnessID;
  onBack: () => void;
  onSelect: (scope: InstallScope) => void;
}) {
  const selected = harness(harnessID);
  const options: { value: InstallScope; label: string; detail: string; icon: typeof User }[] = [
    {
      value: "user",
      label: "Personal install",
      detail: "Configures this machine for every project.",
      icon: User,
    },
    {
      value: "project",
      label: "Team install (this project)",
      detail: selected.projectDetail,
      icon: GitBranch,
    },
  ];

  return (
    <div className="flex w-full max-w-lg flex-col items-center gap-6">
      <div className="flex flex-col items-center gap-2 text-center">
        <Text variant="h3" className="text-lg font-semibold">
          Personal or team install?
        </Text>
        <Text className="text-xs text-muted-foreground">
          You can always find both commands later in Settings.
        </Text>
      </div>
      <div className="flex w-full flex-col gap-3">
        {options.map(o => {
          const Icon = o.icon;
          return (
            <button
              key={o.value}
              type="button"
              onClick={() => onSelect(o.value)}
              className="flex items-center gap-4 rounded-xl border border-border-darker bg-background p-4 text-left transition-colors hover:border-brand"
            >
              <Icon className="size-4 shrink-0 text-muted-foreground" />
              <div className="flex min-w-0 flex-col gap-0.5">
                <Text className="text-xs font-medium">{o.label}</Text>
                <Text className="text-2xs text-muted-foreground">{o.detail}</Text>
              </div>
              <ChevronRight className="ml-auto size-4 shrink-0 text-muted-foreground" />
            </button>
          );
        })}
      </div>
      <Button appearance={Appearance.Hollow} onClick={onBack} className="text-xs text-muted-foreground">
        Back
      </Button>
    </div>
  );
}

function InstallStep({
  harnessID,
  scope,
  token,
  onBack,
  onComplete,
}: {
  harnessID: HarnessID;
  scope: InstallScope;
  token: string;
  onBack: () => void;
  onComplete: () => void;
}) {
  const origin = routerOrigin();
  const selected = harness(harnessID);
  const pinged = useFirstRequest();

  useEffect(() => {
    if (pinged) onComplete();
  }, [pinged, onComplete]);

  return (
    <div className="flex w-full max-w-xl flex-col items-center gap-6">
      <div className="flex flex-col items-center gap-2 text-center">
        <Text variant="h3" className="text-lg font-semibold">
          Run the install command
        </Text>
        <Text className="text-xs text-muted-foreground">
          Paste this into your terminal. It points {selected.label} at this router with your key
          already inlined.
        </Text>
      </div>

      <div className="flex w-full flex-col gap-2">
        {origin === "" ? (
          <Text className="text-2xs text-muted-foreground">Preparing install command…</Text>
        ) : (
          <CopyBlock
            value={installCommand(harnessID, scope, token, origin)}
            title="Copy install command"
          />
        )}
      </div>

      <div className="flex w-full flex-col gap-2">
        <Text className="text-xs font-medium">Your API key</Text>
        <CopyBlock value={token} title="Copy API key" />
        <Text className="text-2xs text-muted-foreground">
          This is the only time the full key is shown. Save it somewhere safe.
        </Text>
      </div>

      <div className="flex items-center gap-2 text-muted-foreground">
        <Loader2 className="size-4 animate-spin text-brand" />
        <Text className="text-2xs">
          Waiting for your first request — send one prompt through {selected.label} and this page
          moves on by itself.
        </Text>
      </div>

      <div className="flex items-center gap-2">
        <Button appearance={Appearance.Hollow} onClick={onBack} className="text-xs text-muted-foreground">
          Back
        </Button>
        <Button appearance={Appearance.Outlined} size="sm" onClick={onComplete}>
          Skip to dashboard
        </Button>
      </div>
    </div>
  );
}

/**
 * True once any routing key reports a `last_used_at`, i.e. the router has
 * actually served a request. Polls because the signal arrives out-of-band
 * (from the user's terminal), not from anything this page did.
 */
function useFirstRequest(): boolean {
  const [pinged, setPinged] = useState(false);

  useEffect(() => {
    if (pinged) return;
    let cancelled = false;

    function check() {
      api.keys
        .list()
        .then(res => {
          if (cancelled) return;
          if ((res.keys ?? []).some(hasBeenUsed)) setPinged(true);
        })
        // Non-fatal: a failed poll just means we try again on the next tick.
        .catch(() => undefined);
    }

    check();
    const interval = setInterval(check, POLL_INTERVAL_MS);
    return () => {
      cancelled = true;
      clearInterval(interval);
    };
  }, [pinged]);

  return pinged;
}

function hasBeenUsed(key: APIKey): boolean {
  return key.scope === "routing" && key.last_used_at != null;
}
