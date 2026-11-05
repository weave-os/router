"use client";

import { subYears } from "date-fns";

import { DayPicker, MonthCaptionProps, useDayPicker } from "react-day-picker";

import { cn } from "../../../tools/cn";
import { Icon } from "../../atoms/Icon";

export type CalendarProps = React.ComponentProps<typeof DayPicker>;

const threeYearsAgo = subYears(new Date(), 3);

/** Year range derived from calendar navigation bounds so the dropdown only shows navigable years. */
function getYearRangeFromBounds(startMonth = threeYearsAgo, endMonth = new Date()) {
  return { endYear: endMonth.getFullYear(), startYear: startMonth.getFullYear() };
}

interface DropdownOption {
  label: string;
  value: number;
}

interface DropdownProps {
  onChange: (e: React.ChangeEvent<HTMLSelectElement>) => void;
  options: DropdownOption[];
  value: number;
}

function Dropdown({ onChange, options, value }: DropdownProps) {
  return (
    <div className="relative inline-flex items-center">
      <select
        value={value}
        onChange={onChange}
        className="cursor-pointer appearance-none rounded-md border border-wv-default bg-wv-inset py-1 pl-2 pr-6 text-body text-wv-primary hover:border-wv-hover focus:border-wv-focus focus:outline-none focus:ring-1 focus:ring-wv-focus"
      >
        {options.map(option => (
          <option key={option.value} value={option.value}>
            {option.label}
          </option>
        ))}
      </select>
      <Icon
        type="ChevronDown"
        className="pointer-events-none absolute right-1.5 size-3 text-wv-tertiary"
      />
    </div>
  );
}

function MonthCaption({ calendarMonth }: MonthCaptionProps) {
  const { dayPickerProps, goToMonth, months, nextMonth, previousMonth } = useDayPicker();

  const monthOptions: DropdownOption[] = Array.from({ length: 12 }, (_, i) => ({
    label: new Date(2000, i).toLocaleString("default", { month: "short" }),
    value: i,
  }));

  const currentYear = calendarMonth.date.getFullYear();
  const { endYear, startYear } = getYearRangeFromBounds(
    dayPickerProps.startMonth,
    dayPickerProps.endMonth,
  );
  const yearOptions: DropdownOption[] = Array.from({ length: endYear - startYear + 1 }, (_, i) => ({
    label: String(startYear + i),
    value: startYear + i,
  }));

  const handleMonthChange = (e: React.ChangeEvent<HTMLSelectElement>) => {
    const newMonth = new Date(calendarMonth.date);
    newMonth.setMonth(Number(e.target.value));
    goToMonth(newMonth);
  };

  const handleYearChange = (e: React.ChangeEvent<HTMLSelectElement>) => {
    const newMonth = new Date(calendarMonth.date);
    newMonth.setFullYear(Number(e.target.value));
    goToMonth(newMonth);
  };

  const isFirstMonth = months[0].date.getTime() === calendarMonth.date.getTime();

  return (
    <div className="flex h-10 items-center justify-between">
      <button
        type="button"
        disabled={previousMonth == null}
        onClick={() => previousMonth != null && goToMonth(previousMonth)}
        className={cn(
          "flex size-7 items-center justify-center rounded-md border border-wv-default bg-wv-inset p-0 text-wv-secondary hover:border-wv-hover hover:bg-wv-hover hover:text-wv-primary focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-wv-focus disabled:pointer-events-none disabled:opacity-25",
          !isFirstMonth && "invisible",
        )}
      >
        <Icon type="ChevronLeft" className="size-4" />
      </button>
      <div className="flex items-center gap-2">
        <Dropdown
          options={monthOptions}
          value={calendarMonth.date.getMonth()}
          onChange={handleMonthChange}
        />
        <Dropdown onChange={handleYearChange} options={yearOptions} value={currentYear} />
      </div>
      <button
        type="button"
        disabled={nextMonth == null}
        onClick={() => nextMonth != null && goToMonth(nextMonth)}
        className={cn(
          "flex size-7 items-center justify-center rounded-md border border-wv-default bg-wv-inset p-0 text-wv-secondary hover:border-wv-hover hover:bg-wv-hover hover:text-wv-primary focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-wv-focus disabled:pointer-events-none disabled:opacity-25",
          !isFirstMonth && "invisible",
        )}
      >
        <Icon type="ChevronRight" className="size-4" />
      </button>
    </div>
  );
}

/**
 * A calendar component built on top of react-day-picker.
 *
 * Default startMonth is 3 years ago; prefer passing startMonth from
 * useCalendarStartMonth so the year dropdown floors to org earliest activity.
 *
 * @example
 * <Calendar mode="single" selected={date} onSelect={setDate} />
 *
 * @example
 * <Calendar mode="range" selected={range} onSelect={setRange} captionLayout="dropdown" />
 */
export function Calendar({
  className,
  classNames,
  // oxlint-disable-next-line react/purity -- existing warning baseline
  endMonth = new Date(),
  startMonth = threeYearsAgo,
  ...props
}: CalendarProps) {
  return (
    <DayPicker
      className={cn("p-3", className)}
      hideNavigation
      startMonth={startMonth}
      endMonth={endMonth}
      classNames={{
        day: cn(
          "group relative size-8 rounded-md p-0 text-center text-body focus-within:relative focus-within:z-20 first:[&:has([aria-selected])]:rounded-l-md last:[&:has([aria-selected])]:rounded-r-md [&:has([aria-selected].day-range-end)]:rounded-r-md",
        ),
        day_button: cn(
          "inline-flex size-8 items-center justify-center rounded-md p-0 text-body font-normal transition-colors focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-wv-focus disabled:pointer-events-none disabled:opacity-50",
          "hover:bg-wv-hover hover:text-wv-primary",
          "group-[.day-selected]:hover:!bg-brand group-[.day-selected]:hover:!text-wv-on-action",
          "aria-selected:bg-brand aria-selected:text-wv-on-action",
        ),
        disabled: "text-wv-disabled opacity-50",
        footer: "hidden",
        hidden: "invisible",
        month: "flex flex-col gap-4",
        month_grid: "w-full border-collapse",
        months: "flex flex-col gap-4 sm:flex-row",
        outside:
          "day-outside text-wv-tertiary aria-selected:bg-wv-selected aria-selected:text-wv-secondary",
        range_end: "day-range-end",
        range_middle: "aria-selected:bg-wv-selected aria-selected:text-wv-primary",
        range_start: "day-range-start",
        selected:
          "day-selected bg-brand text-wv-on-action hover:bg-brand hover:text-wv-on-action focus:bg-brand focus:text-wv-on-action",
        today: "bg-wv-active text-wv-primary",
        week: "mt-2 flex w-full",
        weekday: "w-8 text-caption font-normal text-wv-secondary",
        weekdays: "flex",
        ...classNames,
      }}
      components={{
        MonthCaption,
        ...props.components,
      }}
      {...props}
    />
  );
}
