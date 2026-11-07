"use client";

import { cn } from "@weave-os/ui/tools/cn";

import { usePathname } from "next/navigation";

import { type ReactNode, type Ref, useLayoutEffect, useRef } from "react";

import styles from "./RouterSidebarSwap.module.css";

export enum RouterSidebarPanel {
  Main = "main",
  Settings = "settings",
}

interface RouterSidebarSwapProps {
  main: ReactNode;
  settings: ReactNode;
  settingsPath: string;
}

function isSettingsPath(pathname: string, settingsPath: string) {
  return pathname === settingsPath || pathname.startsWith(`${settingsPath}/`);
}

/**
 * Swaps the whole router sidebar for the settings sidebar under `/settings`,
 * pushing one panel out as the other slides in. Both panels stay mounted so a
 * quick back-and-forth reverses from wherever the slide has reached.
 */
export function RouterSidebarSwap({ main, settings, settingsPath }: RouterSidebarSwapProps) {
  const activePanel = isSettingsPath(usePathname(), settingsPath)
    ? RouterSidebarPanel.Settings
    : RouterSidebarPanel.Main;
  const mainPanelRef = useRef<HTMLDivElement>(null);
  const settingsPanelRef = useRef<HTMLDivElement>(null);

  // A layout effect runs before the browser blurs the control that just went
  // inert, so a keyboard user who toggled from inside the outgoing panel can be
  // handed to the page they landed on instead of being dropped to <body>.
  useLayoutEffect(() => {
    const [incomingPanel, outgoingPanel] =
      activePanel === RouterSidebarPanel.Settings
        ? [settingsPanelRef.current!, mainPanelRef.current!]
        : [mainPanelRef.current!, settingsPanelRef.current!];
    if (!outgoingPanel.contains(document.activeElement)) return;

    const focusTarget =
      incomingPanel.querySelector<HTMLElement>('[aria-current="page"]') ??
      incomingPanel.querySelector<HTMLElement>("a[href], button");
    // The incoming panel is still off-track; scrolling it into view would yank
    // the overflow-hidden track and break the slide.
    focusTarget?.focus({ preventScroll: true });
  }, [activePanel]);

  return (
    <div
      className={cn("relative h-full w-12 shrink-0 overflow-hidden sm:w-64", styles.track)}
      data-active-panel={activePanel}
    >
      <SidebarPanel
        ref={mainPanelRef}
        className={styles.mainPanel}
        inactive={activePanel !== RouterSidebarPanel.Main}
      >
        {main}
      </SidebarPanel>
      <SidebarPanel
        ref={settingsPanelRef}
        className={styles.settingsPanel}
        inactive={activePanel !== RouterSidebarPanel.Settings}
      >
        {settings}
      </SidebarPanel>
    </div>
  );
}

interface SidebarPanelProps {
  children: ReactNode;
  className: string;
  inactive: boolean;
  ref: Ref<HTMLDivElement>;
}

function SidebarPanel({ children, className, inactive, ref }: SidebarPanelProps) {
  return (
    <div
      ref={ref}
      className={cn("absolute inset-0", className)}
      inert={inactive}
      aria-hidden={inactive}
    >
      {children}
    </div>
  );
}
