"use client";

import { isUnavailable, useRouterDashboardDataSource } from "../../data";
import { RouterDashboardSlot, RouterDashboardSlotName } from "../../slots";
import { Text } from "../atoms/Text";
import { DashboardPageFilters, useDashboardFilters } from "../DashboardPageFilters";
import { Page } from "../Page";
import { PageHeader } from "../PageHeader";
import { RouterOverviewTab, RouterOverviewTabs, useRouterOverviewTab } from "../RouterOverviewTabs";
import { RouterOverviewTabPanel } from "./RouterOverviewTabPanel";

/** The Router cost & savings dashboard: header, overview tabs, filters and the active tab's cards. */
export interface RouterDashboardPageProps {
  /** Where the savings card's "How we measure" link goes. */
  methodologyHref?: string;
}

export function RouterDashboardPage({ methodologyHref }: RouterDashboardPageProps) {
  const filters = useDashboardFilters();
  const tab = useRouterOverviewTab();
  const showSubscriptions = !isUnavailable(useRouterDashboardDataSource().subscriptions);

  return (
    <Page
      header={
        <PageHeader
          left={
            <Text tabIndex={-1} variant="h4" as="h2" className="truncate">
              Router cost &amp; savings
            </Text>
          }
          leftColumnClassName="truncate"
          right={
            <div className="flex animate-fade-in flex-row items-center gap-2 motion-reduce:animate-none">
              <RouterDashboardSlot name={RouterDashboardSlotName.DashboardHeaderActions} />
            </div>
          }
        />
      }
      tabs={<RouterOverviewTabs showSubscriptions={showSubscriptions} />}
    >
      {/* The subscription pool shows current limits, so date filters don't apply. */}
      {tab !== RouterOverviewTab.Subscriptions && <DashboardPageFilters result={filters} />}

      <Page.Section className="pt-0">
        <RouterOverviewTabPanel
          filters={filters.filters}
          methodologyHref={methodologyHref}
          tab={tab}
        />
      </Page.Section>
    </Page>
  );
}
