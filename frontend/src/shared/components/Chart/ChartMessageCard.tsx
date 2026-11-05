import React from "react";

import { cn } from "../../tools/cn";
import { Icon } from "../atoms/Icon";
import { Card, CardProps } from "../molecules/Card";
import { Intent } from "../types";

export interface ChartMessageCardProps extends CardProps {
  /**
   * If true, renders an overlay over the chart below.
   *
   * @default false
   */
  overlay?: boolean;
}

export function ChartMessageCard({ className, overlay = false, ...props }: ChartMessageCardProps) {
  return (
    <>
      {overlay && (
        <div className="pointer-events-none absolute inset-0 z-10 animate-fade-in">
          <div className="h-full w-full bg-wv-surface opacity-60" />
        </div>
      )}

      <Card
        className={cn("absolute-center z-10 max-h-full min-w-[60%] overflow-auto", className)}
        size="sm"
        {...props}
      />
    </>
  );
}

interface ChartMessageCardEmptyProps {
  emptyMessage?: React.ReactNode;
}

ChartMessageCard.Empty = function ChartMessageCardEmpty({
  emptyMessage,
}: ChartMessageCardEmptyProps) {
  return (
    <Card
      className="absolute inset-0 z-10 items-center justify-center gap-y-3 overflow-auto text-balance border-0 bg-wv-status-neutral-subtle text-center shadow-none"
      size="sm"
    >
      <div className="flex size-10 shrink-0 items-center justify-center rounded-full border bg-wv-surface">
        <Icon type="BarChart" className="size-5 text-wv-secondary" />
      </div>
      {React.Children.count(emptyMessage) > 0 ? (
        emptyMessage
      ) : (
        <Card.Header className="text-center">
          <Card.Title>No data yet</Card.Title>
        </Card.Header>
      )}
    </Card>
  );
};

interface ChartMessageCardLoadingProps {
  overlay?: boolean;
}

ChartMessageCard.Loading = function ChartMessageCardLoading({
  overlay = true,
}: ChartMessageCardLoadingProps) {
  return (
    <ChartMessageCard className="w-min min-w-0 rounded-full p-2" overlay={overlay}>
      <Card.Header className="text-center">
        <Icon type="Loading" className="size-6 animate-spin" />
      </Card.Header>
    </ChartMessageCard>
  );
};

interface ChartMessageCardErrorProps {
  error: Error;
}

ChartMessageCard.Error = function ChartMessageCardError({ error }: ChartMessageCardErrorProps) {
  return (
    <ChartMessageCard
      icon={<Icon type="TriangleAlert" className="size-5" />}
      intent={Intent.Danger}
    >
      <Card.Header>
        <Card.Title>Could not load data</Card.Title>
        <Card.Description>Error: {error.message}</Card.Description>
      </Card.Header>
    </ChartMessageCard>
  );
};
