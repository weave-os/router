package proxy

import (
	"context"
	"time"

	"weave-os/router/internal/flags"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/router/hmm"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/routingpolicy"
)

const switchDecideShadowDefault = true

func logSwitchShadow(ctx context.Context, stayPin sessionpin.Pin, gate hmm.CostGateInput, now time.Time, res turnLoopResult) {
	if !flags.BoolOr(ctx, flags.KeySwitchDecideShadow, switchDecideShadowDefault) {
		return
	}
	input := routingpolicy.SwitchInput{
		Current:              stayPin.Model,
		Fresh:                gate.Fresh.Model,
		EstimatedInputTokens: gate.EstimatedInputTokens,
		PromptTokens:         routingpolicy.PriorPromptTokens(stayPin),
		OutputTokens:         stayPin.LastOutputTokens,
		CacheCold:            stayPin.Model != "" && pinCacheCold(stayPin, gate.PrefixBroken, now),
		PrefixBroken:         gate.PrefixBroken,
		Now:                  now,
		MessageCount:         res.MessageCount,
		UserPrompt:           res.UserPromptTurn,
		ModelCaches:          gate.HMMHistory.ModelCaches,
	}
	verdict := routingpolicy.DecideSwitch(input)
	observability.FromContext(ctx).Info("router.switch_shadow",
		"installation_id", res.InstallationID.String(),
		"role", res.PinRole,
		"turn_type", res.TurnType,
		"current_model", input.Current,
		"fresh_model", input.Fresh,
		"cache_cold", input.CacheCold,
		"prefix_broken", input.PrefixBroken,
		"estimated_input_tokens", input.EstimatedInputTokens,
		"prompt_tokens", input.PromptTokens,
		"message_count", input.MessageCount,
		"model_caches", len(input.ModelCaches),
		"served_model", res.Decision.Model,
		"pin_tier", res.PinTier,
		"would_serve", verdict.Serve,
		"would_switch", verdict.Switch,
		"would_reason", string(verdict.Reason),
		"would_diverge", verdict.Serve != res.Decision.Model,
	)
}
