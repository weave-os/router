package proxy

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// overloadStubPinStore records overload-counter and provider-disable calls
// so each two-strike branch can be asserted independently; incrementNext
// drives the threshold directly, mirroring evictionStubPinStore.
type overloadStubPinStore struct {
	mu                sync.Mutex
	incrementCalls    int
	incrementNext     []int // values returned by IncrementOverloadErrors, in order
	resetCalls        int
	disabledProviders []string
	upserts           []sessionpin.Pin
}

func (s *overloadStubPinStore) Get(context.Context, [sessionpin.SessionKeyLen]byte, string) (sessionpin.Pin, bool, error) {
	return sessionpin.Pin{}, false, nil
}

func (s *overloadStubPinStore) Upsert(_ context.Context, p sessionpin.Pin) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.upserts = append(s.upserts, p)
	return nil
}

func (s *overloadStubPinStore) UpdateUsage(context.Context, [sessionpin.SessionKeyLen]byte, string, sessionpin.Usage) error {
	return nil
}

func (s *overloadStubPinStore) IncrementUpstreamErrors(context.Context, [sessionpin.SessionKeyLen]byte, string, router.Strategy) (int, error) {
	return 0, nil
}

func (s *overloadStubPinStore) ResetUpstreamErrors(context.Context, [sessionpin.SessionKeyLen]byte, string, router.Strategy) error {
	return nil
}

func (s *overloadStubPinStore) IncrementOverloadErrors(context.Context, [sessionpin.SessionKeyLen]byte, string, router.Strategy) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.incrementCalls++
	if len(s.incrementNext) == 0 {
		return 0, nil
	}
	v := s.incrementNext[0]
	s.incrementNext = s.incrementNext[1:]
	return v, nil
}

func (s *overloadStubPinStore) ResetOverloadErrors(context.Context, [sessionpin.SessionKeyLen]byte, string, router.Strategy) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resetCalls++
	return nil
}

func (s *overloadStubPinStore) DisableProvider(_ context.Context, _ [sessionpin.SessionKeyLen]byte, _, provider string, _ router.Strategy) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.disabledProviders = append(s.disabledProviders, provider)
	return nil
}

func (s *overloadStubPinStore) Consume(context.Context, [sessionpin.SessionKeyLen]byte, string, router.Strategy) (sessionpin.Pin, bool, error) {
	return sessionpin.Pin{}, false, nil
}

func (s *overloadStubPinStore) SweepExpired(context.Context) error { return nil }

func newOverloadTestService(store *overloadStubPinStore) *Service {
	return NewService(
		nil,
		nil,
		nil,
		false,
		nil,
		store,
		false,
		"anthropic", "claude-haiku-4-5",
		nil,
	)
}

// A single exhausted 529 must increment the counter but not disable
// the provider — two consecutive strikes are required.
func TestMaybeDisableProviderAfterOverload_FirstStrikeOnlyIncrements(t *testing.T) {
	store := &overloadStubPinStore{incrementNext: []int{1}}
	svc := newOverloadTestService(store)

	overloadErr := &providers.UpstreamErrorResponse{Status: providerOverloadedStatus}
	svc.maybeDisableProviderAfterOverload(
		context.Background(),
		true, // stickyHit
		overloadErr,
		"anthropic",
		"cluster:v0.57 model=claude-sonnet-5 provider=anthropic",
		uuid.New(),
		nonZeroSessionKey(),
		sessionpin.DefaultRole, sessionpin.DefaultRole,
	)

	assert.Equal(t, 1, store.incrementCalls, "first exhausted 529 must increment exactly once")
	assert.Equal(t, 0, store.resetCalls, "reset must not fire on a failed turn")
	assert.Empty(t, store.disabledProviders, "first strike must not disable the provider — waits for strike #2")
	assert.Empty(t, store.upserts, "first strike must not evict the pin")
}

