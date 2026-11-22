package proxy

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const accessOtherModel = "claude-opus-5"

func modelAccessError() error {
	return upstreamErr(http.StatusNotFound, `{"type":"error","error":{"type":"not_found_error","message":"model: `+parityAnthropicModel+`"}}`)
}

func TestSubscriptionModelAccessDoesNotPayAndRemembers(t *testing.T) {
	in := parityAnthropicIngress()
	for _, stream := range []bool{false, true} {
		t.Run(boolLit(stream), func(t *testing.T) {
			upstream := &parityUpstream{subErr: modelAccessError(), okBody: in.upstreamOK(stream)}
			svc := in.parityService(upstream)
			for i := 0; i < 2; i++ {
				rec, req, body := in.request(t, stream)
				require.ErrorIs(t, in.call(svc, billing.WithWeaveCreditsDenied(in.subCtx()), body, rec, req), credentials.ErrWeaveCreditsDisabled)
				assert.Empty(t, rec.Body.String(), "the handler owns rendering the unpaid refusal")
			}
			assert.Equal(t, 1, upstream.subDispatches)
			assert.Zero(t, upstream.paidDispatches, "a model denial is not quota exhaustion")
		})
	}
}

func TestSubscriptionModelAccessOpenAIIngressDoesNotPayAndRemembers(t *testing.T) {
	in := parityAnthropicIngress()
	upstream := &parityUpstream{subErr: modelAccessError(), okBody: in.upstreamOK(false)}
	svc := in.parityService(upstream)
	body := []byte(`{"model":"` + in.model + `","max_tokens":4096,"stream":false,"messages":[{"role":"user","content":"investigate the failing dispatch"}]}`)

	for range 2 {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
		require.ErrorIs(t, svc.ProxyOpenAIChatCompletion(billing.WithWeaveCreditsDenied(in.subCtx()), body, recorder, request), credentials.ErrWeaveCreditsDisabled)
		assert.Empty(t, recorder.Body.String(), "the handler owns rendering the unpaid refusal")
	}

	assert.Equal(t, 1, upstream.subDispatches)
	assert.Zero(t, upstream.paidDispatches, "a model denial is not quota exhaustion")
}

func TestSubscriptionModelAccessPreservesFailedAttemptAttribution(t *testing.T) {
	in := parityAnthropicIngress()
	upstream := &parityUpstream{subErr: modelAccessError(), paidErr: modelAccessError()}
	svc := in.parityService(upstream)
	telemetry := &auxTelemetryRepo{}
	svc.turnTelemetry.repo = telemetry
	rec, req, body := in.request(t, false)
	require.ErrorIs(t, in.call(svc, billing.WithWeaveCreditsDenied(in.subCtx()), body, rec, req), credentials.ErrWeaveCreditsDisabled)
	assert.Empty(t, rec.Body.String(), "blocked funding must not commit a success response")
	assert.Equal(t, 1, upstream.subDispatches)
	assert.Zero(t, upstream.paidDispatches)
	// Learning a denial must not retroactively change the credential of the failed attempt.
	rows := telemetry.waitForRows(1)
	require.Len(t, rows, 1)
	assert.Equal(t, credSourceSubscription, rows[0].CredentialSource)
}

func TestSubscriptionModelAccessDoesNotSpendWithoutPermission(t *testing.T) {
	in := parityAnthropicIngress()
	for _, subscriptionOnly := range []bool{false, true} {
		t.Run(boolLit(subscriptionOnly), func(t *testing.T) {
			upstream := &parityUpstream{subErr: modelAccessError(), okBody: in.upstreamOK(false)}
			svc := in.parityService(upstream)
			ctx := billing.WithWeaveCreditsDenied(in.subCtx())
			if subscriptionOnly {
				ctx = billing.WithSubscriptionOnly(ctx, billing.SubscriptionOnlyCreditsDepleted)
			} else {
				svc.WithDeploymentKeyedProviders(map[string]struct{}{})
			}
			rec, req, body := in.request(t, false)
			require.Error(t, in.call(svc, ctx, body, rec, req))
			assert.Equal(t, http.StatusNotFound, rec.Code)
			assert.Zero(t, upstream.paidDispatches)
		})
	}
}

