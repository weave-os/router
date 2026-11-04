import type { Capability } from "./availability";
import type {
  RouterMetricSeries,
  RouterPerson,
  RouterMetricSeriesRequest,
  RouterReportSummary,
} from "./metricSeries";
import type { SubscriptionsDataSource } from "./subscriptionPool";
import type {
  APIKey,
  APIKeyScope,
  APIKeyUsage,
  DateRange,
  ExcludedModelsResponse,
  ExcludedProvidersResponse,
  ExternalKey,
  IndividualMetrics,
  IndividualModelMixRow,
  IssueAPIKeyResponse,
  MetricsDetails,
  MetricsGranularity,
  MetricsModelBreakdown,
  MetricsSummary,
  MetricsTimeseries,
  RoutingPreferencesResponse,
  UpsertProviderKeyInput,
} from "./types";

export interface TimeseriesQuery {
  granularity: MetricsGranularity;
  range: DateRange;
}

export interface MetricsDataSource {
  summary(range: DateRange): Promise<MetricsSummary>;
  /** Requested vs actual cost per bucket; savings is the difference. */
  costTimeseries(query: TimeseriesQuery): Promise<MetricsTimeseries>;
  /** Requests, tokens and cost per decision model per bucket. */
  modelMix(query: TimeseriesQuery): Promise<MetricsModelBreakdown>;
  /** Everyone who can route with their latest request ever; ignores the date filter. */
  people(): Promise<readonly RouterPerson[]>;
  /** Totals for the stat cards, with savings against the comparison model. */
  reportSummary(range: DateRange): Promise<RouterReportSummary>;
  /** One metric, optionally grouped, per bucket; feeds every dashboard chart card. */
  series(request: RouterMetricSeriesRequest): Promise<RouterMetricSeries>;
}

export interface IndividualsDataSource {
  /** One row per user email. */
  list(range: DateRange): Promise<IndividualMetrics[]>;
  modelMix(userEmail: string, range: DateRange): Promise<IndividualModelMixRow[]>;
}

export interface RequestsQuery {
  range: DateRange;
  limit: number;
}

export interface RequestsDataSource {
  /** Newest first. */
  list(query: RequestsQuery): Promise<MetricsDetails>;
}

export interface ModelSettingsDataSource {
  get(): Promise<ExcludedModelsResponse>;
  updateExcluded(excluded: string[]): Promise<ExcludedModelsResponse>;
}

export interface ProviderSettingsDataSource {
  get(): Promise<ExcludedProvidersResponse>;
  updateExcluded(excluded: string[]): Promise<ExcludedProvidersResponse>;
}

export interface RoutingPreferences {
  quality: number;
  price: number;
}

export interface RoutingSettingsDataSource {
  get(): Promise<RoutingPreferencesResponse>;
  update(preferences: RoutingPreferences): Promise<RoutingPreferencesResponse>;
  reset(): Promise<RoutingPreferencesResponse>;
}

export interface APIKeysDataSource {
  list(): Promise<APIKey[]>;
  issue(input: { name?: string; scope: APIKeyScope }): Promise<IssueAPIKeyResponse>;
  rotate(id: string): Promise<IssueAPIKeyResponse>;
  delete(id: string): Promise<void>;
}

export interface ProviderKeysDataSource {
  list(): Promise<ExternalKey[]>;
  upsert(input: UpsertProviderKeyInput): Promise<ExternalKey>;
  /** Replaces the alias map without re-entering the stored secret. */
  updateModelAliases(id: string, modelAliases: Record<string, string>): Promise<ExternalKey>;
  delete(id: string): Promise<void>;
  /** Model IDs a saved key's endpoint publishes. */
  listUpstreamModels(id: string): Promise<string[]>;
  /** Model IDs an endpoint publishes, using a credential that is not persisted. */
  discoverModels(input: { provider: string; key: string; baseURL?: string }): Promise<string[]>;
}

export interface UsageDataSource {
  byAPIKey(range: DateRange): Promise<APIKeyUsage[]>;
}

export interface SettingsDataSource {
  models: Capability<ModelSettingsDataSource>;
  providers: Capability<ProviderSettingsDataSource>;
  routing: Capability<RoutingSettingsDataSource>;
  apiKeys: Capability<APIKeysDataSource>;
  providerKeys: Capability<ProviderKeysDataSource>;
  usage: Capability<UsageDataSource>;
}

/**
 * Every read and write a shared Router dashboard page performs. Hosts implement it once (hosted
 * over GraphQL, self-hosted over `/admin/v1`) and pass it to `RouterDashboardDataProvider`.
 */
export interface RouterDashboardDataSource {
  metrics: MetricsDataSource;
  individuals: Capability<IndividualsDataSource>;
  requests: Capability<RequestsDataSource>;
  settings: SettingsDataSource;
  /** The Claude/Codex subscription-account pool; hosts without one hide the Subscriptions tab. */
  subscriptions: Capability<SubscriptionsDataSource>;
}
