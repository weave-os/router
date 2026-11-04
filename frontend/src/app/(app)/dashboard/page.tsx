"use client";

import { RouterOnboarding } from "@/components/RouterOnboarding";
import { api } from "@/lib/api";
import { RouterDashboardPage } from "@/shared/components/RouterDashboardPage";

import { useEffect, useState } from "react";

// "checking" suppresses a flash of either surface until the onboarding probe
// lands.
type OnboardingState = "checking" | "needed" | "done";

// Set when the user chooses "Skip to dashboard". Persisted rather than held in
// memory so a refresh doesn't drop them back into a flow they opted out of;
// the server-side flag still takes over for good once a request is served.
const SKIP_ONBOARDING_KEY = "weave-router.onboarding-skipped";

function onboardingSkipped(): boolean {
  if (typeof window === "undefined") return false;
  try {
    return window.localStorage.getItem(SKIP_ONBOARDING_KEY) === "true";
  } catch {
    // Private-mode/blocked storage: treat as not skipped rather than throwing
    // on the render path.
    return false;
  }
}

function rememberOnboardingSkipped() {
  try {
    window.localStorage.setItem(SKIP_ONBOARDING_KEY, "true");
  } catch {
    // Non-fatal: the skip just won't survive this reload.
  }
}

export default function DashboardPage() {
  const [onboarding, setOnboarding] = useState<OnboardingState>("checking");

  // A router that has never served a request has nothing to chart, so a fresh
  // install lands in onboarding instead of on empty charts. The gate is the
  // installation-level first_request_served_at, not a key's last_used_at:
  // that flag survives rotation, so rotating the key that served the first
  // request can't send an established install back through onboarding.
  useEffect(() => {
    let cancelled = false;
    api.onboarding
      .get()
      .then(res => {
        if (cancelled) return;
        const served = res.first_request_served_at != null;
        setOnboarding(served || onboardingSkipped() ? "done" : "needed");
      })
      // Non-fatal: on a failed probe show the dashboard rather than trapping
      // an established install in onboarding.
      .catch(() => {
        if (!cancelled) setOnboarding("done");
      });
    return () => {
      cancelled = true;
    };
  }, []);

  if (onboarding === "checking") return null;
  if (onboarding === "needed") {
    return (
      <RouterOnboarding
        onComplete={() => setOnboarding("done")}
        onSkip={() => {
          rememberOnboardingSkipped();
          setOnboarding("done");
        }}
      />
    );
  }

  return <RouterDashboardPage />;
}
