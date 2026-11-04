"use client";

import { format, formatDistanceToNowStrict } from "date-fns";

import { type ReactNode, useMemo, useState } from "react";

import { type RouterPerson, RouterUserLink, useRouterPeople } from "../../data";
import { ChartColorToken } from "../../tools/chartColors";
import { cn } from "../../tools/cn";
import { formatInteger, formatPercentage } from "../../tools/format";
import { LoadState } from "../../tools/LoadState";
import { pluralize } from "../../tools/strings";
import { DateFormat } from "../../tools/time";
import { TypographyRole } from "../../tools/typography";
import { Avatar } from "../atoms/Avatar";
import { Icon } from "../atoms/Icon";
import { MetricValue } from "../atoms/MetricValue";
import { Skeleton } from "../atoms/Skeleton";
import { Text } from "../atoms/Text";
import { CHART_CARD_TITLE_CLASS } from "../ChartCard";
import { LoadStateWrapper } from "../LoadStateWrapper";
import { Badge } from "../molecules/Badge";
import { Card } from "../molecules/Card";
import { SearchableList } from "../molecules/SearchableList";
import { StackedBar } from "../molecules/StackedBar";
import { Tooltip } from "../molecules/Tooltip";

const DAY_MS = 24 * 60 * 60 * 1000;
const ACTIVE_WINDOW_DAYS = 30;
const INACTIVE_AFTER_DAYS = 7;
const INACTIVE_PAGE_SIZE = 5;
const ALL_TIME_CAVEAT = "All time, not affected by the date filter";

enum AdoptionSlice {
  Active = "active",
  NotUsedYet = "not_used_yet",
  UsedNotActive = "used_not_active",
}

interface InactivePerson {
  id: string;
  lastRequestAt: Date;
  link: RouterUserLink;
  name: string;
}

interface AdoptionCounts {
  active: number;
  engineers: number;
  inactive: readonly InactivePerson[];
  routerUsers: number;
  unlinkedActive: number;
  unlinkedRouterUsers: number;
}

/** Roster users split by how recently they routed; unlinked keys are counted beside them. */
export function adoptionCounts(people: readonly RouterPerson[], now: number): AdoptionCounts {
  const daysSince = (lastRequestAt: Date) => (now - lastRequestAt.getTime()) / DAY_MS;
  const used = people.flatMap(person =>
    person.lastRequestAt == null
      ? []
      : [
          {
            id: person.id,
            lastRequestAt: new Date(person.lastRequestAt),
            link: person.link,
            name: person.displayName || person.email || person.id,
          },
        ],
  );
  const linked = used.filter(({ link }) => link === RouterUserLink.Linked);
  const unlinked = used.filter(({ link }) => link === RouterUserLink.Unlinked);
  const isActive = ({ lastRequestAt }: InactivePerson) =>
    daysSince(lastRequestAt) <= ACTIVE_WINDOW_DAYS;
  return {
    active: linked.filter(isActive).length,
    engineers: people.filter(({ link }) => link === RouterUserLink.Linked).length,
    inactive: used
      .filter(({ lastRequestAt }) => daysSince(lastRequestAt) > INACTIVE_AFTER_DAYS)
      .sort((a, b) => a.lastRequestAt.getTime() - b.lastRequestAt.getTime()),
    routerUsers: linked.length,
    unlinkedActive: unlinked.filter(isActive).length,
    unlinkedRouterUsers: unlinked.length,
  };
}

function useAdoptionCounts() {
  const people = useRouterPeople();
  const [now] = useState(() => Date.now());
  return useMemo(() => LoadState.map(people, rows => adoptionCounts(rows, now)), [now, people]);
}

function adoptionSlices({ active, engineers, routerUsers }: AdoptionCounts) {
  return [
    {
      amount: active,
      color: ChartColorToken.Scale1,
      id: AdoptionSlice.Active,
      label: `Active in the last ${ACTIVE_WINDOW_DAYS} days`,
      shortLabel: "active",
    },
    {
      amount: routerUsers - active,
      color: ChartColorToken.Orange2,
      id: AdoptionSlice.UsedNotActive,
      label: `Used, but not in the last ${ACTIVE_WINDOW_DAYS} days`,
      shortLabel: "used before",
    },
    {
      amount: engineers - routerUsers,
      color: "var(--wv-chart-adoption-nonuser)",
      id: AdoptionSlice.NotUsedYet,
      label: "Not used yet",
      shortLabel: "not yet",
    },
  ];
}

function unlinkedAdoptionDetails({ unlinkedActive, unlinkedRouterUsers }: AdoptionCounts) {
  return unlinkedRouterUsers === 0
    ? []
    : [
        `${formatInteger(unlinkedRouterUsers)} unlinked router ${pluralize(unlinkedRouterUsers, "user")} ${unlinkedRouterUsers === 1 ? "has" : "have"} used the router`,
        `${formatInteger(unlinkedActive)} of ${formatInteger(unlinkedRouterUsers)} unlinked router ${pluralize(unlinkedRouterUsers, "user")} active in the last ${ACTIVE_WINDOW_DAYS} days`,
      ];
}

function CardLoadState({
  children,
  state,
}: {
  children: (counts: AdoptionCounts) => ReactNode;
  state: LoadState<AdoptionCounts>;
}) {
  return (
    <LoadStateWrapper
      state={state}
      loading={
        <>
          <Skeleton.Text size="h1" className="mt-2 w-24" />
          <Skeleton.Text size="xs" className="mt-1 w-48" />
        </>
      }
      error={() => (
        <Text className="mt-2 text-caption text-wv-secondary">
          Couldn&apos;t load. Try refreshing.
        </Text>
      )}
    >
      {children}
    </LoadStateWrapper>
  );
}