func TestSubscriptionModelAccessScopeAndExpiry(t *testing.T) {
	in := parityAnthropicIngress()
	svc := in.deploymentKeyedService()
	now := time.Unix(1_700_000_000, 0)
	svc.clock = func() time.Time { return now }
	svc.recordSubscriptionModelRejection(in.resolved(billing.WithWeaveCreditsDenied(in.subCtx())), in.provider, in.model, modelAccessError())
	resolved := svc.resolveCredentials(billing.WithWeaveCreditsDenied(in.subCtx()), in.provider, in.model, nil)
	assert.Nil(t, CredentialsFromContext(resolved))
	assert.Nil(t, CredentialsFromContext(resolveAndInjectCredentials(resolved, in.provider, in.model, nil)))
	assert.True(t, servedOnSubscription(svc.resolveCredentials(resolved, in.provider, accessOtherModel, nil)))
	assert.True(t, servedOnSubscription(svc.resolveCredentials(context.WithValue(billing.WithWeaveCreditsDenied(in.subCtx()), credentials.AnthropicSubscriptionContextKey{}, "sk-ant-oat01-other"), in.provider, in.model, nil)))
	assert.False(t, servedOnSubscription(svc.resolveCredentials(billing.WithSubscriptionOnly(billing.WithWeaveCreditsDenied(in.subCtx()), billing.SubscriptionOnlyCreditsDepleted), in.provider, in.model, nil)), "known denied OAuth must never be replayed")
	linkedFirstContext := billing.WithSubscriptionOnly(billing.WithWeaveCreditsDenied(in.subCtx()), billing.SubscriptionOnlyLinkedFirst)
	assert.False(t, servedOnSubscription(svc.resolveCredentials(linkedFirstContext, in.provider, in.model, nil)), "a denied linked subscription must not be replayed")
	now = now.Add(16 * time.Minute)
	assert.True(t, servedOnSubscription(svc.resolveCredentials(billing.WithWeaveCreditsDenied(in.subCtx()), in.provider, in.model, nil)), "access changes are rechecked after expiry")
}

func TestSubscriptionModelAccessOnlyLearnsModelRejection(t *testing.T) {
	in := parityAnthropicIngress()
	for _, err := range []error{
		upstreamErr(http.StatusNotFound, `{"error":{"type":"not_found_error","message":"unknown endpoint"}}`),
		upstreamErr(http.StatusNotFound, `<html>Not Found</html>`),
		upstreamErr(http.StatusForbidden, `{"error":{"type":"permission_error","message":"model: unavailable"}}`),
		upstreamErr(http.StatusTooManyRequests, `{"error":{"type":"rate_limit_error"}}`),
		nil,
	} {
		svc := in.deploymentKeyedService()
		svc.recordSubscriptionModelRejection(in.resolved(billing.WithWeaveCreditsDenied(in.subCtx())), in.provider, in.model, err)
		assert.True(t, servedOnSubscription(svc.resolveCredentials(billing.WithWeaveCreditsDenied(in.subCtx()), in.provider, in.model, nil)))
	}
	svc := in.deploymentKeyedService()
	svc.recordSubscriptionModelRejection(in.resolved(in.byokCtx(context.Background())), in.provider, in.model, modelAccessError())
	assert.True(t, servedOnSubscription(svc.resolveCredentials(billing.WithWeaveCreditsDenied(in.subCtx()), in.provider, in.model, nil)))
}

func TestSubscriptionModelAccessLearnsServedModelsPerSubscription(t *testing.T) {
	in := parityAnthropicIngress()
	for _, stream := range []bool{false, true} {
		t.Run(boolLit(stream), func(t *testing.T) {
			svc := in.parityService(&parityUpstream{okBody: in.upstreamOK(stream)})
			rec, req, body := in.request(t, stream)
			require.NoError(t, in.call(svc, billing.WithWeaveCreditsDenied(in.subCtx()), body, rec, req))

			now := time.Now()
			assert.Equal(t, []string{in.model}, svc.credentials.SubscriptionModels.ServedForCaller(CredentialsFromContext(in.resolved(billing.WithWeaveCreditsDenied(in.subCtx()))), now))
			other := context.WithValue(billing.WithWeaveCreditsDenied(in.subCtx()), credentials.AnthropicSubscriptionContextKey{}, "sk-ant-oat01-other")
			assert.Empty(t, svc.credentials.SubscriptionModels.ServedForCaller(CredentialsFromContext(in.resolved(other)), now), "served models are per subscription")
			assert.Empty(t, svc.credentials.SubscriptionModels.ServedForCaller(CredentialsFromContext(in.resolved(billing.WithWeaveCreditsDenied(in.subCtx()))), now.Add(credentials.SubscriptionModelServedTTL+time.Minute)), "served models expire")
		})
	}
}

