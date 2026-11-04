"use client";

import { createContext, useContext } from "react";

/**
 * Places where a host may inject its own UI (e.g. hosted credits, plan and subscription panels).
 * Shared pages render whatever the host supplies and nothing otherwise.
 */
export enum RouterDashboardSlotName {
  DashboardHeaderActions = "DashboardHeaderActions",
  DashboardBanner = "DashboardBanner",
  DashboardAside = "DashboardAside",
  /** Panel for the hosted-only Subscriptions tab; the tab is hidden when the host leaves it empty. */
  DashboardSubscriptionsTab = "DashboardSubscriptionsTab",
  IndividualsHeaderActions = "IndividualsHeaderActions",
  SettingsSections = "SettingsSections",
  SidebarFooter = "SidebarFooter",
}

export type RouterDashboardSlots = Partial<Record<RouterDashboardSlotName, React.ReactNode>>;

const RouterDashboardSlotsContext = createContext<RouterDashboardSlots>({});

export interface RouterDashboardSlotsProviderProps extends React.PropsWithChildren {
  slots: RouterDashboardSlots;
}

export function RouterDashboardSlotsProvider({
  children,
  slots,
}: RouterDashboardSlotsProviderProps) {
  return <RouterDashboardSlotsContext value={slots}>{children}</RouterDashboardSlotsContext>;
}

export interface RouterDashboardSlotProps {
  name: RouterDashboardSlotName;
  /** Rendered when the host leaves the slot empty. */
  fallback?: React.ReactNode;
}

export function RouterDashboardSlot({ fallback = null, name }: RouterDashboardSlotProps) {
  const content = useContext(RouterDashboardSlotsContext)[name];
  return <>{content ?? fallback}</>;
}

export function useHasRouterDashboardSlot(name: RouterDashboardSlotName): boolean {
  return useContext(RouterDashboardSlotsContext)[name] != null;
}
