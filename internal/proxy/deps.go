package proxy

import (
	"maps"
	"strings"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/conversation"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/ingress"
	"weave-os/router/internal/router/candidates"
	"weave-os/router/internal/router/continuity"
	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/llmescalation"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/routingpolicy"
	"weave-os/router/internal/telemetry"
	"weave-os/router/internal/telemetry/capture"
	"weave-os/router/internal/telemetry/policyshadow"
)

type Deps struct {
	Telemetry                TelemetryDeps
	Conversation             conversation.Config
	Ingress                  *ingress.Formats
	Escalation               EscalationDeps
	Routing                  *RoutingDeps
	BYOKOnly                 bool
	DeploymentKeyedProviders map[string]struct{}
	Credentials              *credentials.Brokerage
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
	PrefixTrimFreeSwitch      *bool
	EffortEscalation          bool
	AvailableModels           map[string]struct{}
	DefaultBaselineModel      string
	ExcludedModels            []string
	HardPinResolver           HardPinResolver
	ExplicitUtilityHardPin    bool
	SubAgentProvider          string
	SubAgentModel             string
	Deadline                  routingpolicy.PolicyDeadline
	GlobalAutomaticExclusions candidates.GlobalAutomaticExclusionStore
	Plans                     *policy.PlanResolver
	Continuity                *continuity.Config
	HMM                       *continuity.AuthoritativeConfig
	BandSwap                  bool
	Strategies                []policy.StrategySpec
	Escalation                *RoutingEscalation
	ClassifierSessions        *ClassifierSessions
	RecipeShadow              *policyshadow.RecipeRecorder
}

type RoutingEscalation struct {
	Store         escalation.Store
	Observer      escalation.Observer
	LLMStore      llmescalation.Store
	LLMJudge      llmescalation.Judge
	QwenJudge     llmescalation.Judge
	ShadowDefault flags.EscalationClassifier
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
		applyRoutingDeps(s, *deps.Routing)
	}
	s.credentials.BYOKOnly = deps.BYOKOnly
	s.credentials.KeyedProviders = maps.Clone(deps.DeploymentKeyedProviders)
	if deps.Credentials != nil {
		applyCredentialDeps(s, deps.Credentials)
	}
	if deps.Dispatch != nil {
		applyDispatchConfig(s, *deps.Dispatch)
	}
	if s.lifecycle == nil {
		s.lifecycle = &conversation.Lifecycle{}
	}
	s.lifecycle.Configure(deps.Conversation)
	return s
}

func applyCredentialDeps(s *Service, b *credentials.Brokerage) {
	s.credentials.ExternalKeyUsage = b.ExternalKeyUsage
	s.credentials.UsageObserver = b.UsageObserver
	s.credentials.ClaudePlans = b.ClaudePlans
	s.credentials.CodexExtraCredits = b.CodexExtraCredits
	s.credentials.ManagedSubscriptions = b.ManagedSubscriptions
	s.credentials.PassthroughEligible = maps.Clone(b.PassthroughEligible)
	s.credentials.ExcludedProvidersOverride = maps.Clone(b.ExcludedProvidersOverride)
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

func DispatchEngine(s *Service) *dispatch.Engine {
	if s == nil {
		return nil
	}
	return s.engine
}

func applyRoutingDeps(s *Service, deps RoutingDeps) {
	if routingEscalation := deps.Escalation; routingEscalation != nil {
		s.escalationHelper().WithXGB(routingEscalation.Store, routingEscalation.Observer)
		s.escalationHelper().WithLLM(routingEscalation.LLMStore, routingEscalation.LLMJudge)
		s.escalationHelper().WithQwen(routingEscalation.QwenJudge)
		s.escalationHelper().WithShadowDefault(routingEscalation.ShadowDefault)
	}
	for _, spec := range deps.Strategies {
		registerPolicyStrategy(s, spec)
	}
	hardPins := s.routing.HardPins()
	hardPins.Resolver, hardPins.ExplicitUtility = deps.HardPinResolver, deps.ExplicitUtilityHardPin
	hardPins.SubAgentProvider, hardPins.SubAgentModel = deps.SubAgentProvider, deps.SubAgentModel
	routing := s.routing.WithHardPins(hardPins).WithDeadline(deps.Deadline).WithGlobalAutomaticExclusions(deps.GlobalAutomaticExclusions).
		WithAvailableModels(deps.AvailableModels).WithDefaultBaselineModel(deps.DefaultBaselineModel).WithEffortEscalation(deps.EffortEscalation).
		WithPlansFrom(routingpolicy.New(deps.Plans)).WithExcludedModelsOverride(deps.ExcludedModels)
	if deps.PrefixTrimFreeSwitch != nil {
		routing = routing.WithPrefixTrimFreeSwitch(*deps.PrefixTrimFreeSwitch)
	}
	s.routing = routing
	if deps.Continuity != nil {
		s.continuityConfig = *deps.Continuity
	}
	if deps.HMM != nil {
		s.hmmConfig = *deps.HMM
	}
	withBandSwap(s, deps.BandSwap)
	if deps.ClassifierSessions != nil {
		s.classifierSessions = deps.ClassifierSessions
	}
	if deps.RecipeShadow != nil {
		s.turnTelemetry.recipe = *deps.RecipeShadow
	}
}
