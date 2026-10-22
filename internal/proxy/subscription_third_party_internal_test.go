package proxy

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/subscriptions"
	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// thirdPartyRefusalBody is the verbatim body Anthropic returned on 2026-10-09
// for OpenCode turns leased onto a linked Claude Max account.
const thirdPartyRefusalBody = `{"type":"error","error":{"type":"invalid_request_error","message":"Third-party apps now draw from your extra usage, not your plan limits. Add more at claude.ai/settings/usage and keep going."}}`

func thirdPartyRefusalError() error {
	return upstreamErr(http.StatusBadRequest, thirdPartyRefusalBody)
}

func clientAppContext(ctx context.Context, clientApp string) context.Context {
	return context.WithValue(ctx, ClientIdentityContextKey{}, ClientIdentity{ClientApp: clientApp})
}

func TestAnthropicSubscriptionThirdPartyRefused(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "verbatim refusal", err: thirdPartyRefusalError(), want: true},
		{
			name: "refusal wrapped by dispatch",
			err:  errors.Join(errors.New("dispatch"), thirdPartyRefusalError()),
			want: true,
		},
		{
			name: "unrelated 400 invalid_request_error stays terminal",
			err:  upstreamErr(http.StatusBadRequest, `{"type":"error","error":{"type":"invalid_request_error","message":"messages: at least one message is required"}}`),
			want: false,
		},
		{
			name: "same message on a different status is not this refusal",
			err:  upstreamErr(http.StatusForbidden, `{"type":"error","error":{"type":"invalid_request_error","message":"Third-party apps now draw from your extra usage"}}`),
			want: false,
		},
		{
			name: "same message under a different error type is not this refusal",
			err:  upstreamErr(http.StatusBadRequest, `{"type":"error","error":{"type":"api_error","message":"Third-party apps now draw from your extra usage"}}`),
			want: false,
		},
		{name: "unparseable body", err: upstreamErr(http.StatusBadRequest, `not json`), want: false},
		{name: "transport error", err: errors.New("connection reset"), want: false},
		{name: "nil", err: nil, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, anthropicSubscriptionThirdPartyRefused(tc.err))
		})
	}
}

func dispatchManagedThirdPartyTurn(t *testing.T, svc *Service, ctx context.Context) (*httptest.ResponseRecorder, error) {
	t.Helper()
	return dispatchManagedThirdPartyTurnWithReason(t, svc, ctx, "")
}

func dispatchManagedThirdPartyTurnWithReason(t *testing.T, svc *Service, ctx context.Context, reason string) (*httptest.ResponseRecorder, error) {
	t.Helper()
	recorder := httptest.NewRecorder()
	buffer := newPreludeBuffer(recorder)
	_, err := svc.dispatchWithFallback(ctx, failoverInputs{
		w:               recorder,
		buf:             buffer,
		initialDecision: router.Decision{Model: parityAnthropicModel, Provider: providers.ProviderAnthropic, Reason: reason},
		purpose:         inference.PurposeAnthropicMessages,
		bindings:        []catalog.ProviderBinding{{Provider: providers.ProviderAnthropic}},
		attempt: func(ctx context.Context, decision router.Decision, client providers.Client) error {
			buffer.Seal()
			return client.Proxy(ctx, decision, providers.PreparedRequest{}, buffer, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
		},
	})
	return recorder, err
}

func TestDispatchWithFallbackDoesNotPayForManagedThirdPartyRefusal(t *testing.T) {
	leaser := &scriptedSubscriptionLeaser{
		leases:     []subscriptions.Lease{{AccountID: "opaque-a", AccessToken: "token-a"}},
		repeatLast: true,
	}
	upstream := &parityUpstream{subErr: thirdPartyRefusalError(), okBody: "served"}
	svc := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderAnthropic: upstream}).
		WithManagedSubscriptions(leaser).
		WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAnthropic: {}})
	svc.retrySleep = noopSleep

	for range 2 {
		recorder, err := dispatchManagedThirdPartyTurn(t, svc, clientAppContext(managedSubscriptionTestContext(), ClientAppOpencode))
		require.Error(t, err)
		assert.NotEqual(t, "served", recorder.Body.String())
	}

	assert.Equal(t, 1, upstream.subDispatches, "the refused account is skipped for this client app on later turns")
	assert.Zero(t, upstream.paidDispatches, "a client-specific refusal does not prove the account quota is exhausted")
	assert.Empty(t, leaser.disabledIDs, "a third-party refusal must not mark the account as needing reconnect")
	assert.Empty(t, leaser.cooldownIDs, "a third-party refusal must not cool the account down for every client")
}

