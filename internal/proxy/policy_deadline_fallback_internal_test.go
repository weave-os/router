package proxy

import (
	"context"
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

// policyContractViolationTestErr preserves the classifier contract fault for recovery diagnostics.
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
	assert.Equal(t, policyRecoveryReason, result.Decision.Reason)
	assert.False(t, result.StickyHit)
	assert.True(t, result.PolicyFallback)
	assert.Equal(t, policyRecoveryReason, result.PinTier)

	store.mu.Lock()
	defer store.mu.Unlock()
	assert.Empty(t, store.upserts, "classification recovery must not persist an unserved target")
}

func TestTurnLoop_RecoverySkipsExcludedPreference(t *testing.T) {
	strategy := router.Strategy("policy-deadline-fallback-default-excluded-test")
	const defaultModel = "claude-haiku-4-5"

	store := newStubPinStore()
	store.getFound = false

	svc := buildPolicyDeadlineFallbackService(t, strategy, policyDeadlineTestErr, store, true, defaultModel)

	result, err := runPolicyDeadlineFallbackTurnLoop(t, svc, strategy, defaultModel)

	require.NoError(t, err)
	assert.NotEqual(t, defaultModel, result.Decision.Model)
	require.NotNil(t, result.Decision.Recovery)

	store.mu.Lock()
	upserts := append([]sessionpin.Pin(nil), store.upserts...)
	store.mu.Unlock()
	assert.Empty(t, upserts, "an excluded model must never be persisted as a pin")
}

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
	assert.Equal(t, policyRecoveryReason, result.Decision.Reason)
	assert.False(t, result.StickyHit, "the tier-3 default is a fresh pin, not a sticky reuse")
	assert.True(t, result.PolicyFallback)
	assert.Equal(t, policyRecoveryReason, result.PinTier)

	store.mu.Lock()
	upserts := append([]sessionpin.Pin(nil), store.upserts...)
	store.mu.Unlock()
	assert.Empty(t, upserts, "an unserved recovery decision must not become a session pin")
}

func TestTurnLoop_DeadlineFallbackNoPinNoDefault(t *testing.T) {
	strategy := router.Strategy("policy-deadline-fallback-no-default-test")

	store := newStubPinStore()
	store.getFound = false

	svc := buildPolicyDeadlineFallbackService(t, strategy, policyDeadlineTestErr, store, true, "")

	result, err := runPolicyDeadlineFallbackTurnLoop(t, svc, strategy)

	require.NoError(t, err)
	require.NotNil(t, result.Decision.Recovery)
	assert.NotEmpty(t, result.Decision.Model)
}

func TestRouteOnlyPreservesStrictEvaluationHeaders(t *testing.T) {
	for _, header := range []string{"", "x-weave-cluster-version", "x-weave-embed-only-user-message"} {
		t.Run(header, func(t *testing.T) {
			strategy := router.Strategy("route-recovery-contract")
			svc := buildPolicyDeadlineFallbackService(t, strategy, policyContractViolationTestErr, nil, true, "claude-haiku-4-5")
			headers := http.Header{}
			if header != "" {
				headers.Set(header, "synthetic")
			}
			decision, err := svc.RouteAnthropicRequest(router.WithStrategy(context.Background(), strategy), []byte(`{"model":"claude-opus-4-8","max_tokens":2048,"messages":[{"role":"user","content":"Synthetic task"}]}`), headers)
			if header != "" {
				assert.ErrorIs(t, err, hmm.ErrHMMUnavailable)
				assert.Nil(t, decision.Recovery)
			} else {
				require.NoError(t, err)
				assert.Equal(t, "claude-haiku-4-5", decision.Model)
				require.NotNil(t, decision.Recovery)
			}
		})
	}
}

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

func TestTurnLoop_ContractFailureUsesIndependentRecovery(t *testing.T) {
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

	result, err := runPolicyDeadlineFallbackTurnLoop(t, svc, strategy)

	require.NoError(t, err)
	assert.Equal(t, pinnedModel, result.Decision.Model)
	assert.Nil(t, result.Decision.Metadata, "recovery must not fabricate HMM metadata")
	require.NotNil(t, result.Decision.Recovery)
	assert.ErrorIs(t, result.Decision.Recovery.Failure, hmm.ErrHMMUnavailable)
}