/**
 * The roster split three ways: active in the last 30 days, used the router but not since, and
 * never used it. The three always add up to the roster.
 */
export function RouterAdoptionCard({ className }: { className?: string }) {
  const state = useAdoptionCounts();
  const counts = LoadState.unwrap(state);

  return (
    <div aria-live="polite" className={cn("card-default surface-card rounded-lg p-4", className)}>
      <div className="flex flex-wrap items-start justify-between gap-x-4 gap-y-1">
        <Card.Title variant="h4" className={CHART_CARD_TITLE_CLASS}>
          Adoption
          <Tooltip
            content={
              <span className="flex flex-col gap-1">
                <span>Engineer accounts on the routing roster. {ALL_TIME_CAVEAT}.</span>
                {counts != null &&
                  unlinkedAdoptionDetails(counts).map(line => <span key={line}>{line}.</span>)}
              </span>
            }
            contentClassName="max-w-xs"
            interactiveChild
          >
            <button
              type="button"
              aria-label="About adoption"
              className="flex size-6 items-center justify-center rounded text-wv-secondary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-wv-focus"
            >
              <Icon type="Info" className="size-3.5" aria-hidden />
            </button>
          </Tooltip>
        </Card.Title>
        {counts != null && counts.engineers > 0 && (
          <ul className="flex min-h-8 flex-wrap items-center justify-end gap-x-3 gap-y-1 text-caption text-wv-secondary">
            {adoptionSlices(counts).map(({ amount, color, id, label, shortLabel }) => (
              <li key={id} className="flex items-center gap-1.5" title={label}>
                <span
                  aria-hidden="true"
                  className="size-2.5 shrink-0 rounded-sm"
                  style={{ backgroundColor: color }}
                />
                <span>
                  <span className="tabular-nums text-wv-primary">{formatInteger(amount)}</span>{" "}
                  {shortLabel}
                  <span className="sr-only">: {label}</span>
                </span>
              </li>
            ))}
          </ul>
        )}
      </div>
      <CardLoadState state={state}>
        {adoption => (
          <>
            <MetricValue
              className="mt-2 block tracking-tight"
              size={TypographyRole.Display}
              value={
                adoption.engineers === 0
                  ? "—"
                  : formatPercentage(adoption.routerUsers / adoption.engineers)
              }
            />
            <Text className="mt-1 text-caption text-wv-secondary">
              {formatInteger(adoption.routerUsers)} of {formatInteger(adoption.engineers)} engineers
              have used the router · all time
            </Text>
            {adoption.engineers > 0 && (
              <StackedBar
                className="mt-4 gap-1"
                segments={adoptionSlices(adoption)}
                size="md"
                total={adoption.engineers}
              />
            )}
          </>
        )}
      </CardLoadState>
    </div>
  );
}

/** People with no router request in a week, searchable and paged. */
export function RouterInactiveCard({ className }: { className?: string }) {
  const state = useAdoptionCounts();

  return (
    <div aria-live="polite" className={cn("card-default surface-card rounded-lg p-4", className)}>
      <Card.Title variant="h4" className={CHART_CARD_TITLE_CLASS}>
        Inactive
      </Card.Title>
      <CardLoadState state={state}>
        {({ inactive }) => {
          const linked = inactive.filter(({ link }) => link === RouterUserLink.Linked).length;
          const unlinked = inactive.length - linked;
          return (
            <>
              <MetricValue
                className="mt-2 block"
                size={TypographyRole.Display}
                value={formatInteger(linked)}
              />
              <Text className="mt-1 text-caption text-wv-secondary">
                linked people with no request in {INACTIVE_AFTER_DAYS}+ days ·{" "}
                {ALL_TIME_CAVEAT.toLowerCase()}
              </Text>
              {unlinked > 0 && (
                <Text className="text-caption text-wv-secondary">
                  {formatInteger(unlinked)} unlinked router {pluralize(unlinked, "user")} with no
                  request in {INACTIVE_AFTER_DAYS}+ days
                </Text>
              )}
              {inactive.length > 0 && (
                <div className="mt-3 flex flex-col gap-2">
                  <SearchableList
                    ariaLabel="Search inactive router users"
                    getSearchValue={({ name }) => name}
                    itemLabel="people"
                    items={[...inactive]}
                    pageSize={INACTIVE_PAGE_SIZE}
                    placeholder="Search people"
                  >
                    {visible => (
                      <ul aria-label="Inactive router users">
                        {visible.map(person => (
                          <InactivePersonRow key={person.id} person={person} />
                        ))}
                      </ul>
                    )}
                  </SearchableList>
                </div>
              )}
            </>
          );
        }}
      </CardLoadState>
    </div>
  );
}

function InactivePersonRow({ person: { lastRequestAt, link, name } }: { person: InactivePerson }) {
  return (
    <li className="flex min-h-10 items-center gap-3 py-1">
      <Avatar className="size-6 shrink-0" name={name} />
      <Text className="min-w-0 flex-1 truncate text-body" title={name}>
        {name}
      </Text>
      {link === RouterUserLink.Unlinked && <Badge className="shrink-0">Unlinked</Badge>}
      <Tooltip content={format(lastRequestAt, DateFormat.DateTimeHumanReadable)} interactiveChild>
        <span className="shrink-0 text-caption text-wv-secondary" tabIndex={0}>
          {formatDistanceToNowStrict(lastRequestAt, { addSuffix: true })}
        </span>
      </Tooltip>
    </li>
  );
}