func TestDispatchWithFallbackKeepsManagedThirdPartyRefusalClientSpecific(t *testing.T) {
	leaser := &scriptedSubscriptionLeaser{
		leases:     []subscriptions.Lease{{AccountID: "opaque-a", AccessToken: "token-a"}},
		repeatLast: true,
	}
	upstream := &parityUpstream{subErr: thirdPartyRefusalError(), okBody: "served"}
	svc := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderAnthropic: upstream}).
		WithManagedSubscriptions(leaser).
		WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAnthropic: {}})
	svc.retrySleep = noopSleep

	_, err := dispatchManagedThirdPartyTurn(t, svc, clientAppContext(managedSubscriptionTestContext(), ClientAppOpencode))
	require.Error(t, err)
	require.Equal(t, 1, upstream.subDispatches)

	upstream.subErr = nil
	claudeCodeCtx := clientAppContext(managedSubscriptionTestContext(), ClientAppClaudeCode)
	recorder, err := dispatchManagedThirdPartyTurn(t, svc, claudeCodeCtx)
	require.NoError(t, err)
	assert.Equal(t, "served", recorder.Body.String())
	assert.Equal(t, 2, upstream.subDispatches, "Claude Code still leases the account OpenCode was refused on")
	assert.Zero(t, upstream.paidDispatches)
	assert.True(t, servedOnSubscription(claudeCodeCtx))
}

func TestDispatchWithFallbackSurfacesManagedThirdPartyRefusalWithoutFallbackKey(t *testing.T) {
	leaser := &scriptedSubscriptionLeaser{
		leases:     []subscriptions.Lease{{AccountID: "opaque-a", AccessToken: "token-a"}},
		repeatLast: true,
	}
	upstream := &parityUpstream{subErr: thirdPartyRefusalError(), okBody: "served"}
	svc := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderAnthropic: upstream}).
		WithManagedSubscriptions(leaser)
	svc.retrySleep = noopSleep

	_, err := dispatchManagedThirdPartyTurn(t, svc, clientAppContext(managedSubscriptionTestContext(), ClientAppOpencode))

	require.Error(t, err)
	assert.Equal(t, 1, upstream.subDispatches)
	assert.Zero(t, upstream.paidDispatches, "no paid credential exists to fail over to")
}

func TestSubscriptionThirdPartyRefusalDoesNotPayForInboundSubscription(t *testing.T) {
	in := parityAnthropicIngress()
	for _, stream := range []bool{false, true} {
		t.Run(boolLit(stream), func(t *testing.T) {
			upstream := &parityUpstream{subErr: thirdPartyRefusalError(), okBody: in.upstreamOK(stream)}
			svc := in.parityService(upstream)
			rec, req, body := in.request(t, stream)
			require.Error(t, in.call(svc, in.subCtx(), body, rec, req))
			assert.NotContains(t, rec.Body.String(), "output_tokens")
			assert.Equal(t, 1, upstream.subDispatches)
			assert.Zero(t, upstream.paidDispatches)
		})
	}
}

func TestSubscriptionThirdPartyRefusalDoesNotPayForOpenAIIngress(t *testing.T) {
	in := parityAnthropicIngress()
	upstream := &parityUpstream{subErr: thirdPartyRefusalError(), okBody: in.upstreamOK(false)}
	svc := in.parityService(upstream)
	body := []byte(`{"model":"` + in.model + `","max_tokens":4096,"stream":false,"messages":[{"role":"user","content":"investigate the failing dispatch"}]}`)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))

	require.Error(t, svc.ProxyOpenAIChatCompletion(in.subCtx(), body, recorder, request))

	assert.Equal(t, 1, upstream.subDispatches)
	assert.Zero(t, upstream.paidDispatches)
}

func TestDispatchWithFallbackRejectsPaidFallbackForForcedThirdPartyRefusal(t *testing.T) {
	leaser := &scriptedSubscriptionLeaser{
		leases:     []subscriptions.Lease{{AccountID: "opaque-a", AccessToken: "token-a"}},
		repeatLast: true,
	}
	upstream := &parityUpstream{subErr: thirdPartyRefusalError(), okBody: "served"}
	svc := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderAnthropic: upstream}).
		WithManagedSubscriptions(leaser).
		WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAnthropic: {}})
	svc.retrySleep = noopSleep

	for range 2 {
		_, err := dispatchManagedThirdPartyTurnWithReason(t, svc, clientAppContext(managedSubscriptionTestContext(), ClientAppOpencode), translate.ReasonUserForceModel)
		require.ErrorIs(t, err, ErrSubscriptionClientAppRefused)
		cls, ok := ClassifyDispatchError(err)
		require.True(t, ok)
		assert.Equal(t, http.StatusForbidden, cls.Status)
	}

	assert.Equal(t, 1, upstream.subDispatches, "the refused account is skipped for this client app on later turns")
	assert.Zero(t, upstream.paidDispatches, "a forced Claude turn must not bill paid API for a refused third-party client")
}

