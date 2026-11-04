export enum RouterOverviewTab {
  Activity = "activity",
  Models = "models",
  Routing = "routing",
  Spend = "spend",
  Subscriptions = "subscriptions",
}

export const ROUTER_OVERVIEW_TAB_PARAM = "tab";

export const ROUTER_OVERVIEW_TAB_LABEL: Record<RouterOverviewTab, string> = {
  [RouterOverviewTab.Activity]: "Activity",
  [RouterOverviewTab.Models]: "Models",
  [RouterOverviewTab.Routing]: "Routing",
  [RouterOverviewTab.Spend]: "Spend",
  [RouterOverviewTab.Subscriptions]: "Subscriptions",
};

/** Tabs every host renders, in tab-bar order. Subscriptions only appears when the host supplies its panel. */
export const ROUTER_OVERVIEW_BASE_TABS: readonly RouterOverviewTab[] = [
  RouterOverviewTab.Activity,
  RouterOverviewTab.Spend,
  RouterOverviewTab.Routing,
  RouterOverviewTab.Models,
];

/** Unknown or missing values land on Activity. */
export function parseRouterOverviewTab(value: string | null): RouterOverviewTab {
  return Object.values(RouterOverviewTab).find(tab => tab === value) ?? RouterOverviewTab.Activity;
}
