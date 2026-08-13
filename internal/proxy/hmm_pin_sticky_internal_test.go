package proxy

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"workweave/router/internal/providers"
	"workweave/router/internal/router"
	"workweave/router/internal/router/policy"
	"workweave/router/internal/router/sessionpin"
	"workweave/router/internal/translate"
)

const hmmPinStickyTestFallbackReason = "arm-selector unavailable for 'high': arm selector requires " +
	"at least 2 trained eligible arm(s) in cluster 'high'; " +
	"eligible=['anthropic/claude-opus-4-7', 'openai/gpt-5.6-terra']; " +
	"legacy pairwise arm 'z-ai/glm-5.2' among 2/5 eligible [explored] " +
	hmmArmSelectorUnavailableSentinel

// TestStickPinOnArmSelectorUnavailable exercises the pure predicate against the full stick/no-stick matrix.
func TestStickPinOnArmSelectorUnavailable(t *testing.T) {
	const pinnedModel = "claude-opus-4-7"         // catalog.TierHigh
	const sameTierFresh = "claude-opus-4-6"       // catalog.TierHigh
	const differentTierFresh = "claude-haiku-4-5" // catalog.TierLow — different tier, but the legacy bandit draws within one cluster, not within one catalog tier, so tier is not what gates this case.
	const pinnedGroup = "high"
	const otherGroup = "low"

	basePin := sessionpin.Pin{
		Provider:    providers.ProviderAnthropic,
		Model:       pinnedModel,
		Reason:      "hmm_policy(classifier 'high' (p=0.32))",
		PolicyGroup: pinnedGroup,
	}

	// fallbackDecision builds a fresh decision carrying the arm-selector
	// fallback sentinel and the supplied policy group.
	fallbackDecision := func(model, group string) router.Decision {
		return router.Decision{
			Model:    model,
			Reason:   hmmPinStickyTestFallbackReason,
			Metadata: &router.RoutingMetadata{PolicyGroup: group},
		}
	}

	tests := []struct {
		name         string
		fresh        router.Decision
		pin          sessionpin.Pin
		pinFound     bool
		prefixBroken bool
		want         bool
	}{
		{
			name:     "sticks: fallback sentinel, same HMM pin, same group, different model",
			fresh:    fallbackDecision(sameTierFresh, pinnedGroup),
			pin:      basePin,
			pinFound: true,
			want:     true,
		},
		{
			name:     "sticks even if catalog tier differs, as long as the group matches",
			fresh:    fallbackDecision(differentTierFresh, pinnedGroup),
			pin:      basePin,
			pinFound: true,
			want:     true,
		},
		{
			name:     "no stick: classifier escalated into a different cluster",
			fresh:    fallbackDecision(differentTierFresh, otherGroup),
			pin:      basePin,
			pinFound: true,
			want:     false,
		},
		{
			name:     "no stick: fresh decision reports no group (cannot prove same cluster)",
			fresh:    router.Decision{Model: sameTierFresh, Reason: hmmPinStickyTestFallbackReason},
			pin:      basePin,
			pinFound: true,
			want:     false,
		},
		{
			name:  "no stick: pin predates group persistence",
			fresh: fallbackDecision(sameTierFresh, pinnedGroup),
			pin: sessionpin.Pin{
				Provider: providers.ProviderAnthropic,
				Model:    pinnedModel,
				Reason:   "hmm_policy(classifier 'high' (p=0.32))",
			},
			pinFound: true,
			want:     false,
		},
		{
			name:     "no stick: fresh has no sentinel (real scorer decision)",
			fresh:    router.Decision{Model: sameTierFresh, Reason: "hmm_policy(classifier 'high' (p=0.91); arm-selector XGBoost greedy arm)", Metadata: &router.RoutingMetadata{PolicyGroup: pinnedGroup}},
			pin:      basePin,
			pinFound: true,
			want:     false,
		},
		{
			name:     "no stick: no active pin",
			fresh:    fallbackDecision(sameTierFresh, pinnedGroup),
			pin:      sessionpin.Pin{},
			pinFound: false,
			want:     false,
		},
		{
			name:     "no stick: pin found but empty model",
			fresh:    fallbackDecision(sameTierFresh, pinnedGroup),
			pin:      sessionpin.Pin{Reason: "hmm_policy(...)", PolicyGroup: pinnedGroup},
			pinFound: true,
			want:     false,
		},
		{
			name:  "no stick: pin reason is not HMM-written (stale cluster pin)",
			fresh: fallbackDecision(sameTierFresh, pinnedGroup),
			pin: sessionpin.Pin{
				Provider:    providers.ProviderAnthropic,
				Model:       pinnedModel,
				Reason:      "cluster:v0.2",
				PolicyGroup: pinnedGroup,
			},
			pinFound: true,
			want:     false,
		},
		{
			name:     "no stick: scorer agrees with pin (no-op)",
			fresh:    fallbackDecision(pinnedModel, pinnedGroup),
			pin:      basePin,
			pinFound: true,
			want:     false,
		},
		{
			name:         "no stick: prefix trimmed (cache broken)",
			fresh:        fallbackDecision(sameTierFresh, pinnedGroup),
			pin:          basePin,
			pinFound:     true,
			prefixBroken: true,
			want:         false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := stickPinOnArmSelectorUnavailable(test.fresh, test.pin, test.pinFound, test.prefixBroken)
			assert.Equal(t, test.want, got)
		})
	}
}

