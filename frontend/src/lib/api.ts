import type {
  RouterMetricSeries,
  RouterMetricSeriesRequest,
  RouterRoutingState,
  RouterSubscriptionProvider,
  RouterSubscriptionSeatState,
} from "@/shared/data";
import type {
  AnthropicFederationConfig,
  APIKey,
  APIKeyScope,
  ExcludedModelsResponse,
  ExcludedProvidersResponse,
  ExternalKey,
  IssueAPIKeyResponse,
  MeResponse,
  MetricsDetails,
  MetricsModelBreakdown,
  MetricsSummary,
  MetricsTimeseries,
  OnboardingStatus,
  ProviderAuthType,
  RouterConfig,
  RoutingPreferencesResponse,
} from "@/shared/data/types";

export type * from "@/shared/data/types";

const BASE = "/admin/v1";

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const headers: Record<string, string> = {
    "Content-Type": "application/json",
    ...((init?.headers as Record<string, string>) ?? {}),
  };
  const res = await fetch(`${BASE}${path}`, {
    ...init,
    credentials: "include",
    headers,
  });
  if (res.status === 401 && typeof window !== "undefined") {
    // Session expired or never authenticated — bounce to login. The
    // (app) layout's auth gate handles the same case on initial load,
    // but in-flight calls (chart refreshes, mutations) need their own
    // bounce so users don't sit on a broken page.
    if (!window.location.pathname.startsWith("/ui/login")) {
      // Strip the /ui basePath so Next's router.replace() doesn't re-prepend
      // it after login (which would yield /ui/ui/...). Anything outside the
      // basePath isn't an app path; default to /dashboard.
      const path = window.location.pathname;
      const internal = path.startsWith("/ui/") ? path.slice(3) : "/dashboard";
      const next = encodeURIComponent(internal);
      window.location.href = `/ui/login?next=${next}`;
      throw new Error("401: redirecting to login");
    }
  }
  if (!res.ok) {
    const body = await res.text();
    throw new Error(`${res.status}: ${body}`);
  }
  if (res.status === 204) return undefined as T;
  return res.json() as Promise<T>;
}

export type SubscriptionLoginProvider = "claude" | "codex";

export interface SubscriptionOwner {
  subscriber_id: string;
  display_name: string;
  email: string;
  key_prefix: string;
  key_suffix: string;
}

export interface SubscriptionOwnersResponse {
  enabled: boolean;
  owners: SubscriptionOwner[];
}

export interface SubscriptionSeat {
  account_id: string;
  subscriber_id: string;
  provider: SubscriptionLoginProvider;
  display_name: string;
  enabled: boolean;
  owner_display_name: string;
  owner_email: string;
  owner_has_routing_key: boolean;
}

export interface SubscriptionSeatsResponse {
  enabled: boolean;
  seats: SubscriptionSeat[];
}

export interface StartedSubscriptionLogin {
  login_token: string;
  verification_url: string;
  user_code?: string;
  poll_interval_seconds?: number;
  expires_at: string;
}

export interface LinkedSubscriptionAccount {
  id: string;
  provider: SubscriptionLoginProvider;
  display_name?: string | null;
}

