package admin

import (
	"testing"
	"time"

	"weave-os/router/internal/proxy"
)

func TestFactPointsCostTypeRepricesOnlyRouterPickedAPITurns(t *testing.T) {
	bucket := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	facts := []proxy.TelemetryDashboardFact{
		{Bucket: bucket, DecisionModel: "claude-sonnet-5", DecisionType: string(dashboardDecisionRouterPick), RequestCount: 1, InputTokens: 1_000_000, ActualCostUSD: 1},
		{Bucket: bucket, DecisionModel: "claude-sonnet-5", DecisionType: "forced", RequestCount: 1, InputTokens: 1_000_000, ActualCostUSD: 2},
		{Bucket: bucket, DecisionModel: "claude-sonnet-5", DecisionType: string(dashboardDecisionRouterPick), SubscriptionServed: true, RequestCount: 1, InputTokens: 1_000_000, ActualCostUSD: 5},
	}

	points := factPoints(facts, dashboardMetricCost, dashboardGroupCostType, false)

	got := map[string]float64{}
	for _, point := range points {
		got[*point.Group] = point.Value
	}
	if got[costTypeActual] != 3 {
		t.Fatalf("actual cost = %v, want 3 (subscription turns cost nothing)", got[costTypeActual])
	}
	// 1M fresh input tokens at the comparison model's $4/MTok, plus the forced turn's recorded $2.
	if got[costTypeComparison] != 6 {
		t.Fatalf("comparison cost = %v, want 6", got[costTypeComparison])
	}
}

func TestFactPointsTotalCollapsesBuckets(t *testing.T) {
	first := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	facts := []proxy.TelemetryDashboardFact{
		{Bucket: first, DecisionModel: "a", RequestCount: 2},
		{Bucket: first.AddDate(0, 0, 1), DecisionModel: "a", RequestCount: 3},
		{Bucket: first, DecisionModel: "b", SubscriptionServed: true, RequestCount: 4},
	}

	points := factPoints(facts, dashboardMetricModelUsage, dashboardGroupBilling, true)

	if len(points) != 2 || points[0].Time != nil {
		t.Fatalf("points = %+v, want two untimed groups", points)
	}
	if *points[0].Group != billingAPI || points[0].Value != 5 || *points[1].Group != billingSubscription || points[1].Value != 4 {
		t.Fatalf("points = %+v", points)
	}
}
