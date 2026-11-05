import { type DateRange, type MetricsGranularity } from "./types";

export enum RouterMetricType {
  ActiveUsers = "active_users",
  /** Catalog cost of every request at its served model's API list price. */
  ApiListPriceSpend = "api_list_price_spend",
  /** Actual cost alongside the comparison-model cost; group by `CostType`. */
  Cost = "cost",
  ModelSpend = "model_spend",
  ModelUsage = "model_usage",
}

export enum RouterMetricGroup {
  Billing = "billing",
  CostType = "cost_type",
  DecisionType = "decision_type",
  Model = "model",
  /** Narrows a drill-down to one Individuals row; not a chart series group. */
  Person = "person",
  RoutingState = "routing_state",
  UserLink = "user_link",
}

export enum RouterCostType {
  Actual = "Actual cost",
  Comparison = "Expensive model cost",
}

export enum RouterUserLink {
  Linked = "linked",
  Unlinked = "unlinked",
}

export enum RouterRoutingState {
  Passthrough = "passthrough",
  Routed = "routed",
}

export enum RouterBilling {
  API = "api",
  Subscription = "subscription",
}

export enum RouterDecisionType {
  Forced = "forced",
  Passthrough = "passthrough",
  RouterPick = "router_pick",
  SubscriptionBypass = "subscription_bypass",
  Utility = "utility",
}

export interface RouterMetricSeriesRequest {
  /** Only requests billed this way; omitted counts every request. */
  billing?: RouterBilling;
  /** `null` aggregates the whole range into one value per group. */
  granularity: MetricsGranularity | null;
  group?: RouterMetricGroup;
  range: DateRange;
  type: RouterMetricType;
}

export interface RouterMetricPoint {
  /** `null` when the request has no group. */
  group: string | null;
  /** Bucket start, epoch milliseconds; `null` for an aggregated request. */
  time: number | null;
  value: number;
}

export interface RouterMetricSeries {
  values: readonly RouterMetricPoint[];
}

/** Someone who can route: a roster user (linked) or an API key used without one (unlinked). */
export interface RouterPerson {
  displayName: string;
  email: string;
  id: string;
  lastRequestAt: string | null;
  link: RouterUserLink;
}

/** Totals behind the dashboard's stat cards, priced against `comparisonModel`. */
export interface RouterReportSummary {
  comparisonCostUSD: number;
  comparisonModel: string;
  lastRequestAt: string | null;
  requestCount: number;
  savingsUSD: number;
  totalCostUSD: number;
  totalTokens: number;
}
