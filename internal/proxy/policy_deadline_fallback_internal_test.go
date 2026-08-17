package proxy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"workweave/router/internal/providers"
	"workweave/router/internal/router"
	"workweave/router/internal/router/hmm"
	"workweave/router/internal/router/policy"
	"workweave/router/internal/router/sessionpin"
	"workweave/router/internal/translate"
)

// policyDeadlineTestErr mirrors the real error chain a deadline-exceeded
// sidecar call produces: policyclient wraps context.DeadlineExceeded with %w,
// SidecarRouter.Decide wraps that with hmm.ErrHMMUnavailable via %w: %w (see
// internal/router/policy/sidecar_router.go:311). Both sentinels must survive
// via errors.Is for isPolicyDeadlineErr to see them.
var policyDeadlineTestErr = fmt.Errorf(
	"hmm_embedding: sidecar decide: policy sidecar retries exhausted: %w: %w",
	context.DeadlineExceeded,
	hmm.ErrHMMUnavailable,
)

// policyContractViolationTestErr mirrors a genuine contract violation
// (sidecar_router.go:367/370) — also wraps hmm.ErrHMMUnavailable, but without
// a deadline/cancel in the chain. isPolicyDeadlineErr must return false for
// this so contract violations still fail closed.
var policyContractViolationTestErr = fmt.Errorf(
	"hmm_embedding: sidecar returned unknown arm %q or model %q: %w",
	"bogus-arm", "bogus-model",
	hmm.ErrHMMUnavailable,
)

// erroringTestRouter always returns a fixed error, simulating a policy
// sidecar deadline/transport failure or contract violation.
type erroringTestRouter struct {
	err error
}

func (r *erroringTestRouter) Route(_ context.Context, _ router.Request) (router.Decision, error) {
	return router.Decision{}, r.err
}

func TestIsPolicyDeadlineErr(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "deadline exceeded wrapping ErrHMMUnavailable",
			err:  policyDeadlineTestErr,
			want: true,
		},
		{
			name: "context canceled wrapping ErrHMMUnavailable",
			err: fmt.Errorf("hmm_embedding: sidecar decide: %w: %w",
				context.Canceled, hmm.ErrHMMUnavailable),
			want: true,
		},
		{
			name: "contract violation (unknown arm) must still fail closed",
			err:  policyContractViolationTestErr,
			want: false,
		},
		{
			name: "contract violation (provider mismatch) must still fail closed",
			err: fmt.Errorf("hmm_embedding: sidecar returned provider %q for %q, expected %q: %w",
				"openai", "claude-opus-4-7", "anthropic", hmm.ErrHMMUnavailable),
			want: false,
		},
		{
			name: "ErrHMMUnavailable without a deadline/cancel is not a deadline error",
			err:  fmt.Errorf("hmm_embedding: sidecar unavailable: %w", hmm.ErrHMMUnavailable),
			want: false,
		},
		{
			name: "unrelated error",
			err:  errors.New("boom"),
			want: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := isPolicyDeadlineErr(test.err)
			assert.Equal(t, test.want, got)
		})
	}
}

// buildPolicyDeadlineFallbackService constructs a *Service wired with an
// erroring policy strategy so runTurnLoop's routeFor call fails with err.
func buildPolicyDeadlineFallbackService(
	t *testing.T,
	strategy router.Strategy,
	err error,
	store sessionpin.Store,
	fallbackEnabled bool,
	defaultModel string,
) *Service {
	t.Helper()
	return NewService(
		nil,
		map[string]providers.Client{providers.ProviderAnthropic: nil},
		nil,
		false,
		nil,
		store,
		false,
		providers.ProviderAnthropic,
		"claude-haiku-4-5",
		nil,
	).WithPolicyDeadlineFallback(fallbackEnabled).
		WithPolicyDeadlineDefaultModel(defaultModel).
		WithPolicyStrategy(policy.StrategySpec{
			Strategy: strategy,
			Router:   &erroringTestRouter{err: err},
			Capabilities: policy.Capabilities{
				SchemaVersion: policy.SchemaVersionV1,
			},
		})
}

func runPolicyDeadlineFallbackTurnLoop(t *testing.T, svc *Service, strategy router.Strategy) (turnLoopResult, error) {
	t.Helper()
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
	return svc.runTurnLoop(ctx, env, features, "api-key", uuid.New(), "", http.Header{}, req)
}

// TestTurnLoop_DeadlineFallbackToPin covers T5: with a pin present and the
// fallback enabled, a deadline error degrades to the pin instead of a 503.
func TestTurnLoop_DeadlineFallbackToPin(t *testing.T) {
	strategy := router.Strategy("policy-deadline-fallback-pin-test")
	const pinnedModel = "claude-sonnet-4-6"
	const pinnedProvider = providers.ProviderAnthropic

	store := newStubPinStore()
	store.getFound = true
	store.getPin = sessionpin.Pin{
		Provider:        pinnedProvider,
		Model:           pinnedModel,
		Reason:          "hmm_policy(classifier 'mid' (p=0.55))",
		PolicyGroup:     "mid",
		PinnedUntil:     time.Now().Add(time.Hour),
		LastTurnEndedAt: time.Now().Add(-time.Minute),
		LastServedModel: pinnedModel,
	}

	svc := buildPolicyDeadlineFallbackService(t, strategy, policyDeadlineTestErr, store, true, "")

	result, err := runPolicyDeadlineFallbackTurnLoop(t, svc, strategy)

	require.NoError(t, err, "a policy deadline miss with a pin present must serve, not error")
	assert.Equal(t, pinnedModel, result.Decision.Model)
	assert.Equal(t, pinnedProvider, result.Decision.Provider)
	assert.Equal(t, policyDeadlineFallbackReason, result.Decision.Reason)
	assert.True(t, result.StickyHit)
	assert.True(t, result.PolicyFallback)
	assert.Equal(t, policyDeadlineFallbackReason, result.PinTier)

	// The pin must be refreshed, not silently left to expire.
	store.mu.Lock()
	upserts := append([]sessionpin.Pin(nil), store.upserts...)
	store.mu.Unlock()
	require.NotEmpty(t, upserts)
	assert.Equal(t, pinnedModel, upserts[len(upserts)-1].Model)
}

