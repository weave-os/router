"use client";

import { Text } from "@/components/atoms/Text";
import { ChartCard } from "@/components/ChartCard";
import { CostBreakdownChart } from "@/components/charts/CostBreakdownChart";
import { CumulativeSavingsChart } from "@/components/charts/CumulativeSavingsChart";
import { ModelBreakdownChart } from "@/components/charts/ModelBreakdownChart";
import { RouterCostSavingsChart } from "@/components/charts/RouterCostSavingsChart";
import { SavingsRateChart } from "@/components/charts/SavingsRateChart";
import {
  DashboardPageFilters,
  useDashboardFilters,
} from "@/components/DashboardPageFilters";
import { Card } from "@/components/molecules/Card";
import { Page } from "@/components/Page";
import { PageHeader } from "@/components/PageHeader";
import { ResponsiveGrid } from "@/components/ResponsiveGrid";
import { RouterOnboarding } from "@/components/RouterOnboarding";
import { Statistic } from "@/components/Statistic";
import {
  api,
  type MetricsSummary,
  type ModelBreakdownBucket,
  type TimeseriesBucket,
} from "@/lib/api";
import { cn } from "@/lib/cn";
import { LoadState } from "@/tools/LoadState";
import {
  ArrowRight,
  CheckCircle2,
  CircleDot,
  KeyRound,
  Network,
  Radar,
  Route,
  Server,
} from "lucide-react";
import Link from "next/link";
import * as React from "react";
import { useEffect, useState } from "react";

function formatUSD(v: number): string {
  if (v === 0) return "$0.00";
  if (Math.abs(v) < 0.001) return `$${v.toFixed(4)}`;
  return `$${v.toFixed(2)}`;
}

function formatNumber(v: number): string {
  if (v >= 1_000_000) return `${(v / 1_000_000).toFixed(1)}M`;
  if (v >= 1_000) return `${(v / 1_000).toFixed(1)}K`;
  return String(v);
}

// "checking" suppresses a flash of either surface until the onboarding probe
// lands.
type OnboardingState = "checking" | "needed" | "done";

