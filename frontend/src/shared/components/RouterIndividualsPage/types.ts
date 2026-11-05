import { type RouterIndividual, type RouterRoutingState } from "../../data";
import { type RouterRankedMetric } from "./constants";

export type RouterIndividualUsage = RouterIndividual["usage"];

/** A row's place among peers with usage for one metric; position 0 is the best. */
export interface RouterMetricRank {
  count: number;
  position: number;
}

export type RouterRanking = Readonly<Partial<Record<RouterRankedMetric, RouterMetricRank>>>;

/** A run of buckets, first to last inclusive, where most of a person's requests weren't routed. */
export interface RouterPassthroughPeriod {
  end: number;
  start: number;
}

export enum RouterIndividualRowKind {
  Group = "group",
  Person = "person",
}

export interface RouterPersonRow {
  id: string;
  kind: RouterIndividualRowKind.Person;
  name: string;
  person: RouterIndividual;
  passthroughBands: readonly RouterPassthroughPeriod[];
  passthroughPeriods: readonly RouterPassthroughPeriod[];
  ranking: RouterRanking;
  routingState: RouterRoutingState;
  usage: RouterIndividualUsage;
}

/** A section header over people sharing a routing status. */
export interface RouterGroupRow {
  group: RouterRoutingState;
  id: string;
  kind: RouterIndividualRowKind.Group;
  name: string;
  subRows: readonly RouterIndividualRow[];
  usage: RouterIndividualUsage;
}

export type RouterIndividualRow = RouterGroupRow | RouterPersonRow;
