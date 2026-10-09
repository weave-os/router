package proxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/subscriptions"

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
	recorder := httptest.NewRecorder()
	buffer := newPreludeBuffer(recorder)
	_, err := svc.dispatchWithFallback(ctx, failoverInputs{
		w:               recorder,
		buf:             buffer,
		initialDecision: router.Decision{Model: parityAnthropicModel, Provider: providers.ProviderAnthropic},
		purpose:         inference.PurposeAnthropicMessages,
		bindings:        []catalog.ProviderBinding{{Provider: providers.ProviderAnthropic}},
		attempt: func(ctx context.Context, decision router.Decision, client providers.Client) error {
			buffer.Seal()
			return client.Proxy(ctx, decision, providers.PreparedRequest{}, buffer, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
		},
	})
	return recorder, err
}

func TestDispatchWithFallbackRecoversManagedThirdPartyRefusal(t *testing.T) {
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
		require.NoError(t, err)
		assert.Equal(t, "served", recorder.Body.String())
	}

	assert.Equal(t, 1, upstream.subDispatches, "the refused account is skipped for this client app on later turns")
	assert.Equal(t, 2, upstream.paidDispatches)
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
	require.NoError(t, err)
	require.Equal(t, 1, upstream.subDispatches)

	upstream.subErr = nil
	claudeCodeCtx := clientAppContext(managedSubscriptionTestContext(), ClientAppClaudeCode)
	recorder, err := dispatchManagedThirdPartyTurn(t, svc, claudeCodeCtx)
	require.NoError(t, err)
	assert.Equal(t, "served", recorder.Body.String())
	assert.Equal(t, 2, upstream.subDispatches, "Claude Code still leases the account OpenCode was refused on")
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

func TestSubscriptionThirdPartyRefusalRescuesInboundSubscription(t *testing.T) {
	in := parityAnthropicIngress()
	for _, stream := range []bool{false, true} {
		t.Run(boolLit(stream), func(t *testing.T) {
			upstream := &parityUpstream{subErr: thirdPartyRefusalError(), okBody: in.upstreamOK(stream)}
			svc := in.parityService(upstream)
			rec, req, body := in.request(t, stream)
			require.NoError(t, in.call(svc, in.subCtx(), body, rec, req))
			assert.Equal(t, http.StatusOK, rec.Code)
			assert.NotContains(t, rec.Body.String(), "Third-party apps")
			assert.Contains(t, rec.Body.String(), "output_tokens")
			assert.Equal(t, 1, upstream.subDispatches)
			assert.Equal(t, 1, upstream.paidDispatches)
		})
	}
}
