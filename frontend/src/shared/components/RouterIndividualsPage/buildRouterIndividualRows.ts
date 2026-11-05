import { type RouterActivityPoint, type RouterIndividual, RouterRoutingState } from "../../data";
import { COLLATOR } from "../../tools/collator";
import { normalizeSearchText } from "../../tools/strings";
import { buildRankLookup } from "./buildRankLookup";
import { GROUP_LABEL, LOWER_IS_BETTER, ROUTING_GROUP_ORDER, RouterRankedMetric } from "./constants";
import {
  type RouterGroupRow,
  type RouterIndividualRow,
  RouterIndividualRowKind,
  type RouterIndividualUsage,
  type RouterMetricRank,
  type RouterPassthroughPeriod,
  type RouterRanking,
} from "./types";

const EMPTY_USAGE: RouterIndividualUsage = {
  cacheCreationTokens: 0,
  cacheReadTokens: 0,
  cost: 0,
  costActivity: [],
  inputTokens: 0,
  outputTokens: 0,
  passthroughRequestActivity: [],
  passthroughRequests: 0,
  requestActivity: [],
  requests: 0,
};

const TOTAL_FIELDS = [
  "cacheCreationTokens",
  "cacheReadTokens",
  "cost",
  "inputTokens",
  "outputTokens",
  "passthroughRequests",
  "requests",
] as const;
const SERIES_FIELDS = ["costActivity", "passthroughRequestActivity", "requestActivity"] as const;

const RANKED_FIELD: Readonly<Record<RouterRankedMetric, "cost" | "requests">> = {
  [RouterRankedMetric.Cost]: "cost",
  [RouterRankedMetric.Requests]: "requests",
};

export function totalTokens(usage: RouterIndividualUsage): number {
  return usage.inputTokens + usage.outputTokens + usage.cacheReadTokens + usage.cacheCreationTokens;
}

function sumSeries(series: readonly (readonly RouterActivityPoint[])[]) {
  const totalByTime = new Map<number, number>();
  for (const points of series) {
    for (const point of points) {
      totalByTime.set(point.x, (totalByTime.get(point.x) ?? 0) + point.y);
    }
  }
  return [...totalByTime.entries()].sort(([a], [b]) => a - b).map(([x, y]) => ({ x, y }));
}

function sumUsage(rows: readonly RouterIndividualRow[]): RouterIndividualUsage {
  const totalUsage = { ...EMPTY_USAGE };
  for (const { usage } of rows) {
    for (const field of TOTAL_FIELDS) totalUsage[field] += usage[field];
  }
  for (const field of SERIES_FIELDS) {
    totalUsage[field] = sumSeries(rows.map(({ usage }) => usage[field]));
  }
  return totalUsage;
}

/** Most of the range's requests decide which section a person sits in. */
export function routingStateOf({ passthroughRequests, requests }: RouterIndividualUsage) {
  return passthroughRequests * 2 > requests
    ? RouterRoutingState.Passthrough
    : RouterRoutingState.Routed;
}

/** Ranks each sparkline metric among people with usage, as the main Individuals table does. */
function rankUsage(usages: readonly RouterIndividualUsage[]) {
  const rankLookups = Object.values(RouterRankedMetric).map(metric => {
    const field = RANKED_FIELD[metric];
    const values = usages
      .map(usage => usage[field])
      .filter(value => value > 0)
      .sort((a, b) => a - b);
    return {
      count: values.length,
      field,
      lookup: buildRankLookup(values, LOWER_IS_BETTER[metric]),
      metric,
    };
  });

  return (usage: RouterIndividualUsage): RouterRanking => {
    const ranking: Partial<Record<RouterRankedMetric, RouterMetricRank>> = {};
    for (const { count, field, lookup, metric } of rankLookups) {
      const rank = lookup.get(usage[field]);
      if (rank != null) ranking[metric] = { count, position: rank - 1 };
    }
    return ranking;
  };
}

/**
 * Runs of buckets where a person was on passthrough. A bucket takes the state that served most of
 * its requests; buckets without requests inside a run keep it going, so a quiet day doesn't split
 * one passthrough stretch in two.
 */
export function passthroughPeriodsFor({
  passthroughRequestActivity,
  requestActivity,
}: RouterIndividualUsage): RouterPassthroughPeriod[] {
  const passthroughByTime = new Map(passthroughRequestActivity.map(point => [point.x, point.y]));
  const periods: RouterPassthroughPeriod[] = [];
  let current: RouterPassthroughPeriod | null = null;
  for (const { x, y } of requestActivity) {
    if (y <= 0) continue;
    const isPassthrough = (passthroughByTime.get(x) ?? 0) * 2 > y;
    if (isPassthrough && current == null) {
      current = { end: x, start: x };
      periods.push(current);
    } else if (isPassthrough && current != null) {
      current.end = x;
    } else {
      current = null;
    }
  }
  return periods;
}

/**
 * The passthrough periods to shade on the sparkline. Someone who was never routed gets none: one
 * band over their whole line contrasts with nothing, though the tooltip still lists the period.
 */
export function passthroughBandsFor(
  { requestActivity }: RouterIndividualUsage,
  periods: readonly RouterPassthroughPeriod[],
): readonly RouterPassthroughPeriod[] {
  const wasRouted = requestActivity.some(
    ({ x, y }) => y > 0 && !periods.some(({ end, start }) => start <= x && x <= end),
  );
  return wasRouted ? periods : [];
}

/**
 * Table rows for everyone who can route, ranked among people with the same routing status before
 * searching so a search can't shift anyone's position. Search matches the displayed name only.
 */
export function buildRouterIndividualRows(
  individuals: readonly RouterIndividual[],
  searchQuery: string,
): RouterIndividualRow[] {
  const unranked = individuals.map(person => {
    const passthroughPeriods = passthroughPeriodsFor(person.usage);
    return {
      id: person.id,
      kind: RouterIndividualRowKind.Person as const,
      name: person.name,
      passthroughBands: passthroughBandsFor(person.usage, passthroughPeriods),
      passthroughPeriods,
      person,
      routingState: routingStateOf(person.usage),
      usage: person.usage,
    };
  });
  const rankAmong = (routingState: RouterRoutingState) =>
    rankUsage(unranked.filter(row => row.routingState === routingState).map(row => row.usage));
  const rankingByRoutingState = {
    [RouterRoutingState.Passthrough]: rankAmong(RouterRoutingState.Passthrough),
    [RouterRoutingState.Routed]: rankAmong(RouterRoutingState.Routed),
  };
  const query = normalizeSearchText(searchQuery);
  const rows = unranked
    .map(row => ({ ...row, ranking: rankingByRoutingState[row.routingState](row.usage) }))
    .filter(row => query === "" || normalizeSearchText(row.name).includes(query))
    .sort((a, b) => COLLATOR.compare(a.name, b.name));

  // Everyone routed needs no sections.
  if (!unranked.some(row => row.routingState === RouterRoutingState.Passthrough)) return rows;
  return ROUTING_GROUP_ORDER.flatMap(group => {
    const members = rows.filter(row => row.routingState === group);
    if (members.length === 0) return [];
    const groupRow: RouterGroupRow = {
      group,
      id: `group:${group}`,
      kind: RouterIndividualRowKind.Group,
      name: GROUP_LABEL[group],
      subRows: members,
      usage: sumUsage(members),
    };
    return [groupRow];
  });
}
