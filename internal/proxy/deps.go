package proxy

import (
	"maps"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/conversation"
	"weave-os/router/internal/ingress"
	"weave-os/router/internal/telemetry"
	"weave-os/router/internal/telemetry/capture"
)

type Deps struct {
	Telemetry                TelemetryDeps
	Conversation             conversation.Config
	Ingress                  *ingress.Formats
	Escalation               EscalationDeps
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

type EscalationDeps struct {
	SwitchyardActive bool
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
	if deps.Ingress != nil {
		s.ingress = *deps.Ingress
	}
	s.escalationHelper().WithSwitchyardActive(deps.Escalation.SwitchyardActive)
	s.credentials.BYOKOnly = deps.BYOKOnly
	s.credentials.KeyedProviders = maps.Clone(deps.DeploymentKeyedProviders)
	if s.lifecycle == nil {
		s.lifecycle = &conversation.Lifecycle{}
	}
	s.lifecycle.Configure(deps.Conversation)
	return s
}
