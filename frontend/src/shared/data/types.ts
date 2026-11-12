// Wire shapes of every Router dashboard read and write. Self-hosted `/admin/v1` returns these
// directly; hosted adapters map their own transport into them.

export type ISODateTime = string;

export interface DateRange {
  /** Inclusive start. */
  from: ISODateTime;
  /** Exclusive end. */
  to: ISODateTime;
}

export type MetricsGranularity = "hour" | "day" | "week";

export interface MetricsSummary {
  request_count: number;
  total_tokens: number;
  total_requested_cost_usd: number;
  total_actual_cost_usd: number;
  total_savings_usd: number;
}

export interface TimeseriesBucket {
  bucket: string;
  requested_cost_usd: number;
  actual_cost_usd: number;
}

export interface MetricsTimeseries {
  buckets: TimeseriesBucket[];
}

export interface ModelBreakdownBucket {
  bucket: string;
  decision_model: string;
  request_count: number;
  total_tokens: number;
  actual_cost_usd: number;
}

export interface MetricsModelBreakdown {
  buckets: ModelBreakdownBucket[];
}

export interface MetricsDetailRow {
  timestamp: string;
  request_id: string;
  requested_model: string;
  decision_model: string;
  decision_provider: string;
  decision_reason: string;
  sticky_hit: boolean;
  input_tokens: number;
  output_tokens: number;
  cache_creation_tokens: number | null;
  cache_read_tokens: number | null;
  requested_cost_usd: number;
  actual_cost_usd: number;
  total_latency_ms: number;
  upstream_status_code: number;
  router_user_id: string;
  client_app: string;
  turn_type: string;
  user_email: string;
}

export interface MetricsDetails {
  rows: MetricsDetailRow[];
}

// "routing" keys (rk_) proxy inference; "analytics_read" keys (ra_) are
// read-only export credentials that cannot route or spend.
export type APIKeyScope = "routing" | "analytics_read";

export interface APIKey {
  id: string;
  name: string | null;
  key_prefix: string;
  key_suffix: string;
  scope: APIKeyScope;
  last_used_at: string | null;
  created_at: string;
}

export interface IssueAPIKeyResponse {
  key: APIKey;
  token: string;
}

// ProviderAuthType is how a BYOK key authenticates upstream. "wif" keys hold no
// secret at all: the router presents its own workload attestation per request.
// "azure_entra" stores an Entra client secret and mints a short-lived token.
// "anthropic_wif" exchanges workload assertions using per-key federation settings.
export type ProviderAuthType = "bearer" | "keypair_jwt" | "wif" | "azure_entra" | "anthropic_wif";

export interface AnthropicFederationConfig {
  federation_rule_id: string;
  organization_id: string;
  service_account_id: string;
  workspace_id?: string;
  audience?: string;
}

export interface ExternalKey {
  id: string;
  provider: string;
  name: string | null;
  key_prefix: string;
  key_suffix: string;
  // Endpoint this key is scoped to; absent when the provider's default is used.
  base_url?: string;
  // Catalog model ID -> the ID this endpoint publishes it under; absent when it
  // uses catalog names directly.
  model_aliases?: Record<string, string>;
  // "bearer" (send the stored secret), "keypair_jwt" (the secret is an RSA
  // private key the router signs short-lived tokens with), "wif" (no stored
  // secret; the router attests its own workload identity), or "azure_entra"
  // (the secret is an Entra client secret used to mint a short-lived token);
  // absent means bearer.
  auth_type?: ProviderAuthType;
  federation_config?: AnthropicFederationConfig;
  // Principal a minted token is issued for; present only with keypair_jwt or
  // azure_entra. For azure_entra these are the tenant ID and client ID.
  auth_account?: string;
  auth_user?: string;
  last_used_at: string | null;
  created_at: string;
}

export interface RouterConfig {
  embed_only_user_message: boolean;
  sticky_decision_ttl_ms: string;
  otel_enabled: boolean;
  dev_mode: boolean;
  semantic_cache_enabled: boolean;
  env_provider_keys: string[];
}

// Whether this installation has ever served a routed request. Set once and
// never cleared, so it survives key rotation — the dashboard gates first-run
// onboarding on this rather than on a key's last_used_at.
export interface OnboardingStatus {
  first_request_served_at: string | null;
}

export interface MeResponse {
  authenticated: boolean;
  subject?: string;
}

export interface DeployedModel {
  model: string;
  provider: string;
}

export interface ExcludedModelsResponse {
  available: DeployedModel[];
  excluded: string[];
  env_override_active: boolean;
}

export interface ExcludedProvidersResponse {
  available: string[];
  excluded: string[];
  env_override_active: boolean;
}

export interface RoutingPreferencesResponse {
  quality: number;
  price: number;
  is_default: boolean;
}

/** One person's usage over a range. Hosted and self-hosted both key people by email. */
export interface IndividualMetrics {
  user_email: string;
  request_count: number;
  total_tokens: number;
  total_requested_cost_usd: number;
  total_actual_cost_usd: number;
  total_savings_usd: number;
}

export interface IndividualModelMixRow {
  decision_model: string;
  request_count: number;
  total_tokens: number;
  actual_cost_usd: number;
}

/** Usage attributed to one routing key over a range. */
export interface APIKeyUsage {
  api_key_id: string;
  request_count: number;
  total_tokens: number;
  actual_cost_usd: number;
}

export interface ProviderKeyAuth {
  type: ProviderAuthType;
  account?: string;
  user?: string;
  federationConfig?: AnthropicFederationConfig;
}

export interface UpsertProviderKeyInput {
  provider: string;
  key: string;
  name?: string;
  baseURL?: string;
  modelAliases?: Record<string, string>;
  auth?: ProviderKeyAuth;
}
