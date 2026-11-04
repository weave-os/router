import { type ChartSeriesConfig } from "@/shared/components/Chart";
import {
  RouterBilling,
  RouterDecisionType,
  RouterRoutingState,
  RouterUserLink,
} from "@/shared/data";
import { AccountChartColorToken, ChartColorToken } from "@/shared/tools/chartColors";

export type RouterSeriesConfigs = Readonly<Record<string, ChartSeriesConfig<number, number>>>;

export const ROUTER_DECISION_TYPE_SERIES_CONFIGS: RouterSeriesConfigs = {
  [RouterDecisionType.Forced]: { color: ChartColorToken.Orange2, label: "Forced" },
  [RouterDecisionType.Passthrough]: { color: ChartColorToken.Scale5, label: "Passthrough" },
  [RouterDecisionType.RouterPick]: { color: ChartColorToken.Scale3, label: "Router pick" },
  [RouterDecisionType.SubscriptionBypass]: {
    color: ChartColorToken.Green,
    label: "Subscription bypass",
  },
  [RouterDecisionType.Utility]: { color: AccountChartColorToken.Gray, label: "Utility" },
};

export const ROUTER_BILLING_SERIES_CONFIGS: RouterSeriesConfigs = {
  [RouterBilling.API]: { color: ChartColorToken.Scale2, label: "API" },
  [RouterBilling.Subscription]: { color: ChartColorToken.Green, label: "Subscription" },
};

export const ROUTER_ROUTING_STATE_SERIES_CONFIGS: RouterSeriesConfigs = {
  [RouterRoutingState.Passthrough]: {
    color: AccountChartColorToken.Gray,
    label: "Control (passthrough)",
  },
  [RouterRoutingState.Routed]: { color: ChartColorToken.Scale1, label: "Experiment (routed)" },
};

export const ROUTER_USER_LINK_SERIES_CONFIGS: RouterSeriesConfigs = {
  [RouterUserLink.Linked]: { color: ChartColorToken.Scale1, label: "Linked people" },
  [RouterUserLink.Unlinked]: { color: AccountChartColorToken.Gray, label: "Unlinked router users" },
};
