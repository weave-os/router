"use client";

import { format, formatDistanceStrict } from "date-fns";

import { useEffect, useState } from "react";

import {
  isUnavailable,
  RouterForbiddenError,
  RouterSubscriptionProvider,
  RouterSubscriptionSeatState,
  useRouterDashboardDataSource,
  useRouterDashboardQuery,
} from "../../data";
import { cn } from "../../tools/cn";
import { formatInteger, formatPercentage } from "../../tools/format";
import { DateFormat } from "../../tools/time";
import { EXTERNAL_ICON_NAME, ExternalIcon, ExternalIconType } from "../atoms/ExternalIcon";
import { MetricValue } from "../atoms/MetricValue";
import { Skeleton } from "../atoms/Skeleton";
import { Text } from "../atoms/Text";
import { CHART_CARD_TITLE_CLASS } from "../ChartCard";
import { LoadStateWrapper } from "../LoadStateWrapper";
import { Accordion } from "../molecules/Accordion";
import { Badge } from "../molecules/Badge";
import { Card } from "../molecules/Card";
import { Tooltip } from "../molecules/Tooltip";
import { Intent } from "../types";
import { ProviderRunwayChart } from "./ProviderRunwayChart";
import {
  describeRunway,
  forecastProviderRunway,
  isOutOfPoolSeat,
  isServingSeat,
  isSessionWindow,
  limitWindowLabel,
  type PoolLimitWindow,
  type PoolSeat,
  type ProviderPoolSummary,
  type ProviderRunway,
  readLimitWindow,
  readLimitWindowPace,
  summarizeProviderPool,
} from "./subscriptionPoolDisplay";

const PROVIDER_ASSISTANT: Readonly<Record<RouterSubscriptionProvider, ExternalIconType>> = {
  [RouterSubscriptionProvider.Claude]: ExternalIconType.ClaudeCode,
  [RouterSubscriptionProvider.Codex]: ExternalIconType.Codex,
};

const SEAT_STATE_LABEL: Readonly<Record<RouterSubscriptionSeatState, string>> = {
  [RouterSubscriptionSeatState.Active]: "Connected",
  [RouterSubscriptionSeatState.Cooldown]: "Cooling down",
  [RouterSubscriptionSeatState.Disabled]: "Disabled",
  [RouterSubscriptionSeatState.Exhausted]: "Usage exhausted",
  [RouterSubscriptionSeatState.ReconnectRequired]: "Reconnect required",
  [RouterSubscriptionSeatState.Unknown]: "Checking connection",
};

const SEAT_STATE_INTENT: Readonly<Record<RouterSubscriptionSeatState, Intent>> = {
  [RouterSubscriptionSeatState.Active]: Intent.Success,
  [RouterSubscriptionSeatState.Cooldown]: Intent.Warning,
  [RouterSubscriptionSeatState.Disabled]: Intent.Default,
  [RouterSubscriptionSeatState.Exhausted]: Intent.Warning,
  [RouterSubscriptionSeatState.ReconnectRequired]: Intent.Danger,
  [RouterSubscriptionSeatState.Unknown]: Intent.Default,
};

/** Reset countdowns and refills re-evaluate on this cadence while the tab stays open. */
const CLOCK_TICK_MS = 60_000;

const PROVIDER_ORDER = [RouterSubscriptionProvider.Claude, RouterSubscriptionProvider.Codex];

const CARD_CLASS = "card-default surface-card rounded-lg p-4";
const SUMMARY_GRID_CLASS =
  "grid grid-cols-1 gap-2.5 @xl:grid-cols-[repeat(auto-fit,minmax(16rem,1fr))]";
const SEAT_CELL_CLASS = "py-3 pr-6 align-top last:pr-0";
const SEAT_HEADER_CLASS = "py-2 pr-6 font-normal last:pr-0";

function seatName(seat: PoolSeat) {
  return seat.displayName ?? `${EXTERNAL_ICON_NAME[PROVIDER_ASSISTANT[seat.provider]]} seat`;
}

function useClock() {
  const [now, setNow] = useState(() => new Date());
  useEffect(() => {
    const tickId = window.setInterval(() => setNow(new Date()), CLOCK_TICK_MS);
    return () => window.clearInterval(tickId);
  }, []);
  return now;
}

function useSubscriptionPool() {
  const { subscriptions } = useRouterDashboardDataSource();
  return useRouterDashboardQuery(isUnavailable(subscriptions) ? null : () => subscriptions.pool(), [
    subscriptions,
  ]);
}

/**
 * Every subscription in the organization's router pool with how much of each provider limit it
 * last reported using. Readings come from the seat's latest served turn, so an idle seat's
 * reading can predate its reset; those windows show as refilled rather than as stale usage.
 */
