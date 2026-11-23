package proxy

import (
	"maps"
	"strings"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/conversation"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/ingress"
	"weave-os/router/internal/telemetry"
	"weave-os/router/internal/telemetry/capture"
)

type Deps struct {
	Telemetry                TelemetryDeps
	Conversation             conversation.Config
	Ingress                  *ingress.Formats
	Escalation               EscalationDeps
	Routing                  *RoutingDeps
	BYOKOnly                 bool
	DeploymentKeyedProviders map[string]struct{}
	Dispatch                 *dispatch.Config
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

type RoutingDeps struct {
	PrefixTrimFreeSwitch *bool
	EffortEscalation     bool
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
	if deps.Routing != nil {
		s.routing = s.routing.WithEffortEscalation(deps.Routing.EffortEscalation)
		if deps.Routing.PrefixTrimFreeSwitch != nil {
			s.routing = s.routing.WithPrefixTrimFreeSwitch(*deps.Routing.PrefixTrimFreeSwitch)
		}
	}
	s.credentials.BYOKOnly = deps.BYOKOnly
	s.credentials.KeyedProviders = maps.Clone(deps.DeploymentKeyedProviders)
	if deps.Dispatch != nil {
		applyDispatchConfig(s, *deps.Dispatch)
	}
	if s.lifecycle == nil {
		s.lifecycle = &conversation.Lifecycle{}
	}
	s.lifecycle.Configure(deps.Conversation)
	return s
}

func applyDispatchConfig(s *Service, cfg dispatch.Config) {
	switch cfg.TranslationCompatibilityMode {
	case TranslationCompatibilityOff, TranslationCompatibilityShadow, TranslationCompatibilityEnforce:
		s.translationCompatibilityMode = cfg.TranslationCompatibilityMode
	}
	fallbackModel := s.rescueConfig.CyberRefusalFallbackModel
	if model := strings.TrimSpace(cfg.Rescue.CyberRefusalFallbackModel); model != "" {
		fallbackModel = model
	}
	s.rescueConfig = cfg.Rescue
	s.rescueConfig.CyberRefusalFallbackModel = fallbackModel
	s.armDemotion = cfg.ArmDemotion
	s.throttling = cfg.Throttling
	s.engine.SetExecutor(cfg.Executor)
	s.engine.SetPlans(cfg.Plans)
	s.engine.SetDeployment(cfg.Deployment)
	s.translationMetrics = cfg.TranslationMetrics
}