// Two consecutive exhausted 529s on the same pinned provider disable it and
// evict the pin so the next turn re-routes.
func TestMaybeDisableProviderAfterOverload_SecondStrikeDisablesAndEvicts(t *testing.T) {
	store := &overloadStubPinStore{incrementNext: []int{providerOverloadStrikeThreshold}}
	svc := newOverloadTestService(store)

	overloadErr := &providers.UpstreamErrorResponse{Status: providerOverloadedStatus}
	installationID := uuid.New()
	sessionKey := nonZeroSessionKey()

	svc.maybeDisableProviderAfterOverload(
		context.Background(),
		true,
		overloadErr,
		"anthropic",
		"cluster:v0.57 model=claude-sonnet-5 provider=anthropic",
		installationID,
		sessionKey,
		sessionpin.DefaultRole, sessionpin.DefaultRole,
	)

	require.Len(t, store.disabledProviders, 1)
	assert.Equal(t, "anthropic", store.disabledProviders[0])
	require.Len(t, store.upserts, 2, "threshold strike must expire both the main pin and its HMM history row")
	expired := store.upserts[0]
	assert.Equal(t, sessionpin.DefaultRole, expired.Role)
	assert.Equal(t, installationID, expired.InstallationID)
	// Second upsert is the HMM history row.
	assert.Equal(t, hmmHistoryRole(sessionpin.DefaultRole), store.upserts[1].Role, "must expire the _hmm_history row too")
	assert.Empty(t, expired.Provider, "expired pin must clear provider so loadPin discards it")
	assert.Empty(t, expired.Model, "expired pin must clear model so loadPin discards it")
	assert.True(t, expired.PinnedUntil.Before(time.Now()),
		"PinnedUntil must be in the past so loadPin's expiry check discards the row")
	assert.Equal(t, "provider_overloaded", expired.Reason,
		"eviction reason is the audit trail that distinguishes this path from upstream_error_strike_threshold")
}

// A successful turn between two 529s must reset the counter, so strikes
// track consecutive failures, not lifetime ones.
func TestMaybeDisableProviderAfterOverload_SuccessResets(t *testing.T) {
	store := &overloadStubPinStore{}
	svc := newOverloadTestService(store)

	svc.maybeDisableProviderAfterOverload(
		context.Background(),
		true,
		nil, // success
		"anthropic",
		"cluster:v0.57 model=claude-sonnet-5 provider=anthropic",
		uuid.New(),
		nonZeroSessionKey(),
		sessionpin.DefaultRole, sessionpin.DefaultRole,
	)

	assert.Equal(t, 1, store.resetCalls, "successful turn on a sticky pin must clear the overload strike counter")
	assert.Equal(t, 0, store.incrementCalls)
	assert.Empty(t, store.disabledProviders)
	assert.Empty(t, store.upserts)
}

// Non-529 exhaustion (5xx/429/408) must not touch the overload counter;
// dispatchWithFallback's retry/failover already handles those.
func TestMaybeDisableProviderAfterOverload_NonOverloadStatusIgnored(t *testing.T) {
	for _, status := range []int{http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusBadRequest} {
		store := &overloadStubPinStore{incrementNext: []int{99}}
		svc := newOverloadTestService(store)

		svc.maybeDisableProviderAfterOverload(
			context.Background(),
			true,
			&providers.UpstreamErrorResponse{Status: status},
			"anthropic",
			"cluster:v0.57 model=claude-sonnet-5 provider=anthropic",
			uuid.New(),
			nonZeroSessionKey(),
			sessionpin.DefaultRole, sessionpin.DefaultRole,
		)

		assert.Zero(t, store.incrementCalls, "status %d is not a 529 and must not bump the overload counter", status)
		assert.Empty(t, store.disabledProviders, "status %d must not disable a provider", status)
	}
}

