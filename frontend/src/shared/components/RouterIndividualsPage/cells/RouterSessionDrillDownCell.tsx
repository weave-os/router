"use client";

import { Button } from "@weave-os/ui/components/molecules/Button";
import { Tooltip } from "@weave-os/ui/components/molecules/Tooltip";
import { Appearance } from "@weave-os/ui/components/types";

import { type ReactNode } from "react";

import {
  type DateRange,
  RouterMetricGroup,
  RouterMetricType,
  useRouterDrillDown,
} from "../../../data";
import { type RouterIndividualRow, RouterIndividualRowKind } from "../types";

interface RouterSessionDrillDownCellProps {
  children: ReactNode;
  range: DateRange;
  row: RouterIndividualRow;
  /** Shown on hover or focus; the cell itself is the trigger, so it needs no focus stop of its own. */
  tooltip?: ReactNode;
}

/** Every column of a person's row opens the same view of their router sessions. */
export function RouterSessionDrillDownCell({
  children,
  range,
  row,
  tooltip,
}: RouterSessionDrillDownCellProps) {
  const { drillDownModal, openDrillDown } = useRouterDrillDown({
    granularity: null,
    group: RouterMetricGroup.Person,
    range,
    type: RouterMetricType.ModelSpend,
  });
  const isPerson = row.kind === RouterIndividualRowKind.Person;

  const trigger =
    isPerson && openDrillDown != null ? (
      <Button
        appearance={Appearance.Hollow}
        className="flex w-full min-w-0 items-center p-0 font-normal"
        onClick={() => openDrillDown({ key: row.id })}
      >
        {children}
        <span className="sr-only">Open {row.name}&apos;s router sessions</span>
      </Button>
    ) : (
      <div className="flex h-full w-full min-w-0 items-center">{children}</div>
    );

  return (
    <div className="-mx-4 flex h-full w-[calc(100%+2rem)] min-w-0 items-center">
      {tooltip == null ? (
        trigger
      ) : (
        <Tooltip content={tooltip} interactiveChild>
          {trigger}
        </Tooltip>
      )}
      {drillDownModal}
    </div>
  );
}
