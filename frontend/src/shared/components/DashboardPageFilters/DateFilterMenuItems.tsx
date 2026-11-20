import * as PopoverPrimitive from "@radix-ui/react-popover";
import { addDays, addYears, format, startOfMonth, subYears } from "date-fns";

import { useCallback, useMemo, useRef, useState } from "react";
import { type DateRange } from "react-day-picker";

import { DateScalar } from "../../objects/DateScalar";
import { cn } from "../../tools/cn";
import {
  Filter,
  formatArbitraryDateRange,
  getDateFilterTitle,
  lastNMonthsStartScalar,
  parseDateFilterSearch,
} from "../../tools/dateFilter";
import { DateFormat } from "../../tools/time";
import { TimeGranularity } from "../../tools/timeGranularity";
import { addGranularity, startOfGranularity, WeekStartsOn } from "../../tools/timeGranularity";
import { Icon } from "../atoms/Icon";
import { Button } from "../molecules/Button";
import { Calendar } from "../molecules/Calendar";
import { Command } from "../molecules/Command";
import { Appearance, Intent } from "../types";

export interface DateFilterOption {
  end?: DateScalar;
  start?: DateScalar;
  title?: string;
}

interface DateFilterMenuItemProps extends Pick<DateFilterMenuItemsProps, "onSelectDate"> {
  end?: DateScalar;
  start?: DateScalar;
  title?: string;
  weekStartsOn?: WeekStartsOn;
}

function DateFilterMenuItem({
  end,
  onSelectDate,
  start,
  title,
  weekStartsOn = 1,
}: DateFilterMenuItemProps) {
  const name = title ?? getDateFilterTitle(Filter.date(start, end), { weekStartsOn });

  // When end date is undefined, add 1 day to compensate for formatArbitraryDateRange's subtraction
  const endDate =
    end != null
      ? DateScalar.toDate(end)
      : addGranularity(DateScalar.toDate(DateScalar.today()), TimeGranularity.Day, 1);

  const description = formatArbitraryDateRange(
    DateScalar.toDate(start ?? DateScalar.today()),
    endDate,
  );

  return (
    <Command.Item
      onSelect={() => onSelectDate(start, end)}
      value={name}
      className="flex flex-col items-start gap-0.5"
    >
      <span className="truncate">{name}</span>
      <span className="text-caption text-wv-secondary">{description}</span>
    </Command.Item>
  );
}

interface CustomRangePickerProps {
  onSelectDate: (start?: DateScalar, end?: DateScalar) => void;
}

/** How far back the custom range picker allows navigating. */
const CUSTOM_RANGE_PAST_YEARS = 3;

// A function so lint keeps the clock read out of render.
function customRangePastLimit() {
  return subYears(new Date(), CUSTOM_RANGE_PAST_YEARS);
}

/** How far into the future the custom range picker allows navigating (e.g. for release dates). */
const CUSTOM_RANGE_FUTURE_YEARS = 3;

type PopoverState = "closed" | "hover" | "sticky";

