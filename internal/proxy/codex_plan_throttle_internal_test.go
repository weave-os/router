package proxy

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy/usage"
	"weave-os/router/internal/subscriptions"
)

func TestCodexPlanRateLimitRefusesAsCallerThrottle(t *testing.T) {
	svc := &Service{usageObserver: usage.NewObserver([]byte("test-salt"), time.Hour, time.Now)}
	ctx := WithManagedSubscriptionUsage(codexSubscriptionTestCtx())

	svc.recordCodexQuotaExhaustion(ctx, http.Header{}, &providers.UpstreamErrorResponse{
		Status:  http.StatusTooManyRequests,
		Headers: http.Header{"Retry-After": {"30"}},
		Body:    []byte(`{"detail":"Rate limit exceeded"}`),
	})

	err := callerPlanRefusal(ctx, subscriptions.ProviderCodex, true, time.Now())
	assert.ErrorIs(t, err, errCallerPlanThrottled, "a rate-limited caller plan must refuse as throttled, not as an unknown-quota outage: %v", err)
	class, classified := ClassifyDispatchError(err)
	assert.True(t, classified)
	assert.Equal(t, http.StatusTooManyRequests, class.Status)
}
