package proxy

import (
	"maps"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/telemetry"
	"weave-os/router/internal/telemetry/capture"
)

type Deps struct {
	Telemetry                TelemetryDeps
	BYOKOnly                 bool
	DeploymentKeyedProviders map[string]struct{}
}

type TelemetryDeps struct {
	Clock                  auth.Clock
	SessionTurnClock       telemetry.SessionTurnClock
	ContentCapture         capture.Mode
	ContentCaptureMaxBytes int
	Billing                *billing.Service
	Flags                  *TelemetryFlags
}

func WithDeps(s *Service, deps Deps) *Service {
	s.clock = deps.Telemetry.Clock
	s.turnTelemetry.turnClock = deps.Telemetry.SessionTurnClock
	s.captureMode = deps.Telemetry.ContentCapture
	if deps.Telemetry.ContentCaptureMaxBytes > 0 {
		s.captureMaxBytes = deps.Telemetry.ContentCaptureMaxBytes
	}
	s.billing = deps.Telemetry.Billing
	if deps.Telemetry.Flags != nil {
		s.turnTelemetry.flags = *deps.Telemetry.Flags
	}
	s.credentials.BYOKOnly = deps.BYOKOnly
	s.credentials.KeyedProviders = maps.Clone(deps.DeploymentKeyedProviders)
	return s
}
