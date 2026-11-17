package proxy

import (
	"context"
	"slices"
	"sort"
	"time"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/sessionpin"
)

const DefaultRateLimitCooldownSeconds = 45

func mergeDemotionCooldowns(a, b map[string]time.Time) map[string]time.Time {
	if len(a) == 0 {
		return b
	}
	if len(b) == 0 {
		return a
	}
	merged := make(map[string]time.Time, len(a)+len(b))
	for model, until := range a {
		merged[model] = until
	}
	for model, until := range b {
		if current, ok := merged[model]; !ok || until.After(current) {
			merged[model] = until
		}
	}
	return merged
}

func activeDemotionCooldowns(cooldowns map[string]time.Time, now time.Time) map[string]time.Time {
	active := make(map[string]time.Time, len(cooldowns))
	for _, model := range sessionpin.ActiveCooldowns(cooldowns, now) {
		active[model] = cooldowns[model]
	}
	if len(active) == 0 {
		return nil
	}
	return active
}

func readmittableCooldowns(cooling map[string]time.Time, permanent []string, imageCount int) map[string]time.Time {
	out := make(map[string]time.Time, len(cooling))
	for model, until := range cooling {
		if slices.Contains(permanent, model) {
			continue
		}
		if !catalog.AcceptsImageCount(model, imageCount, nil) {
			continue
		}
		out[model] = until
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func imageSafeModels(models []string, imageCount int) []string {
	if imageCount <= 0 {
		return models
	}
	var out []string
	for _, model := range models {
		if catalog.AcceptsImageCount(model, imageCount, nil) {
			out = append(out, model)
		}
	}
	return out
}

func harnessSafeModels(models []string, hasTools bool) []string {
	if !hasTools || len(models) == 0 {
		return models
	}
	toolUseLow, agenticLow := catalog.ToolUseLowSet(), catalog.AgenticLowSet()
	var out []string
	for _, model := range models {
		_, weakTools := toolUseLow[model]
		_, weakAgentic := agenticLow[model]
		if !weakTools && !weakAgentic {
			out = append(out, model)
		}
	}
	return out
}

func expiredDemotionCooldowns(cooldowns map[string]time.Time, now time.Time) map[string]time.Time {
	var expired map[string]time.Time
	for model, until := range cooldowns {
		if now.Before(until) {
			continue
		}
		if expired == nil {
			expired = make(map[string]time.Time, len(cooldowns))
		}
		expired[model] = until
	}
	return expired
}

func cooldownsByExpiry(cooldowns map[string]time.Time) []string {
	models := make([]string, 0, len(cooldowns))
	for model := range cooldowns {
		models = append(models, model)
	}
	sort.Slice(models, func(i, j int) bool {
		if !cooldowns[models[i]].Equal(cooldowns[models[j]]) {
			return cooldowns[models[i]].Before(cooldowns[models[j]])
		}
		return models[i] < models[j]
	})
	return models
}

func isRateLimitedPrimaryFailure(err error) bool {
	return providers.IsUpstreamRateLimited(err)
}

func (s *Service) withRateLimitTurn(ctx context.Context) (context.Context, *dispatch.ThrottleLedger) {
	if !s.ResolveTransientRateLimit(ctx) {
		return ctx, nil
	}
	return dispatch.WithThrottleLedger(ctx)
}