export function RouterSubscriptionPool({ className }: { className?: string }) {
  const poolState = useSubscriptionPool();
  const now = useClock();

  return (
    <div aria-live="polite" className={cn("flex flex-col gap-2.5", className)}>
      <LoadStateWrapper
        state={poolState}
        loading={
          <>
            <div className={SUMMARY_GRID_CLASS}>
              {PROVIDER_ORDER.map(provider => (
                <div key={provider} className={CARD_CLASS}>
                  <Skeleton.Text size="sm" className="w-28" />
                  <Skeleton.Text size="h3" className="mt-2 w-24" />
                  <Skeleton.Text size="xs" className="mt-1 w-48" />
                  <Skeleton className="mt-4 h-48 w-full" />
                </div>
              ))}
            </div>
            <div className={CARD_CLASS}>
              <Skeleton.Text size="sm" className="w-28" />
              <Skeleton.Text size="xs" className="mt-4 w-full" />
              <Skeleton.Text size="xs" className="mt-3 w-full" />
            </div>
          </>
        }
        error={error => (
          <PoolMessage>
            {error instanceof RouterForbiddenError
              ? "Only team leads and admins can see the subscription pool."
              : "Couldn't load the subscription pool. Try refreshing."}
          </PoolMessage>
        )}
      >
        {({ enabled, seats }) => {
          if (!enabled) {
            return <PoolMessage>Subscription pools are turned off on this router.</PoolMessage>;
          }
          if (seats.length === 0) {
            return <PoolMessage>No subscriptions in the router pool yet.</PoolMessage>;
          }
          return <PoolSeats seats={seats} now={now} />;
        }}
      </LoadStateWrapper>
    </div>
  );
}

function PoolMessage({ children }: { children: string }) {
  return (
    <div className={CARD_CLASS}>
      <Card.Title variant="h4" className={CHART_CARD_TITLE_CLASS}>
        Subscription pool
      </Card.Title>
      <Text className="mt-2 text-caption text-wv-secondary">{children}</Text>
    </div>
  );
}

function PoolSeats({ now, seats }: { now: Date; seats: readonly PoolSeat[] }) {
  const outOfPoolSeats = seats.filter(isOutOfPoolSeat);
  const providers = PROVIDER_ORDER.filter(provider =>
    seats.some(seat => seat.provider === provider && !isOutOfPoolSeat(seat)),
  );

  return (
    <>
      <div className={SUMMARY_GRID_CLASS}>
        {providers.map(provider => (
          <ProviderSummaryCard
            key={provider}
            now={now}
            runway={forecastProviderRunway(provider, seats, now)}
            summary={summarizeProviderPool(provider, seats, now)}
          />
        ))}
      </div>
      {providers.map(provider => (
        <ProviderPool
          key={provider}
          now={now}
          provider={provider}
          seats={seats
            .filter(seat => seat.provider === provider && !isOutOfPoolSeat(seat))
            .sort((a, b) => Number(isServingSeat(b)) - Number(isServingSeat(a)))}
        />
      ))}
      {outOfPoolSeats.length > 0 && <OutOfPoolSeats seats={outOfPoolSeats} />}
    </>
  );
}

function ProviderSummaryCard({
  now,
  runway,
  summary,
}: {
  now: Date;
  runway: ProviderRunway | null;
  summary: ProviderPoolSummary;
}) {
  const assistant = PROVIDER_ASSISTANT[summary.provider];
  const headingId = `router-subscription-pool-summary-${summary.provider}`;
  return (
    <section aria-labelledby={headingId} className={CARD_CLASS}>
      <div className="flex items-center justify-between gap-3">
        <Card.Title id={headingId} variant="h4" className={CHART_CARD_TITLE_CLASS}>
          <ExternalIcon type={assistant} alt="" />
          {`${EXTERNAL_ICON_NAME[assistant]} weekly limit left`}
        </Card.Title>
        {runway != null && (
          <Badge intent={runway.isBurningFast ? Intent.Warning : Intent.Default}>
            {runway.isBurningFast ? "Burning fast" : "On track"}
          </Badge>
        )}
      </div>
      <MetricValue
        className="mt-2 block tracking-tight"
        value={
          summary.weeklyRemainingFraction == null
            ? "—"
            : formatPercentage(summary.weeklyRemainingFraction)
        }
      />
      <Text className="mt-1 text-caption text-wv-secondary">
        {summary.weeklyRemainingFraction == null
          ? "No seat has reported a weekly limit yet"
          : "Pooled average"}
        {` · ${formatInteger(summary.servingSeats)} of ${formatInteger(summary.pooledSeats)} seats serving`}
      </Text>
      {runway != null && (
        <>
          <ProviderRunwayChart
            label={`${EXTERNAL_ICON_NAME[assistant]} limit left over the next 7 days`}
            now={now}
            runway={runway}
          />
          <Text className="mt-2 text-caption text-wv-secondary">{describeRunway(runway, now)}</Text>
        </>
      )}
    </section>
  );
}

