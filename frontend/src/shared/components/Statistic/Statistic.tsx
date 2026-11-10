"use client";

import { MetricValue } from "@weave-os/ui/components/atoms/MetricValue";
import { Skeleton } from "@weave-os/ui/components/atoms/Skeleton";
import { Text } from "@weave-os/ui/components/atoms/Text";
import { cn } from "@weave-os/ui/tools/cn";
import { LoadState } from "@weave-os/ui/tools/LoadState";
import { GRANULARITY_NAME, type TimeGranularity } from "@weave-os/ui/tools/timeGranularity";

import { type ReactNode } from "react";

import { LoadStateWrapper } from "../LoadStateWrapper";

export interface StatisticRate {
  granularity: TimeGranularity;
  rate: string;
}

export interface StatisticValue {
  /** The total output to display in the statistic. */
  total?: string;
  /** The rate of the value, shown as `rate/granularity`. */
  rate?: StatisticRate;
}

export interface StatisticProps {
  change?: ReactNode;
  className?: string;
  statistic: LoadState<StatisticValue>;
}

/** A chart card's headline value with its change pill below. */
export function Statistic({ change, className, statistic }: StatisticProps) {
  return (
    <div className={cn("flex flex-col items-start gap-0.5 empty:hidden", className)}>
      <LoadStateWrapper
        loading={
          <>
            <Skeleton.Text size="h2" className="w-16" />
            <Skeleton.Text size="xs" className="w-28" />
          </>
        }
        loadingDelayMs={0}
        state={statistic}
      >
        {({ rate, total }) => (
          <>
            <div className="flex items-baseline">
              {rate != null ? (
                <div className="flex items-baseline whitespace-nowrap">
                  <MetricValue className="text-display" value={rate.rate} />
                  <Text variant="h4" as="p" className="text-base">
                    /{GRANULARITY_NAME[rate.granularity].toLocaleLowerCase()}
                  </Text>
                </div>
              ) : (
                <MetricValue className="text-display" value={total != null ? total : "-"} />
              )}
            </div>
            <div className="flex flex-wrap items-baseline gap-x-3">{change}</div>
          </>
        )}
      </LoadStateWrapper>
    </div>
  );
}
