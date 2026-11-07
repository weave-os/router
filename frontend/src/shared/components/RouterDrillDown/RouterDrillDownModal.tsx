"use client";

import type {
  LoadRouterDrillDown,
  RouterDrillDownModalProps,
  RouterDrillDownPerson,
  RouterDrillDownResult,
} from "../../data/drillDown";

import { Avatar } from "@weave-os/ui/components/atoms/Avatar";
import { Icon } from "@weave-os/ui/components/atoms/Icon";
import { Skeleton } from "@weave-os/ui/components/atoms/Skeleton";
import { Text } from "@weave-os/ui/components/atoms/Text";
import { ErrorCard } from "@weave-os/ui/components/ErrorCard";
import { Badge } from "@weave-os/ui/components/molecules/Badge";
import { Button } from "@weave-os/ui/components/molecules/Button";
import { Modal } from "@weave-os/ui/components/molecules/Modal";
import { Appearance, Intent } from "@weave-os/ui/components/types";
import { cn } from "@weave-os/ui/tools/cn";
import { formatInteger, formatUSD } from "@weave-os/ui/tools/format";
import { LoadState } from "@weave-os/ui/tools/LoadState";

import { useEffect, useState } from "react";

import { LoadStateWrapper } from "../LoadStateWrapper";
import {
  personTotals,
  type RouterDrillDownTotals,
  totalPairs,
  usageType,
} from "./routerDrillDownRows";

export interface RouterDrillDownModalComponentProps extends RouterDrillDownModalProps {
  load: LoadRouterDrillDown;
}

const COLUMNS = [
  "User",
  "Catalog cost",
  "Comparison cost",
  "Savings",
  "Requests",
  "Usage type",
  "Session",
  "AI tool",
  "Requested model",
  "Decision models",
  "Provider",
  "Started at",
  "Finished at",
] as const;

const COLUMN_WIDTH: Partial<Record<(typeof COLUMNS)[number], string>> = {
  User: "min-w-48",
  "Usage type": "min-w-52",
};

const DATE_TIME = new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" });

/** The hosted Router cost breakdown table, fed by a host loader instead of GraphQL. */
export function RouterDrillDownModal({
  load,
  onClose,
  request,
}: RouterDrillDownModalComponentProps) {
  const [drillDownState, setDrillDownState] = useState<LoadState<RouterDrillDownResult>>(
    LoadState.loading,
  );
  useEffect(() => {
    let isCurrent = true;
    load(request).then(
      value => isCurrent && setDrillDownState(LoadState.loaded(value)),
      (error: unknown) =>
        isCurrent &&
        setDrillDownState(
          LoadState.error(error instanceof Error ? error : new Error(String(error))),
        ),
    );
    return () => {
      isCurrent = false;
    };
  }, [load, request]);

  return (
    <Modal open={true} onOpenChange={open => !open && onClose()}>
      <Modal.Backdrop />
      <Modal.Content
        className="flex h-[80dvh] w-[90dvw] max-w-screen-2xl flex-col gap-0 p-0"
        hideCloseButton
      >
        <Modal.Title className="shrink-0 border-b px-4 py-3 text-card-title font-semibold">
          Router cost breakdown
        </Modal.Title>
        <div className="min-h-0 flex-1 overflow-auto">
          <LoadStateWrapper
            state={drillDownState}
            loadingDelayMs={0}
            loading={<Skeleton className="m-4 h-64" />}
            error={error => (
              <ErrorCard
                className="mx-auto mt-8 h-fit max-w-text-width"
                title="Could not load data"
                description={`Error: ${error.message}`}
              />
            )}
          >
            {({ people }) => <RouterDrillDownTable people={people} />}
          </LoadStateWrapper>
        </div>
      </Modal.Content>
    </Modal>
  );
}

// Hosted hides the per-request detail columns until "Model details" is pressed.
const DETAIL_COLUMN_START = 7;
const UNLINKED_LABEL = "Unlinked router user";

function personLabel({ displayName, email }: RouterDrillDownPerson) {
  if (displayName !== "") return displayName;
  return email === "" ? UNLINKED_LABEL : email;
}

