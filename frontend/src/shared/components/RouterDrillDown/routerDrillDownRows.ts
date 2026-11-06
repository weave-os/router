import type { RouterDrillDownModelPair, RouterDrillDownPerson } from "../../data/drillDown";

/** One table row: a person, one of their sessions, or one model pair within a session. */
export interface RouterDrillDownTotals {
  catalogCostUsd: number;
  comparisonCostUsd: number;
  decisionModels: readonly string[];
  providers: readonly string[];
  requestedModels: readonly string[];
  requests: number;
  subscriptionRequests: number;
}

function unique(values: Iterable<string>) {
  return [...new Set(values)].filter(value => value !== "").sort();
}

export function totalPairs(pairs: readonly RouterDrillDownModelPair[]): RouterDrillDownTotals {
  return {
    catalogCostUsd: pairs.reduce((sum, pair) => sum + pair.catalogCostUsd, 0),
    comparisonCostUsd: pairs.reduce((sum, pair) => sum + pair.comparisonCostUsd, 0),
    decisionModels: unique(pairs.map(pair => pair.decisionModel)),
    providers: unique(pairs.flatMap(pair => pair.providers)),
    requestedModels: unique(pairs.map(pair => pair.requestedModel)),
    requests: pairs.reduce((sum, pair) => sum + pair.requests, 0),
    subscriptionRequests: pairs.reduce((sum, pair) => sum + pair.subscriptionRequests, 0),
  };
}

export function personTotals(person: RouterDrillDownPerson): RouterDrillDownTotals {
  return totalPairs(person.sessions.flatMap(session => session.models));
}

/** Hosted's "Usage type" cell: API, Subscription, or the split when a row mixes both. */
export function usageType({ requests, subscriptionRequests }: RouterDrillDownTotals): string {
  if (subscriptionRequests === 0) return "API";
  if (subscriptionRequests === requests) return "Subscription";
  return `${requests - subscriptionRequests} API / ${subscriptionRequests} subscription`;
}