export const api = {
  auth: {
    me: () => request<MeResponse>("/auth/me"),
    login: (password: string) =>
      request<{ ok: boolean; expires_at: string }>("/auth/login", {
        method: "POST",
        body: JSON.stringify({ password }),
      }),
    logout: () => request<{ ok: boolean }>("/auth/logout", { method: "POST" }),
  },
  metrics: {
    summary: (from?: string, to?: string) => {
      const params = new URLSearchParams();
      if (from) params.set("from", from);
      if (to) params.set("to", to);
      const qs = params.toString();
      return request<MetricsSummary>(`/metrics/summary${qs ? `?${qs}` : ""}`);
    },
    timeseries: (granularity: "hour" | "day" | "week", from?: string, to?: string) => {
      const params = new URLSearchParams({ granularity });
      if (from) params.set("from", from);
      if (to) params.set("to", to);
      return request<MetricsTimeseries>(`/metrics/timeseries?${params.toString()}`);
    },
    details: (from: string, to: string, limit: number = 100) => {
      const params = new URLSearchParams({ from, to, limit: String(limit) });
      return request<MetricsDetails>(`/metrics/details?${params.toString()}`);
    },
    modelBreakdown: (granularity: "hour" | "day" | "week", from?: string, to?: string) => {
      const params = new URLSearchParams({ granularity });
      if (from) params.set("from", from);
      if (to) params.set("to", to);
      return request<MetricsModelBreakdown>(`/metrics/model-breakdown?${params.toString()}`);
    },
    series: ({ billing, granularity, group, range, type }: RouterMetricSeriesRequest) => {
      const params = new URLSearchParams({ granularity: granularity ?? "total", type });
      if (billing) params.set("billing", billing);
      if (group) params.set("group", group);
      params.set("from", range.from);
      params.set("to", range.to);
      return request<RouterMetricSeries>(`/metrics/series?${params.toString()}`);
    },
    people: () => request<PeopleResponse>("/metrics/people"),
    individuals: (granularity: "hour" | "day" | "week", from: string, to: string) =>
      request<IndividualsResponse>(
        `/metrics/individuals?${new URLSearchParams({ from, granularity, to }).toString()}`,
      ),
    drillDown: (params: URLSearchParams) =>
      request<DrillDownResponse>(`/metrics/drill-down?${params.toString()}`),
    reportSummary: (from: string, to: string, routingState?: RouterRoutingState) => {
      const params = new URLSearchParams({ from, to });
      if (routingState) params.set("routing_state", routingState);
      return request<ReportSummaryResponse>(`/metrics/report-summary?${params.toString()}`);
    },
  },
  subscriptionLogins: {
    owners: () => request<SubscriptionOwnersResponse>("/subscription-owners"),
    start: (subscriberID: string, provider: SubscriptionLoginProvider) =>
      request<StartedSubscriptionLogin>(`/subscription-logins/${subscriberID}`, {
        method: "POST",
        body: JSON.stringify({ provider }),
      }),
    /** Resolves to null while a Codex device login is still waiting for approval. */
    complete: async (subscriberID: string, loginToken: string, authCode?: string, signal?: AbortSignal) => {
      const outcome = await request<LinkedSubscriptionAccount | { status: "pending" }>(
        `/subscription-logins/${subscriberID}/complete`,
        {
          method: "POST",
          body: JSON.stringify({ login_token: loginToken, code: authCode ?? "" }),
          ...(signal == null ? {} : { signal }),
        },
      );
      return "status" in outcome ? null : outcome;
    },
    seats: () => request<SubscriptionSeatsResponse>("/subscription-accounts"),
    unlink: (subscriberID: string, accountID: string) =>
      request<void>(`/subscription-accounts/${subscriberID}/${accountID}`, { method: "DELETE" }),
  },
  subscriptionPool: () => request<SubscriptionPoolResponse>("/subscription-pool"),
  keys: {
    list: () => request<{ keys: APIKey[] }>("/keys"),
    issue: (name?: string, scope: APIKeyScope = "routing") =>
      request<IssueAPIKeyResponse>("/keys", {
        method: "POST",
        body: JSON.stringify({ name: name ?? "", scope }),
      }),
    // Soft-deletes the named key and issues a replacement in one round-trip.
    // The previous token stops working immediately. Carries forward the
    // previous key's name server-side.
    rotate: (id: string) => request<IssueAPIKeyResponse>(`/keys/${id}/rotate`, { method: "POST" }),
    delete: (id: string) => request<void>(`/keys/${id}`, { method: "DELETE" }),
  },
  providerKeys: {
    list: () => request<{ keys: ExternalKey[] }>("/provider-keys"),
    upsert: (
      provider: string,
      key: string,
      name?: string,
      baseURL?: string,
      modelAliases?: Record<string, string>,
      auth?: {
        type: ProviderAuthType;
        account?: string;
        user?: string;
        federationConfig?: AnthropicFederationConfig;
      },
    ) =>
      request<ExternalKey>("/provider-keys", {
        method: "POST",
        body: JSON.stringify({
          provider,
          key,
          name,
          base_url: baseURL,
          model_aliases: modelAliases,
          auth_type: auth?.type ?? "bearer",
          federation_config: auth?.federationConfig,
          auth_account: auth?.account,
          auth_user: auth?.user,
        }),
      }),
    // Replaces the alias map in place; the stored secret is untouched, so
    // retargeting model names doesn't need the credential re-entered.
    updateModelAliases: (id: string, modelAliases: Record<string, string>) =>
      request<ExternalKey>(`/provider-keys/${id}/model-aliases`, {
        method: "PUT",
        body: JSON.stringify({ model_aliases: modelAliases }),
      }),
    delete: (id: string) => request<void>(`/provider-keys/${id}`, { method: "DELETE" }),
    // Lists the model IDs a saved key's endpoint publishes.
    listUpstreamModels: (id: string) =>
      request<{ models: string[] }>(`/provider-keys/${id}/models`),
    // Lists an endpoint's models before the key is saved; the credential is
    // used for the one upstream call and never persisted.
    discoverModels: (provider: string, key: string, baseURL?: string) =>
      request<{ models: string[] }>("/provider-keys/discover-models", {
        method: "POST",
        body: JSON.stringify({ provider, key, base_url: baseURL }),
      }),
  },
  config: {
    get: () => request<RouterConfig>("/config"),
  },
  onboarding: {
    get: () => request<OnboardingStatus>("/onboarding"),
  },
  excludedModels: {
    get: () => request<ExcludedModelsResponse>("/excluded-models"),
    update: (excluded: string[]) =>
      request<ExcludedModelsResponse>("/excluded-models", {
        method: "PUT",
        body: JSON.stringify({ excluded }),
      }),
  },
  excludedProviders: {
    get: () => request<ExcludedProvidersResponse>("/excluded-providers"),
    update: (excluded: string[]) =>
      request<ExcludedProvidersResponse>("/excluded-providers", {
        method: "PUT",
        body: JSON.stringify({ excluded }),
      }),
  },
  routingPreferences: {
    get: () => request<RoutingPreferencesResponse>("/routing-preferences"),
    update: (body: { quality: number; price: number }) =>
      request<RoutingPreferencesResponse>("/routing-preferences", {
        method: "PUT",
        body: JSON.stringify(body),
      }),
    reset: () =>
      request<RoutingPreferencesResponse>("/routing-preferences", {
        method: "PUT",
        body: JSON.stringify({ reset: true }),
      }),
  },
};

