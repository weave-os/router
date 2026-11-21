package proxy

import (
	"net/http"

	"weave-os/router/internal/telemetry"
)

type turnTelemetry struct {
	emitter   TelemetryEmitter
	repo      TelemetryRepository
	turnClock SessionTurnClock
}

func (t turnTelemetry) newRecorder() telemetry.Recorder {
	if t.emitter == nil {
		return nil
	}
	return t.emitter.NewRecorder()
}

func (t turnTelemetry) newUsageExtractor(inner http.ResponseWriter, provider string) (*telemetry.UsageExtractor, http.ResponseWriter) {
	if t.emitter == nil {
		return newFallbackUsageExtractor(inner, provider)
	}
	return t.emitter.NewUsageExtractor(inner, provider)
}