// TestTurnLoop_DeadlineFallbackToTierThreeDefault covers the pinless branch:
// no session pin yet, but a tier-3 static default model is configured.
func TestTurnLoop_DeadlineFallbackToTierThreeDefault(t *testing.T) {
	strategy := router.Strategy("policy-deadline-fallback-default-test")
	const defaultModel = "claude-haiku-4-5"

	store := newStubPinStore()
	store.getFound = false // no pin: session start

	svc := buildPolicyDeadlineFallbackService(t, strategy, policyDeadlineTestErr, store, true, defaultModel)

	result, err := runPolicyDeadlineFallbackTurnLoop(t, svc, strategy)

	require.NoError(t, err, "a policy deadline miss with a configured tier-3 default must serve, not error")
	assert.Equal(t, defaultModel, result.Decision.Model)
	assert.Equal(t, providers.ProviderAnthropic, result.Decision.Provider)
	assert.Equal(t, policyDeadlineDefaultReason, result.Decision.Reason)
	assert.False(t, result.StickyHit, "the tier-3 default is a fresh pin, not a sticky reuse")
	assert.True(t, result.PolicyFallback)
	assert.Equal(t, policyDeadlineFallbackReason, result.PinTier)

	store.mu.Lock()
	upserts := append([]sessionpin.Pin(nil), store.upserts...)
	store.mu.Unlock()
	require.NotEmpty(t, upserts, "the tier-3 default decision must be written as a new pin")
	assert.Equal(t, defaultModel, upserts[len(upserts)-1].Model)
}

// TestTurnLoop_DeadlineFallbackNoPinNoDefault covers the last rung: no pin,
// no tier-3 default configured — must still fail closed with a 503-mapping error.
func TestTurnLoop_DeadlineFallbackNoPinNoDefault(t *testing.T) {
	strategy := router.Strategy("policy-deadline-fallback-no-default-test")

	store := newStubPinStore()
	store.getFound = false

	svc := buildPolicyDeadlineFallbackService(t, strategy, policyDeadlineTestErr, store, true, "")

	_, err := runPolicyDeadlineFallbackTurnLoop(t, svc, strategy)

	require.Error(t, err, "no pin and no tier-3 default must preserve the 503 (fail closed)")
	assert.ErrorIs(t, err, hmm.ErrHMMUnavailable)
}

// TestTurnLoop_DeadlineFallbackKillSwitchOff proves ROUTER_POLICY_DEADLINE_FALLBACK=false
// preserves the 503 even with a pin present.
func TestTurnLoop_DeadlineFallbackKillSwitchOff(t *testing.T) {
	strategy := router.Strategy("policy-deadline-fallback-killswitch-test")
	const pinnedModel = "claude-sonnet-4-6"

	store := newStubPinStore()
	store.getFound = true
	store.getPin = sessionpin.Pin{
		Provider:        providers.ProviderAnthropic,
		Model:           pinnedModel,
		Reason:          "hmm_policy(classifier 'mid' (p=0.55))",
		PolicyGroup:     "mid",
		PinnedUntil:     time.Now().Add(time.Hour),
		LastTurnEndedAt: time.Now().Add(-time.Minute),
		LastServedModel: pinnedModel,
	}

	svc := buildPolicyDeadlineFallbackService(t, strategy, policyDeadlineTestErr, store, false, "")

	_, err := runPolicyDeadlineFallbackTurnLoop(t, svc, strategy)

	require.Error(t, err, "kill switch off must preserve the 503 even with a pin present")
	assert.ErrorIs(t, err, hmm.ErrHMMUnavailable)
}

// TestTurnLoop_DeadlineFallbackContractViolationStillFailsClosed is the
// required regression: a contract violation (not a deadline/transport
// failure) must never degrade, even with the fallback enabled and a pin
// present. Serving on a contract violation would write a wrong route ledger.
func TestTurnLoop_DeadlineFallbackContractViolationStillFailsClosed(t *testing.T) {
	strategy := router.Strategy("policy-deadline-fallback-contract-violation-test")
	const pinnedModel = "claude-sonnet-4-6"

	store := newStubPinStore()
	store.getFound = true
	store.getPin = sessionpin.Pin{
		Provider:        providers.ProviderAnthropic,
		Model:           pinnedModel,
		Reason:          "hmm_policy(classifier 'mid' (p=0.55))",
		PolicyGroup:     "mid",
		PinnedUntil:     time.Now().Add(time.Hour),
		LastTurnEndedAt: time.Now().Add(-time.Minute),
		LastServedModel: pinnedModel,
	}

	svc := buildPolicyDeadlineFallbackService(t, strategy, policyContractViolationTestErr, store, true, "claude-haiku-4-5")

	_, err := runPolicyDeadlineFallbackTurnLoop(t, svc, strategy)

	require.Error(t, err, "a contract violation must fail closed even with fallback enabled, a pin, and a tier-3 default")
	assert.ErrorIs(t, err, hmm.ErrHMMUnavailable)
}
