package admin

import (
	"testing"
	"time"

	"weave-os/router/internal/proxy"
)

func individualFact(person, model, decisionType string, at time.Time, requests int64, cost float64) proxy.TelemetryIndividualFact {
	return proxy.TelemetryIndividualFact{
		TelemetryDashboardFact: proxy.TelemetryDashboardFact{
			Bucket: at, DecisionModel: model, DecisionType: decisionType, RequestCount: requests, ActualCostUSD: cost,
		},
		PersonID: person,
		Linked:   true,
	}
}

func TestDashboardIndividualsSumsUsageAndRoutedModels(t *testing.T) {
	day := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	seen := day.Add(time.Hour)
	individuals := dashboardIndividuals([]proxy.TelemetryIndividualFact{
		individualFact("ada", "gpt-5", string(dashboardDecisionRouterPick), day, 4, 2),
		individualFact("ada", "claude-haiku-4-5", string(dashboardDecisionRouterPick), day, 3, 1),
		individualFact("ada", "gemini-2.5-pro", string(dashboardDecisionRouterPick), day.AddDate(0, 0, 1), 2, 1),
		individualFact("ada", "gpt-5-mini", string(dashboardDecisionRouterPick), day.AddDate(0, 0, 1), 1, 1),
		individualFact("ada", "claude-opus-4-1", string(dashboardDecisionPassthrough), day.AddDate(0, 0, 1), 5, 3),
	}, []proxy.TelemetryPerson{{ID: "ada", Linked: true, LastRequestAt: &seen}, {ID: "idle", Linked: true}})

	if len(individuals) != 2 || individuals[1].ID != "idle" || individuals[1].Requests != 0 {
		t.Fatalf("individuals = %+v, want ada plus idle with no usage", individuals)
	}
	ada := individuals[0]
	if ada.Requests != 15 || ada.CostUSD != 8 || ada.PassthroughRequests != 5 {
		t.Fatalf("ada totals = %+v", ada)
	}
	if len(ada.RequestActivity) != 2 || ada.RequestActivity[1].Value != 8 {
		t.Fatalf("request activity = %+v, want 7 then 8", ada.RequestActivity)
	}
	if len(ada.Models) != 3 || ada.Models[0].Model != "gpt-5" || ada.OtherRequests != 1 {
		t.Fatalf("models = %+v other = %d, want passthrough excluded and the fourth model in other", ada.Models, ada.OtherRequests)
	}
	if ada.LastRequestAt == nil || *ada.LastRequestAt != seen.Format(time.RFC3339) {
		t.Fatalf("last request = %v", ada.LastRequestAt)
	}
}
