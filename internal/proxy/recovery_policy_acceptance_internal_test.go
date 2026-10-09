//go:build recovery_acceptance

package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/sessionpin"
)

// These are opt-in acceptance requirements for a proposed policy, not claims
// that the current permanent-strike policy implements transient recovery.
func TestRecoveryAcceptanceRescued502HasBoundedWithdrawal(t *testing.T) {
	store := &cooldownStubPinStore{}
	svc := newRateLimitTestService(store, true, 45)
	primary := router.Decision{Provider: providers.ProviderOpenAIGateway, Model: catalog.ModelGPT6Luna}
	model, _ := svc.maybeStrikeArmAfterRescuedFailure(context.Background(), true, false,
		&providers.UpstreamErrorResponse{Status: http.StatusBadGateway}, primary,
		uuid.New(), nonZeroSessionKey(), sessionpin.DefaultRole, sessionpin.DefaultRole)
	require.Equal(t, catalog.ModelGPT6Luna, model)
	assert.Empty(t, store.demotions, "a single transient 502 must not create session-lifetime withdrawal")
	require.Len(t, store.cooldowns, 2, "temporary withdrawal must cover base and history roles")
	for _, withdrawal := range store.cooldowns {
		assert.True(t, withdrawal.until.After(rateLimitTestNow))
		assert.True(t, withdrawal.until.Before(rateLimitTestNow.Add(time.Hour)), "expiry must be bounded independently of session activity")
	}
}

type blockingRecoveryClient struct {
	entered chan struct{}
	release chan struct{}
}

type recoveryAlternativeClient struct {
	mu    sync.Mutex
	calls int
}

func (c *recoveryAlternativeClient) Proxy(_ context.Context, _ router.Decision, _ providers.PreparedRequest, w http.ResponseWriter, _ *http.Request) error {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	_, _ = w.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"synthetic\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-opus-5-5\",\"content\":[],\"stop_reason\":null,\"stop_sequence\":null,\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"ok\"}}\n\n" +
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":1}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	return nil
}

func (*recoveryAlternativeClient) Passthrough(context.Context, providers.PreparedRequest, http.ResponseWriter, *http.Request) error {
	return providers.ErrNotImplemented
}