function RouterDrillDownTable({ people }: { people: readonly RouterDrillDownPerson[] }) {
  const [showDetails, setShowDetails] = useState(false);
  const visibleColumns = showDetails ? COLUMNS : COLUMNS.slice(0, DETAIL_COLUMN_START);
  const [expanded, setExpanded] = useState<ReadonlySet<string>>(new Set());
  const toggle = (id: string) =>
    setExpanded(previous => {
      const next = new Set(previous);
      if (!next.delete(id)) next.add(id);
      return next;
    });

  if (people.length === 0) {
    return (
      <Text variant="p" className="py-12 text-center text-wv-secondary">
        No requests in this range.
      </Text>
    );
  }
  return (
    <>
      <div className="flex shrink-0 flex-wrap items-center justify-between gap-2 border-b px-3 py-2">
        <Text variant="p" className="text-caption text-wv-secondary">
          Estimated API costs at catalog rates. Subscription requests contribute $0.
        </Text>
        <Button
          appearance={Appearance.Hollow}
          className="min-h-11 shrink-0 px-3"
          aria-label="Model details"
          aria-pressed={showDetails}
          onClick={() => setShowDetails(value => !value)}
        >
          Model details
        </Button>
      </div>
      <table className="w-full border-collapse text-body">
        <thead className="bg-wv-card sticky top-0 z-10 shadow-[inset_0_-1px_0_hsl(var(--border))]">
          <tr>
            {visibleColumns.map(column => (
              <th
                key={column}
                scope="col"
                className={cn(
                  "whitespace-nowrap border-r border-wv-subtle px-3 py-3 text-left font-medium last:border-r-0",
                  COLUMN_WIDTH[column] ?? "min-w-32",
                )}
              >
                {column}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {people.map(person => {
            const isExpanded = expanded.has(person.id);
            return [
              <DrillDownRow
                key={person.id}
                columns={visibleColumns.length}
                totals={personTotals(person)}
                user={
                  <button
                    type="button"
                    aria-expanded={isExpanded}
                    className="flex max-w-48 items-center gap-2"
                    onClick={() => toggle(person.id)}
                  >
                    <Icon
                      type={isExpanded ? "ChevronDown" : "ChevronRight"}
                      className="size-4 shrink-0"
                    />
                    <Avatar name={personLabel(person)} className="size-6 shrink-0" />
                    <span className="truncate">{personLabel(person)}</span>
                  </button>
                }
                session={`${formatInteger(person.sessionCount)} ${person.sessionCount === 1 ? "session" : "sessions"}`}
              />,
              ...(isExpanded
                ? person.sessions.flatMap(session => [
                    <DrillDownRow
                      key={`${person.id}:${session.sessionId}`}
                      columns={visibleColumns.length}
                      className="bg-wv-subtle/40"
                      totals={totalPairs(session.models)}
                      session={session.sessionId === "" ? "No session" : session.sessionId}
                      aiTool={session.clientApp}
                      startedAt={session.startedAt}
                      finishedAt={session.finishedAt}
                    />,
                  ])
                : []),
            ];
          })}
        </tbody>
      </table>
    </>
  );
}

interface DrillDownRowProps {
  aiTool?: string;
  className?: string;
  columns: number;
  finishedAt?: number;
  session: string;
  startedAt?: number;
  totals: RouterDrillDownTotals;
  user?: React.ReactNode;
}

function DrillDownRow({
  aiTool,
  className,
  columns,
  finishedAt,
  session,
  startedAt,
  totals,
  user,
}: DrillDownRowProps) {
  const cells = [
    user,
    formatUSD(totals.catalogCostUsd),
    formatUSD(totals.comparisonCostUsd),
    formatUSD(totals.comparisonCostUsd - totals.catalogCostUsd),
    formatInteger(totals.requests),
    <Badge key="usage" appearance={Appearance.Tinted} intent={Intent.Primary}>
      {usageType(totals)}
    </Badge>,
    session,
    aiTool,
    totals.requestedModels.join(", "),
    totals.decisionModels.join(", "),
    totals.providers.join(", "),
    startedAt == null ? undefined : DATE_TIME.format(startedAt),
    finishedAt == null ? undefined : DATE_TIME.format(finishedAt),
  ];
  return (
    <tr className={cn("border-t border-wv-subtle", className)}>
      {cells.slice(0, columns).map((cell, index) => (
        <td
          key={COLUMNS[index]}
          className={cn("whitespace-nowrap px-3 py-2", index > 0 && index < 5 && "tabular-nums")}
        >
          {cell ?? "—"}
        </td>
      ))}
    </tr>
  );
}