function ProviderPool({
  now,
  provider,
  seats,
}: {
  now: Date;
  provider: RouterSubscriptionProvider;
  seats: readonly PoolSeat[];
}) {
  const headingId = `router-subscription-pool-${provider}`;
  // Pooled Codex plans report only a weekly window, so the column follows what seats report.
  const hasSessionLimit = seats.some(seat => seat.limitWindows.some(isSessionWindow));

  return (
    <section aria-labelledby={headingId} className={CARD_CLASS}>
      <Card.Title id={headingId} variant="h4" className={CHART_CARD_TITLE_CLASS}>
        <ExternalIcon type={PROVIDER_ASSISTANT[provider]} alt="" />
        {EXTERNAL_ICON_NAME[PROVIDER_ASSISTANT[provider]]} seats
      </Card.Title>
      <Text className="mt-1 text-caption text-wv-secondary">As last reported by each seat</Text>
      {/* Narrow screens scroll the table sideways instead of crushing the meters. */}
      <div className="mt-3 overflow-x-auto">
        <table aria-labelledby={headingId} className="w-full min-w-[36rem] table-fixed text-left">
          <thead className="text-caption text-wv-secondary">
            <tr>
              <th scope="col" className={cn(SEAT_HEADER_CLASS, "w-[32%]")}>
                Seat
              </th>
              {hasSessionLimit && (
                <th scope="col" className={SEAT_HEADER_CLASS}>
                  Session limit
                </th>
              )}
              <th scope="col" className={SEAT_HEADER_CLASS}>
                Weekly limit
              </th>
              <th scope="col" className={cn(SEAT_HEADER_CLASS, "w-28 text-right")}>
                Requests (7 days)
              </th>
            </tr>
          </thead>
          <tbody>
            {seats.map(seat => (
              <SeatRow key={seat.id} hasSessionLimit={hasSessionLimit} now={now} seat={seat} />
            ))}
          </tbody>
        </table>
      </div>
    </section>
  );
}

function SeatRow({
  hasSessionLimit,
  now,
  seat,
}: {
  hasSessionLimit: boolean;
  now: Date;
  seat: PoolSeat;
}) {
  const sessionWindow = seat.limitWindows.find(isSessionWindow);
  const weeklyWindow = seat.limitWindows.find(limitWindow => !isSessionWindow(limitWindow));

  return (
    <tr className="border-t border-wv-subtle">
      <th scope="row" className={cn(SEAT_CELL_CLASS, "font-normal")}>
        <Text
          className={cn(
            "truncate text-body font-medium",
            isServingSeat(seat) ? "text-wv-primary" : "text-wv-secondary",
          )}
        >
          {seatName(seat)}
        </Text>
        <div className="mt-0.5 flex flex-wrap items-center gap-2">
          <Text className="truncate text-caption text-wv-secondary">
            {seat.ownerName ?? "Unassigned owner"}
          </Text>
          {seat.state !== RouterSubscriptionSeatState.Active && (
            <Badge intent={SEAT_STATE_INTENT[seat.state]} className="text-chart">
              {SEAT_STATE_LABEL[seat.state]}
            </Badge>
          )}
        </div>
      </th>
      {hasSessionLimit && (
        <td className={SEAT_CELL_CLASS}>
          <LimitMeter now={now} observedAt={seat.limitsObservedAt} limitWindow={sessionWindow} />
        </td>
      )}
      <td className={SEAT_CELL_CLASS}>
        <LimitMeter now={now} observedAt={seat.limitsObservedAt} limitWindow={weeklyWindow} />
      </td>
      <td className={cn(SEAT_CELL_CLASS, "text-right text-body tabular-nums text-wv-primary")}>
        {formatInteger(seat.servedRequests)}
      </td>
    </tr>
  );
}

