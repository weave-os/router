package proxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/subscriptions"
)

const slowFirstOutput = 12 * time.Second

// slowAttempt writes output after delay unless its context ends first, so a
// pre-fix rotation timer that cancels in-flight inference makes it fail.
func slowAttempt(buf *preludeBuffer, delay time.Duration, output string, seen *[]*Credentials) func(context.Context, router.Decision, providers.Client) error {
	return func(ctx context.Context, _ router.Decision, _ providers.Client) error {
		*seen = append(*seen, CredentialsFromContext(ctx))
		buf.Seal()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		_, err := buf.Write([]byte(output))
		return err
	}
}

func TestLinkedClaudeSubscriptionSurvivesSlowFirstOutput(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		svc := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderAnthropic: &fakeClient{name: providers.ProviderAnthropic}}).
			WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAnthropic: {}})
		linked := &Credentials{APIKey: []byte("sk-ant-oat01-synthetic"), OAuth: true, Source: credSourceSubscription}
		ctx := context.WithValue(WithManagedSubscriptionUsage(context.Background()), CredentialsContextKey{}, linked)
		rec := httptest.NewRecorder()
		buf := newPreludeBuffer(rec)
		in := plannedInputs(rec, buf, []catalog.ProviderBinding{{Provider: providers.ProviderAnthropic}}, nil)
		in.initialDecision = router.Decision{Provider: providers.ProviderAnthropic, Model: "claude-opus-4-8"}
		var seen []*Credentials
		in.attempt = slowAttempt(buf, slowFirstOutput, "slow answer", &seen)

		_, err := svc.dispatchWithFallback(ctx, in)

		require.NoError(t, err)
		require.Equal(t, "slow answer", rec.Body.String())
		require.Len(t, seen, 1, "a slow subscription response must not be retried or rescued")
		require.True(t, seen[0].OAuth)
	})
}

func TestManagedSubscriptionSurvivesSlowFirstOutput(t *testing.T) {
	for _, tc := range []struct {
		name     string
		provider auth.SubscriptionProvider
		upstream string
		model    string
	}{
		{"claude", auth.SubscriptionProviderClaude, providers.ProviderAnthropic, "claude-opus-4-8"},
		{"codex", auth.SubscriptionProviderCodex, providers.ProviderOpenAI, codexCoveredModel},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				leaser := &scriptedSubscriptionLeaser{leases: []subscriptions.Lease{{AccountID: "seat-a", AccessToken: "token-a"}, {AccountID: "seat-b", AccessToken: "token-b"}}}
				svc := newServiceWithProviders(t, map[string]providers.Client{tc.upstream: &fakeClient{name: tc.upstream}}).
					WithManagedSubscriptions(leaser).
					WithDeploymentKeyedProviders(map[string]struct{}{tc.upstream: {}})
				ctx := managedSubscriptionContext(tc.provider)
				rec := httptest.NewRecorder()
				buf := newPreludeBuffer(rec)
				in := plannedInputs(rec, buf, []catalog.ProviderBinding{{Provider: tc.upstream}}, nil)
				in.initialDecision = router.Decision{Provider: tc.upstream, Model: tc.model}
				var seen []*Credentials
				in.attempt = slowAttempt(buf, slowFirstOutput, "slow seat answer", &seen)

				_, err := svc.dispatchWithFallback(ctx, in)

				require.NoError(t, err)
				require.Equal(t, "slow seat answer", rec.Body.String())
				require.Len(t, seen, 1)
				require.Equal(t, "seat-a", seen[0].SubscriptionAccountID)
				require.Empty(t, leaser.cooldownIDs, "a slow success must not cool down the account")
				require.True(t, ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage).Served)
			})
		})
	}
}

func TestSecondAccountAdmittedBeforeBudgetMayFinishAfterIt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		leaser := &scriptedSubscriptionLeaser{leases: []subscriptions.Lease{{AccountID: "seat-a", AccessToken: "token-a"}, {AccountID: "seat-b", AccessToken: "token-b"}, {AccountID: "seat-c", AccessToken: "token-c"}}}
		svc := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderAnthropic: &fakeClient{name: providers.ProviderAnthropic}}).WithManagedSubscriptions(leaser)
		ctx := managedSubscriptionTestContext()
		rec := httptest.NewRecorder()
		buf := newPreludeBuffer(rec)
		in := plannedInputs(rec, buf, []catalog.ProviderBinding{{Provider: providers.ProviderAnthropic}}, nil)
		in.initialDecision = router.Decision{Provider: providers.ProviderAnthropic, Model: "claude-opus-4-8"}
		var seen []string
		in.attempt = func(ctx context.Context, _ router.Decision, _ providers.Client) error {
			seen = append(seen, CredentialsFromContext(ctx).SubscriptionAccountID)
			buf.Seal()
			if len(seen) == 1 {
				time.Sleep(9 * time.Second)
				return &providers.UpstreamErrorResponse{Status: 529}
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(11 * time.Second):
			}
			_, err := buf.Write([]byte("second seat answer"))
			return err
		}

		_, err := svc.dispatchWithFallback(ctx, in)

		require.NoError(t, err)
		require.Equal(t, []string{"seat-a", "seat-b"}, seen)
		require.Equal(t, "second seat answer", rec.Body.String())
	})
}