// A force-model'd session must never auto-strike its provider — that would
// silently revert the user's explicit choice; /unforce-model is the intended
// escape hatch.
func TestMaybeDisableProviderAfterOverload_UserForcedSkipped(t *testing.T) {
	for _, reason := range []string{translate.ReasonUserForceModel, translate.ReasonUserForceModel + "+tier_clamp"} {
		store := &overloadStubPinStore{incrementNext: []int{providerOverloadStrikeThreshold}}
		svc := newOverloadTestService(store)

		svc.maybeDisableProviderAfterOverload(
			context.Background(),
			true,
			&providers.UpstreamErrorResponse{Status: providerOverloadedStatus},
			"anthropic",
			reason,
			uuid.New(),
			nonZeroSessionKey(),
			sessionpin.DefaultRole, sessionpin.DefaultRole,
		)

		assert.Zero(t, store.incrementCalls, "user-forced pins (%q) must skip the counter increment", reason)
		assert.Zero(t, store.resetCalls)
		assert.Empty(t, store.disabledProviders, "user-forced pins must never be auto-struck (%q)", reason)
	}
}

// A freshly-scored turn (no sticky pin) has no prior decision to reconsider.
func TestMaybeDisableProviderAfterOverload_NoStickyHitSkipped(t *testing.T) {
	store := &overloadStubPinStore{incrementNext: []int{providerOverloadStrikeThreshold}}
	svc := newOverloadTestService(store)

	svc.maybeDisableProviderAfterOverload(
		context.Background(),
		false, // !stickyHit
		&providers.UpstreamErrorResponse{Status: providerOverloadedStatus},
		"anthropic",
		"cluster:v0.57 model=claude-sonnet-5 provider=anthropic",
		uuid.New(),
		nonZeroSessionKey(),
		sessionpin.DefaultRole, sessionpin.DefaultRole,
	)

	assert.Zero(t, store.incrementCalls)
	assert.Empty(t, store.disabledProviders)
	assert.Empty(t, store.upserts)
}

// Guards against a corrupted pin row shared by every zero-keyed caller.
func TestMaybeDisableProviderAfterOverload_ZeroSessionKeySkipped(t *testing.T) {
	store := &overloadStubPinStore{incrementNext: []int{providerOverloadStrikeThreshold}}
	svc := newOverloadTestService(store)

	svc.maybeDisableProviderAfterOverload(
		context.Background(),
		true,
		&providers.UpstreamErrorResponse{Status: providerOverloadedStatus},
		"anthropic",
		"cluster:v0.57 model=claude-sonnet-5 provider=anthropic",
		uuid.New(),
		[sessionpin.SessionKeyLen]byte{}, // zero key
		sessionpin.DefaultRole, sessionpin.DefaultRole,
	)

	assert.Zero(t, store.incrementCalls)
	assert.Empty(t, store.disabledProviders)
}

// Unauthenticated path (no installation_id) must no-op rather than write a
// uuid.Nil-installed row to Postgres.
func TestMaybeDisableProviderAfterOverload_NilInstallationSkipped(t *testing.T) {
	store := &overloadStubPinStore{incrementNext: []int{providerOverloadStrikeThreshold}}
	svc := newOverloadTestService(store)

	svc.maybeDisableProviderAfterOverload(
		context.Background(),
		true,
		&providers.UpstreamErrorResponse{Status: providerOverloadedStatus},
		"anthropic",
		"cluster:v0.57 model=claude-sonnet-5 provider=anthropic",
		uuid.Nil,
		nonZeroSessionKey(),
		sessionpin.DefaultRole, sessionpin.DefaultRole,
	)

	assert.Zero(t, store.incrementCalls)
	assert.Empty(t, store.disabledProviders)
}

// A generic transport/build/context-cancel error has no upstream status and
// is not a 529, so the overload counter must not advance.
func TestMaybeDisableProviderAfterOverload_NonUpstreamErrorIgnored(t *testing.T) {
	store := &overloadStubPinStore{incrementNext: []int{providerOverloadStrikeThreshold}}
	svc := newOverloadTestService(store)

	svc.maybeDisableProviderAfterOverload(
		context.Background(),
		true,
		errors.New("upstream call: dial tcp: connection refused"),
		"anthropic",
		"cluster:v0.57 model=claude-sonnet-5 provider=anthropic",
		uuid.New(),
		nonZeroSessionKey(),
		sessionpin.DefaultRole, sessionpin.DefaultRole,
	)

	assert.Zero(t, store.incrementCalls)
	assert.Empty(t, store.disabledProviders)
}
