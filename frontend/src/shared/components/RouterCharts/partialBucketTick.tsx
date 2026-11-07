import { Tooltip } from "@weave-os/ui/components/molecules/Tooltip";
import { DateFormat } from "@weave-os/ui/tools/time";
import {
  addGranularity,
  formatTime,
  type TimeGranularity,
} from "@weave-os/ui/tools/timeGranularity";
import { format, isSameMonth, subDays } from "date-fns";

import { type ReactNode, useCallback } from "react";

import { type DateRange } from "../../data";

/** The days of a bucket the range covers, when the range cuts the bucket short. */
function partialBucketLabel(bucketStart: number, granularity: TimeGranularity, range: DateRange) {
  const from = Date.parse(range.from);
  const to = Date.parse(range.to);
  const bucketEnd = addGranularity(bucketStart, granularity, 1).getTime();
  if (bucketStart >= from && bucketEnd <= to) return null;
  const overlapStart = new Date(Math.max(bucketStart, from));
  const overlapEnd = subDays(Math.min(bucketEnd, to), 1);
  const start = format(overlapStart, DateFormat.MonthDayShort);
  if (overlapEnd <= overlapStart) return `This is a partial bucket for ${start}`;
  const sameMonth = isSameMonth(overlapStart, overlapEnd);
  const end = format(overlapEnd, sameMonth ? "d" : DateFormat.MonthDayShort);
  return `This is a partial bucket for ${start}–${end}`;
}

/** Renders time-axis ticks like the hosted metric charts, flagging buckets the range cuts short. */
export function usePartialBucketTick(range: DateRange, granularity: TimeGranularity) {
  return useCallback(
    (value: number): ReactNode => {
      const label = partialBucketLabel(value, granularity, range);
      const tick = (
        <span className="text-caption text-wv-secondary">{formatTime(value, granularity)}</span>
      );
      return (
        <div className="flex h-full w-full items-center justify-center">
          {label == null ? (
            tick
          ) : (
            <Tooltip content={label} delayDuration={200} interactiveChild side="top">
              <span className="cursor-help" tabIndex={0}>
                {tick}
              </span>
            </Tooltip>
          )}
        </div>
      );
    },
    [granularity, range],
  );
}
