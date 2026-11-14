import { type DateRange, type MetricsGranularity } from "./types";

/** One value per bucket start, epoch milliseconds. */
export interface RouterActivityPoint {
  x: number;
  y: number;
}

export interface RouterIndividualModelMix {
  /** The person's most-routed models, most requests first. */
  models: readonly { model: string; requests: number }[];
  otherRequests: number;
}

/** One person's router usage over a range, for the Individuals table. */
export interface RouterIndividual {
  avatarUrl: string | null;
  email: string | null;
  id: string;
  lastRequestAt: string | null;
  linked: boolean;
  /** Routed models only: forced, passthrough and utility turns are left out. */
  modelMix: RouterIndividualModelMix | null;
  name: string;
  usage: {
    cacheCreationTokens: number;
    cacheReadTokens: number;
    cost: number;
    costActivity: readonly RouterActivityPoint[];
    inputTokens: number;
    outputTokens: number;
    passthroughRequestActivity: readonly RouterActivityPoint[];
    passthroughRequests: number;
    requestActivity: readonly RouterActivityPoint[];
    requests: number;
  };
}

export interface RouterIndividualsQuery {
  granularity: MetricsGranularity;
  range: DateRange;
}
