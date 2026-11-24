package proxy

import (
	"testing"
	"time"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/routingpolicy"
	"weave-os/router/internal/telemetry"

	"github.com/stretchr/testify/assert"
)

func TestWithServedCacheWriteTTLPrefersTheDispatchedBody(t *testing.T) {
	var res routingpolicy.TurnRoute
	res.SetCacheShape(routingpolicy.TurnCacheShape{CacheWriteTTL: time.Hour})
	assert.Equal(t, time.Hour, withServedCacheWriteTTL(res, nil).CacheShape().CacheWriteTTL)

	fiveMinutes := &telemetry.UsageExtractor{}
	fiveMinutes.SetRequestCacheTTL1h(func() bool { return false })
	assert.Equal(t, 5*time.Minute, withServedCacheWriteTTL(res, fiveMinutes).CacheShape().CacheWriteTTL)

	oneHour := &telemetry.UsageExtractor{}
	oneHour.SetRequestCacheTTL1h(func() bool { return true })
	assert.Equal(t, time.Hour, withServedCacheWriteTTL(routingpolicy.TurnRoute{}, oneHour).CacheShape().CacheWriteTTL)
}

func TestServedPromptTokensCountsCacheOnlyWhereInputExcludesIt(t *testing.T) {
	assert.Equal(t, 100_000, servedPromptTokens(providers.ProviderAnthropic, 100, 90_000, 9_900))
	assert.Equal(t, 100, servedPromptTokens(providers.ProviderFireworks, 100, 90_000, 9_900))
}
