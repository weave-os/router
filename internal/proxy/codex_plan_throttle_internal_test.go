package proxy

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/credentials/usage"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/subscriptions"
)

func TestCodexPlanRateLimitRefusesAsCallerThrottle(t *testing.T) {
	svc := &Service{engine: dispatch.NewEngine(nil), strategyServing: hmmPorts(), credentials: credentials.Brokerage{UsageObserver: usage.NewObserver([]byte("test-salt"), time.Hour, time.Now)}}
	ctx := credentials.WithManagedSubscriptionUsage(codexSubscriptionTestCtx())

	svc.recordCodexQuotaExhaustion(ctx, http.Header{}, &providers.UpstreamErrorResponse{
		Status:  http.StatusTooManyRequests,
		Headers: http.Header{"Retry-After": {"30"}},
		Body:    []byte(`{"detail":"Rate limit exceeded"}`),
	})

	require.NoError(t, svc.credentials.CallerPlanRefusal(ctx, subscriptions.ProviderCodex, true, time.Now()), "Weave credits fund a turn a rate-limited caller plan would fail")
	err := svc.credentials.CallerPlanRefusal(billing.WithWeaveCreditsDenied(ctx), subscriptions.ProviderCodex, true, time.Now())
	require.ErrorIs(t, err, credentials.ErrCallerPlanThrottled, "a rate-limited caller plan must refuse as throttled, not as an unknown-quota outage: %v", err)
	class, classified := ClassifyDispatchError(err)
	assert.True(t, classified)
	assert.Equal(t, http.StatusTooManyRequests, class.Status)
}
