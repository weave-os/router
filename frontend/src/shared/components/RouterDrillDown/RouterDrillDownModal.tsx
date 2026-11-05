"use client";

import type {
  LoadRouterDrillDown,
  RouterDrillDownModalProps,
  RouterDrillDownPerson,
  RouterDrillDownResult,
} from "../../data/drillDown";

import { useEffect, useState } from "react";

import { cn } from "../../tools/cn";
import { formatInteger, formatUSD } from "../../tools/format";
import { LoadState } from "../../tools/LoadState";
import { Icon } from "../atoms/Icon";
import { Skeleton } from "../atoms/Skeleton";
import { Text } from "../atoms/Text";
import { ErrorCard } from "../ErrorCard";
import { LoadStateWrapper } from "../LoadStateWrapper";
import { Modal } from "../molecules/Modal";
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

const DATE_TIME = new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" });

/** The hosted Router cost breakdown table, fed by a host loader instead of GraphQL. */
export function RouterDrillDownModal({
  load,
  onClose,
  request,
}: RouterDrillDownModalComponentProps) {
  const [result, setResult] = useState<LoadState<RouterDrillDownResult>>(LoadState.loading);
  useEffect(() => {
    let isCurrent = true;
    load(request).then(
      value => isCurrent && setResult(LoadState.loaded(value)),
      (error: unknown) =>
        isCurrent &&
        setResult(LoadState.error(error instanceof Error ? error : new Error(String(error)))),
    );
    return () => {
      isCurrent = false;
    };
  }, [load, request]);

  return (
    <Modal open={true} onOpenChange={open => !open && onClose()}>
      <Modal.Backdrop />
      <Modal.Content
        className="flex h-[80dvh] w-[90dvw] max-w-screen-2xl flex-col p-0"
        hideCloseButton
      >
        <Modal.Title className="shrink-0 border-b px-4 py-3 text-card-title font-semibold">
          Router cost breakdown
        </Modal.Title>
        <div className="min-h-0 flex-1 overflow-auto">
          <LoadStateWrapper
            state={result}
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

function RouterDrillDownTable({ people }: { people: readonly RouterDrillDownPerson[] }) {
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
    <table className="w-full border-collapse text-body">
      <thead className="bg-wv-card sticky top-0 z-10 shadow-[inset_0_-1px_0_hsl(var(--border))]">
        <tr>
          {COLUMNS.map(column => (
            <th
              key={column}
              scope="col"
              className="whitespace-nowrap px-3 py-2 text-left text-caption font-medium text-wv-secondary"
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
              totals={personTotals(person)}
              user={
                <button
                  type="button"
                  aria-expanded={isExpanded}
                  className="flex items-center gap-1 font-medium"
                  onClick={() => toggle(person.id)}
                >
                  <Icon
                    type={isExpanded ? "ChevronDown" : "ChevronRight"}
                    className="size-4 shrink-0"
                  />
                  {person.displayName === "" ? person.email : person.displayName}
                </button>
              }
              session={`${formatInteger(person.sessions.length)} sessions`}
            />,
            ...(isExpanded
              ? person.sessions.flatMap(session => [
                  <DrillDownRow
                    key={`${person.id}:${session.sessionId}`}
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
  );
}

interface DrillDownRowProps {
  aiTool?: string;
  className?: string;
  finishedAt?: number;
  session: string;
  startedAt?: number;
  totals: RouterDrillDownTotals;
  user?: React.ReactNode;
}

function DrillDownRow({
  aiTool,
  className,
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
    usageType(totals),
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
      {cells.map((cell, index) => (
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
