import { resolveRouterOverviewTab, RouterOverviewTab } from "./routerOverviewTab";

test.each([
  ["router_requests_by_decision", RouterOverviewTab.Routing],
  ["router_cost_per_request", RouterOverviewTab.Spend],
  ["router_model_spend", RouterOverviewTab.Models],
])("a %s chart deep link opens the %s tab", (chart, tab) => {
  expect(resolveRouterOverviewTab(RouterOverviewTab.Activity, chart)).toBe(tab);
});

test("an unknown chart keeps the tab in the URL", () => {
  expect(resolveRouterOverviewTab(RouterOverviewTab.Spend, "unknown")).toBe(
    RouterOverviewTab.Spend,
  );
});