func TestSubscriptionModelAccessFollowsTheWinningCredentialOfAFinishedManagedTurn(t *testing.T) {
	now := time.Now()
	svc := &Service{clock: func() time.Time { return now }}
	requested := &Credentials{APIKey: []byte("sk-ant-oat01-requested"), OAuth: true}
	winning := &Credentials{APIKey: []byte("sk-ant-oat01-winning"), OAuth: true}
	ctx := context.WithValue(context.Background(), CredentialsContextKey{}, requested)
	ctx = context.WithValue(ctx, credentials.ManagedSubscriptionUsageContextKey{}, &credentials.ManagedSubscriptionUsage{Finished: true, WinningCredentials: winning})

	recordSubscriptionModelOutcome(svc, ctx, providers.ProviderAnthropic, "claude-sonnet-4-5", nil)

	assert.Equal(t, []string{"claude-sonnet-4-5"}, svc.credentials.SubscriptionModels.ServedForCaller(winning, now))
	assert.Empty(t, svc.credentials.SubscriptionModels.ServedForCaller(requested, now), "the served model belongs to the credential that served the turn")
}

func finishedManagedTurnOnAnotherCredential() (ctx context.Context, requested, winning *Credentials) {
	requested = &Credentials{APIKey: []byte("sk-ant-oat01-requested"), OAuth: true}
	winning = &Credentials{APIKey: []byte("sk-ant-oat01-winning"), OAuth: true}
	ctx = context.WithValue(context.Background(), CredentialsContextKey{}, requested)
	ctx = context.WithValue(ctx, credentials.ManagedSubscriptionUsageContextKey{}, &credentials.ManagedSubscriptionUsage{Finished: true, WinningCredentials: winning})
	return ctx, requested, winning
}

type blankPurposePlan struct{ inference.ResolvedPlan }

func (blankPurposePlan) Purpose() inference.Purpose { return inference.Purpose("") }

func TestLeasedProviderAttemptMarksTheModelServedForTheWinningCredential(t *testing.T) {
	now := time.Now()
	svc := &Service{clock: func() time.Time { return now }}
	ctx, requested, winning := finishedManagedTurnOnAnotherCredential()
	succeedingAttempt := func(context.Context, router.Decision, dispatch.Upstream) error { return nil }

	_, err := invokeLeasedProviderAttempt(svc, ctx, blankPurposePlan{}, leasedProviderAttempt{
		inputs:   dispatch.FailoverInputs{Attempt: succeedingAttempt},
		decision: router.Decision{Provider: providers.ProviderAnthropic, Model: "claude-sonnet-5-5"},
	})

	require.NoError(t, err)
	assert.Equal(t, []string{"claude-sonnet-5-5"}, svc.credentials.SubscriptionModels.ServedForCaller(winning, now))
	assert.Empty(t, svc.credentials.SubscriptionModels.ServedForCaller(requested, now), "the served model belongs to the credential that served the turn")
}

func TestClassifierRescuePolicyReadsServedAndDeniedModelsOfTheWinningCredential(t *testing.T) {
	now := time.Now()
	svc := &Service{clock: func() time.Time { return now }}
	ctx, requested, winning := finishedManagedTurnOnAnotherCredential()
	access := &svc.credentials.SubscriptionModels
	access.MarkServedForCaller(winning, providers.ProviderAnthropic, "claude-sonnet-5-5", now)
	access.MarkServedForCaller(requested, providers.ProviderAnthropic, "claude-haiku-5-5", now)
	access.Deny(winning.APIKey, "claude-opus-5-5", now.Add(time.Hour))

	policy := newClassifierRescuePolicy(ctx, svc)

	assert.Equal(t, []string{"claude-sonnet-5-5"}, policy.models[1:], "rescue candidates come from the credential that served the turn")
	assert.True(t, policy.unavailable("claude-opus-5-5"), "a model denied to the winning credential is skipped")
}
