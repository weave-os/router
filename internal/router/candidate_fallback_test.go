package router_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/router"
)

func permitAll(string) bool { return true }

func TestEligibleRankedFallbackStaysInsideTheRescueRoster(t *testing.T) {
	decision := router.Decision{Model: "picked", Provider: "p", Metadata: &router.RoutingMetadata{
		RosterFailover:     true,
		RescueModels:       []string{"picked", "rescue-excluded"},
		CandidateModels:    []string{"picked", "other-cluster"},
		CandidateScores:    map[string]float32{"other-cluster": 1},
		CandidateProviders: map[string]string{"rescue-excluded": "p", "other-cluster": "p"},
	}}
	req := router.Request{Candidates: router.Candidates{SafetyExcludedModels: map[string]struct{}{"picked": {}, "rescue-excluded": {}}}}

	_, found := router.EligibleRankedFallback(req, decision, permitAll)

	assert.False(t, found)
}

func TestEligibleRankedFallbackDropsTheRejectedArmsSelection(t *testing.T) {
	decision := router.Decision{Model: "picked", Provider: "p", Metadata: &router.RoutingMetadata{
		CandidateModels:     []string{"picked", "fallback"},
		CandidateProviders:  map[string]string{"fallback": "p"},
		PolicyGroup:         "high",
		SelectedArmID:       "arm-picked",
		SelectedRosterArmID: "roster-picked",
		SelectedUpstreamID:  "upstream-picked",
		BindingIndex:        2,
		SelectionTrace:      &router.SelectionTrace{SelectedGroup: "high"},
	}}
	req := router.Request{Candidates: router.Candidates{SafetyExcludedModels: map[string]struct{}{"picked": {}}}}

	fallback, found := router.EligibleRankedFallback(req, decision, permitAll)

	require.True(t, found)
	assert.Equal(t, "fallback", fallback.Model)
	assert.Equal(t, router.CandidateFallbackReason, fallback.Reason)
	assert.Empty(t, fallback.Metadata.PolicyGroup)
	assert.Empty(t, fallback.Metadata.SelectedArmID)
	assert.Empty(t, fallback.Metadata.SelectedRosterArmID)
	assert.Nil(t, fallback.Metadata.SelectionTrace)
	assert.Empty(t, fallback.Metadata.SelectedUpstreamID)
	assert.Equal(t, "high", decision.Metadata.PolicyGroup)
}
