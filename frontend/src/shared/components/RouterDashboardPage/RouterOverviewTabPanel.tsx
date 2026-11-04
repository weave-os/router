"use client";

import { useMemo } from "react";

import { ChartFillPattern, ChartSeriesType } from "../Chart";
import { RouterMetricGroup, RouterMetricType } from "../../data";
import { RouterDashboardSlot, RouterDashboardSlotName } from "../../slots";
import { type UseDashboardFiltersResult } from "../DashboardPageFilters";
import { ResponsiveGrid } from "../ResponsiveGrid";
import {
  RouterAdoptionCard,
  RouterDailyRequestsHeatmap,
  RouterInactiveCard,
  RouterRequestsPerActiveUserChart,
} from "../RouterActivity";
import { RouterGroupedChartCard } from "../RouterCharts/RouterGroupedChartCard";
import {
  ROUTER_BILLING_SERIES_CONFIGS,
  ROUTER_DECISION_TYPE_SERIES_CONFIGS,
  ROUTER_USER_LINK_SERIES_CONFIGS,
} from "../RouterCharts/routerSeriesConfigs";
import {
  ACTIVITY_SUMMARY_CARDS,
  RouterDashboardStats,
  SPEND_SUMMARY_CARDS,
} from "../RouterDashboardStats";
import { RouterOverviewTab } from "../RouterOverviewTabs";

export interface RouterOverviewTabPanelProps {
  filters: UseDashboardFiltersResult["filters"];
  methodologyHref?: string;
  tab: RouterOverviewTab;
}

/** Only the active tab mounts, so each tab's cards fetch only while visible. */
export function RouterOverviewTabPanel({ filters, methodologyHref, tab }: RouterOverviewTabPanelProps) {
  const { fromISO, granularity, toISO } = filters;
  const range = useMemo(() => ({ from: fromISO, to: toISO }), [fromISO, toISO]);

  switch (tab) {
    case RouterOverviewTab.Activity:
      return (
        <ResponsiveGrid>
          <div className={`${ResponsiveGrid.Full} grid grid-cols-1 gap-2.5 @4xl:grid-cols-2`}>
            <RouterDashboardStats cards={ACTIVITY_SUMMARY_CARDS} range={range} />
            <RouterAdoptionCard />
          </div>
          <RouterDailyRequestsHeatmap className={ResponsiveGrid.Medium} range={range} />
          <RouterGroupedChartCard
            className={ResponsiveGrid.Medium}
            fillPattern={ChartFillPattern.Dots}
            granularity={granularity}
            group={RouterMetricGroup.UserLink}
            metric={RouterMetricType.ActiveUsers}
            range={range}
            seriesConfigs={ROUTER_USER_LINK_SERIES_CONFIGS}
            seriesType={ChartSeriesType.Area}
            subtitle="Distinct people who sent router requests per bucket, stacked as unlinked router users and linked Weave accounts."
            title="Active people"
          />
          <RouterInactiveCard className={ResponsiveGrid.Medium} />
          <RouterRequestsPerActiveUserChart
            className={ResponsiveGrid.Medium}
            granularity={granularity}
            range={range}
          />
        </ResponsiveGrid>
      );

    case RouterOverviewTab.Spend:
      return (
        <ResponsiveGrid>
          <RouterDashboardStats
            cards={SPEND_SUMMARY_CARDS}
            className={ResponsiveGrid.Full}
            methodologyHref={methodologyHref}
            range={range}
          />
        </ResponsiveGrid>
      );

    case RouterOverviewTab.Routing:
      return (
        <ResponsiveGrid>
          <RouterGroupedChartCard
            className={ResponsiveGrid.Medium}
            granularity={granularity}
            group={RouterMetricGroup.DecisionType}
            metric={RouterMetricType.ModelUsage}
            range={range}
            seriesConfigs={ROUTER_DECISION_TYPE_SERIES_CONFIGS}
            seriesType={ChartSeriesType.Bar}
            shareToggle
            subtitle="Router requests per bucket by how the model was chosen."
            title="Routing decisions"
          />
          <RouterGroupedChartCard
            className={ResponsiveGrid.Medium}
            granularity={granularity}
            group={RouterMetricGroup.Billing}
            metric={RouterMetricType.ModelUsage}
            range={range}
            seriesConfigs={ROUTER_BILLING_SERIES_CONFIGS}
            seriesType={ChartSeriesType.Bar}
            shareToggle
            subtitle="Router requests per bucket by subscription or API billing."
            title="Subscription vs API requests"
          />
        </ResponsiveGrid>
      );

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
