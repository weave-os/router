"use client";

import { useState } from "react";

import { useRouterDashboardDataSource } from "../../data";
import {
  RouterDashboardSlot,
  RouterDashboardSlotName,
  useHasRouterDashboardSlot,
} from "../../slots";
import { cn } from "../../tools/cn";
import { getDateFilterTitle } from "../../tools/dateFilter";
import { TimeGranularity } from "../../tools/timeGranularity";
import { Icon } from "../atoms/Icon";
import { Kbd } from "../atoms/Kbd";
import { Button } from "../molecules/Button";
import { Command } from "../molecules/Command";
import { Popover } from "../molecules/Popover";
import { Tooltip } from "../molecules/Tooltip";
import { Page } from "../Page";
import { Appearance } from "../types";
import { DateFilterMenuItems } from "./DateFilterMenuItems";
import { FilterPill } from "./FilterPill";
import { isDefaultDateFilter, UseDashboardFiltersResult } from "./useDashboardFilters";

const DATE_SEARCH_HINT = `(try "this year")`;

enum FilterMenu {
  AddFilter = "add-filter",
  Date = "date",
  Granularity = "granularity",
}

/** A host-controlled Date menu, so host shortcuts can open it. */
export interface DashboardDateMenuControl {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

export interface DashboardPageFiltersProps {
  className?: string;
  dateMenu?: DashboardDateMenuControl;
  /** Hides the Interval pill on pages that pick their own buckets. */
  hideGranularity?: boolean;
  result: UseDashboardFiltersResult;
  right?: React.ReactNode;
}

/** Sticky dashboard filter bar: Interval pill, Date pill and the add-filter button. */
export function DashboardPageFilters({
  className,
  dateMenu,
  hideGranularity = false,
  result,
  right,
}: DashboardPageFiltersProps) {
  const [openMenu, setOpenMenu] = useState<FilterMenu | null>(null);
  const menuOpenChange = (menu: FilterMenu) => (open: boolean) => setOpenMenu(open ? menu : null);
  const hasHostAddFilter = useHasRouterDashboardSlot(RouterDashboardSlotName.DashboardAddFilter);

  return (
    <Page.Section
      className={cn(
        "sticky top-0 z-20 flex flex-row items-start gap-4 gap-y-2 bg-gradient-to-b from-wv-surface via-wv-surface via-80% to-transparent",
        right != null && "justify-between",
        className,
      )}
    >
      <div className="flex flex-row flex-wrap items-center gap-3 gap-y-2">
        {!hideGranularity && (
          <GranularityPill
            open={openMenu === FilterMenu.Granularity}
            onOpenChange={menuOpenChange(FilterMenu.Granularity)}
            result={result}
          />
        )}
        <DatePill
          open={dateMenu?.open ?? openMenu === FilterMenu.Date}
          onOpenChange={open => {
            menuOpenChange(FilterMenu.Date)(open);
            dateMenu?.onOpenChange(open);
          }}
          result={result}
        />
        <RouterDashboardSlot name={RouterDashboardSlotName.DashboardFilterPills} />
        {hasHostAddFilter ? (
          <RouterDashboardSlot name={RouterDashboardSlotName.DashboardAddFilter} />
        ) : (
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
                  {!hideGranularity && (
                    <Command.Item onSelect={() => setOpenMenu(FilterMenu.Granularity)}>
                      <Icon type="Clock" />
                      Interval
                    </Command.Item>
                  )}
                  <Command.Item onSelect={() => setOpenMenu(FilterMenu.Date)}>
                    <Icon type="Calendar" />
                    Date
                  </Command.Item>
                </Command.List>
              </Command>
            </Popover.Content>
          </Popover>
        )}
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
  const currentGranularity = result.filters.granularity;
  return (
    <ShortcutTooltip label="Change granularity" shortcut="G">
      <FilterPill>
        <FilterPill.Label>Interval</FilterPill.Label>
        <Popover open={open} onOpenChange={onOpenChange}>
          <Popover.Trigger>
            <FilterPill.Button>{currentGranularity}</FilterPill.Button>
          </Popover.Trigger>
          <Popover.Content className="w-64 p-1" align="start">
            <Command>
              <Command.Input placeholder="Search granularity..." />
              <Command.List>
                {result.granularityOptions.map(option => (
                  <Command.Item
                    key={option}
                    onSelect={() => {
                      result.setGranularity(option);
                      onOpenChange(false);
                    }}
                  >
                    <div className="flex items-center gap-2">
                      <CheckSlot checked={currentGranularity === option} /> {option}
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
  const { weekStartsOn } = useRouterDashboardDataSource();
  const [search, setSearch] = useState("");
  const { dateFilter, granularity } = result.filters;

  return (
    <ShortcutTooltip label="Change date" shortcut="D">
      <FilterPill
        onRemove={() => {
          if (!isDefaultDateFilter(dateFilter)) result.resetDateFilter();
        }}
      >
        <FilterPill.Label>Date</FilterPill.Label>
        <Popover
          open={open}
          onOpenChange={nextOpen => {
            if (nextOpen) setSearch("");
            onOpenChange(nextOpen);
          }}
        >
          <Popover.Trigger>
            <FilterPill.Button>
              {getDateFilterTitle(dateFilter, { lowercase: true, weekStartsOn })}
            </FilterPill.Button>
          </Popover.Trigger>
          <Popover.Content className="w-64 p-0" align="start">
            <Command>
              <Command.Input
                value={search}
                onValueChange={setSearch}
                placeholder={DATE_SEARCH_HINT}
              />
              <Command.List>
                <DateFilterMenuItems
                  granularity={granularity === "week" ? TimeGranularity.Week : undefined}
                  search={search}
                  weekStartsOn={weekStartsOn}
                  onSelectDate={(start, end) => {
                    result.setDateFilter(start, end);
                    onOpenChange(false);
                  }}
                />
              </Command.List>
            </Command>
          </Popover.Content>
        </Popover>
      </FilterPill>
    </ShortcutTooltip>
  );
}
