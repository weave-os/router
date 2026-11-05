export enum RouterOverviewTab {
  Activity = "activity",
  Models = "models",
  Routing = "routing",
  Spend = "spend",
  Subscriptions = "subscriptions",
}

export const ROUTER_OVERVIEW_TAB_PARAM = "router-tab";

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

/** Only the active tab mounts, so a `?chart=` deep link must open the tab that renders it. */
const ROUTER_OVERVIEW_TAB_BY_CHART: ReadonlyMap<string, RouterOverviewTab> = new Map([
  ["router_active_users", RouterOverviewTab.Activity],
  ["router_requests_per_active_user", RouterOverviewTab.Activity],
  ["router_model_spend", RouterOverviewTab.Models],
  ["router_requests_by_decision", RouterOverviewTab.Routing],
  ["router_requests_by_billing", RouterOverviewTab.Routing],
  ["router_cost_breakdown", RouterOverviewTab.Spend],
  ["router_cost_per_request", RouterOverviewTab.Spend],
  ["router_cumulative_savings", RouterOverviewTab.Spend],
  ["router_savings_rate", RouterOverviewTab.Spend],
  ["router_spend_by_maker", RouterOverviewTab.Spend],
]);

/** A chart deep link overrides the tab already in the URL. */
export function resolveRouterOverviewTab(
  tab: string | null,
  chart: string | null,
): RouterOverviewTab {
  return (
    (chart == null ? undefined : ROUTER_OVERVIEW_TAB_BY_CHART.get(chart)) ??
    parseRouterOverviewTab(tab)
  );
}
