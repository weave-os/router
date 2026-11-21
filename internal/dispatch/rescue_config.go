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

func (c RescueConfig) EarlyStopRedoMode(ctx context.Context) flags.EarlyStopRedoMode {
	mode, err := flags.ParseEarlyStopRedoMode(flags.StringOr(ctx, flags.KeyEarlyStopRedo, string(c.EarlyStopRedo)))
	if err != nil {
		return flags.EarlyStopRedoOff
	}
	return mode
}
