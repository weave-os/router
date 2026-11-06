"use client";

import { Tabs } from "@weave-os/ui/components/molecules/Tabs";

export interface ChartTabToggleOption<T extends string> {
  label: string;
  value: T;
}

export interface ChartTabToggleProps<T extends string> {
  ariaLabel: string;
  className?: string;
  onChange: (value: T) => void;
  options: readonly ChartTabToggleOption<T>[];
  value: T;
}

/** The segmented control in a chart card's corner, e.g. Volume | Share. */
export function ChartTabToggle<T extends string>({
  ariaLabel,
  className,
  onChange,
  options,
  value,
}: ChartTabToggleProps<T>) {
  return (
    <Tabs className={className} value={value} onValueChange={nextValue => onChange(nextValue as T)}>
      <Tabs.List aria-label={ariaLabel}>
        {options.map(option => (
          <Tabs.Trigger key={option.value} value={option.value}>
            {option.label}
          </Tabs.Trigger>
        ))}
      </Tabs.List>
    </Tabs>
  );
}
