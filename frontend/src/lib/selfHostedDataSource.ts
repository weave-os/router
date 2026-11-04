import { api } from "@/lib/api";
import {
  type RouterDashboardDataSource,
  RouterUserLink,
  unavailable,
  UnavailableReason,
} from "@/shared/data";

export const selfHostedDataSource: RouterDashboardDataSource = {
  individuals: unavailable(
    UnavailableReason.NotSupportedByHost,
    "Per-person usage is not available on self-hosted Router yet.",
  ),
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
    reportSummary: async range => {
      const summary = await api.metrics.reportSummary(range.from, range.to);
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
};
