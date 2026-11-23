package dispatch

import (
	"context"
	"strings"

	"weave-os/router/internal/flags"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/translate"
)

type RescueConfig struct {
	SiblingFailover             bool
	EarlyStopRedo               flags.EarlyStopRedoMode
	CyberRefusalRepin           bool
	CyberRefusalRetry           bool
	CyberRefusalFallbackModel   string
	AnthropicServerSideFallback bool
}

func (c RescueConfig) EarlyStopRedoMode(ctx context.Context) flags.EarlyStopRedoMode {
	mode, err := flags.ParseEarlyStopRedoMode(flags.StringOr(ctx, flags.KeyEarlyStopRedo, string(c.EarlyStopRedo)))
	if err != nil {
		return flags.EarlyStopRedoOff
	}
	return mode
}

func (c RescueConfig) SiblingFailoverEnabled(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeySiblingFailover, c.SiblingFailover)
}

func (c RescueConfig) CyberRefusalRepinEnabled(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyCyberRefusalRepin, c.CyberRefusalRepin)
}

func (c RescueConfig) CyberRefusalRetryEnabled(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyCyberRefusalRetry, c.CyberRefusalRetry)
}

func (c RescueConfig) CyberRefusalFallbackModelFor(ctx context.Context) string {
	return flags.StringOr(ctx, flags.KeyCyberRefusalFallback, c.CyberRefusalFallbackModel)
}

func (c RescueConfig) AnthropicServerSideFallbackEnabled(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyAnthropicServerFallback, c.AnthropicServerSideFallback)
}

func (c RescueConfig) EarlyStopRedoModeFor(ctx context.Context, routeRes RescueTurn, decision router.Decision, streaming, agentShadow bool) flags.EarlyStopRedoMode {
	mode := c.EarlyStopRedoMode(ctx)
	if mode == flags.EarlyStopRedoOff || !streaming || agentShadow ||
		requestcontext.ClientIdentityFrom(ctx).ClientApp != requestcontext.ClientAppClaudeCode ||
		routeRes.TurnType != turntype.ToolResult ||
		routeRes.HardPinned || routeRes.CallerModelPassthrough || routeRes.UsageBypass ||
		strings.HasPrefix(decision.Reason, translate.ReasonUserForceModel) {
		return flags.EarlyStopRedoOff
	}
	return mode
}