func TestRotationBudgetPaidRescueRejectsForcedThirdPartyRefusal(t *testing.T) {
	leaser := &scriptedSubscriptionLeaser{leases: []subscriptions.Lease{{AccountID: "opaque-a", AccessToken: "token-a"}}}
	svc := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderAnthropic: &fakeClient{name: providers.ProviderAnthropic}}).
		WithManagedSubscriptions(leaser).
		WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAnthropic: {}})
	ctx := expiredRotationContext(auth.SubscriptionProviderClaude)
	markSubscriptionClientAppRefused(ctx)
	rec := httptest.NewRecorder()
	buf := newPreludeBuffer(rec)
	in := plannedInputs(rec, buf, []catalog.ProviderBinding{{Provider: providers.ProviderAnthropic}}, nil)
	in.initialDecision = router.Decision{Provider: providers.ProviderAnthropic, Model: parityAnthropicModel, Reason: translate.ReasonUserForceModel}
	var apiCalls int
	in.attempt = func(attemptCtx context.Context, _ router.Decision, _ providers.Client) error {
		if creds := CredentialsFromContext(attemptCtx); creds == nil || !creds.OAuth {
			apiCalls++
		}
		return nil
	}

	_, err := svc.dispatchWithFallback(ctx, in)

	require.ErrorIs(t, err, ErrSubscriptionClientAppRefused)
	assert.Zero(t, apiCalls, "the rotation-budget paid rescue must not serve a forced Claude turn the subscriptions refused")
}

// admittedThirdPartyRefusalContext admits the one linked seat the leaser hands
// out, so paid authorization judges that seat rather than a missing snapshot.
func admittedThirdPartyRefusalContext(paidFallbackConsented bool) context.Context {
	return admittedClientRefusalContext(paidFallbackConsented, ClientAppOpencode)
}

func admittedClientRefusalContext(paidFallbackConsented bool, clientApp string) context.Context {
	ctx := WithManagedSubscriptionCandidates(managedSubscriptionTestContext(), []*auth.SubscriptionAccount{{
		ID: "opaque-a", Provider: auth.SubscriptionProviderClaude, Enabled: true, State: auth.SubscriptionAccountStateActive,
	}})
	if paidFallbackConsented {
		ctx = context.WithValue(ctx, APIKeyPaidFallbackOnSubscriptionRefusalContextKey{}, true)
	}
	return clientAppContext(ctx, clientApp)
}

func newThirdPartyRefusalService(t *testing.T) (*Service, *parityUpstream) {
	t.Helper()
	leaser := &scriptedSubscriptionLeaser{
		leases:     []subscriptions.Lease{{AccountID: "opaque-a", AccessToken: "token-a"}},
		repeatLast: true,
	}
	upstream := &parityUpstream{subErr: thirdPartyRefusalError(), okBody: "served"}
	svc := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderAnthropic: upstream}).
		WithManagedSubscriptions(leaser).
		WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAnthropic: {}})
	svc.retrySleep = noopSleep
	return svc, upstream
}

