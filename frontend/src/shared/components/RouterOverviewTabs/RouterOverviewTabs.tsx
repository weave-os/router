"use client";

import { usePathname, useSearchParams } from "next/navigation";

import { URLParam } from "../../tools/shareUrl";
import { ReportSectionTabs } from "../ReportSectionTabs";
import {
  resolveRouterOverviewTab,
  ROUTER_OVERVIEW_BASE_TABS,
  ROUTER_OVERVIEW_TAB_LABEL,
  ROUTER_OVERVIEW_TAB_PARAM,
  RouterOverviewTab,
} from "./routerOverviewTab";

export function useRouterOverviewTab(): RouterOverviewTab {
  const searchParams = useSearchParams();
  return resolveRouterOverviewTab(
    searchParams.get(ROUTER_OVERVIEW_TAB_PARAM),
    searchParams.get(URLParam.Chart),
  );
}

export interface RouterOverviewTabsProps {
  showSubscriptions: boolean;
}

export function RouterOverviewTabs({ showSubscriptions }: RouterOverviewTabsProps) {
  const pathname = usePathname();
  const searchParams = useSearchParams();
  const activeTab = useRouterOverviewTab();
  const tabs = showSubscriptions
    ? [...ROUTER_OVERVIEW_BASE_TABS, RouterOverviewTab.Subscriptions]
    : ROUTER_OVERVIEW_BASE_TABS;

  const items = tabs.map(tab => {
    const tabParams = new URLSearchParams(searchParams.toString());
    tabParams.set(ROUTER_OVERVIEW_TAB_PARAM, tab);
    tabParams.delete(URLParam.Chart);
    return {
      href: `${pathname}?${tabParams.toString()}`,
      isActive: activeTab === tab,
      label: ROUTER_OVERVIEW_TAB_LABEL[tab],
    };
  });

  return <ReportSectionTabs ariaLabel="Router sections" items={items} />;
}
