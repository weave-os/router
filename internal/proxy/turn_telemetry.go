package proxy

import (
	"net/http"

	"weave-os/router/internal/telemetry"
	"weave-os/router/internal/telemetry/turnrecord"
)

type turnTelemetry struct {
	emitter   telemetry.Emitter
	repo      turnrecord.TelemetryRepository
	turnClock telemetry.SessionTurnClock
}

func (t turnTelemetry) newUsageExtractor(inner http.ResponseWriter, provider string) (*telemetry.UsageExtractor, http.ResponseWriter) {
	if t.emitter == nil {
		return newFallbackUsageExtractor(inner, provider)
	}
	return t.emitter.NewUsageExtractor(inner, provider)
}
