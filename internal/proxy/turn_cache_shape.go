package proxy

import (
	"context"
	"time"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/routingpolicy"
	"weave-os/router/internal/telemetry"
	"weave-os/router/internal/translate"
)

func turnCacheShape(ctx context.Context, env *translate.RequestEnvelope, feats translate.RoutingFeatures) routingpolicy.TurnCacheShape {
	return routingpolicy.TurnCacheShape{
		MessageCount:  feats.MessageCount,
		UserPrompt:    routingEndsWithUserPrompt(ctx, env),
		SideRequest:   turntype.IsSideRequest(feats.Model, env.LastUserMessage().Text),
		CacheWriteTTL: env.CacheWriteTTL(),
	}
}

func withServedCacheWriteTTL(res routingpolicy.TurnRoute, extractor *telemetry.UsageExtractor) routingpolicy.TurnRoute {
	oneHour, known := extractor.RequestCacheTTL1h()
	shape := res.CacheShape()
	switch {
	case known && oneHour:
		shape.CacheWriteTTL = time.Hour
	case known:
		shape.CacheWriteTTL = 5 * time.Minute
	}
	res.SetCacheShape(shape)
	return res
}

func servedPromptTokens(provider string, in, cacheRead, cacheWrite int) int {
	if providers.FamilyFor(provider) == providers.FamilyAnthropic {
		return in + cacheRead + cacheWrite
	}
	return in
}
