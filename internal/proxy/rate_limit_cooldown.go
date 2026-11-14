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

// DefaultRateLimitCooldownSeconds is how long a rescued transient upstream
// failure keeps the primary arm out of automatic session selection when
// ROUTER_RATE_LIMIT_COOLDOWN_SECONDS is unset.
const DefaultRateLimitCooldownSeconds = 45

// mergeDemotionCooldowns unions two pins' cooldown maps, keeping the later
// expiry when both name a model.
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

// activeDemotionCooldowns narrows cooldowns to those still in force at now.
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

// readmittableCooldowns narrows the active cooldowns to the arms an exhausted
// rescue may readmit this turn: a session-lifetime strike wins over a
// leftover cooldown on the same model, and a text-only arm cannot take an
// image-bearing turn the scorer already kept it out of.
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

// imageSafeModels drops the text-only models an image-bearing turn cannot
// be rescued onto.
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

// harnessSafeModels drops the models the scorer filters off a tool-bearing
// turn (ToolUseLow, AgenticLow), so a last-resort rescue never hands the
// harness loop to a model this turn deliberately scored out.
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

// expiredDemotionCooldowns is the complement of activeDemotionCooldowns: the
// arms whose cooldown has lapsed and now need a recovery probe.
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

// cooldownsByExpiry lists the models in cooldowns soonest-to-recover first,
// ties broken by name, so an exhausted rescue readmits the arm that has
// cooled the longest before one that was throttled a moment ago.
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

// isRateLimitedPrimaryFailure reports whether the rescued primary's failure
// is a buffered upstream 429: throttling of this caller, not a dead arm.
func isRateLimitedPrimaryFailure(err error) bool {
	return providers.IsUpstreamRateLimited(err)
}

// withRateLimitTurn attaches a turn account when transient_rate_limit is on
// for the request; off, ctx is returned as-is and the account is nil, so
// every consumer keeps the flag-off path.
func (s *Service) withRateLimitTurn(ctx context.Context) (context.Context, *dispatch.ThrottleLedger) {
	if !s.ResolveTransientRateLimit(ctx) {
		return ctx, nil
	}
	return dispatch.WithThrottleLedger(ctx)
}
