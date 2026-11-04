"use client";

import { usePathname, useSearchParams } from "next/navigation";

import { ReportSectionTabs } from "../ReportSectionTabs";
import {
  parseRouterOverviewTab,
  ROUTER_OVERVIEW_BASE_TABS,
  ROUTER_OVERVIEW_TAB_LABEL,
  ROUTER_OVERVIEW_TAB_PARAM,
  RouterOverviewTab,
} from "./routerOverviewTab";

export function useRouterOverviewTab(): RouterOverviewTab {
  return parseRouterOverviewTab(useSearchParams().get(ROUTER_OVERVIEW_TAB_PARAM));
}

export interface RouterOverviewTabsProps {
  showSubscriptions: boolean;
}

export function RouterOverviewTabs({ showSubscriptions }: RouterOverviewTabsProps) {
  const pathname = usePathname();
  const searchParams = useSearchParams();
  const activeTab = parseRouterOverviewTab(searchParams.get(ROUTER_OVERVIEW_TAB_PARAM));
  const tabs = showSubscriptions
    ? [...ROUTER_OVERVIEW_BASE_TABS, RouterOverviewTab.Subscriptions]
    : ROUTER_OVERVIEW_BASE_TABS;

  const items = tabs.map(tab => {
    const tabParams = new URLSearchParams(searchParams.toString());
    tabParams.set(ROUTER_OVERVIEW_TAB_PARAM, tab);
    return {
      href: `${pathname}?${tabParams.toString()}`,
      isActive: activeTab === tab,
      label: ROUTER_OVERVIEW_TAB_LABEL[tab],
    };
  });

  return <ReportSectionTabs ariaLabel="Router sections" items={items} />;
}
