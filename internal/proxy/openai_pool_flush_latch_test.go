package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/subscriptions"
)

// fixedRouter routes every request to one decision.
type fixedRouter struct{ decision router.Decision }

func (f fixedRouter) Route(context.Context, router.Request) (router.Decision, error) {
	return f.decision, nil
}

// TestProxyOpenAIChatCompletion_ManagedPoolFailureIsRenderedOnce: the held
// upstream error must not be written alongside a second pool failure. Rotation
// preserves the original throttle, and the client receives the billing override once.
func TestProxyOpenAIChatCompletion_ManagedPoolFailureIsRenderedOnce(t *testing.T) {
	leaser := &scriptedSubscriptionLeaser{leases: []subscriptions.Lease{{AccountID: "opaque-a", AccessToken: "token-a"}}}
	openAI := &fakeClient{name: providers.ProviderOpenAI, outcomes: []fakeOutcome{
		{err: &providers.UpstreamErrorResponse{
			Status: http.StatusTooManyRequests,
			Body:   []byte(`{"error":{"type":"rate_limit_error","message":"quota exhausted"}}`),
		}},
	}}
	svc := NewService(
		fixedRouter{decision: router.Decision{
			Provider: providers.ProviderOpenAI,
			Model:    "gpt-5.6-sol",
			Metadata: &router.RoutingMetadata{CandidateModels: []string{"gpt-5.6-sol"}},
		}},
		map[string]providers.Client{
			providers.ProviderOpenAI:    openAI,
			providers.ProviderAnthropic: &fakeClient{name: providers.ProviderAnthropic},
		},
		nil, false, nil, nil, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil,
	).WithManagedSubscriptions(leaser).
		WithDeploymentKeyedProviders(map[string]struct{}{
			providers.ProviderOpenAI:    {},
			providers.ProviderAnthropic: {},
		})

	ctx := context.WithValue(managedSubscriptionContext(auth.SubscriptionProviderCodex),
		InstallationIDContextKey{}, "33333333-3333-3333-3333-333333333333")
	ctx = billing.WithSubscriptionOnly(ctx, billing.SubscriptionOnlyCreditsDepleted)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(""))
	body := []byte(`{"model":"gpt-5.6-sol","stream":true,"messages":[{"role":"user","content":"hi"}]}`)

	err := svc.ProxyOpenAIChatCompletion(ctx, body, rec, req)

	require.ErrorIs(t, err, ErrSubscriptionPoolExhausted,
		"the pool sentinel must survive for the handler to classify")
	class, classified := ClassifyDispatchError(err)
	require.True(t, classified)
	assert.Equal(t, DispatchErrorCreditsExhausted, class.Kind)
	assert.Equal(t, http.StatusPaymentRequired, class.Status)
	assert.Equal(t, http.StatusOK, rec.Code, "the visible Chat prelude has committed SSE delivery")
	assert.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))
	assert.Equal(t, 1, strings.Count(rec.Body.String(), `"error":`),
		"the client sees the classified billing failure exactly once")
	assert.Contains(t, rec.Body.String(), "Your Weave router credits are exhausted")
	assert.Contains(t, rec.Body.String(), `"code":"upstream_error"`)
	assert.NotContains(t, rec.Body.String(), "quota exhausted")
}