// hmmPinStickyTestRouter always returns a fixed fresh decision, mirroring
// authoritativeTestRouter in authoritative_turnloop_internal_test.go.
type hmmPinStickyTestRouter struct {
	decision router.Decision
}

func (r *hmmPinStickyTestRouter) Route(_ context.Context, _ router.Request) (router.Decision, error) {
	return r.decision, nil
}

// TestHMMPinStickyOnArmSelectorUnavailableWiredIntoTurnLoop proves the kill switch gates the
// predicate end to end and that an authoritative cluster escalation still serves.
func TestHMMPinStickyOnArmSelectorUnavailableWiredIntoTurnLoop(t *testing.T) {
	strategy := router.Strategy("hmm-pin-sticky-wiring-test")
	const pinnedModel = "claude-opus-4-7"
	const sameTierFresh = "claude-opus-4-6"
	const pinnedGroup = "high"
	const otherGroup = "low"

	tests := []struct {
		name          string
		enabled       bool
		freshGroup    string
		wantStickyHit bool
		wantModel     string
		wantPinTier   string
	}{
		{
			name:          "enabled, same group: suppresses reroute, keeps pin",
			enabled:       true,
			freshGroup:    pinnedGroup,
			wantStickyHit: true,
			wantModel:     pinnedModel,
			wantPinTier:   hmmPinStickyArmSelectorUnavailReason,
		},
		{
			name:          "enabled, different group: authoritative escalation still serves",
			enabled:       true,
			freshGroup:    otherGroup,
			wantStickyHit: false,
			wantModel:     sameTierFresh,
			wantPinTier:   "authoritative_per_turn",
		},
		{
			name:          "disabled: fresh decision serves as usual",
			enabled:       false,
			freshGroup:    pinnedGroup,
			wantStickyHit: false,
			wantModel:     sameTierFresh,
			wantPinTier:   "authoritative_per_turn",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newStubPinStore()
			store.getFound = true
			store.getPin = sessionpin.Pin{
				Provider:        providers.ProviderAnthropic,
				Model:           pinnedModel,
				Reason:          "hmm_policy(classifier 'high' (p=0.32))",
				PolicyGroup:     pinnedGroup,
				PinnedUntil:     time.Now().Add(time.Hour),
				LastTurnEndedAt: time.Now().Add(-time.Minute),
				LastServedModel: pinnedModel,
			}
			policyRouter := &hmmPinStickyTestRouter{decision: router.Decision{
				Provider: providers.ProviderAnthropic,
				Model:    sameTierFresh,
				Reason:   hmmPinStickyTestFallbackReason,
				Metadata: &router.RoutingMetadata{PolicyGroup: test.freshGroup},
			}}
			svc := NewService(
				nil,
				nil,
				nil,
				false,
				nil,
				store,
				false,
				providers.ProviderAnthropic,
				"claude-haiku-4-5",
				nil,
			).WithHMPinStickyOnArmSelectorUnavail(test.enabled).
				WithPolicyStrategy(policy.StrategySpec{
					Strategy: strategy,
					Router:   policyRouter,
					Capabilities: policy.Capabilities{
						SchemaVersion:                 policy.SchemaVersionV1,
						AuthoritativePerTurnSelection: true,
					},
				})
			env, err := translate.ParseAnthropic(
				[]byte(`{"model":"claude-opus-4-8","messages":[{"role":"user","content":"continue"}]}`),
			)
			require.NoError(t, err)
			features := env.RoutingFeatures(false)
			ctx := router.WithStrategy(context.Background(), strategy)
			req := router.Request{
				RequestedModel:       features.Model,
				EstimatedInputTokens: features.Tokens,
				HasTools:             features.HasTools,
				ConversationMessages: conversationMessagesForRouting(env),
			}

			result, err := svc.runTurnLoop(
				ctx,
				env,
				features,
				"api-key",
				uuid.New(),
				"",
				http.Header{},
				req,
			)

			require.NoError(t, err)
			assert.Equal(t, test.wantStickyHit, result.StickyHit)
			assert.Equal(t, test.wantModel, result.Decision.Model)
			assert.Equal(t, test.wantPinTier, result.PinTier)

			// The predicate can only compare groups if the write path actually
			// persists one: a fresh authoritative write carries the decision's
			// group, a suppressed reroute carries the pin's forward.
			store.mu.Lock()
			upserts := append([]sessionpin.Pin(nil), store.upserts...)
			store.mu.Unlock()
			require.NotEmpty(t, upserts)
			wantGroup := test.freshGroup
			if test.wantStickyHit {
				wantGroup = pinnedGroup
			}
			assert.Equal(t, wantGroup, upserts[len(upserts)-1].PolicyGroup)
		})
	}
}