func TestForcedThirdPartyRefusalPaysOnlyWhenKeyConsents(t *testing.T) {
	t.Run("consenting key is served on paid capacity", func(t *testing.T) {
		svc, upstream := newThirdPartyRefusalService(t)

		for turn := range 2 {
			recorder, err := dispatchManagedThirdPartyTurnWithReason(t, svc, admittedThirdPartyRefusalContext(true), translate.ReasonUserForceModel)
			require.NoError(t, err)
			assert.Equal(t, "served", recorder.Body.String())
			assert.Equal(t, turn+1, upstream.paidDispatches)
		}
		assert.Equal(t, 1, upstream.subDispatches, "the refused seat stays skipped for this client app")
	})

	t.Run("consenting key with an unrecognized client is served on paid capacity", func(t *testing.T) {
		svc, upstream := newThirdPartyRefusalService(t)

		recorder, err := dispatchManagedThirdPartyTurnWithReason(t, svc, admittedClientRefusalContext(true, ""), translate.ReasonUserForceModel)

		require.NoError(t, err)
		assert.Equal(t, "served", recorder.Body.String())
		assert.Equal(t, 1, upstream.subDispatches)
		assert.Equal(t, 1, upstream.paidDispatches)
	})

	t.Run("key without consent still fails the forced turn", func(t *testing.T) {
		svc, upstream := newThirdPartyRefusalService(t)

		_, err := dispatchManagedThirdPartyTurnWithReason(t, svc, admittedThirdPartyRefusalContext(false), translate.ReasonUserForceModel)

		require.ErrorIs(t, err, ErrSubscriptionClientAppRefused)
		assert.Equal(t, 1, upstream.subDispatches)
		assert.Zero(t, upstream.paidDispatches)
	})

	t.Run("consent does not fund routed turns", func(t *testing.T) {
		svc, upstream := newThirdPartyRefusalService(t)

		_, err := dispatchManagedThirdPartyTurn(t, svc, admittedThirdPartyRefusalContext(true))

		require.Error(t, err)
		assert.Equal(t, 1, upstream.subDispatches)
		assert.Zero(t, upstream.paidDispatches, "a refusal is not quota proof for a routed turn")
	})
}

func TestForcedThirdPartyRefusalPaysThroughSubscriptionStateModelsOnlyWhenKeyConsents(t *testing.T) {
	for _, consented := range []bool{false, true} {
		t.Run(map[bool]string{false: "without consent", true: "with consent"}[consented], func(t *testing.T) {
			upstream := &parityUpstream{subErr: thirdPartyRefusalError(), okBody: "served"}
			svc := NewService(stateModelRouter{}, map[string]providers.Client{providers.ProviderAnthropic: upstream}, nil, false, nil, nil, false, "", "", nil).
				WithManagedSubscriptions(&stateModelLeaser{activeProviders: map[subscriptions.Provider]bool{subscriptions.ProviderClaude: true}}).
				WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAnthropic: {}})
			svc.retrySleep = noopSleep
			ctx := WithManagedSubscriptionCandidates(managedSubscriptionTestContext(), []*auth.SubscriptionAccount{{
				ID: "synthetic-claude", Provider: auth.SubscriptionProviderClaude, Enabled: true, State: auth.SubscriptionAccountStateActive,
			}})
			if consented {
				ctx = context.WithValue(ctx, APIKeyPaidFallbackOnSubscriptionRefusalContextKey{}, true)
			}
			ctx = clientAppContext(ctx, ClientAppOpencode)
			ctx = context.WithValue(ctx, InstallationSubscriptionModelsWhenActiveContextKey{}, []string{stateClaudeModel})
			ctx = context.WithValue(ctx, InstallationSubscriptionModelsWhenInactiveContextKey{}, []string{stateClaudeModel})
			ctx = withTurnFailures(ctx)
			req := router.Request{AllowedModels: allowedModelsForRequest(ctx), EnabledProviders: modelSet([]string{providers.ProviderAnthropic}), ForceModel: stateClaudeModel}
			recorder := httptest.NewRecorder()
			buffer := newPreludeBuffer(recorder)

			_, err := svc.dispatchWithFallback(ctx, failoverInputs{
				w: recorder, buf: buffer, subscriptionStateRequest: &req,
				subscriptionStateWinnerProvider: new(string),
				initialDecision:                 router.Decision{Model: stateClaudeModel, Provider: providers.ProviderAnthropic, Reason: translate.ReasonUserForceModel},
				purpose:                         inference.PurposeAnthropicMessages,
				origin:                          policy.OverrideSourceRequest,
				buildAlternative: func(router.Decision) (dispatchAttempt, error) {
					return func(attemptCtx context.Context, decision router.Decision, client providers.Client) error {
						buffer.Seal()
						return client.Proxy(attemptCtx, decision, providers.PreparedRequest{}, buffer, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
					}, nil
				},
			})

			assert.Equal(t, 1, upstream.subDispatches)
			if consented {
				require.NoError(t, err)
				assert.Equal(t, "served", recorder.Body.String())
				assert.Equal(t, 1, upstream.paidDispatches)
				return
			}
			require.Error(t, err)
			assert.Zero(t, upstream.paidDispatches)
		})
	}
}

