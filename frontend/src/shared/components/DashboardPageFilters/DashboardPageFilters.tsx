"use client";

import { useState } from "react";

import { cn } from "../../tools/cn";
import { Icon } from "../atoms/Icon";
import { Kbd } from "../atoms/Kbd";
import { Button } from "../molecules/Button";
import { Command } from "../molecules/Command";
import { Popover } from "../molecules/Popover";
import { Tooltip } from "../molecules/Tooltip";
import { Page } from "../Page";
import { Appearance } from "../types";
import { FilterPill } from "./FilterPill";
import {
  DATE_RANGES,
  DEFAULT_RANGE_ID,
  Granularity,
  UseDashboardFiltersResult,
} from "./useDashboardFilters";

const GRANULARITY_OPTIONS: readonly Granularity[] = ["hour", "day", "week"];

enum FilterMenu {
  AddFilter = "add-filter",
  Date = "date",
  Granularity = "granularity",
}

export interface DashboardPageFiltersProps {
  className?: string;
  result: UseDashboardFiltersResult;
  right?: React.ReactNode;
}

/** Sticky dashboard filter bar: Interval pill, Date pill and the add-filter button. */
export function DashboardPageFilters({ className, result, right }: DashboardPageFiltersProps) {
  const [openMenu, setOpenMenu] = useState<FilterMenu | null>(null);
  const menuOpenChange = (menu: FilterMenu) => (open: boolean) => setOpenMenu(open ? menu : null);

  return (
    <Page.Section
      className={cn(
        "sticky top-0 z-20 flex flex-row items-start gap-4 gap-y-2 bg-gradient-to-b from-wv-surface via-wv-surface via-80% to-transparent",
        right != null && "justify-between",
        className,
      )}
    >
      <div className="flex flex-row flex-wrap items-center gap-3 gap-y-2">
        <GranularityPill
          open={openMenu === FilterMenu.Granularity}
          onOpenChange={menuOpenChange(FilterMenu.Granularity)}
          result={result}
        />
        <DatePill
          open={openMenu === FilterMenu.Date}
          onOpenChange={menuOpenChange(FilterMenu.Date)}
          result={result}
        />
        <Popover
          open={openMenu === FilterMenu.AddFilter}
          onOpenChange={menuOpenChange(FilterMenu.AddFilter)}
        >
          <Popover.Trigger>
            <Button
              size="icon"
              appearance={Appearance.Hollow}
              className="size-9 shrink-0"
              aria-label="Add filter"
            >
              <Icon type="Plus" />
            </Button>
          </Popover.Trigger>
          <Popover.Content className="w-64 p-1" align="start">
            <Command>
              <Command.Input placeholder="Filter by..." />
              <Command.List>
                <Command.Item onSelect={() => setOpenMenu(FilterMenu.Granularity)}>
                  <Icon type="Clock" />
                  Interval
                </Command.Item>
                <Command.Item onSelect={() => setOpenMenu(FilterMenu.Date)}>
                  <Icon type="Calendar" />
                  Date
                </Command.Item>
              </Command.List>
            </Command>
          </Popover.Content>
        </Popover>
      </div>

      {right}
    </Page.Section>
  );
}

interface FilterPillMenuProps {
  onOpenChange: (open: boolean) => void;
  open: boolean;
  result: UseDashboardFiltersResult;
}

function CheckSlot({ checked }: { checked: boolean }) {
  return (
    <div className="flex h-4 w-4 items-center justify-center">
      {checked && <Icon type="Check" />}
    </div>
  );
}

function ShortcutTooltip({
  children,
  label,
  shortcut,
}: React.PropsWithChildren<{ label: string; shortcut: string }>) {
  return (
    <Tooltip
      content={
        <span className="flex items-center gap-1.5">
          {label}{" "}
          <Kbd inverted size="sm">
            {shortcut}
          </Kbd>
        </span>
      }
      interactiveChild
    >
      <span className="inline-flex">{children}</span>
    </Tooltip>
  );
}

function GranularityPill({ onOpenChange, open, result }: FilterPillMenuProps) {
  const current = result.filters.granularity;
  return (
    <ShortcutTooltip label="Change granularity" shortcut="G">
      <FilterPill>
        <FilterPill.Label>Interval</FilterPill.Label>
        <Popover open={open} onOpenChange={onOpenChange}>
          <Popover.Trigger>
            <FilterPill.Button>{current}</FilterPill.Button>
          </Popover.Trigger>
          <Popover.Content className="w-64 p-1" align="start">
            <Command>
              <Command.Input placeholder="Search granularity..." />
              <Command.List>
                {GRANULARITY_OPTIONS.map(option => (
                  <Command.Item
                    key={option}
                    onSelect={() => {
                      result.setGranularity(option);
                      onOpenChange(false);
                    }}
                  >
                    <div className="flex items-center gap-2">
                      <CheckSlot checked={current === option} /> {option}
                    </div>
                  </Command.Item>
                ))}
              </Command.List>
            </Command>
          </Popover.Content>
        </Popover>
      </FilterPill>
    </ShortcutTooltip>
  );
}

function DatePill({ onOpenChange, open, result }: FilterPillMenuProps) {
  const currentId = result.filters.range.id;
  return (
    <ShortcutTooltip label="Change date" shortcut="D">
      <FilterPill onRemove={() => result.setRangeId(DEFAULT_RANGE_ID)}>
        <FilterPill.Label>Date</FilterPill.Label>
        <Popover open={open} onOpenChange={onOpenChange}>
          <Popover.Trigger>
            <FilterPill.Button>
              {result.filters.range.pillLabel?.(new Date(result.filters.fromISO)) ??
                result.filters.range.label.toLowerCase()}
            </FilterPill.Button>
          </Popover.Trigger>
          <Popover.Content className="w-64 p-1" align="start">
            <Command>
              <Command.Input placeholder="Search date range..." />
              <Command.List>
                {DATE_RANGES.map(range => (
                  <Command.Item
                    key={range.id}
                    value={range.label}
                    onSelect={() => {
                      result.setRangeId(range.id);
                      onOpenChange(false);
                    }}
                  >
                    <div className="flex items-center gap-2">
                      <CheckSlot checked={currentId === range.id} /> {range.label}
                    </div>
                  </Command.Item>
                ))}
              </Command.List>
            </Command>
          </Popover.Content>
        </Popover>
      </FilterPill>
    </ShortcutTooltip>
  );
}