export interface PeopleResponse {
  people: {
    display_name: string;
    email: string;
    id: string;
    last_request_at: string | null;
    linked: boolean;
  }[];
}

interface ActivityPoint {
  time: number;
  value: number;
}

export interface IndividualsResponse {
  individuals: {
    id: string;
    linked: boolean;
    display_name: string;
    email: string;
    last_request_at: string | null;
    requests: number;
    cost_usd: number;
    input_tokens: number;
    output_tokens: number;
    cache_read_tokens: number;
    cache_creation_tokens: number;
    passthrough_requests: number;
    request_activity: ActivityPoint[];
    cost_activity: ActivityPoint[];
    passthrough_activity: ActivityPoint[];
    models: { model: string; requests: number }[];
    other_requests: number;
  }[];
}

export interface DrillDownResponse {
  comparison_model: string;
  people: {
    display_name: string;
    email: string;
    id: string;
    linked: boolean;
    session_count: number;
    sessions: {
      client_app: string;
      finished_at: string;
      models: {
        catalog_cost_usd: number;
        comparison_cost_usd: number;
        decision_model: string;
        providers: string[] | null;
        requested_model: string;
        requests: number;
        subscription_requests: number;
      }[];
      session_id: string;
      started_at: string;
    }[];
  }[];
}

export interface SubscriptionPoolResponse {
  enabled: boolean;
  seats: {
    cooldown_until: string | null;
    display_name: string | null;
    enabled: boolean;
    id: string;
    limit_windows: { resets_at: string | null; used_fraction: number; window_minutes: number }[];
    limits_observed_at: string | null;
    owner_name: string | null;
    provider: RouterSubscriptionProvider;
    served_requests: number;
    state: RouterSubscriptionSeatState;
  }[];
}

export interface ReportSummaryResponse {
  comparison_cost_usd: number;
  comparison_model: string;
  last_request_at: string | null;
  request_count: number;
  savings_usd: number;
  total_cost_usd: number;
  total_tokens: number;
}