interface ControlPlaneSnapshot {
  providerKeys: number;
  routableModels: number;
  providerCount: number;
  clusterVersion: string;
}

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
  const dashboardFilters = useDashboardFilters("30d");
  const { fromISO, toISO, granularity, range } = dashboardFilters.filters;

  const [summary, setSummary] = useState<MetricsSummary | null>(null);
  const [buckets, setBuckets] = useState<TimeseriesBucket[]>([]);
  const [modelBuckets, setModelBuckets] = useState<ModelBreakdownBucket[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [onboarding, setOnboarding] = useState<OnboardingState>("checking");
  const [controlPlane, setControlPlane] = useState<ControlPlaneSnapshot | null>(null);

  // A router that has never served a request has nothing to chart, so a fresh
  // install lands in onboarding instead of on six empty charts. The gate is the
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

  useEffect(() => {
    if (onboarding !== "done") return;
    let cancelled = false;
    Promise.all([
      api.providerKeys.list(),
      api.excludedModels.get(),
      api.excludedProviders.get(),
      api.config.get(),
    ])
      .then(([keys, models, providers, config]) => {
        if (cancelled) return;
        setControlPlane({
          providerKeys: keys.keys?.length ?? 0,
          routableModels: Math.max(0, (models.available?.length ?? 0) - (models.excluded?.length ?? 0)),
          providerCount: Math.max(0, (providers.available?.length ?? 0) - (providers.excluded?.length ?? 0)),
          clusterVersion: config.cluster_version || "default",
        });
      })
      .catch(() => {
        // The metrics surface remains useful if optional inventory probes fail.
      });
    return () => {
      cancelled = true;
    };
  }, [onboarding]);

  useEffect(() => {
    if (onboarding !== "done") return;
    let cancelled = false;
    setError(null);
    Promise.all([
      api.metrics.summary(fromISO, toISO),
      api.metrics.timeseries(granularity, fromISO, toISO),
      api.metrics.modelBreakdown(granularity, fromISO, toISO),
    ])
      .then(([s, ts, mb]) => {
        if (cancelled) return;
        setSummary(s);
        setBuckets(ts.buckets ?? []);
        setModelBuckets(mb.buckets ?? []);
      })
      .catch((err: unknown) => {
        if (cancelled) return;
        setError(err instanceof Error ? err.message : "Failed to load metrics.");
      });
    return () => {
      cancelled = true;
    };
  }, [fromISO, toISO, granularity, onboarding]);

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

  if (error) {
    return (
      <Page
        header={
          <PageHeader
            left={
              <Text
                variant="h4"
                as="h2"
                className="flex flex-row items-center gap-1 whitespace-nowrap"
              >
                Dashboard
              </Text>
            }
          />
        }
      >
        <Page.Section>
          <div className="rounded-lg border border-danger/30 bg-danger/5 p-4 text-sm text-danger">
            {error}
          </div>
        </Page.Section>
      </Page>
    );
  }

  const savingsRate =
    summary == null || summary.total_requested_cost_usd === 0
      ? 0
      : (summary.total_savings_usd / summary.total_requested_cost_usd) * 100;
  const avgTokensPerReq =
    summary == null || summary.request_count === 0
      ? 0
      : summary.total_tokens / summary.request_count;
  const empty = buckets.length === 0;
  const modelEmpty = modelBuckets.length === 0;

  return (
    <Page
      header={
        <PageHeader
          left={
            <Text
              variant="h4"
              as="h2"
              className="flex flex-row items-center gap-1 whitespace-nowrap"
            >
              Router cost &amp; savings
            </Text>
          }
        />
      }
    >
      <Page.Section>
        <RouterOverview snapshot={controlPlane} />
        <DashboardPageFilters result={dashboardFilters} />
        <ResponsiveGrid>
          <MetricCard
            className={ResponsiveGrid.Small}
            label="Cost saved"
            value={summary == null ? "—" : formatUSD(Math.abs(summary.total_savings_usd))}
            sub={
              summary == null
                ? undefined
                : summary.total_savings_usd >= 0
                  ? `${savingsRate.toFixed(1)}% of requested`
                  : "Over requested cost"
            }
            accent={
              summary == null
                ? "default"
                : summary.total_savings_usd >= 0
                  ? "success"
                  : "danger"
            }
          />
          <MetricCard
            className={ResponsiveGrid.Small}
            label="Requests"
            value={summary == null ? "—" : formatNumber(summary.request_count)}
            sub={
              summary == null ? undefined : `actual ${formatUSD(summary.total_actual_cost_usd)}`
            }
          />
          <MetricCard
            className={ResponsiveGrid.Small}
            label="Tokens"
            value={summary == null ? "—" : formatNumber(summary.total_tokens)}
            sub={summary == null ? undefined : `${formatNumber(avgTokensPerReq)} avg / req`}
          />

          <ChartCard
            className={ResponsiveGrid.Full}
            title="Router cost savings"
            subtitle="Actual cost vs. what would have been charged for the requested model."
            topRight={
              <Statistic
                statistic={
                  summary == null
                    ? LoadState.loading()
                    : LoadState.loaded({
                        total:
                          summary.total_savings_usd >= 0
                            ? `${formatUSD(summary.total_savings_usd)} saved`
                            : `${formatUSD(Math.abs(summary.total_savings_usd))} extra`,
                      })
                }
              />
            }
          >
            {empty ? (
              <EmptyChart />
            ) : (
              <RouterCostSavingsChart buckets={buckets} granularity={granularity} />
            )}
          </ChartCard>

          <ChartCard
            className={ResponsiveGrid.Medium}
            title="Cumulative savings"
            subtitle={`Running total of dollars saved across the ${range.label.toLowerCase()}.`}
          >
            {empty ? (
              <EmptyChart height={220} />
            ) : (
              <CumulativeSavingsChart buckets={buckets} granularity={granularity} />
            )}
          </ChartCard>

          <ChartCard
            className={ResponsiveGrid.Medium}
            title="Savings rate"
            subtitle="Percent of requested cost avoided per bucket."
          >
            {empty ? (
              <EmptyChart height={200} />
            ) : (
              <SavingsRateChart buckets={buckets} granularity={granularity} />
            )}
          </ChartCard>

          <ChartCard
            className={ResponsiveGrid.Full}
            title="Cost breakdown per bucket"
            subtitle="Actual cost stacked with realized savings."
          >
            {empty ? (
              <EmptyChart height={220} />
            ) : (
              <CostBreakdownChart buckets={buckets} granularity={granularity} />
            )}
          </ChartCard>

          <ChartCard
            className={ResponsiveGrid.Medium}
            title="Model usage"
            subtitle="Requests per bucket broken down by the model the router selected."
          >
            {modelEmpty ? (
              <EmptyChart height={220} />
            ) : (
              <ModelBreakdownChart
                buckets={modelBuckets}
                granularity={granularity}
                metric="requests"
              />
            )}
          </ChartCard>

          <ChartCard
            className={ResponsiveGrid.Medium}
            title="Model spend"
            subtitle="Actual cost per bucket broken down by the model the router selected."
          >
            {modelEmpty ? (
              <EmptyChart height={220} />
            ) : (
              <ModelBreakdownChart
                buckets={modelBuckets}
                granularity={granularity}
                metric="spend"
              />
            )}
          </ChartCard>
        </ResponsiveGrid>
      </Page.Section>
    </Page>
  );
}

