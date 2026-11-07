"use client";

import { ReportSectionTabs } from "@weave-os/ui/components/ReportSectionTabs";
import { URLParam } from "@weave-os/ui/tools/shareUrl";

import { usePathname, useSearchParams } from "next/navigation";

import {
  resolveRouterOverviewTab,
  ROUTER_OVERVIEW_BASE_TABS,
  ROUTER_OVERVIEW_TAB_LABEL,
  ROUTER_OVERVIEW_TAB_PARAM,
  RouterOverviewTab,
} from "./routerOverviewTab";

export function useRouterOverviewTab(showSubscriptions: boolean): RouterOverviewTab {
  const searchParams = useSearchParams();
  return resolveRouterOverviewTab(
    searchParams.get(ROUTER_OVERVIEW_TAB_PARAM),
    searchParams.get(URLParam.Chart),
    showSubscriptions,
  );
}

export interface RouterOverviewTabsProps {
  showSubscriptions: boolean;
}

export function RouterOverviewTabs({ showSubscriptions }: RouterOverviewTabsProps) {
  const pathname = usePathname();
  const searchParams = useSearchParams();
  const activeTab = useRouterOverviewTab(showSubscriptions);
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