function LimitMeter({
  now,
  observedAt,
  limitWindow,
}: {
  now: Date;
  observedAt: PoolSeat["limitsObservedAt"];
  limitWindow: PoolLimitWindow | undefined;
}) {
  if (limitWindow == null) {
    return <Text className="text-caption text-wv-secondary">No reading in the last 8 days</Text>;
  }

  const { hasRefilled, usedFraction } = readLimitWindow(limitWindow, now);
  const pace = readLimitWindowPace(limitWindow, now);
  const isExhausted = usedFraction >= 1;
  const label = limitWindowLabel(limitWindow.windowMinutes);
  const resetsAt = limitWindow.resetsAt == null ? null : new Date(limitWindow.resetsAt);
  const status = hasRefilled
    ? "Refilled since last reading"
    : `${formatPercentage(1 - usedFraction)} left`;
  const resetDetail =
    resetsAt == null || hasRefilled ? null : `resets in ${formatDistanceStrict(resetsAt, now)}`;
  const summary = [
    `${label} limit: ${hasRefilled ? "refilled since the last reading" : `${formatPercentage(usedFraction)} used`}`,
    resetsAt == null
      ? null
      : `${hasRefilled ? "reset" : "resets"} ${format(resetsAt, DateFormat.DateTimeHumanReadable)}`,
    observedAt == null ? null : `reported ${formatDistanceStrict(new Date(observedAt), now)} ago`,
    pace?.isBehindPace === true ? "Behind pace" : null,
  ]
    .filter(part => part != null)
    .join(" · ");

  return (
    <Tooltip content={summary} interactiveChild>
      <div role="img" aria-label={summary} className="flex flex-col gap-1">
        <div className="relative h-2 w-full rounded-full bg-wv-track">
          <div
            className={cn(
              "absolute inset-y-0 left-0 rounded-full",
              pace?.isBehindPace === true
                ? "bg-wv-status-warning-solid"
                : "bg-wv-status-neutral-solid",
            )}
            style={{ width: `${(1 - usedFraction) * 100}%` }}
          />
          {pace != null && (
            <div
              aria-hidden
              className="absolute -inset-y-1 w-0.5 -translate-x-1/2 rounded-full bg-wv-neutral-solid"
              style={{ left: `${pace.evenBurnRemainingFraction * 100}%` }}
            />
          )}
        </div>
        <Text className="text-caption text-wv-secondary">
          {isExhausted ? "Limit reached" : status}
          {resetDetail != null && ` · ${resetDetail}`}
          {pace?.isBehindPace === true && " · Behind pace"}
        </Text>
      </div>
    </Tooltip>
  );
}

/** Disabled wins over reconnect: re-enabling is the first thing a disabled seat needs. */
function outOfPoolReason(seat: PoolSeat) {
  return seat.enabled
    ? RouterSubscriptionSeatState.ReconnectRequired
    : RouterSubscriptionSeatState.Disabled;
}

const OUT_OF_POOL_REASON_ORDER = [
  RouterSubscriptionSeatState.ReconnectRequired,
  RouterSubscriptionSeatState.Disabled,
];

function OutOfPoolSeats({ seats }: { seats: readonly PoolSeat[] }) {
  return (
    <Accordion type="single" collapsible className={CARD_CLASS}>
      <Accordion.Item value="out-of-pool">
        <Accordion.Header asChild>
          <h4>
            <Accordion.Trigger
              className={cn(
                CHART_CARD_TITLE_CLASS,
                "w-fit grow-0 basis-auto text-card-title text-[var(--wv-card-heading)] hover:no-underline",
              )}
            >
              <Accordion.TriggerIcon />
              Not serving
              <span className="text-caption font-normal text-wv-secondary">
                {formatInteger(seats.length)} {seats.length === 1 ? "seat" : "seats"}
              </span>
            </Accordion.Trigger>
          </h4>
        </Accordion.Header>
        <Accordion.Content className="flex flex-col gap-4 pb-0 pt-2">
          <Text className="text-caption text-wv-secondary">
            These seats stay out of the pool until their owner reconnects or re-enables them.
          </Text>
          {OUT_OF_POOL_REASON_ORDER.map(reason => {
            const reasonSeats = seats.filter(seat => outOfPoolReason(seat) === reason);
            if (reasonSeats.length === 0) return null;
            const listId = `router-subscription-pool-out-of-pool-${reason}`;
            return (
              <div key={reason} className="flex flex-col gap-2">
                <Badge id={listId} intent={SEAT_STATE_INTENT[reason]} className="w-fit text-chart">
                  {SEAT_STATE_LABEL[reason]} · {formatInteger(reasonSeats.length)}
                </Badge>
                <ul
                  aria-labelledby={listId}
                  className="grid gap-x-6 gap-y-2 sm:grid-cols-2 lg:grid-cols-3"
                >
                  {reasonSeats.map(seat => (
                    <li key={seat.id} className="flex min-w-0 items-center gap-2">
                      <ExternalIcon type={PROVIDER_ASSISTANT[seat.provider]} alt="" />
                      <Text className="truncate text-body text-wv-primary">{seatName(seat)}</Text>
                      <Text className="shrink-0 text-caption text-wv-secondary">
                        {seat.ownerName ?? "Unassigned owner"}
                      </Text>
                    </li>
                  ))}
                </ul>
              </div>
            );
          })}
        </Accordion.Content>
      </Accordion.Item>
    </Accordion>
  );
}
