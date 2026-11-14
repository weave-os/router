package proxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/anthropic"
	"weave-os/router/internal/proxy/usage"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/subscriptions"
)

func TestAnthropicBYOKDispatchIgnoresSubscriptionState(t *testing.T) {
	const oauthToken = "sk-ant-oat01-synthetic-subscription"
	for _, tc := range []struct {
		name              string
		inbound           bool
		managed           bool
		enrollmentUnknown bool
		overage           bool
		credentialErr     error
		upstreamErr       error
	}{
		{name: "cold direct OAuth"},
		{name: "cold inbound OAuth", inbound: true},
		{name: "observed paid overage", overage: true},
		{name: "linked subscription", managed: true},
		{name: "unknown enrollment", enrollmentUnknown: true},
		{name: "BYOK rejected", managed: true, upstreamErr: &providers.UpstreamErrorResponse{Status: http.StatusUnauthorized}},
		{name: "BYOK credential unavailable", credentialErr: errors.New("synthetic credential unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := &bypassFakeProvider{proxyErr: tc.upstreamErr}
			leaser := &scriptedSubscriptionLeaser{leases: []subscriptions.Lease{{AccountID: "synthetic-seat", AccessToken: oauthToken}}}
			svc := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderAnthropic: upstream}).WithManagedSubscriptions(leaser)
			observer := usage.NewObserver([]byte("synthetic-salt"), time.Minute, time.Now)
			if tc.overage {
				observer.Record(observer.Key([]byte(oauthToken)), usage.Snapshot{OverageInUse: true})
			}
			svc.WithUsageObserver(observer)
			byok := &auth.ExternalAPIKey{Provider: providers.ProviderAnthropic, FundingPriority: auth.FundingPriorityKeyFirst, Plaintext: []byte("synthetic-byok"), CredentialError: tc.credentialErr}
			if tc.credentialErr != nil {
				byok.Plaintext = nil
			}
			ctx := context.WithValue(context.Background(), ExternalAPIKeysContextKey{}, []*auth.ExternalAPIKey{byok})
			headers := http.Header{}
			if tc.inbound {
				headers.Set("Authorization", "Bearer "+oauthToken)
			} else {
				ctx = context.WithValue(ctx, AnthropicSubscriptionContextKey{}, oauthToken)
			}
			if tc.managed {
				ctx = WithManagedSubscriptionCandidates(ctx, []*auth.SubscriptionAccount{{ID: "synthetic-seat", Provider: auth.SubscriptionProviderClaude, Enabled: true, State: auth.SubscriptionAccountStateUnknown}})
			}
			if tc.enrollmentUnknown {
				ctx = context.WithValue(ctx, ManagedSubscriptionEnrollmentUnavailableContextKey{}, true)
			}
			model := catalog.ModelIDClaudeOpus48.String()
			ctx = svc.resolveCredentials(ctx, providers.ProviderAnthropic, model, headers)
			recorder := httptest.NewRecorder()
			buffer := newPreludeBuffer(recorder)
			in := plannedInputs(recorder, buffer, []catalog.ProviderBinding{{Provider: providers.ProviderAnthropic}}, nil)
			in.initialDecision = router.Decision{Provider: providers.ProviderAnthropic, Model: model}
			in.subscriptionStateHeaders = headers
			_, err := svc.dispatchWithFallback(ctx, in)
			if tc.upstreamErr != nil {
				require.ErrorIs(t, err, tc.upstreamErr)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, 1, upstream.dispatches)
			creds := CredentialsFromContext(upstream.capturedCtx)
			require.NotNil(t, creds)
			require.Equal(t, credSourceBYOK, creds.Source)
			require.False(t, creds.OAuth)
			require.Equal(t, byok.Plaintext, creds.APIKey)
			require.Equal(t, tc.credentialErr, creds.CredentialError)
			require.Empty(t, leaser.providers, "BYOK must not probe a linked subscription or fall back to one")
		})
	}
}

func TestAnthropicBYOKDisablesSubscriptionBypass(t *testing.T) {
	const oauthToken = "sk-ant-oat01-synthetic-subscription"
	for _, inbound := range []bool{false, true} {
		t.Run(map[bool]string{false: "direct", true: "inbound"}[inbound], func(t *testing.T) {
			svc := newServiceWithProviders(t, nil).WithUsageObserver(usage.NewObserver([]byte("synthetic-salt"), time.Minute, time.Now))
			ctx := context.WithValue(context.Background(), ExternalAPIKeysContextKey{}, []*auth.ExternalAPIKey{{Provider: providers.ProviderAnthropic, FundingPriority: auth.FundingPriorityKeyFirst, Plaintext: []byte("synthetic-byok")}})
			ctx = context.WithValue(ctx, InstallationUsageBypassContextKey{}, UsageBypassConfig{Enabled: true})
			headers := http.Header{}
			if inbound {
				headers.Set("Authorization", "Bearer "+oauthToken)
			} else {
				ctx = context.WithValue(ctx, AnthropicSubscriptionContextKey{}, oauthToken)
			}
			req := router.Request{RequestedModel: catalog.ModelIDClaudeOpus48.String()}
			for _, turn := range []turntype.TurnType{turntype.MainLoop, turntype.Classifier} {
				_, engaged := svc.usageBypassDecision(ctx, headers, req, nil, turn)
				require.False(t, engaged, "subscription bypass must not relabel BYOK traffic as subscription-funded")
			}
			require.NotContains(t, subscriptionProvidersForRequest(ctx), providers.ProviderAnthropic)
			require.False(t, pickedModelOnOwnPlan(fundOnOwnPlan(ctx, providers.ProviderAnthropic)))
		})
	}
}

