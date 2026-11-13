"use client";

import * as TabsPrimitive from "@radix-ui/react-tabs";

import { forwardRef, useCallback } from "react";

import { cn } from "../../../tools/cn";
import styles from "./TabsMotion.module.css";
import { useActiveTabIndicator } from "./useActiveTabIndicator";

/**
 * Switches panels in place on the same page; the selection is component state.
 * For views addressed by the URL (shareable, Back-navigable), use `ViewSwitcher`.
 */
const Tabs = TabsPrimitive.Root;

export const tabsListClassName = cn(
  styles.tray,
  "tabs-tray surface-inset inline-flex items-center justify-center gap-0.5 rounded-lg p-0.5 text-wv-secondary",
);

/**
 * Active styling keys off `data-state="active"`, which non-Radix callers set themselves. A tray
 * using `useActiveTabIndicator` paints that fill as one pill sliding between triggers instead.
 */
export const tabsTriggerClassName = cn(
  styles.trigger,
  "inline-flex h-7 items-center justify-center whitespace-nowrap rounded-md px-2.5 text-caption ring-offset-wv-surface hover:bg-wv-hover hover:text-wv-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-wv-focus focus-visible:ring-offset-2 active:bg-wv-active disabled:pointer-events-none disabled:opacity-50 data-[state=active]:bg-wv-tab-active data-[state=active]:text-wv-primary data-[state=active]:shadow-sm [@media(pointer:coarse)]:min-h-11",
);

interface TabsListProps extends React.ComponentPropsWithoutRef<typeof TabsPrimitive.List> {
  /** Turn off for lists that restyle their triggers away from the tray's segmented look. */
  slidingIndicator?: boolean;
}

const TabsList = forwardRef<React.ElementRef<typeof TabsPrimitive.List>, TabsListProps>(
  ({ className, slidingIndicator = true, ...props }, forwardedRef) => {
    const trayRef = useActiveTabIndicator<HTMLDivElement>(slidingIndicator);
    const setTray = useCallback(
      (tray: HTMLDivElement | null) => {
        trayRef.current = tray;
        if (typeof forwardedRef === "function") {
          return (forwardedRef as React.RefCallback<HTMLDivElement>)(tray);
        }
        if (forwardedRef !== null) {
          forwardedRef.current = tray;
        }
      },
      [trayRef, forwardedRef],
    );
    return (
      <TabsPrimitive.List ref={setTray} className={cn(tabsListClassName, className)} {...props} />
    );
  },
);
TabsList.displayName = TabsPrimitive.List.displayName;

const TabsTrigger = forwardRef<
  React.ElementRef<typeof TabsPrimitive.Trigger>,
  React.ComponentPropsWithoutRef<typeof TabsPrimitive.Trigger>
>(({ className, ...props }, ref) => (
  <TabsPrimitive.Trigger ref={ref} className={cn(tabsTriggerClassName, className)} {...props} />
));
TabsTrigger.displayName = TabsPrimitive.Trigger.displayName;

const TabsContent = forwardRef<
  React.ElementRef<typeof TabsPrimitive.Content>,
  React.ComponentPropsWithoutRef<typeof TabsPrimitive.Content>
>(({ className, ...props }, ref) => (
  <TabsPrimitive.Content
    ref={ref}
    className={cn(
      "mt-2 ring-offset-wv-surface focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-wv-focus focus-visible:ring-offset-2",
      className,
    )}
    {...props}
  />
));
TabsContent.displayName = TabsPrimitive.Content.displayName;

const TabsExport = Object.assign(Tabs, {
  Content: TabsContent,
  List: TabsList,
  Trigger: TabsTrigger,
});

export { TabsExport as Tabs };
