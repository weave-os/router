package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/sessionpin"
)

const recoveryOpusModel = "claude-opus-5-5"

func TestTwoModelGatewayRecoveryReadmitsStruckSibling(t *testing.T) {
	primary := &failingClient{err: &providers.UpstreamErrorResponse{Status: http.StatusBadGateway}}
	sibling := &fakeClient{outcomes: []fakeOutcome{{writeBytes: []byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")}}}
	store := &rolePinStore{byRole: map[string]sessionpin.Pin{
		hmmHistoryRole(roleForTier(catalog.TierFor(catalog.ModelGPT6Luna))): {
			Strategy: router.StrategyCluster, DemotedModels: []string{catalog.ModelGPT6Luna, recoveryOpusModel},
		},
	}}
	decision := router.Decision{Provider: providers.ProviderOpenAIGateway, Model: catalog.ModelGPT6Luna, Metadata: &router.RoutingMetadata{
		RosterFailover:    true,
		RescueModels:      []string{catalog.ModelGPT6Luna},
		SidecarRescuePool: []string{recoveryOpusModel},
		CandidateModels:   []string{catalog.ModelGPT6Luna},
	}}
	svc := NewService(staticRouter{decision: decision}, map[string]providers.Client{
		providers.ProviderOpenAIGateway: primary, providers.ProviderAnthropicGateway: sibling,
	}, nil, false, nil, store, false, providers.ProviderAnthropic, recoveryOpusModel, nil).WithRetrySleep(func(context.Context, time.Duration) error { return nil })
	svc.now = func() time.Time { return rateLimitTestNow }
	body := []byte(`{"model":"gpt-6-luna","stream":true,"messages":[{"role":"user","content":"synthetic exhausted pool"}]}`)
	rec := httptest.NewRecorder()
	err := svc.ProxyMessages(twoModelGatewayContext(), body, rec, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
	assert.Equal(t, 3, primary.calls)
	assert.Equal(t, 1, sibling.calls)
	require.NoError(t, err, "after both strikes a precommit primary failure must still try the permitted sibling")
	assert.Equal(t, recoveryOpusModel, rec.Header().Get(HeaderRouterModel))
	assert.Contains(t, rec.Body.String(), "message_stop")
}

func TestTwoModelRecoveryOnlyLeasesOneExpiredArmPerRequest(t *testing.T) {
	cooldowns := map[string]time.Time{
		catalog.ModelGPT6Luna: rateLimitTestNow,
		recoveryOpusModel:     rateLimitTestNow,
	}
	store := &rolePinStore{byRole: map[string]sessionpin.Pin{
		hmmHistoryRole(sessionpin.DefaultRole): {Strategy: router.StrategyCluster, DemotionCooldowns: cooldowns},
	}}
	svc, _ := cooldownTurnLoopService(store, true)
	result := runDemotionTurnLoop(t, svc, context.Background())
	defer result.releaseCooldownProbes()

	require.Len(t, result.CooldownProbeReleases, 1, "one request must not lock both allowed models while selecting its probe")
}

func twoModelGatewayContext() context.Context {
	ctx := rescuedFailureCtx()
	ctx = context.WithValue(ctx, InstallationAllowedModelsContextKey{}, []string{catalog.ModelGPT6Luna, recoveryOpusModel})
	return context.WithValue(ctx, ExternalAPIKeysContextKey{}, []*auth.ExternalAPIKey{
		{Provider: providers.ProviderOpenAIGateway, Plaintext: []byte("synthetic-key"), ModelAliases: map[string]string{catalog.ModelGPT6Luna: catalog.ModelGPT6Luna}},
		{Provider: providers.ProviderAnthropicGateway, Plaintext: []byte("synthetic-key"), ModelAliases: map[string]string{recoveryOpusModel: recoveryOpusModel}},
	})
}

func TestTwoModelGatewayRecoveryRetriesThenRecordsWithdrawal(t *testing.T) {
	for _, scenario := range []struct {
		name      string
		status    int
		transient bool
		cooldown  bool
		reason    sessionpin.DemotionReason
	}{
		{name: "502 gets bounded cooldown", status: http.StatusBadGateway, transient: true, cooldown: true, reason: sessionpin.DemotionReasonTransientFailure},
		{name: "429 flag off remains permanent", status: http.StatusTooManyRequests, reason: sessionpin.DemotionReasonRescuedFailure},
		{name: "429 flag on has bounded cooldown", status: http.StatusTooManyRequests, transient: true, cooldown: true, reason: sessionpin.DemotionReasonRateLimited},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			store := &cooldownStubPinStore{}
			primary := &failingClient{err: &providers.UpstreamErrorResponse{Status: scenario.status}}
			sibling := &fakeClient{outcomes: []fakeOutcome{{writeBytes: []byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"synthetic\",\"model\":\"" + recoveryOpusModel + "\",\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")}}}
			decision := router.Decision{Provider: providers.ProviderOpenAIGateway, Model: catalog.ModelGPT6Luna, Metadata: &router.RoutingMetadata{
				RosterFailover:  true,
				RescueModels:    []string{catalog.ModelGPT6Luna, recoveryOpusModel},
				CandidateModels: []string{catalog.ModelGPT6Luna, recoveryOpusModel},
			}}
			svc := NewService(staticRouter{decision: decision}, map[string]providers.Client{
				providers.ProviderOpenAIGateway:    primary,
				providers.ProviderAnthropicGateway: sibling,
			}, nil, false, nil, store, false, providers.ProviderAnthropic, recoveryOpusModel, nil).
				WithRescuedFailureArmDemotion(true).WithTransientRateLimit(scenario.transient, 45)
			now := rateLimitTestNow
			svc.now = func() time.Time { return now }
			var retryWaits []time.Duration
			svc.WithRetrySleep(func(_ context.Context, wait time.Duration) error {
				retryWaits = append(retryWaits, wait)
				now = now.Add(wait)
				return nil
			})
			rec := httptest.NewRecorder()
			body := []byte(`{"model":"gpt-6-luna","stream":true,"messages":[{"role":"user","content":"synthetic recovery check"}]}`)
			err := svc.ProxyMessages(twoModelGatewayContext(), body, rec, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
			require.NoError(t, err)
			assert.Equal(t, 3, primary.calls)
			assert.Equal(t, 1, sibling.calls)
			assert.Len(t, retryWaits, 2)
			assert.Equal(t, recoveryOpusModel, rec.Header().Get(HeaderRouterModel))
			assert.Contains(t, rec.Body.String(), "message_stop")
			if scenario.cooldown {
				assert.Empty(t, store.demotions)
				require.Len(t, store.cooldowns, 2)
				for _, withdrawal := range store.cooldowns {
					assert.Equal(t, catalog.ModelGPT6Luna, withdrawal.model)
					assert.Equal(t, now.Add(45*time.Second), withdrawal.until)
					assert.Equal(t, scenario.reason, withdrawal.reason)
				}
			} else {
				assert.Empty(t, store.cooldowns)
				require.Len(t, store.demotions, 2)
				for _, withdrawal := range store.demotions {
					assert.Equal(t, catalog.ModelGPT6Luna, withdrawal.model)
					assert.Equal(t, sessionpin.DemotionReasonRescuedFailure, withdrawal.reason)
				}
			}
		})
	}
}

// A fixed expiry must survive history refreshes. Captured writes test the Go
// writer contract; this deliberately does not reimplement SQL merge semantics.
func TestTwoModelRecoveryHMMRefreshDoesNotRewriteCooldown(t *testing.T) {
	until := rateLimitTestNow.Add(45 * time.Second)
	store := &rolePinStore{byRole: map[string]sessionpin.Pin{
		hmmHistoryRole(sessionpin.DefaultRole): coolingPin(demotedPinModel, map[string]time.Time{demotedPinModel: until}),
	}}
	svc, scorer := cooldownTurnLoopService(store, true)
	now := rateLimitTestNow
	svc.now = func() time.Time { return now }
	res := turnLoopResult{InstallationID: uuid.New(), SessionKey: nonZeroSessionKey(), PinRole: sessionpin.DefaultRole,
		Decision: router.Decision{Provider: providers.ProviderAnthropic, Model: freshTurnModel, Reason: hmmHistoryReason}}
	for step := 0; step <= 96; step++ {
		now = rateLimitTestNow.Add(time.Duration(step) * 30 * time.Minute)
		svc.recordHMMTurnHistory(res, providers.ProviderAnthropic, freshTurnModel, 0, 0, 0, 0, false)
		turn := runDemotionTurnLoop(t, svc, context.Background())
		turn.releaseCooldownProbes()
		if step == 0 {
			assert.Contains(t, turn.SessionDemotedModels, demotedPinModel)
		} else {
			assert.NotContains(t, turn.SessionDemotedModels, demotedPinModel)
			assert.NotContains(t, scorer.requests[len(scorer.requests)-1].AutomaticExcludedModels, demotedPinModel)
		}
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, write := range store.upserts {
		assert.Empty(t, write.DemotionCooldowns, "history/pin refresh must not emit a new cooldown")
		assert.Empty(t, write.DemotedModels, "history/pin refresh must not manufacture a permanent strike")
	}
}