func (c *blockingRecoveryClient) Proxy(ctx context.Context, _ router.Decision, _ providers.PreparedRequest, w http.ResponseWriter, _ *http.Request) error {
	c.entered <- struct{}{}
	select {
	case <-c.release:
		writeChatCompletionText(w, "synthetic completed probe")
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (*blockingRecoveryClient) Passthrough(context.Context, providers.PreparedRequest, http.ResponseWriter, *http.Request) error {
	return providers.ErrNotImplemented
}

type recoveryBucketRead struct {
	key  [sessionpin.SessionKeyLen]byte
	role string
}

type recoveryObservedStore struct {
	rolePinStore
	reads       chan recoveryBucketRead
	leaseMu     sync.Mutex
	probeLeases map[string]struct {
		token uuid.UUID
	}
}

func (s *recoveryObservedStore) Get(ctx context.Context, key [sessionpin.SessionKeyLen]byte, role string) (sessionpin.Pin, bool, error) {
	s.reads <- recoveryBucketRead{key: key, role: role}
	return s.rolePinStore.Get(ctx, key, role)
}

func (s *recoveryObservedStore) AcquireRecoveryProbe(_ context.Context, key [sessionpin.SessionKeyLen]byte, model string, token uuid.UUID, _ time.Duration) (bool, error) {
	s.leaseMu.Lock()
	defer s.leaseMu.Unlock()
	if s.probeLeases == nil {
		s.probeLeases = make(map[string]struct {
			token uuid.UUID
		})
	}
	leaseKey := string(key[:]) + "\x00" + model
	if _, exists := s.probeLeases[leaseKey]; exists {
		return false, nil
	}
	s.probeLeases[leaseKey] = struct {
		token uuid.UUID
	}{token: token}
	return true, nil
}

func (s *recoveryObservedStore) ReleaseRecoveryProbe(_ context.Context, key [sessionpin.SessionKeyLen]byte, model string, token uuid.UUID) error {
	s.leaseMu.Lock()
	defer s.leaseMu.Unlock()
	leaseKey := string(key[:]) + "\x00" + model
	if lease := s.probeLeases[leaseKey]; lease.token == token {
		delete(s.probeLeases, leaseKey)
	}
	return nil
}

func (s *recoveryObservedStore) ClearDemotionCooldown(context.Context, [sessionpin.SessionKeyLen]byte, string, string, time.Time) error {
	return nil
}

type recoveryObservedRouter struct {
	requests chan router.Request
}

func (r *recoveryObservedRouter) Route(_ context.Context, req router.Request) (router.Decision, error) {
	r.requests <- req
	if _, excluded := req.AutomaticExcludedModels[catalog.ModelGPT6Luna]; excluded {
		return router.Decision{Provider: providers.ProviderAnthropicGateway, Model: recoveryOpusModel}, nil
	}
	return router.Decision{Provider: providers.ProviderOpenAIGateway, Model: catalog.ModelGPT6Luna}, nil
}

func TestRecoveryAcceptanceCooldownExpiryAdmitsOneConcurrentProbe(t *testing.T) {
	const callers = 8
	provider := &blockingRecoveryClient{entered: make(chan struct{}, callers), release: make(chan struct{})}
	scorer := &recoveryObservedRouter{requests: make(chan router.Request, callers+1)}
	store := &recoveryObservedStore{rolePinStore: rolePinStore{byRole: map[string]sessionpin.Pin{
		hmmHistoryRole(roleForTier(catalog.TierFor(catalog.ModelGPT6Luna))): {Strategy: router.StrategyCluster, DemotionCooldowns: map[string]time.Time{catalog.ModelGPT6Luna: rateLimitTestNow}},
	}}, reads: make(chan recoveryBucketRead, 128)}
	sibling := &recoveryAlternativeClient{}
	svc := NewService(scorer, map[string]providers.Client{providers.ProviderOpenAIGateway: provider, providers.ProviderAnthropicGateway: sibling},
		nil, false, nil, store, false, providers.ProviderAnthropic, recoveryOpusModel, nil).WithTransientRateLimit(true, 45)
	now := rateLimitTestNow.Add(-time.Nanosecond)
	svc.now = func() time.Time { return now }
	ctx, cancel := context.WithTimeout(twoModelGatewayContext(), 5*time.Second)
	defer cancel()
	body := []byte(`{"model":"gpt-6-luna","stream":true,"messages":[{"role":"user","content":"synthetic concurrent recovery"}]}`)
	rec := httptest.NewRecorder()
	require.NoError(t, svc.ProxyMessages(ctx, body, rec, httptest.NewRequest(http.MethodPost, "/v1/messages", nil)))
	require.Equal(t, recoveryOpusModel, rec.Header().Get(HeaderRouterModel))
	require.Contains(t, (<-scorer.requests).AutomaticExcludedModels, catalog.ModelGPT6Luna, "before expiry the real turn loop must load the history cooldown")
	require.Empty(t, provider.entered)
	now = rateLimitTestNow
	var callersDone sync.WaitGroup
	callersDone.Add(callers)
	start := make(chan struct{})
	for range callers {
		go func() {
			defer callersDone.Done()
			<-start
			_ = svc.ProxyMessages(ctx, body, httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
		}()
	}
	close(start)
	probeEligible := 0
	for range callers {
		select {
		case req := <-scorer.requests:
			if _, excluded := req.AutomaticExcludedModels[catalog.ModelGPT6Luna]; !excluded {
				probeEligible++
			}
		case <-ctx.Done():
			t.Fatal("deadlock waiting for every caller to reach routing")
		}
	}
	assert.Equal(t, 1, probeEligible, "cooldown expiry must grant exactly one request the half-open probe")
	// Hold all provider attempts open so the observation cannot count sequential
	// healthy calls as simultaneous probes.
	started := 0
	observationEnds := time.NewTimer(4 * time.Second)
observe:
	for {
		select {
		case <-provider.entered:
			started++
			if started == callers {
				break observe
			}
		case <-observationEnds.C:
			break observe
		}
	}
	observationEnds.Stop()
	close(provider.release)
	callersDone.Wait()
	close(store.reads)
	var bucket [sessionpin.SessionKeyLen]byte
	historyReads := 0
	for read := range store.reads {
		if read.role != hmmHistoryRole(roleForTier(catalog.TierFor(catalog.ModelGPT6Luna))) {
			continue
		}
		if bucket == ([sessionpin.SessionKeyLen]byte{}) {
			bucket = read.key
		}
		require.Equal(t, bucket, read.key, "all callers must address the same full session key")
		if read.role == hmmHistoryRole(roleForTier(catalog.TierFor(catalog.ModelGPT6Luna))) {
			historyReads++
		}
	}
	require.NotEqual(t, [sessionpin.SessionKeyLen]byte{}, bucket)
	require.GreaterOrEqual(t, historyReads, callers+1, "before/after routing must read the same history role")
	assert.Equal(t, 1, started, "cooldown expiry must grant one in-flight probe for the same session bucket")
}
