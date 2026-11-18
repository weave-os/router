package proxy

import (
	"context"

	"weave-os/router/internal/flags"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/routingpolicy"
)

type switchShadowTurn struct {
	Request      router.Request
	ActivePin    sessionpin.Pin
	HMMHistory   sessionpin.Pin
	Fresh        router.Decision
	InputTokens  int
	PrefixBroken bool
}

func (s *Service) logSwitchShadow(ctx context.Context, turn switchShadowTurn, res turnLoopResult) {
	req, activePin, hmmHistory, fresh := turn.Request, turn.ActivePin, turn.HMMHistory, turn.Fresh
	inputTokens, prefixBroken := turn.InputTokens, turn.PrefixBroken
	if !flags.BoolOr(ctx, flags.KeySwitchDecideShadow, s.switchDecideShadow) {
		return
	}
	input := routingpolicy.SwitchInput{
		Fresh:                fresh.Model,
		EstimatedInputTokens: inputTokens,
		PrefixBroken:         prefixBroken,
	}
	if stayPin, found := hmmStayPin(s, req, activePin, hmmHistory); found {
		input.Current = stayPin.Model
		input.CacheCold = pinCacheCold(stayPin, prefixBroken, s.clock.Now())
	}
	verdict := routingpolicy.DecideSwitch(input)
	observability.FromContext(ctx).Info("router.switch_shadow",
		"installation_id", res.InstallationID.String(),
		"role", res.PinRole,
		"turn_type", res.TurnType,
		"current_model", input.Current,
		"fresh_model", fresh.Model,
		"cache_cold", input.CacheCold,
		"prefix_broken", prefixBroken,
		"estimated_input_tokens", inputTokens,
		"served_model", res.Decision.Model,
		"pin_tier", res.PinTier,
		"would_serve", verdict.Serve,
		"would_switch", verdict.Switch,
		"would_reason", string(verdict.Reason),
		"would_diverge", verdict.Serve != res.Decision.Model,
	)
}
