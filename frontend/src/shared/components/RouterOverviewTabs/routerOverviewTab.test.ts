import { resolveRouterOverviewTab, RouterOverviewTab } from "./routerOverviewTab";

test.each([
  ["router_requests_by_decision", RouterOverviewTab.Routing],
  ["router_cost_per_request", RouterOverviewTab.Spend],
  ["router_model_spend", RouterOverviewTab.Models],
])("a %s chart deep link opens the %s tab", (chart, tab) => {
  expect(resolveRouterOverviewTab(RouterOverviewTab.Activity, chart, true)).toBe(tab);
});

test("an unknown chart keeps the tab in the URL", () => {
  expect(resolveRouterOverviewTab(RouterOverviewTab.Spend, "unknown", true)).toBe(
    RouterOverviewTab.Spend,
  );
});

test("a subscriptions link lands on Activity when the host has no subscriptions", () => {
  expect(resolveRouterOverviewTab(RouterOverviewTab.Subscriptions, null, false)).toBe(
    RouterOverviewTab.Activity,
  );
  expect(resolveRouterOverviewTab(RouterOverviewTab.Subscriptions, null, true)).toBe(
    RouterOverviewTab.Subscriptions,
  );
});