function RouterOverview({ snapshot }: { snapshot: ControlPlaneSnapshot | null }) {
  return (
    <div className="flex flex-col gap-4">
      <div className="router-hairline flex flex-col gap-6 rounded-md bg-background p-5 md:flex-row md:items-end md:justify-between md:p-7">
        <div className="max-w-2xl">
          <p className="router-eyebrow">Router control plane / live</p>
          <h1 className="mt-3 font-display text-3xl font-medium tracking-[-0.04em] text-foreground md:text-4xl">
            Prompt in. Best model out.
          </h1>
          <p className="mt-3 max-w-xl text-sm leading-6 text-muted-foreground">
            One gateway for every client. Router reads the request intent, selects an eligible model,
            and dispatches through the right provider endpoint and credential.
          </p>
        </div>
        <div className="flex shrink-0 items-center gap-3 border-t border-border pt-4 md:border-l md:border-t-0 md:pl-6 md:pt-0">
          <span className="relative flex size-2.5">
            <span className="absolute inline-flex size-full animate-ping rounded-full bg-brand opacity-40" />
            <span className="relative inline-flex size-2.5 rounded-full bg-brand" />
          </span>
          <div>
            <p className="text-sm font-medium text-foreground">Gateway online</p>
            <p className="router-mono mt-1 text-[11px] text-muted-foreground">
              {snapshot?.clusterVersion ?? "checking catalog…"}
            </p>
          </div>
        </div>
      </div>

      <div className="grid gap-3 md:grid-cols-[1.15fr_1fr_1fr]">
        <div className="router-hairline rounded-md bg-muted p-4">
          <div className="flex items-center justify-between">
            <p className="router-eyebrow">Dispatch pipeline</p>
            <Route className="size-4 text-brand" />
          </div>
          <div className="mt-5 flex flex-col gap-3">
            <PipelineStep icon={<Radar />} label="Intent signal" detail="prompt-derived tags" />
            <PipelineStep icon={<Network />} label="Model catalog" detail="eligible candidates" />
            <PipelineStep icon={<Server />} label="Provider endpoint" detail="credential-bound dispatch" last />
          </div>
        </div>

        <InventoryCard
          icon={<KeyRound />}
          label="Provider credentials"
          value={snapshot == null ? "—" : String(snapshot.providerKeys)}
          detail="managed bindings"
          href="/settings/providers"
        />
        <InventoryCard
          icon={<CheckCircle2 />}
          label="Routable models"
          value={snapshot == null ? "—" : String(snapshot.routableModels)}
          detail={snapshot == null ? "loading catalog" : `${snapshot.providerCount} active providers`}
          href="/settings/models"
        />
      </div>
    </div>
  );
}

function PipelineStep({
  detail,
  icon,
  label,
  last = false,
}: {
  detail: string;
  icon: React.ReactElement<{ className?: string }>;
  label: string;
  last?: boolean;
}) {
  return (
    <div className="flex items-center gap-3">
      <div className="flex size-8 shrink-0 items-center justify-center rounded-md border border-brand/30 bg-brand/10 text-brand">
        {React.cloneElement(icon, { className: "size-4" })}
      </div>
      <div className="min-w-0">
        <p className="text-sm font-medium text-foreground">{label}</p>
        <p className="router-mono text-[11px] text-muted-foreground">{detail}</p>
      </div>
      {!last && <ArrowRight className="ml-auto size-4 text-muted-foreground/60" />}
      {last && <CircleDot className="ml-auto size-4 text-brand" />}
    </div>
  );
}

function InventoryCard({
  detail,
  href,
  icon,
  label,
  value,
}: {
  detail: string;
  href: string;
  icon: React.ReactNode;
  label: string;
  value: string;
}) {
  return (
    <Link
      href={href}
      className="router-hairline group flex min-h-[150px] flex-col justify-between rounded-md bg-background p-4 transition-colors hover:border-brand/60 hover:bg-muted"
    >
      <div className="flex items-center justify-between">
        <p className="router-eyebrow text-muted-foreground">{label}</p>
        <span className="text-brand">{icon}</span>
      </div>
      <div className="flex items-end justify-between gap-3">
        <div>
          <p className="router-mono text-3xl font-medium tracking-tight text-foreground">{value}</p>
          <p className="mt-1 text-xs text-muted-foreground">{detail}</p>
        </div>
        <ArrowRight className="size-4 text-muted-foreground transition-transform group-hover:translate-x-1 group-hover:text-brand" />
      </div>
    </Link>
  );
}

interface MetricCardProps {
  className?: string;
  label: string;
  value: string;
  sub?: string;
  accent?: "default" | "success" | "danger" | "info";
}

function MetricCard({ className, label, value, sub, accent = "default" }: MetricCardProps) {
  const accentClass =
    accent === "success"
      ? "text-success"
      : accent === "danger"
        ? "text-danger"
        : accent === "info"
          ? "text-primary"
          : "text-foreground";

  return (
    <Card size="sm" className={className}>
      <Card.Header>
        <Text className="text-2xs font-medium uppercase tracking-wider text-muted-foreground">
          {label}
        </Text>
      </Card.Header>
      <Card.Content>
        <Text
          className={cn(
            "font-display text-2xl font-semibold tabular-nums tracking-tight",
            accentClass,
          )}
        >
          {value}
        </Text>
        {sub != null && (
          <Text className="mt-1 text-2xs text-muted-foreground">{sub}</Text>
        )}
      </Card.Content>
    </Card>
  );
}

function EmptyChart({ height = 240 }: { height?: number }) {
  return (
    <div
      className="flex items-center justify-center text-2xs text-muted-foreground"
      style={{ height }}
    >
      No data for this period
    </div>
  );
}
