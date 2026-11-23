package routingpolicy

import (
	"context"
	"time"

	"weave-os/router/internal/flags"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/router/continuity"
	"weave-os/router/internal/router/sessionpin"
)

const switchDecideShadowDefault = true

func logSwitchShadow(ctx context.Context, stayPin sessionpin.Pin, gate continuity.CostGateInput, now time.Time, res TurnRoute) {
	if !flags.BoolOr(ctx, flags.KeySwitchDecideShadow, switchDecideShadowDefault) {
		return
	}
	input := switchShadowInput(stayPin, gate, now, res)
	verdict := DecideSwitch(input)
	fresh := FreshCacheView(input)
	observability.FromContext(ctx).Info("router.switch_shadow",
		"installation_id", res.InstallationID.String(),
		"role", res.PinRole,
		"turn_type", res.TurnType,
		"current_model", input.Current,
		"fresh_model", input.Fresh,
		"cache_cold", input.CacheCold,
		"prefix_broken", input.PrefixBroken,
		"side_request", input.SideRequest,
		"user_prompt", input.UserPrompt,
		"estimated_input_tokens", input.EstimatedInputTokens,
		"prompt_tokens", input.PromptTokens,
		"message_count", input.MessageCount,
		"served_model", res.Decision.Model,
		"pin_tier", res.PinTier,
		"would_serve", verdict.Serve,
		"would_switch", verdict.Switch,
		"would_reason", string(verdict.Reason),
		"would_diverge", verdict.Serve != res.Decision.Model,
		"priced", verdict.Economics.Priced,
		"eviction_usd", verdict.Economics.EvictionUSD,
		"move_value_usd", verdict.Economics.MoveValueUSD,
		"horizon_turns", verdict.Economics.HorizonTurns,
		"downgrade", verdict.Economics.Downgrade,
		"fresh_cache_known", fresh.Known,
		"fresh_messages_since_served", fresh.MessagesSinceServed,
		"fresh_seconds_since_served", int(fresh.SinceServed/time.Second),
		"fresh_cache_ttl_seconds", int(fresh.TTL/time.Second),
		"tracked_model_caches", len(input.ModelCaches),
	)
}

func switchShadowInput(stayPin sessionpin.Pin, gate continuity.CostGateInput, now time.Time, res TurnRoute) SwitchInput {
	return SwitchInput{
		Current:              stayPin.Model,
		Fresh:                gate.Fresh.Model,
		EstimatedInputTokens: gate.EstimatedInputTokens,
		PromptTokens:         PriorPromptTokens(stayPin),
		CacheCold:            stayPin.Model != "" && PinCacheCold(stayPin, gate.PrefixBroken, now),
		PrefixBroken:         gate.PrefixBroken,
		SideRequest:          res.CacheShape().SideRequest,
		Now:                  now,
		MessageCount:         res.CacheShape().MessageCount,
		UserPrompt:           res.CacheShape().UserPrompt,
		ModelCaches:          gate.HMMHistory.ModelCaches,
	}
}