function CustomRangePicker({ onSelectDate }: CustomRangePickerProps) {
  const [popoverState, setPopoverState] = useState<PopoverState>("closed");
  const [range, setRange] = useState<DateRange | undefined>(undefined);
  const closeTimeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const pastLimit = customRangePastLimit();
  const startMonth = startOfMonth(
    range?.from != null && range.from < pastLimit ? range.from : pastLimit,
  );

  const isOpen = popoverState !== "closed";
  const selectedRange =
    range?.from != null && range.to != null ? { from: range.from, to: range.to } : null;

  const isSelectingEnd = range?.from != null && range?.to == null;

  const handleDateSelect = useCallback(
    (newRange: DateRange | undefined, clickedDay: Date) => {
      // Keep the popover open while a selection is in progress
      setPopoverState("sticky");

      if (!isSelectingEnd) {
        setRange({ from: clickedDay, to: undefined });
        return;
      }

      if (newRange?.from != null && newRange?.to == null) {
        setRange({ from: newRange.from, to: undefined });
        return;
      }

      if (newRange?.from != null && newRange?.to != null) {
        setRange({ from: newRange.from, to: newRange.to });
      }
    },
    [isSelectingEnd],
  );

  const handleApply = useCallback(
    (from: Date, to: Date) => {
      // The filter's end is exclusive.
      onSelectDate(DateScalar.fromDate(from), DateScalar.fromDate(addDays(to, 1)));
      setPopoverState("closed");
      setRange(undefined);
    },
    [onSelectDate],
  );

  const handleCancel = useCallback(() => {
    setPopoverState("closed");
    setRange(undefined);
  }, []);

  // Click to toggle sticky state. Preserve an in-progress selection so a
  // re-click doesn't silently wipe it - Cancel is the way to discard.
  const handleClick = useCallback(() => {
    setPopoverState(prev => {
      if (prev === "sticky") {
        if (range?.from == null) {
          setRange(undefined);
        }
        return "hover";
      }
      return "sticky";
    });
  }, [range]);

  // Hover to show (non-sticky), cancel any pending close
  const handleMouseEnter = useCallback(() => {
    if (closeTimeoutRef.current != null) {
      clearTimeout(closeTimeoutRef.current);
      closeTimeoutRef.current = null;
    }
    setPopoverState(prev => (prev === "closed" ? "hover" : prev));
  }, []);

  // Mouse leave hides only if hovering (not sticky), with delay to allow crossing gap
  const handleMouseLeave = useCallback(() => {
    if (closeTimeoutRef.current != null) {
      clearTimeout(closeTimeoutRef.current);
    }
    closeTimeoutRef.current = setTimeout(() => {
      setPopoverState(prev => {
        if (prev === "hover") {
          setRange(undefined);
          return "closed";
        }
        return prev;
      });
      closeTimeoutRef.current = null;
    }, 100);
  }, []);

  const handleOpenChange = useCallback((open: boolean) => {
    if (!open) {
      setPopoverState("closed");
      setRange(undefined);
    }
  }, []);

  return (
    <PopoverPrimitive.Root open={isOpen} onOpenChange={handleOpenChange}>
      <PopoverPrimitive.Anchor asChild>
        <div data-popover-anchor onMouseEnter={handleMouseEnter} onMouseLeave={handleMouseLeave}>
          <Command.Item
            value="Custom range"
            className={cn(
              "flex items-center justify-between gap-2",
              popoverState === "sticky" && "bg-wv-active text-wv-primary",
            )}
            onSelect={handleClick}
          >
            <span className="truncate">Custom range</span>
            <Icon type="ChevronRight" className="size-4 text-wv-secondary" />
          </Command.Item>
        </div>
      </PopoverPrimitive.Anchor>

      <PopoverPrimitive.Portal>
        <PopoverPrimitive.Content
          side="right"
          align="start"
          sideOffset={9}
          alignOffset={-267}
          className={cn(
            "surface-float z-[100] rounded-lg border text-wv-primary shadow-md",
            "animate-in fade-in-0 slide-in-from-left-2",
          )}
          onOpenAutoFocus={e => e.preventDefault()}
          onPointerDownOutside={e => {
            // Prevent closing when clicking on the anchor
            if ((e.target as HTMLElement).closest("[data-popover-anchor]") != null) {
              e.preventDefault();
            }
          }}
          onMouseEnter={handleMouseEnter}
          onMouseLeave={handleMouseLeave}
        >
          <Calendar
            mode="range"
            selected={range}
            onSelect={handleDateSelect}
            numberOfMonths={1}
            defaultMonth={
              range?.from ??
              // oxlint-disable-next-line react/purity -- existing warning baseline
              new Date()
            }
            startMonth={startMonth}
            endMonth={addYears(
              // oxlint-disable-next-line react/purity -- existing warning baseline
              new Date(),
              CUSTOM_RANGE_FUTURE_YEARS,
            )}
          />

          <div className="flex flex-col gap-2 border-t p-3">
            <div className="flex items-center justify-between gap-4 text-caption">
              <div className="flex flex-col gap-0.5">
                <span className="text-wv-secondary">Start date</span>
                <span
                  className={cn(isSelectingEnd && "text-wv-secondary")}
                  title={
                    range?.from != null
                      ? format(range.from, DateFormat.DateTimeHumanReadable)
                      : undefined
                  }
                >
                  {range?.from != null
                    ? format(range.from, DateFormat.MonthDayYear)
                    : "Select a date"}
                </span>
              </div>
              <div className="flex flex-col gap-0.5 text-right">
                <span className="text-wv-secondary">End date</span>
                <span
                  className={cn(range?.from == null && "text-wv-secondary")}
                  title={
                    range?.to != null
                      ? format(range.to, DateFormat.DateTimeHumanReadable)
                      : undefined
                  }
                >
                  {range?.to != null ? format(range.to, DateFormat.MonthDayYear) : "Select a date"}
                </span>
              </div>
            </div>

            <div className="flex items-center justify-end gap-2">
              <Button size="sm" onClick={handleCancel}>
                Cancel
              </Button>
              <Button
                size="sm"
                appearance={Appearance.Tinted}
                intent={Intent.Primary}
                disabled={selectedRange == null}
                onClick={
                  selectedRange == null
                    ? undefined
                    : () => handleApply(selectedRange.from, selectedRange.to)
                }
              >
                Apply
              </Button>
            </div>
          </div>
        </PopoverPrimitive.Content>
      </PopoverPrimitive.Portal>
    </PopoverPrimitive.Root>
  );
}

