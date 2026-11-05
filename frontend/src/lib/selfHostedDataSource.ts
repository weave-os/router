import { api } from "@/lib/api";
import { RouterDrillDownModal } from "@/shared/components/RouterDrillDown";
import {
  drillDownRange,
  type RouterDashboardDataSource,
  type RouterDrillDownRequest,
  RouterUserLink,
  unavailable,
  UnavailableReason,
} from "@/shared/data";

import { createElement } from "react";

async function loadDrillDown(request: RouterDrillDownRequest) {
  const { from, to } = drillDownRange(request);
  const params = new URLSearchParams({ from, to });
  if (request.group != null) params.set("group", request.group);
  if (request.key != null) params.set("key", request.key);
  if (request.models != null) params.set("models", request.models.join(","));
  if (request.billing != null) params.set("billing", request.billing);
  if (request.routingState != null) params.set("routing_state", request.routingState);
  const { comparison_model, people } = await api.metrics.drillDown(params);
  return {
    comparisonModel: comparison_model,
    people: people.map(person => ({
      displayName: person.display_name,
      email: person.email,
      id: person.id,
      linked: person.linked,
      sessionCount: person.session_count,
      sessions: person.sessions.map(session => ({
        clientApp: session.client_app,
        finishedAt: Date.parse(session.finished_at),
        models: session.models.map(pair => ({
          catalogCostUsd: pair.catalog_cost_usd,
          comparisonCostUsd: pair.comparison_cost_usd,
          decisionModel: pair.decision_model,
          providers: pair.providers ?? [],
          requestedModel: pair.requested_model,
          requests: pair.requests,
          subscriptionRequests: pair.subscription_requests,
        })),
        sessionId: session.session_id,
        startedAt: Date.parse(session.started_at),
      })),
    })),
  };
}

export const selfHostedDataSource: RouterDashboardDataSource = {
  drillDown: {
    Modal: props => createElement(RouterDrillDownModal, { ...props, load: loadDrillDown }),
  },
  individuals: {
    list: async ({ granularity, range }) => {
      const { individuals } = await api.metrics.individuals(granularity, range.from, range.to);
      const points = (activity: { time: number; value: number }[]) =>
        activity.map(({ time, value }) => ({ x: time, y: value }));
      return individuals.map(person => ({
        avatarUrl: null,
        email: person.email === "" ? null : person.email,
        id: person.id,
        lastRequestAt: person.last_request_at,
        linked: person.linked,
        modelMix:
          person.models.length === 0
            ? null
            : { models: person.models, otherRequests: person.other_requests },
        name: person.display_name || person.email || "Unlinked router user",
        usage: {
          cacheCreationTokens: person.cache_creation_tokens,
          cacheReadTokens: person.cache_read_tokens,
          cost: person.cost_usd,
          costActivity: points(person.cost_activity),
          inputTokens: person.input_tokens,
          outputTokens: person.output_tokens,
          passthroughRequestActivity: points(person.passthrough_activity),
          passthroughRequests: person.passthrough_requests,
          requestActivity: points(person.request_activity),
          requests: person.requests,
        },
      }));
    },
  },
  metrics: {
    costTimeseries: ({ granularity, range }) =>
      api.metrics.timeseries(granularity, range.from, range.to),
    modelMix: ({ granularity, range }) =>
      api.metrics.modelBreakdown(granularity, range.from, range.to),
    summary: range => api.metrics.summary(range.from, range.to),
    people: async () => {
      const { people } = await api.metrics.people();
      return people.map(person => ({
        displayName: person.display_name,
        email: person.email,
        id: person.id,
        lastRequestAt: person.last_request_at,
        link: person.linked ? RouterUserLink.Linked : RouterUserLink.Unlinked,
      }));
    },
    reportSummary: async (range, routingState) => {
      const summary = await api.metrics.reportSummary(range.from, range.to, routingState);
      return {
        comparisonCostUSD: summary.comparison_cost_usd,
        comparisonModel: summary.comparison_model,
        lastRequestAt: summary.last_request_at,
        requestCount: summary.request_count,
        savingsUSD: summary.savings_usd,
        totalCostUSD: summary.total_cost_usd,
        totalTokens: summary.total_tokens,
      };
    },
    series: request => api.metrics.series(request),
  },
  requests: {
    list: ({ limit, range }) => api.metrics.details(range.from, range.to, limit),
  },
  settings: {
    apiKeys: {
      delete: id => api.keys.delete(id),
      issue: ({ name, scope }) => api.keys.issue(name, scope),
      list: async () => (await api.keys.list()).keys,
      rotate: id => api.keys.rotate(id),
    },
    models: {
      get: () => api.excludedModels.get(),
      updateExcluded: excluded => api.excludedModels.update(excluded),
    },
    providerKeys: {
      delete: id => api.providerKeys.delete(id),
      discoverModels: async ({ baseURL, key, provider }) =>
        (await api.providerKeys.discoverModels(provider, key, baseURL)).models,
      list: async () => (await api.providerKeys.list()).keys,
      listUpstreamModels: async id => (await api.providerKeys.listUpstreamModels(id)).models,
      updateModelAliases: (id, modelAliases) =>
        api.providerKeys.updateModelAliases(id, modelAliases),
      upsert: ({ auth, baseURL, key, modelAliases, name, provider }) =>
        api.providerKeys.upsert(provider, key, name, baseURL, modelAliases, auth),
    },
    providers: {
      get: () => api.excludedProviders.get(),
      updateExcluded: excluded => api.excludedProviders.update(excluded),
    },
    routing: {
      get: () => api.routingPreferences.get(),
      reset: () => api.routingPreferences.reset(),
      update: preferences => api.routingPreferences.update(preferences),
    },
    usage: unavailable(
      UnavailableReason.NotSupportedByHost,
      "Per-key usage is not available on self-hosted Router yet.",
    ),
  },
  subscriptions: {
    pool: async () => {
      const { enabled, seats } = await api.subscriptionPool();
      return {
        enabled,
        seats: seats.map(seat => ({
          cooldownUntil: seat.cooldown_until,
          displayName: seat.display_name,
          enabled: seat.enabled,
          id: seat.id,
          limitWindows: seat.limit_windows.map(limitWindow => ({
            resetsAt: limitWindow.resets_at,
            usedFraction: limitWindow.used_fraction,
            windowMinutes: limitWindow.window_minutes,
          })),
          limitsObservedAt: seat.limits_observed_at,
          ownerName: seat.owner_name,
          provider: seat.provider,
          servedRequests: seat.served_requests,
          state: seat.state,
        })),
      };
    },
  },
  weekStartsOn: 1,
};
