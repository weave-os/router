package admin

import (
	"testing"
	"time"

	"weave-os/router/internal/proxy"
)

func drillDownRow(person, session, model string, at time.Time, cost float64, subscription bool) proxy.TelemetryDrillDownRow {
	return proxy.TelemetryDrillDownRow{
		TelemetryDashboardFact: proxy.TelemetryDashboardFact{
			DecisionModel:      model,
			DecisionType:       string(dashboardDecisionRouterPick),
			SubscriptionServed: subscription,
			RequestCount:       2,
			ActualCostUSD:      cost,
		},
		PersonID:   person,
		Linked:     person != "key",
		SessionID:  session,
		StartedAt:  at,
		FinishedAt: at.Add(time.Minute),
	}
}

func TestDrillDownMatchesClickedSeries(t *testing.T) {
	at := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	row := drillDownRow("key", "s1", "claude-haiku-4-5", at, 1, true)

	cases := []struct {
		name                       string
		group                      dashboardMetricGroup
		key, billing, routingState string
		models                     []string
		want                       bool
	}{
		{name: "no filter", want: true},
		{name: "model key", group: dashboardGroupModel, key: "claude-haiku-4-5", want: true},
		{name: "other model key", group: dashboardGroupModel, key: "gpt-5", want: false},
		{name: "unlinked", group: dashboardGroupUserLink, key: userLinkUnlinked, want: true},
		{name: "linked", group: dashboardGroupUserLink, key: userLinkLinked, want: false},
		{name: "subscription billing", billing: billingSubscription, want: true},
		{name: "api billing", billing: billingAPI, want: false},
		{name: "passthrough only", routingState: routingStatePassthrough, want: false},
		{name: "maker models", models: []string{"gpt-5", "claude-haiku-4-5"}, want: true},
		{name: "cost type key keeps every row", group: dashboardGroupCostType, key: costTypeActual, want: true},
	}
	for _, tc := range cases {
		if got := drillDownMatches(row, tc.group, tc.key, tc.models, tc.billing, tc.routingState); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestDrillDownPeopleGroupsSessionsAndSortsByCost(t *testing.T) {
	at := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	people := drillDownPeople([]proxy.TelemetryDrillDownRow{
		drillDownRow("cheap", "s1", "claude-haiku-4-5", at, 1, false),
		drillDownRow("pricey", "s2", "claude-haiku-4-5", at, 3, false),
		drillDownRow("pricey", "s2", "gpt-5", at.Add(time.Hour), 4, false),
		drillDownRow("pricey", "s3", "gpt-5", at.Add(2*time.Hour), 5, true),
	})

	if len(people) != 2 || people[0].ID != "pricey" {
		t.Fatalf("people = %+v, want pricey first", people)
	}
	if people[0].SessionCount != 2 {
		t.Fatalf("session count = %d, want 2", people[0].SessionCount)
	}
	sessions := people[0].Sessions
	if len(sessions) != 2 || sessions[0].SessionID != "s3" {
		t.Fatalf("sessions = %+v, want newest s3 first", sessions)
	}
	s2 := sessions[1]
	if len(s2.Models) != 2 || !s2.StartedAt.Equal(at) || !s2.FinishedAt.Equal(at.Add(time.Hour+time.Minute)) {
		t.Fatalf("s2 = %+v, want both model pairs spanning the session", s2)
	}
	if got := sessions[0].Models[0]; got.CatalogCostUSD != 0 || got.SubscriptionRequests != 2 {
		t.Fatalf("subscription pair = %+v, want zero billed cost and 2 subscription requests", got)
	}
}
