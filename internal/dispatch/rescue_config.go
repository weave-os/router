package dispatch

import (
	"context"

	"weave-os/router/internal/flags"
)

type RescueConfig struct {
	SiblingFailover             bool
	EarlyStopRedo               flags.EarlyStopRedoMode
	CyberRefusalRepin           bool
	CyberRefusalRetry           bool
	CyberRefusalFallbackModel   string
	AnthropicServerSideFallback bool
}

func (c RescueConfig) SiblingFailoverEnabled(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeySiblingFailover, c.SiblingFailover)
}

func (c RescueConfig) EarlyStopRedoMode(ctx context.Context) flags.EarlyStopRedoMode {
	mode, err := flags.ParseEarlyStopRedoMode(flags.StringOr(ctx, flags.KeyEarlyStopRedo, string(c.EarlyStopRedo)))
	if err != nil {
		return flags.EarlyStopRedoOff
	}
	return mode
}

func (c RescueConfig) CyberRefusalRepinEnabled(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyCyberRefusalRepin, c.CyberRefusalRepin)
}

func (c RescueConfig) CyberRefusalRetryEnabled(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyCyberRefusalRetry, c.CyberRefusalRetry)
}

func (c RescueConfig) CyberRefusalFallback(ctx context.Context) string {
	return flags.StringOr(ctx, flags.KeyCyberRefusalFallback, c.CyberRefusalFallbackModel)
}

func (c RescueConfig) AnthropicServerSideFallbackEnabled(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyAnthropicServerFallback, c.AnthropicServerSideFallback)
}
