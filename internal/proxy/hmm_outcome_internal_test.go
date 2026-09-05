package proxy

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/policy"
)

type captureHMMOutcomeReporter struct {
	ch chan map[string]interface{}
}

func (r *captureHMMOutcomeReporter) ReportOutcome(ctx context.Context, payload map[string]interface{}) error {
	select {
	case r.ch <- payload:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *captureHMMOutcomeReporter) Route(context.Context, router.Request) (router.Decision, error) {
	return router.Decision{}, nil
}

func TestReportPolicyOutcome_UsesFreshMetadataForStickyServedDecision(t *testing.T) {
	reporter := &captureHMMOutcomeReporter{ch: make(chan map[string]interface{}, 1)}
	s := (&Service{}).WithPolicyStrategy(policy.StrategySpec{Strategy: router.StrategyHMM, Router: reporter})

	routeRes := turnLoopResult{
		StickyHit: true,
		Fresh: router.Decision{
			Model:    "moonshotai/kimi-k2.7",
			Provider: providers.ProviderFireworks,
			Metadata: &router.RoutingMetadata{
				RouteID:          "route-fresh",
				Strategy:         string(router.StrategyHMM),
				PolicyRouteKey:   "medium|mid",
				PolicyArtifactID: "hmm-prod",
			},
		},
	}
	served := router.Decision{
		Model:    "claude-haiku-4-5",
		Provider: providers.ProviderAnthropic,
	}

	ctx := context.WithValue(context.Background(), PolicyTrainingAllowedContextKey{}, true)
	ctx = context.WithValue(ctx, ExternalIDContextKey{}, "org-1")
	ctx = context.WithValue(ctx, InstallationIDContextKey{}, "installation-1")
	ctx = context.WithValue(ctx, ClientIdentityContextKey{}, ClientIdentity{ClientApp: ClientAppCodex, RolloutID: "rollout-1"})
	const (
		inputTokens  = 90
		outputTokens = 10
	)
	s.reportPolicyOutcome(ctx, routeRes, served, effortResolution{}, providers.ProviderAnthropic, false, 100, inputTokens, outputTokens, 0, 0, 12, 34, nil, &policyOutcomeResponse{
		Body: []byte(`{"content":[{"type":"text","text":"done"}]}`),
	})

	price, ok := catalog.PriceFor(providers.ProviderAnthropic, "claude-haiku-4-5")
	require.True(t, ok)
	wantCost := catalog.EffectiveInputCost(inputTokens, 0, 0, price, providers.ProviderAnthropic) +
		catalog.EffectiveOutputCost(inputTokens, outputTokens, price)

	select {
	case payload := <-reporter.ch:
		require.Equal(t, "route-fresh", payload["route_id"])
		assert.Equal(t, "moonshotai/kimi-k2.7", payload["selected_model"])
		assert.Equal(t, providers.ProviderFireworks, payload["selected_provider"])
		assert.Equal(t, "claude-haiku-4-5", payload["served_model"])
		assert.Equal(t, providers.ProviderAnthropic, payload["served_provider"])
		assert.Equal(t, false, payload["selected_served_model_match"])
		assert.NotContains(t, payload, "training_exclusion_reason")
		assert.Equal(t, "moonshotai/kimi-k2.7", payload["decision_model"])
		assert.Equal(t, providers.ProviderFireworks, payload["decision_provider"])
		assert.Equal(t, "medium|mid", payload["policy_route_key"])
		assert.Equal(t, "hmm-prod", payload["policy_artifact_id"])
		assert.Equal(t, "org-1", payload["organization_id"])
		assert.Equal(t, "installation-1", payload["installation_id"])
		assert.Equal(t, ClientAppCodex, payload["client_app"])
		assert.Equal(t, "rollout-1", payload["rollout_id"])
		assert.Equal(t, true, payload["training_allowed"])
		assert.Equal(t, true, payload["sticky_hit"])
		assert.Equal(t, "done", payload["response_text"])
		assert.NotContains(t, payload, "response_body")
		assert.NotContains(t, payload, "response_body_format")
		assert.Equal(t, false, payload["response_body_truncated"])
		assert.Equal(t, wantCost, payload["cost_usd"])
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for HMM outcome payload")
	}
}

func TestReportPolicyOutcome_OmitsResponseBodyWhenTrainingIsNotAllowed(t *testing.T) {
	reporter := &captureHMMOutcomeReporter{ch: make(chan map[string]interface{}, 1)}
	s := (&Service{}).WithPolicyStrategy(policy.StrategySpec{Strategy: router.StrategyHMM, Router: reporter})
	routeRes := turnLoopResult{Fresh: router.Decision{
		Model:    "moonshotai/kimi-k2.7",
		Metadata: &router.RoutingMetadata{RouteID: "route-1", Strategy: string(router.StrategyHMM)},
	}}

	s.reportPolicyOutcome(context.Background(), routeRes, routeRes.Fresh, effortResolution{}, providers.ProviderFireworks, false, 1, 1, 1, 0, 0, 1, 1, nil, &policyOutcomeResponse{Body: []byte("private response")})

	select {
	case payload := <-reporter.ch:
		assert.Equal(t, false, payload["training_allowed"])
		assert.NotContains(t, payload, "response_body")
		assert.NotContains(t, payload, "response_body_truncated")
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for policy outcome payload")
	}
}

func TestReportPolicyOutcome_AuthoritativeMismatchFailsClosedForTraining(t *testing.T) {
	strategy := router.Strategy("authoritative-outcome-test")
	reporter := &captureHMMOutcomeReporter{ch: make(chan map[string]interface{}, 1)}
	s := (&Service{}).WithPolicyStrategy(policy.StrategySpec{
		Strategy: strategy,
		Router:   reporter,
	})
	selected := router.Decision{
		Model:    "claude-opus-4-8",
		Provider: providers.ProviderAnthropic,
		Metadata: &router.RoutingMetadata{
			RouteID:                       "route-authoritative",
			Strategy:                      string(strategy),
			AuthoritativePerTurnSelection: true,
		},
	}
	served := router.Decision{
		Model:    "claude-haiku-4-5",
		Provider: providers.ProviderAnthropic,
	}
	ctx := context.WithValue(context.Background(), PolicyTrainingAllowedContextKey{}, true)

	s.reportPolicyOutcome(
		ctx,
		turnLoopResult{Fresh: selected, AuthoritativePerTurn: true},
		served,
		effortResolution{},
		providers.ProviderAnthropic,
		false,
		100,
		90,
		10,
		0,
		0,
		12,
		34,
		nil,
		&policyOutcomeResponse{Body: []byte("must not train")},
	)

	select {
	case payload := <-reporter.ch:
		assert.Equal(t, "claude-opus-4-8", payload["selected_model"])
		assert.Equal(t, "claude-haiku-4-5", payload["served_model"])
		assert.Equal(t, false, payload["selected_served_model_match"])
		assert.Equal(t, false, payload["training_allowed"])
		assert.Equal(t, "selected_served_model_mismatch", payload["training_exclusion_reason"])
		assert.NotContains(t, payload, "response_body")
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for policy outcome payload")
	}
}

// An arm labelled "xhigh" that went out at "high" did not buy what the label
// says, so the turn is reported but withheld from training.
func TestReportPolicyOutcome_EffortMismatchExcludedFromTraining(t *testing.T) {
	strategy := router.Strategy("effort-outcome-test")
	reporter := &captureHMMOutcomeReporter{ch: make(chan map[string]interface{}, 1)}
	s := (&Service{}).WithPolicyStrategy(policy.StrategySpec{
		Strategy: strategy,
		Router:   reporter,
	})
	decision := router.Decision{
		Model:    "gpt-5.5",
		Provider: providers.ProviderOpenAI,
		Effort:   "xhigh",
		Metadata: &router.RoutingMetadata{
			RouteID:  "route-effort",
			Strategy: string(strategy),
		},
	}
	ctx := context.WithValue(context.Background(), PolicyTrainingAllowedContextKey{}, true)
	effort := effortResolutionFor(router.Lookup(decision.Model), decision.Effort, decision.Effort, effortSourceArm)

	s.reportPolicyOutcome(
		ctx,
		turnLoopResult{Fresh: decision},
		decision,
		effort,
		providers.ProviderOpenAI,
		false, 100, 90, 10, 0, 0, 12, 34, nil,
		&policyOutcomeResponse{Body: []byte("must not train")},
	)

	select {
	case payload := <-reporter.ch:
		assert.Equal(t, "xhigh", payload["arm_effort"])
		assert.Equal(t, "xhigh", payload["selected_effort"])
		assert.Equal(t, "high", payload["sent_effort"])
		assert.Equal(t, effortSourceArm, payload["effort_source"])
		assert.Equal(t, false, payload["selected_sent_effort_match"])
		assert.Equal(t, false, payload["training_allowed"])
		assert.Equal(t, "selected_sent_effort_mismatch", payload["training_exclusion_reason"])
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for policy outcome payload")
	}
}