/** This week plus open-ended presets starting on the same day 1, 2, 3, 6 and 12 months ago. */
function getDefaultDateFilters(weekStartsOn: WeekStartsOn = 1): readonly DateFilterOption[] {
  return [
    {
      start: DateScalar.fromDate(
        startOfGranularity(new Date(), TimeGranularity.Week, weekStartsOn),
      ),
    },
    { start: lastNMonthsStartScalar(1) },
    { start: lastNMonthsStartScalar(2) },
    { start: lastNMonthsStartScalar(3) },
    { start: lastNMonthsStartScalar(6) },
    { start: lastNMonthsStartScalar(12) },
  ];
}

/** Number of completed weeks offered as closed-range presets under Week granularity. */
const PREVIOUS_WEEK_PRESET_COUNT = 4;

/**
 * Generates week filter options: the current (open-ended) week followed by the previous
 * `PREVIOUS_WEEK_PRESET_COUNT` calendar weeks as closed ranges, then the default month presets.
 * Week boundaries respect the organization's configured week start.
 */
function getWeekDateFilters(weekStartsOn: WeekStartsOn): readonly DateFilterOption[] {
  const currentWeekStart = startOfGranularity(new Date(), TimeGranularity.Week, weekStartsOn);
  const [thisWeek, ...monthPresets] = getDefaultDateFilters(weekStartsOn);

  const previousWeeks: DateFilterOption[] = [];
  for (let i = 1; i <= PREVIOUS_WEEK_PRESET_COUNT; i++) {
    const weekStart = addGranularity(currentWeekStart, TimeGranularity.Week, -i);
    previousWeeks.push({
      end: DateScalar.fromDate(addGranularity(weekStart, TimeGranularity.Week, 1)),
      start: DateScalar.fromDate(weekStart),
      title: i === 1 ? "Last week" : `Week of ${format(weekStart, DateFormat.MonthDayShort)}`,
    });
  }

  return [thisWeek, ...previousWeeks, ...monthPresets];
}

function getDateFiltersForGranularity(
  granularity: TimeGranularity | undefined,
  weekStartsOn: WeekStartsOn,
): readonly DateFilterOption[] {
  switch (granularity) {
    case TimeGranularity.Week:
      return getWeekDateFilters(weekStartsOn);
    default:
      return getDefaultDateFilters(weekStartsOn);
  }
}

export interface DateFilterMenuItemsProps {
  /** Current granularity, used to generate appropriate date options. */
  granularity?: TimeGranularity;
  onSelectDate: (start?: DateScalar, end?: DateScalar) => void;
  search: string;
  weekStartsOn: WeekStartsOn;
}

/**
 * Renders items allowing a user to select a date filter.
 * Options are generated based on the current granularity:
 * - Week: Shows this week, the previous 4 calendar weeks, then the month presets
 * - Other: Shows default week/month-based options
 */
export function DateFilterMenuItems({
  granularity,
  onSelectDate,
  search,
  weekStartsOn,
}: DateFilterMenuItemsProps) {
  const parsed = parseDateFilterSearch(search, weekStartsOn);

  const options = useMemo(
    () => getDateFiltersForGranularity(granularity, weekStartsOn),
    [granularity, weekStartsOn],
  );

  return (
    <>
      <Command.Group>
        {options.map((props, i) => (
          <DateFilterMenuItem
            key={i}
            {...props}
            onSelectDate={onSelectDate}
            weekStartsOn={weekStartsOn}
          />
        ))}

        {parsed.map((result, i) => (
          <DateFilterMenuItem
            key={i}
            title={search}
            onSelectDate={onSelectDate}
            start={result.start}
            end={result.end}
            weekStartsOn={weekStartsOn}
          />
        ))}

        <Command.Separator />

        <CustomRangePicker onSelectDate={onSelectDate} />
      </Command.Group>
    </>
  );
}
