package proxy

import (
	"context"
	"net/http"

	"weave-os/router/internal/flags"

	"weave-os/router/internal/telemetry"
	"weave-os/router/internal/telemetry/turnrecord"
)

type turnTelemetry struct {
	emitter   telemetry.Emitter
	repo      turnrecord.TelemetryRepository
	turnClock telemetry.SessionTurnClock
	flags     TelemetryFlags
}

type TelemetryFlags struct {
	TurnSignalCapture              bool
	NativeAnthropicResponseSignals bool
	NativeOpenAIResponseSignals    bool
}

func (f TelemetryFlags) resolveTurnSignalCapture(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyTurnSignalCapture, f.TurnSignalCapture)
}

func (f TelemetryFlags) resolveNativeAnthropicResponseSignals(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyNativeAnthropicResponseSignals, f.NativeAnthropicResponseSignals)
}

func (f TelemetryFlags) resolveNativeOpenAIResponseSignals(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyNativeOpenAIResponseSignals, f.NativeOpenAIResponseSignals)
}

func (t turnTelemetry) newUsageExtractor(inner http.ResponseWriter, provider string) (*telemetry.UsageExtractor, http.ResponseWriter) {
	if t.emitter == nil {
		return newFallbackUsageExtractor(inner, provider)
	}
	return t.emitter.NewUsageExtractor(inner, provider)
}
