"use client";

import { RouterDashboardSlot, RouterDashboardSlotName } from "../../slots";
import { type UseDashboardFiltersResult } from "../DashboardPageFilters";
import { ResponsiveGrid } from "../ResponsiveGrid";
import { RouterOverviewTab } from "../RouterOverviewTabs";

export interface RouterOverviewTabPanelProps {
  filters: UseDashboardFiltersResult["filters"];
  tab: RouterOverviewTab;
}

/** Only the active tab mounts, so each tab's cards fetch only while visible. */
export function RouterOverviewTabPanel({ tab }: RouterOverviewTabPanelProps) {
  switch (tab) {
    case RouterOverviewTab.Activity:
    case RouterOverviewTab.Spend:
    case RouterOverviewTab.Routing:
    case RouterOverviewTab.Models:
      return <ResponsiveGrid />;

    case RouterOverviewTab.Subscriptions:
      return (
        <ResponsiveGrid>
          <div className={ResponsiveGrid.Full}>
            <RouterDashboardSlot name={RouterDashboardSlotName.DashboardSubscriptionsTab} />
          </div>
        </ResponsiveGrid>
      );
  }
}