// sharedPoolRefusalContext admits the requester's own seat and a shared
// colleague seat, so paid authorization judges a pool larger than the seats a
// turn actually leases.
func sharedPoolRefusalContext(paidFallbackConsented bool) context.Context {
	ctx := WithManagedSubscriptionCandidates(managedSubscriptionTestContext(), []*auth.SubscriptionAccount{
		{ID: "opaque-a", Provider: auth.SubscriptionProviderClaude, Enabled: true, State: auth.SubscriptionAccountStateActive},
		{ID: "opaque-shared", Provider: auth.SubscriptionProviderClaude, Enabled: true, State: auth.SubscriptionAccountStateActive},
	})
	if paidFallbackConsented {
		ctx = context.WithValue(ctx, APIKeyPaidFallbackOnSubscriptionRefusalContextKey{}, true)
	}
	return clientAppContext(ctx, ClientAppOpencode)
}

func newSharedPoolRefusalService(t *testing.T, leases ...subscriptions.Lease) (*Service, *parityUpstream, *scriptedSubscriptionLeaser) {
	t.Helper()
	leaser := &scriptedSubscriptionLeaser{leases: leases}
	upstream := &parityUpstream{subErr: thirdPartyRefusalError(), okBody: "served"}
	svc := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderAnthropic: upstream}).
		WithManagedSubscriptions(leaser).
		WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAnthropic: {}})
	svc.retrySleep = noopSleep
	return svc, upstream, leaser
}

func TestForcedThirdPartyRefusalConsentSpendsWholeClaudePool(t *testing.T) {
	ownSeat := subscriptions.Lease{AccountID: "opaque-a", AccessToken: "token-a"}
	sharedSeat := subscriptions.Lease{AccountID: "opaque-shared", AccessToken: "token-shared"}

	t.Run("an unleasable shared seat with unknown quota does not block paid capacity", func(t *testing.T) {
		svc, upstream, _ := newSharedPoolRefusalService(t, ownSeat)

		recorder, err := dispatchManagedThirdPartyTurnWithReason(t, svc, sharedPoolRefusalContext(true), translate.ReasonUserForceModel)

		require.NoError(t, err)
		assert.Equal(t, "served", recorder.Body.String())
		assert.Equal(t, 1, upstream.subDispatches)
		assert.Equal(t, 1, upstream.paidDispatches)
	})

	t.Run("a later turn skipping the denied seat is served on paid capacity", func(t *testing.T) {
		svc, upstream, _ := newSharedPoolRefusalService(t, ownSeat)
		_, err := dispatchManagedThirdPartyTurnWithReason(t, svc, sharedPoolRefusalContext(true), translate.ReasonUserForceModel)
		require.NoError(t, err)
		svc.managedSubscriptions = &scriptedSubscriptionLeaser{leases: []subscriptions.Lease{ownSeat}}

		recorder, err := dispatchManagedThirdPartyTurnWithReason(t, svc, sharedPoolRefusalContext(true), translate.ReasonUserForceModel)

		require.NoError(t, err)
		assert.Equal(t, "served", recorder.Body.String())
		assert.Equal(t, 1, upstream.subDispatches, "the denied seat is skipped without a probe")
		assert.Equal(t, 2, upstream.paidDispatches)
	})

	t.Run("shared seats are not probed once the client app was refused", func(t *testing.T) {
		svc, upstream, leaser := newSharedPoolRefusalService(t, ownSeat, sharedSeat)

		recorder, err := dispatchManagedThirdPartyTurnWithReason(t, svc, sharedPoolRefusalContext(true), translate.ReasonUserForceModel)

		require.NoError(t, err)
		assert.Equal(t, "served", recorder.Body.String())
		assert.Equal(t, 1, upstream.subDispatches, "a colleague's seat could serve the app only from their extra usage")
		assert.Equal(t, 1, leaser.next)
		assert.Equal(t, 1, upstream.paidDispatches)
	})

	t.Run("key without consent still fails the forced turn", func(t *testing.T) {
		svc, upstream, _ := newSharedPoolRefusalService(t, ownSeat)

		_, err := dispatchManagedThirdPartyTurnWithReason(t, svc, sharedPoolRefusalContext(false), translate.ReasonUserForceModel)

		require.ErrorIs(t, err, ErrSubscriptionClientAppRefused)
		assert.Equal(t, 1, upstream.subDispatches)
		assert.Zero(t, upstream.paidDispatches)
	})

	t.Run("consent does not fund routed turns", func(t *testing.T) {
		svc, upstream, _ := newSharedPoolRefusalService(t, ownSeat)

		_, err := dispatchManagedThirdPartyTurn(t, svc, sharedPoolRefusalContext(true))

		require.Error(t, err)
		assert.Zero(t, upstream.paidDispatches)
	})
}
