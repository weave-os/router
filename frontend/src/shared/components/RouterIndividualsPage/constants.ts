import { RouterRoutingState } from "../../data";

export enum RouterIndividualsColumn {
  CodeOutput = "code_output",
  Cost = "cost",
  Individual = "individual",
  LastRequest = "last_request",
  Requests = "requests",
  RoutedModels = "routed_models",
  Tokens = "tokens",
}

export const COLUMN_TITLE: Readonly<Record<RouterIndividualsColumn, string>> = {
  [RouterIndividualsColumn.CodeOutput]: "Code output",
  [RouterIndividualsColumn.Cost]: "Cost",
  [RouterIndividualsColumn.Individual]: "Individual",
  [RouterIndividualsColumn.LastRequest]: "Last request",
  [RouterIndividualsColumn.Requests]: "Requests",
  [RouterIndividualsColumn.RoutedModels]: "Routed models",
  [RouterIndividualsColumn.Tokens]: "Tokens",
};

export const COLUMN_INFO: Readonly<Partial<Record<RouterIndividualsColumn, string>>> = {
  [RouterIndividualsColumn.Cost]:
    "Total spend on requests routed through Weave Router only; excludes AI usage outside the router. Requests served by a subscription, such as a Claude Code plan, count as $0. Dots on the sparkline mark periods on passthrough. Click to see their sessions.",
  [RouterIndividualsColumn.LastRequest]:
    "When the person last sent a request through Weave Router, at any time rather than within the selected dates. Can lag by up to 10 minutes.",
  [RouterIndividualsColumn.Requests]:
    "Total requests. The sparkline shows requests across the date range; dots mark periods when most of the person's requests were on passthrough rather than routed. Click to see their sessions.",
  [RouterIndividualsColumn.RoutedModels]:
    "The three models the router chose most often for this person, with the rest as other models. Excludes forced requests, passthrough, and utility turns like titles and compaction. Click to see their sessions.",
  [RouterIndividualsColumn.Tokens]:
    "Total input, output, cache read, and cache write tokens. Click to see their sessions.",
};

/** Shown in place of code output, a Weave Intelligence metric the router can't compute. */
export const CODE_OUTPUT_UNAVAILABLE_INFO =
  "Code output is a Weave metric: normalized units of work for an expert engineer, estimated from the code each person merges. It's enabled when you connect your code platform to the Weave Intelligence platform.";

export enum RouterRankedMetric {
  Cost = "cost",
  Requests = "requests",
}

/** Ranking direction for the sparkline colors, as the main Individuals table's LOWER_IS_BETTER. */
export const LOWER_IS_BETTER: Readonly<Record<RouterRankedMetric, boolean>> = {
  [RouterRankedMetric.Cost]: true,
  [RouterRankedMetric.Requests]: false,
};

export const ROUTING_GROUP_ORDER: readonly RouterRoutingState[] = [
  RouterRoutingState.Routed,
  RouterRoutingState.Passthrough,
];

export const GROUP_LABEL: Readonly<Record<RouterRoutingState, string>> = {
  [RouterRoutingState.Passthrough]: "Passthrough",
  [RouterRoutingState.Routed]: "Routed",
};