func TestAnthropicBYOKHTTPNeverProbesOAuth(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusUnauthorized} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			requests := make(chan http.Header, 2)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests <- r.Header.Clone()
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				if status == http.StatusUnauthorized {
					_, _ = w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"synthetic rejection"}}`))
					return
				}
				_, _ = w.Write([]byte(`{"id":"synthetic","type":"message","role":"assistant","content":[{"type":"text","text":"synthetic answer"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
			}))
			defer upstream.Close()
			model := catalog.ModelIDClaudeOpus48.String()
			leaser := &scriptedSubscriptionLeaser{leases: []subscriptions.Lease{{AccountID: "synthetic-seat", AccessToken: "sk-ant-oat01-linked"}}}
			svc := NewService(staticRouter{decision: router.Decision{Provider: providers.ProviderAnthropic, Model: model}}, map[string]providers.Client{
				providers.ProviderAnthropic: anthropic.NewClient("synthetic-deployment-key", upstream.URL),
			}, nil, nil, false, "", "", nil).WithManagedSubscriptions(leaser)
			ctx := context.WithValue(context.Background(), ExternalAPIKeysContextKey{}, []*auth.ExternalAPIKey{{Provider: providers.ProviderAnthropic, FundingPriority: auth.FundingPriorityKeyFirst, Plaintext: []byte("synthetic-byok")}})
			ctx = WithManagedSubscriptionCandidates(ctx, []*auth.SubscriptionAccount{{ID: "synthetic-seat", Provider: auth.SubscriptionProviderClaude, Enabled: true, State: auth.SubscriptionAccountStateUnknown}})
			ctx = context.WithValue(ctx, InstallationUsageBypassContextKey{}, UsageBypassConfig{Enabled: true})
			body := `{"model":"` + model + `","max_tokens":32,"messages":[{"role":"user","content":"synthetic task"}]}`
			for _, token := range []string{"sk-ant-oat01-original", "sk-ant-oat01-refreshed"} {
				svc.WithUsageObserver(usage.NewObserver([]byte("synthetic-salt"), time.Minute, time.Now))
				request := httptest.NewRequest(http.MethodPost, routePathMessages, strings.NewReader(body))
				request.Header.Set("Authorization", "Bearer "+token)
				recorder := httptest.NewRecorder()
				err := svc.ProxyMessages(ctx, []byte(body), recorder, request)
				if status == http.StatusUnauthorized {
					require.Error(t, err)
				} else {
					require.NoError(t, err)
					require.Contains(t, recorder.Body.String(), "synthetic answer")
				}
				headers := <-requests
				require.Equal(t, "synthetic-byok", headers.Get("x-api-key"))
				require.Empty(t, headers.Get("Authorization"))
				require.Empty(t, requests, "a BYOK rejection must not retry on OAuth")
				require.Empty(t, leaser.providers)
			}
		})
	}
}

func TestAnthropicBYOKDoesNotAuthorizeGatewayBindings(t *testing.T) {
	svc := newServiceWithProviders(t, nil)
	ctx := WithManagedSubscriptionCandidates(context.Background(), []*auth.SubscriptionAccount{{ID: "synthetic-seat", Provider: auth.SubscriptionProviderClaude, Enabled: true, State: auth.SubscriptionAccountStateUnknown}})
	ctx = context.WithValue(ctx, ExternalAPIKeysContextKey{}, []*auth.ExternalAPIKey{{Provider: providers.ProviderAnthropic, FundingPriority: auth.FundingPriorityKeyFirst, Plaintext: []byte("synthetic-byok")}})
	model := catalog.ModelIDClaudeOpus48.String()
	require.NoError(t, svc.subscriptionAPIAuthorization(ctx, providers.ProviderAnthropic, model))
	require.ErrorIs(t, svc.subscriptionAPIAuthorization(ctx, providers.ProviderOpenRouter, model), ErrSubscriptionPoolUnavailable,
		"a native Anthropic key must not authorize spending through a different provider")
}