func TestExpiredRotationBudgetAdmitsNoFurtherSubscriptionAttempt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		leaser := &scriptedSubscriptionLeaser{leases: []subscriptions.Lease{{AccountID: "seat-a", AccessToken: "token-a"}, {AccountID: "seat-b", AccessToken: "token-b"}}}
		svc := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderAnthropic: &fakeClient{name: providers.ProviderAnthropic}}).WithManagedSubscriptions(leaser)
		ctx := managedSubscriptionTestContext()
		rec := httptest.NewRecorder()
		buf := newPreludeBuffer(rec)
		in := plannedInputs(rec, buf, []catalog.ProviderBinding{{Provider: providers.ProviderAnthropic}}, nil)
		in.initialDecision = router.Decision{Provider: providers.ProviderAnthropic, Model: "claude-opus-4-8"}
		calls := 0
		in.attempt = func(ctx context.Context, _ router.Decision, _ providers.Client) error {
			calls++
			buf.Seal()
			time.Sleep(11 * time.Second)
			require.NoError(t, ctx.Err(), "the admitted attempt keeps its live context")
			return &providers.UpstreamErrorResponse{Status: 529}
		}

		_, err := svc.dispatchWithFallback(ctx, in)

		var upstream *providers.UpstreamErrorResponse
		require.ErrorAs(t, err, &upstream, "the provider's own failure is preserved")
		require.Equal(t, 1, calls)
		require.Equal(t, 1, leaser.next, "no second account leased after the budget expired")
	})
}

func TestCallerCancellationDuringSubscriptionAttemptIsTerminal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		svc := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderAnthropic: &fakeClient{name: providers.ProviderAnthropic}}).
			WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAnthropic: {}})
		linked := &Credentials{APIKey: []byte("sk-ant-oat01-synthetic"), OAuth: true, Source: credSourceSubscription}
		parent, cancel := context.WithCancel(WithManagedSubscriptionUsage(context.Background()))
		ctx := context.WithValue(parent, CredentialsContextKey{}, linked)
		rec := httptest.NewRecorder()
		buf := newPreludeBuffer(rec)
		in := plannedInputs(rec, buf, []catalog.ProviderBinding{{Provider: providers.ProviderAnthropic}}, nil)
		in.initialDecision = router.Decision{Provider: providers.ProviderAnthropic, Model: "claude-opus-4-8"}
		calls := 0
		in.attempt = func(ctx context.Context, _ router.Decision, _ providers.Client) error {
			calls++
			buf.Seal()
			time.AfterFunc(3*time.Second, cancel)
			<-ctx.Done()
			return ctx.Err()
		}

		_, err := svc.dispatchWithFallback(ctx, in)

		require.True(t, errors.Is(err, context.Canceled))
		require.Equal(t, 1, calls, "caller cancellation never triggers another attempt")
	})
}

func TestStateModelsTerminalRejectionAfterBudgetIsNotRescued(t *testing.T) {
	claude := &fakeClient{name: providers.ProviderAnthropic, outcomes: []fakeOutcome{{err: &providers.UpstreamErrorResponse{Status: http.StatusBadRequest, Body: []byte(`{"error":{"type":"invalid_request_error","message":"synthetic bad request"}}`)}}}}
	codex := &fakeClient{name: providers.ProviderOpenAI}
	paid := &fakeClient{name: providers.ProviderOpenRouter}
	svc := NewService(stateModelRouter{}, map[string]providers.Client{providers.ProviderAnthropic: claude, providers.ProviderOpenAI: codex, providers.ProviderOpenRouter: paid}, nil, false, nil, nil, false, "", "", nil).
		WithManagedSubscriptions(&stateModelLeaser{activeProviders: map[subscriptions.Provider]bool{subscriptions.ProviderClaude: true, subscriptions.ProviderCodex: true}})
	ctx := context.WithValue(managedSubscriptionTestContext(), ManagedSubscriptionProvidersContextKey{}, map[auth.SubscriptionProvider]struct{}{auth.SubscriptionProviderClaude: {}, auth.SubscriptionProviderCodex: {}})
	ctx = context.WithValue(ctx, InstallationSubscriptionModelsWhenActiveContextKey{}, []string{stateClaudeModel, stateCodexModel})
	ctx = context.WithValue(ctx, InstallationSubscriptionModelsWhenInactiveContextKey{}, []string{statePaidModel})
	req := router.Request{AllowedModels: allowedModelsForRequest(ctx), EnabledProviders: modelSet([]string{providers.ProviderAnthropic, providers.ProviderOpenAI, providers.ProviderOpenRouter})}
	rec := httptest.NewRecorder()
	buf := newPreludeBuffer(rec)
	var served []string
	_, err := svc.dispatchWithFallback(ctx, failoverInputs{
		w: rec, buf: buf, subscriptionStateRequest: &req,
		subscriptionStateWinnerProvider: new(string),
		initialDecision:                 router.Decision{Model: stateClaudeModel, Provider: providers.ProviderAnthropic},
		purpose:                         inference.PurposeAnthropicMessages,
		buildAlternative: func(router.Decision) (dispatchAttempt, error) {
			return func(attemptCtx context.Context, decision router.Decision, client providers.Client) error {
				served = append(served, decision.Model)
				// The rejection arrives after the shared rotation budget has elapsed.
				budget, _ := attemptCtx.Value(subscriptionRotationBudgetKey{}).(context.Context)
				require.NotNil(t, budget)
				<-budget.Done()
				require.NoError(t, attemptCtx.Err(), "the admitted attempt outlives the rotation budget")
				buf.Seal()
				return client.Proxy(attemptCtx, decision, providers.PreparedRequest{}, buf, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
			}, nil
		},
	})

	var upstream *providers.UpstreamErrorResponse
	require.ErrorAs(t, err, &upstream)
	require.Equal(t, http.StatusBadRequest, upstream.Status)
	require.Equal(t, []string{stateClaudeModel}, served, "a terminal rejection is not retried because the attempt was slow")
	require.Zero(t, codex.calls)
	require.Zero(t, paid.calls)
}
