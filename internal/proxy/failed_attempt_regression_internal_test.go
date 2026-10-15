package proxy

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/subscriptions"
)

func TestProxyMessagesNoProgressIgnoresFailedRequests(t *testing.T) {
	for _, status := range []int{http.StatusBadGateway, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			in := parityAnthropicIngress()
			failure := upstreamErr(status, `{"error":{"message":"temporarily unavailable"}}`)
			upstream := &parityUpstream{paidErr: failure}
			svc := in.parityService(upstream)
			body := noProgressRetryBody(in.model)
			ctx := context.WithValue(context.Background(), InstallationIDContextKey{}, parityInstallationID)
			for i := 0; i < 8; i++ {
				rec := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
				err := svc.ProxyMessages(ctx, body, rec, req)
				require.Error(t, err, "retry %d must retain the upstream failure", i)
				assert.NotContains(t, rec.Body.String(), "No-progress loop detected")
			}
			assert.GreaterOrEqual(t, upstream.paidDispatches, 8)
		})
	}
}

func TestDispatchWithFallbackPreservesTransientSubscriptionFailure(t *testing.T) {
	for _, status := range []int{http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusTooManyRequests} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			leaser := &scriptedSubscriptionLeaser{leases: []subscriptions.Lease{{AccountID: "synthetic-account", AccessToken: "synthetic-token"}}}
			failure := &providers.UpstreamErrorResponse{Status: status}
			client := &fakeClient{name: providers.ProviderOpenAI, outcomes: []fakeOutcome{{err: failure}}}
			svc := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderOpenAI: client}).WithManagedSubscriptions(leaser)
			svc.retrySleep = noopSleep
			rec := httptest.NewRecorder()
			buf := newPreludeBuffer(rec)
			_, err := svc.dispatchWithFallback(managedSubscriptionContext(auth.SubscriptionProviderCodex), failoverInputs{
				w: rec, buf: buf, initialDecision: router.Decision{Model: "gpt-5.6-sol", Provider: providers.ProviderOpenAI},
				purpose: inference.PurposeAnthropicMessages, bindings: []catalog.ProviderBinding{{Provider: providers.ProviderOpenAI}},
				attempt: func(ctx context.Context, d router.Decision, c providers.Client) error {
					buf.Seal()
					return c.Proxy(ctx, d, providers.PreparedRequest{}, buf, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
				},
			})
			if status == http.StatusTooManyRequests {
				require.ErrorIs(t, err, ErrSubscriptionPoolExhausted)
				class, ok := ClassifyDispatchError(err)
				require.True(t, ok)
				assert.Equal(t, http.StatusTooManyRequests, class.Status)
			} else {
				require.ErrorIs(t, err, failure)
				assert.False(t, isSubscriptionPoolError(err), "a failed transport is not proof of exhausted quota")
				assert.Equal(t, status, upstreamStatus(err))
				class, ok := ClassifyDispatchError(err)
				if ok {
					assert.NotEqual(t, http.StatusTooManyRequests, class.Status)
				}
			}
			assert.Equal(t, 1, client.calls)
		})
	}
}

func noProgressRetryBody(model string) []byte {
	return []byte(`{"model":"` + model + `","max_tokens":4096,"messages":[{"role":"user","content":"investigate the failing dispatch"},{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"Bash","input":{"command":"true"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"done"}]}],"tools":[{"name":"Bash","input_schema":{"type":"object"}}]}`)
}

func TestProxyMessagesNoProgressStillStopsSuccessfulLoop(t *testing.T) {
	in := parityAnthropicIngress()
	upstream := &parityUpstream{okBody: in.upstreamOK(false)}
	svc := in.parityService(upstream)
	body := noProgressRetryBody(in.model)
	ctx := context.WithValue(context.Background(), InstallationIDContextKey{}, parityInstallationID)
	for i := 0; i < 5; i++ {
		rec := httptest.NewRecorder()
		require.NoError(t, svc.ProxyMessages(ctx, body, rec, httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))))
		if i < 4 {
			assert.NotContains(t, rec.Body.String(), "No-progress loop detected")
		} else {
			assert.Contains(t, rec.Body.String(), "No-progress loop detected")
		}
	}
	assert.Equal(t, 4, upstream.paidDispatches, "only successful model calls establish the loop")
}

func TestSubscriptionRotationBudgetPreservesTransientFailure(t *testing.T) {
	svc := &Service{}
	for _, status := range []int{http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusTooManyRequests} {
		failure := &providers.UpstreamErrorResponse{Status: status}
		err := svc.afterSubscriptionRotationBudget(context.Background(), router.Decision{Model: "gpt-5.6-sol", Provider: providers.ProviderOpenAI}, 1, failure)
		require.ErrorIs(t, err, failure)
		assert.Equal(t, status == http.StatusTooManyRequests, isSubscriptionPoolError(err))
	}
}

func TestNoProgressDetectionDoesNotRecordInflightRequests(t *testing.T) {
	tracker := newNoProgressTracker()
	key := sessionKeyFromString("synthetic-concurrent-retries")
	install := uuid.New()
	fp := computeNoProgressFingerprint(router.Decision{Model: "gpt-5.6-sol", Provider: providers.ProviderOpenAI}, "retry", 3, "one-tool")
	now := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Go(func() {
			looped, count := tracker.detect(key, install, "high", fp, now)
			assert.False(t, looped)
			assert.Equal(t, 1, count)
		})
	}
	wg.Wait()
	for i := 0; i < 4; i++ {
		tracker.recordAndDetect(key, install, "high", fp, now)
	}
	looped, count := tracker.detect(key, install, "high", fp, now)
	assert.True(t, looped)
	assert.Equal(t, 5, count)
	looped, count = tracker.detect(key, install, "high", fp, now.Add(91*time.Second))
	assert.False(t, looped)
	assert.Equal(t, 1, count)
}

func TestProxyMessagesNoProgressStillStopsSuccessfulRescueLoop(t *testing.T) {
	in := parityAnthropicIngress()
	failed := &parityUpstream{paidErr: upstreamErr(http.StatusServiceUnavailable, `{"error":{"message":"temporarily unavailable"}}`)}
	rescued := &parityUpstream{okBody: in.upstreamOK(false)}
	svc := NewService(staticRouter{decision: router.Decision{Provider: providers.ProviderFireworks, Model: "deepseek/deepseek-v4-pro", Reason: "test"}}, map[string]providers.Client{
		providers.ProviderFireworks: failed, providers.ProviderOpenRouter: failed, providers.ProviderAnthropic: rescued,
	}, nil, false, nil, nil, false, providers.ProviderAnthropic, in.model, nil).WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderFireworks: {}, providers.ProviderOpenRouter: {}, providers.ProviderAnthropic: {}})
	svc.retrySleep = noopSleep
	body := noProgressRetryBody(in.model)
	ctx := context.WithValue(context.Background(), InstallationIDContextKey{}, parityInstallationID)
	for i := 0; i < 5; i++ {
		rec := httptest.NewRecorder()
		require.NoError(t, svc.ProxyMessages(ctx, body, rec, httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))))
		if i < 4 {
			assert.Contains(t, rec.Body.String(), `"text":"hi"`)
			assert.NotContains(t, rec.Body.String(), "No-progress loop detected")
		} else {
			assert.Contains(t, rec.Body.String(), "No-progress loop detected")
		}
	}
	assert.Equal(t, 4, rescued.paidDispatches)
}
