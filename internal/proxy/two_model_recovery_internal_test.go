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
			Strategy: router.StrategyHMMEmbedding, DemotedModels: []string{catalog.ModelGPT6Luna, recoveryOpusModel},
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
	}, nil, store, false, providers.ProviderAnthropic, recoveryOpusModel, nil).WithRetrySleep(func(context.Context, time.Duration) error { return nil })
	svc.now = func() time.Time { return strikeTestNow }
	body := []byte(`{"model":"gpt-6-luna","stream":true,"messages":[{"role":"user","content":"synthetic exhausted pool"}]}`)
	rec := httptest.NewRecorder()
	err := svc.ProxyMessages(twoModelGatewayContext(), body, rec, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
	assert.Equal(t, 3, primary.calls)
	assert.Equal(t, 1, sibling.calls)
	require.NoError(t, err, "after both strikes a precommit primary failure must still try the permitted sibling")
	assert.Equal(t, recoveryOpusModel, rec.Header().Get(HeaderRouterModel))
	assert.Contains(t, rec.Body.String(), "message_stop")
}

type clearedCooldown struct {
	role, model string
	until       time.Time
}

// recoveryLeaseStore is rolePinStore with a durable-lease contract so the
// proxy takes the shared-lease path and reports recoveries to the store.
type recoveryLeaseStore struct {
	rolePinStore
	leaseFor  []time.Duration
	cleared   []clearedCooldown
	attempted []string
	denyAll   bool
}

func (s *recoveryLeaseStore) AcquireRecoveryProbe(_ context.Context, _ [sessionpin.SessionKeyLen]byte, model string, _ uuid.UUID, leaseFor time.Duration) (bool, error) {
	s.attempted = append(s.attempted, model)
	s.leaseFor = append(s.leaseFor, leaseFor)
	return !s.denyAll, nil
}

func (s *recoveryLeaseStore) ReleaseRecoveryProbe(context.Context, [sessionpin.SessionKeyLen]byte, string, uuid.UUID) error {
	return nil
}

func (s *recoveryLeaseStore) ClearDemotionCooldown(_ context.Context, _ [sessionpin.SessionKeyLen]byte, role, model string, observedUntil time.Time) error {
	s.cleared = append(s.cleared, clearedCooldown{role: role, model: model, until: observedUntil})
	return nil
}

var _ sessionpin.RecoveryProbeStore = (*recoveryLeaseStore)(nil)

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
		name   string
		status int
	}{
		{name: "502 is struck for the session", status: http.StatusBadGateway},
		{name: "429 is struck for the session", status: http.StatusTooManyRequests},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			store := &demotionStubPinStore{}
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
			}, nil, store, false, providers.ProviderAnthropic, recoveryOpusModel, nil).
				WithRescuedFailureArmDemotion(true)
			now := strikeTestNow
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
			require.Len(t, store.demotions, 2)
			for _, withdrawal := range store.demotions {
				assert.Equal(t, catalog.ModelGPT6Luna, withdrawal.model)
				assert.Equal(t, sessionpin.DemotionReasonRescuedFailure, withdrawal.reason)
			}
		})
	}
}
