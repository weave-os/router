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

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/hmm"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"
)

// policyDeadlineTestErr mirrors the real chain: policyclient wraps context.DeadlineExceeded with %w;
// SidecarRouter wraps that with hmm.ErrHMMUnavailable via %w:%w. Both must survive for errors.Is.
var policyDeadlineTestErr = fmt.Errorf(
	"hmm_embedding: sidecar decide: policy sidecar retries exhausted: %w: %w",
	context.DeadlineExceeded,
	hmm.ErrHMMUnavailable,
)

// policyContractViolationTestErr mirrors a contract violation (sidecar_router.go:367/370): wraps
// ErrHMMUnavailable without a deadline/cancel, so isPolicyDeadlineErr must return false.
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

func runPolicyDeadlineFallbackTurnLoop(
	t *testing.T,
	svc *Service,
	strategy router.Strategy,
	excludedModels ...string,
) (turnLoopResult, error) {
	t.Helper()
	env, err := translate.ParseAnthropic(
		[]byte(`{"model":"claude-opus-4-8","messages":[{"role":"user","content":"continue"}]}`),
	)
	require.NoError(t, err)
	features := env.RoutingFeatures(false)
	ctx := router.WithStrategy(context.Background(), strategy)
	var excluded map[string]struct{}
	if len(excludedModels) > 0 {
		excluded = make(map[string]struct{}, len(excludedModels))
		for _, model := range excludedModels {
			excluded[model] = struct{}{}
		}
	}
	req := router.Request{
		RequestedModel:       features.Model,
		EstimatedInputTokens: features.Tokens,
		HasTools:             features.HasTools,
		ConversationMessages: conversationMessagesForRouting(env),
		ExcludedModels:       excluded,
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
	assert.Equal(t, store.getPin.Reason, upserts[len(upserts)-1].Reason,
		"the persisted pin must keep its hmm_policy reason so later turns still see an HMM pin")
}

// TestTurnLoop_DeadlineFallbackDefaultExcludedFailsClosed proves the tier-3
// default honours this turn's exclusions instead of serving (and pinning) a
// model the request forbids.
func TestTurnLoop_DeadlineFallbackDefaultExcludedFailsClosed(t *testing.T) {
	strategy := router.Strategy("policy-deadline-fallback-default-excluded-test")
	const defaultModel = "claude-haiku-4-5"

	store := newStubPinStore()
	store.getFound = false

	svc := buildPolicyDeadlineFallbackService(t, strategy, policyDeadlineTestErr, store, true, defaultModel)

	_, err := runPolicyDeadlineFallbackTurnLoop(t, svc, strategy, defaultModel)

	require.Error(t, err, "an excluded tier-3 default must fail closed, not be served")
	assert.ErrorIs(t, err, hmm.ErrHMMUnavailable)

	store.mu.Lock()
	upserts := append([]sessionpin.Pin(nil), store.upserts...)
	store.mu.Unlock()
	assert.Empty(t, upserts, "an excluded model must never be persisted as a pin")
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

// TestTurnLoop_DeadlineFallbackContractViolationStillFailsClosed: contract violations must never
// degrade even with fallback enabled — serving one would write a wrong route ledger.
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
