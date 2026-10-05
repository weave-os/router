package proxy

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"weave-os/router/internal/billing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const accessOtherModel = "claude-opus-5"

func modelAccessError() error {
	return upstreamErr(http.StatusNotFound, `{"type":"error","error":{"type":"not_found_error","message":"model: `+parityAnthropicModel+`"}}`)
}

func TestSubscriptionModelAccessRescuesAndRemembers(t *testing.T) {
	in := parityAnthropicIngress()
	for _, stream := range []bool{false, true} {
		t.Run(boolLit(stream), func(t *testing.T) {
			upstream := &parityUpstream{subErr: modelAccessError(), okBody: in.upstreamOK(stream)}
			svc := in.parityService(upstream)
			for i := 0; i < 2; i++ {
				rec, req, body := in.request(t, stream)
				require.NoError(t, in.call(svc, in.subCtx(), body, rec, req))
				assert.Equal(t, http.StatusOK, rec.Code)
				assert.NotContains(t, rec.Body.String(), "not_found_error")
				assert.Contains(t, rec.Body.String(), "output_tokens")
			}
			assert.Equal(t, 1, upstream.subDispatches)
			assert.Equal(t, 2, upstream.paidDispatches)
		})
	}
}

func TestSubscriptionModelAccessOpenAIIngressRescuesAndRemembers(t *testing.T) {
	in := parityAnthropicIngress()
	upstream := &parityUpstream{subErr: modelAccessError(), okBody: in.upstreamOK(false)}
	svc := in.parityService(upstream)
	body := []byte(`{"model":"` + in.model + `","max_tokens":4096,"stream":false,"messages":[{"role":"user","content":"investigate the failing dispatch"}]}`)

	for range 2 {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
		require.NoError(t, svc.ProxyOpenAIChatCompletion(in.subCtx(), body, recorder, request))
		assert.Equal(t, http.StatusOK, recorder.Code)
		assert.NotContains(t, recorder.Body.String(), "not_found_error")
	}

	assert.Equal(t, 1, upstream.subDispatches)
	assert.Equal(t, 2, upstream.paidDispatches)
}

func TestSubscriptionModelAccessPreservesFailedAttemptAttribution(t *testing.T) {
	in := parityAnthropicIngress()
	upstream := &parityUpstream{subErr: modelAccessError(), paidErr: modelAccessError()}
	svc := in.parityService(upstream)
	rec, req, body := in.request(t, false)
	require.Error(t, in.call(svc, in.subCtx(), body, rec, req))
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, 1, upstream.subDispatches)
	assert.Equal(t, 1, upstream.paidDispatches)
	// Learning a denial must not retroactively change the credential of the failed attempt.
	assert.True(t, servedOnSubscription(resolveAndInjectCredentials(in.resolved(in.subCtx()), in.provider, in.model, nil)))
}

func TestSubscriptionModelAccessDoesNotSpendWithoutPermission(t *testing.T) {
	in := parityAnthropicIngress()
	for _, subscriptionOnly := range []bool{false, true} {
		t.Run(boolLit(subscriptionOnly), func(t *testing.T) {
			upstream := &parityUpstream{subErr: modelAccessError(), okBody: in.upstreamOK(false)}
			svc := in.parityService(upstream)
			ctx := in.subCtx()
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
	svc.now = func() time.Time { return now }
	svc.recordSubscriptionModelRejection(in.resolved(in.subCtx()), in.provider, in.model, modelAccessError())
	resolved := svc.resolveCredentials(in.subCtx(), in.provider, in.model, nil)
	assert.Nil(t, CredentialsFromContext(resolved))
	assert.Nil(t, CredentialsFromContext(resolveAndInjectCredentials(resolved, in.provider, in.model, nil)))
	assert.True(t, servedOnSubscription(svc.resolveCredentials(resolved, in.provider, accessOtherModel, nil)))
	assert.True(t, servedOnSubscription(svc.resolveCredentials(context.WithValue(in.subCtx(), AnthropicSubscriptionContextKey{}, "sk-ant-oat01-other"), in.provider, in.model, nil)))
	assert.False(t, servedOnSubscription(svc.resolveCredentials(billing.WithSubscriptionOnly(in.subCtx(), billing.SubscriptionOnlyCreditsDepleted), in.provider, in.model, nil)), "known denied OAuth must never be replayed")
	linkedFirstContext := billing.WithSubscriptionOnly(in.subCtx(), billing.SubscriptionOnlyLinkedFirst)
	assert.False(t, servedOnSubscription(svc.resolveCredentials(linkedFirstContext, in.provider, in.model, nil)), "a denied linked subscription must use the available infrastructure credential")
	now = now.Add(16 * time.Minute)
	assert.True(t, servedOnSubscription(svc.resolveCredentials(in.subCtx(), in.provider, in.model, nil)), "access changes are rechecked after expiry")
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
		svc.recordSubscriptionModelRejection(in.resolved(in.subCtx()), in.provider, in.model, err)
		assert.True(t, servedOnSubscription(svc.resolveCredentials(in.subCtx(), in.provider, in.model, nil)))
	}
	svc := in.deploymentKeyedService()
	svc.recordSubscriptionModelRejection(in.resolved(in.byokCtx(context.Background())), in.provider, in.model, modelAccessError())
	assert.True(t, servedOnSubscription(svc.resolveCredentials(in.subCtx(), in.provider, in.model, nil)))
}
