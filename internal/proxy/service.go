package proxy

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/conversation"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/feedback"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/ingress"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy/usage"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/bandswap"
	"weave-os/router/internal/router/breaker"
	"weave-os/router/internal/router/candidates"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/continuity"
	"weave-os/router/internal/router/escalation/floor"
	"weave-os/router/internal/router/escalationdashboard"
	"weave-os/router/internal/router/handover"
	"weave-os/router/internal/router/llmescalation"
	"weave-os/router/internal/router/planner"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/router/sessionstrategy"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/routingpolicy"
	"weave-os/router/internal/sse"
	"weave-os/router/internal/subscriptions"
	"weave-os/router/internal/subscriptions/entitlement"
	"weave-os/router/internal/telemetry"
	"weave-os/router/internal/timing"
	"weave-os/router/internal/translate"
	providerusage "weave-os/router/internal/usage"
	"weave-os/router/internal/websearch"

	"github.com/google/uuid"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type TelemetryEmitter interface {
	NewRecorder() telemetry.Recorder
	NewUsageExtractor(http.ResponseWriter, string) (*telemetry.UsageExtractor, http.ResponseWriter)
}

type Service struct {
	externalKeyUsage    ExternalKeyUsageRecorder
	recoveryProbeMu     sync.Mutex
	recoveryProbeLeases map[string]localRecoveryProbeLease
	nextRecoveryProbeID uint64

	subscriptionModels           subscriptionModelAccess
	claudePlans                  *subscriptions.PlanResolver
	sessionHolds                 subscriptionSessionHolds
	strategies                   map[router.Strategy]registeredStrategy
	clients                      *dispatch.Clients
	executor                     *dispatch.Executor
	defaultExecutorOnce          sync.Once
	plans                        *policy.PlanResolver
	defaultPlansOnce             sync.Once
	inferenceDeployment          policy.DeploymentPolicyConfig
	translationCompatibilityMode TranslationCompatibilityMode
	scopedSearchRequirement      bool
	searchRequirementDecayTurns  int
	searchUse                    *searchUseTracker
	emitter                      TelemetryEmitter
	translationMetrics           telemetry.TranslationMetrics
	pinStore                     sessionpin.Store
	escalation                   *floor.Helper
	escalationDashboardStore     escalationdashboard.Store
	llmEscalationConfiguration   llmescalation.ConfigurationStore
	llmEscalationInvalidate      func(string)
	sessionStrategyStore         sessionstrategy.Store
	classifierSessions           *classifierSessions
	noProgress                   *conversation.NoProgressTracker
	compaction                   *conversation.CompactionTracker
	prefixTrimFreeSwitch         bool
	escapeNormalize              bool
	hardPinExplore               bool
	explicitUtilityHardPin       bool
	hardPinProvider              string
	hardPinModel                 string
	hardPinResolver              HardPinResolver
	subAgentProvider             string
	subAgentModel                string
	telemetry                    TelemetryRepository
	turnClock                    SessionTurnClock
	captureMode                  ContentCaptureMode
	captureMaxBytes              int
	redactor                     Redactor
	gatewayCaps                  dispatch.GatewayCapabilities
	unservedGatewayModels        sync.Map
	routing                      *routingpolicy.Policy
	globalAutomaticExclusions    *globalAutomaticExclusionCache
	credentials                  credentials.Brokerage
	continuityConfig             continuity.Config
	hmmServing
	policyDeadlineFallback      bool
	policyDeadlineDefaultModel  string
	cyberRefusalRepin           bool
	cyberRefusalRetry           bool
	anthropicServerSideFallback bool

	siblingFailover                bool
	codexExtraCredits              bool
	openAIResponsesBroad           bool
	nativeAnthropicResponseSignals bool
	nativeOpenAIResponseSignals    bool
	allowedModelsHeader            bool
	committedStreamArmDemotion     bool
	rescuedFailureArmDemotion      bool
	transientRateLimit             bool
	rateLimitCooldownSeconds       int
	providerOverloaded             bool
	overloadBreaker                *breaker.Registry
	ingress                        ingress.Formats
	cyberRefusalFallbackModel      string
	effortEscalation               bool
	ccOrchToolsCrossVendor         bool
	ccTaskToolsCrossVendor         bool
	ccAutonomySystemAppend         bool
	earlyStopRedo                  flags.EarlyStopRedoMode
	ccWorkspaceSystemAppend        bool
	bandSwap                       *bandswap.Classifier

	turnSignalCaptureEnabled     bool
	textRepetitionBreakEnabled   bool
	feedbackStore                RouterFeedbackStore
	summarizer                   handover.Summarizer
	webSearch                    websearch.Executor
	compactionHandoverSummarizer handover.Summarizer
	availableModels              map[string]struct{}
	defaultBaselineModel         string
	billing                      *billing.Service
	retrySleep                   func(context.Context, time.Duration) error
	clock                        auth.Clock
	feedbackRepo                 FeedbackRepository
	feedbackSigner               *feedback.Signer
	piHandoffSecret              []byte
	feedbackBaseURL              string
	usageObserver                *usage.Observer

	managedSubscriptions subscriptions.Leaser
}

type registeredStrategy struct {
	router       router.Router
	unavailable  error
	capabilities policy.Capabilities
	outcomes     policy.OutcomeReporter
	feedback     policy.FeedbackReporter
}

type APIKeyIDContextKey struct{}

func apiKeyIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(APIKeyIDContextKey{}).(string)
	return id
}

type ExternalIDContextKey struct{}

type AnthropicSubscriptionContextKey struct{}

type OpenAISubscriptionContextKey struct{}
type OpenAIAccountIDContextKey struct{}

type codexResponsesBodyContextKey struct{}

type nativeResponsesBodyContextKey struct{}

type responsesLastAnnouncedModelContextKey struct{}

type responsesReplayedModelContextKey struct{}

type nativeResponsesReasoningHashContextKey struct{}

type nativeResponsesToolHashContextKey struct{}

type codexSkillContextKey struct{}

type responsesFooterEchoedContextKey struct{}

type codexTitleGenerationContextKey struct{}

type InstallationExcludedModelsContextKey struct{}

type InstallationAllowedModelsContextKey struct{}

type InstallationSubscriptionStateModelsEnabledContextKey struct{}

type InstallationSubscriptionModelsWhenActiveContextKey struct{}

type InstallationSubscriptionModelsWhenInactiveContextKey struct{}

type InstallationExcludedProvidersContextKey struct{}

type SessionDisabledProvidersContextKey struct{}

type SessionDemotedModelsContextKey struct{}

type ObservedOverflowModelsContextKey struct{}

type SessionCooldownModelsContextKey struct{}

type SessionCooldownProbeDeniedModelsContextKey struct{}

type SessionStrikeReadmitModelsContextKey struct{}

type InstallationPreferredModelsContextKey struct{}

type InstallationFastModeModelsContextKey struct{}

type InstallationRoutingKnobsContextKey struct{}

type ClusterModelListsContextKey struct{}

type InstallationUsageBypassContextKey struct{}

type InstallationSubscriptionRoutingDisabledContextKey struct{}

type InstallationPickedModelCreditFallbackContextKey struct{}

type InstallationHideTerminalSurfacesContextKey struct{}

type InstallationShowModelSelectionReasoningContextKey struct{}

type InstallationTrialCaptureContextKey struct{}

type PolicyTrainingAllowedContextKey struct{}

type PolicyDebugEnabledContextKey struct{}

type PolicyRoutingIntentContextKey struct{}

type PolicyRolloutIDContextKey struct{}

type PolicyShadowStrategyContextKey struct{}

type UsageBypassConfig struct {
	Enabled   bool
	Threshold *float64
}

func usageBypassFromContext(ctx context.Context) (UsageBypassConfig, bool) {
	cfg, ok := ctx.Value(InstallationUsageBypassContextKey{}).(UsageBypassConfig)
	if !ok || !cfg.Enabled {
		return UsageBypassConfig{}, false
	}
	return cfg, true
}

const routingMarkerHeader = "X-Weave-Routing-Marker"
const routingMarkerPrefix = "✦ **Weave Router** → "
const maxSidecarDisplayMarkerRunes = 512
const policyOutcomeReportTimeout = 2 * time.Second
const policyFeedbackReportTimeout = 2 * time.Second
const policyShadowDecisionTimeout = 3 * time.Second
const policyOutcomeResponseMaxBytes = 256 * 1024

type policyOutcomeResponse struct {
	Body      []byte
	Truncated bool
}

func suppressMarkerIfRequested(ctx context.Context, h http.Header, marker string) string {
	if hideTerminalSurfacesForRequest(ctx) {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(h.Get(routingMarkerHeader))) {
	case "off", "false", "0", "none":
		return ""
	}
	return marker
}

func machineReadTurn(res turnLoopResult) bool {
	return res.SuggestionMode || res.CallerModelPassthrough || isUnpinnedScoredTurn(res.TurnType) ||
		res.TurnType == turntype.Probe || res.TurnType == turntype.Compaction
}

func muteSlowStartFor(ctx context.Context, res turnLoopResult) {
	if machineReadTurn(res) || res.HardPinned || res.TurnType == turntype.SubAgentDispatch {
		attemptTraceFromContext(ctx).MuteSlowStart()
	}
}

func announcementTurnFor(res turnLoopResult, replayedModel string) turnLoopResult {
	if res.PriorServedModel == "" {
		res.PriorServedModel = replayedModel
	}
	return res
}

func routingMarkerFor(res turnLoopResult) string {
	decision := res.Decision
	if decision.Model == "" {
		return ""
	}
	if machineReadTurn(res) {
		return ""
	}
	if res.ForcedPinDropped {
		parts := []string{"✦ **Weave Router** → " + decision.Model, markerReasonForcedPinDropped}
		if res.ForcedPinModel != "" {
			parts = []string{
				"✦ **Weave Router** → " + decision.Model,
				fmt.Sprintf("%s (%s)", markerReasonForcedPinDropped, res.ForcedPinModel),
			}
		}
		return strings.Join(parts, " · ") + "\n\n"
	}
	if res.HardPinned {
		return ""
	}
	if res.EscalationShadowMarked {
		return routingMarkerPrefix + decision.Model + " · " + markerReasonShadowEscalation + "\n\n"
	}
	if baseModelOf(res.PriorServedModel) == res.Decision.Model {
		return ""
	}
	if decision.Metadata != nil {
		if marker := sanitizeSidecarDisplayMarker(decision.Metadata.DisplayMarker); marker != "" {
			return marker + "\n\n"
		}
	}
	parts := []string{"✦ **Weave Router** → " + decision.Model}
	if reason := routingReasonShort(res); reason != "" {
		parts = append(parts, reason)
	}
	return strings.Join(parts, " · ") + "\n\n"
}

type modelSelectionComplexity string

const (
	selectionComplexityFast     modelSelectionComplexity = "fast"
	selectionComplexityExplore  modelSelectionComplexity = "explore"
	selectionComplexityBalanced modelSelectionComplexity = "balanced"
	selectionComplexityLow      modelSelectionComplexity = "low"
	selectionComplexityMid      modelSelectionComplexity = "mid"
	selectionComplexityMedium   modelSelectionComplexity = "medium"
	selectionComplexityHigh     modelSelectionComplexity = "high"
	selectionComplexityMaximum  modelSelectionComplexity = "maximum"
)

func modelSelectionReason(ports continuity.StrategyPorts, res turnLoopResult, failoverReason string) string {
	if res.Decision.Metadata != nil {
		for _, label := range []string{ports.Facts.ClassifierTelemetryOf(res.Decision.Metadata).PredictedLabel, ports.Facts.ClassifierTelemetryOf(res.Decision.Metadata).PolicyGroup} {
			classification := modelSelectionComplexity(strings.ToLower(strings.TrimSpace(label)))
			switch classification {
			case selectionComplexityMid:
				return "request classified as medium difficulty"
			case selectionComplexityLow, selectionComplexityMedium, selectionComplexityHigh, selectionComplexityMaximum:
				return "request classified as " + string(classification) + " difficulty"
			case selectionComplexityFast, selectionComplexityExplore, selectionComplexityBalanced:
				return "request classified as " + string(classification)
			}
		}
	}
	if failoverReason != "" {
		return failoverReason
	}
	if reason := routingReasonShort(res); reason != "" {
		return reason
	}
	return "model selected for this turn"
}

func sanitizeSidecarDisplayMarker(raw string) string {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	raw = strings.ReplaceAll(raw, "\r", "\n")
	raw = strings.TrimSpace(raw)
	if raw == "" || !strings.HasPrefix(raw, routingMarkerPrefix) {
		return ""
	}
	lines := strings.Split(raw, "\n")
	kept := make([]string, 0, 3)
	for i, line := range lines {
		if i >= 3 {
			break
		}
		line = strings.TrimSpace(line)
		if line == "" {
			break
		}
		if i > 0 && !strings.HasPrefix(line, "↳ ") {
			break
		}
		kept = append(kept, line)
	}
	if len(kept) == 0 {
		return ""
	}
	out := strings.Join(kept, "\n")
	runes := []rune(out)
	if len(runes) > maxSidecarDisplayMarkerRunes {
		out = string(runes[:maxSidecarDisplayMarkerRunes])
	}
	return out
}

const (
	markerReasonShadowEscalation    = "escalation marked — shadow mode; no action taken"
	markerReasonUserForced          = "pinned by force-model"
	markerReasonSwitched            = "switched for positive EV after cache eviction"
	markerReasonStayed              = "stayed on your last pick"
	markerReasonTierUpgrade         = "upgraded to a stronger tier"
	markerReasonBestPick            = "best pick for this turn"
	markerReasonBaseline            = "fell back to baseline after provider outage"
	markerReasonSibling             = "switched after the picked model was unavailable"
	markerReasonSubscriptionCovered = "switched to a subscription-covered model"
	markerReasonCyberRefusal        = "switched after the picked model declined the request"
	markerReasonForcedPinDropped    = "your force-model pin could not be served this turn"
)

func baselineRoutingMarkerFor(res turnLoopResult, baselineModel string) string {
	if res.SuggestionMode || isUnpinnedScoredTurn(res.TurnType) || res.TurnType == turntype.Probe || baselineModel == "" {
		return ""
	}
	if baseModelOf(res.PriorServedModel) == baselineModel {
		return ""
	}
	return "✦ **Weave Router** → " + baselineModel + " · " + markerReasonBaseline + "\n\n"
}

func siblingMarkerReason(ctx context.Context, failedModel string) string {
	if failedModel == "" {
		return markerReasonSibling
	}
	cause := "was unavailable"
	if failed, ok := lastFailureOn(attemptTraceFromContext(ctx), failedModel); ok {
		cause = failureWordingFor(failed).badge
	}
	return "switched after " + failedModel + " " + cause
}

func siblingMarkerFor(ports continuity.StrategyPorts, ctx context.Context, h http.Header, res turnLoopResult, failedModel, siblingModel string) string {
	return switchMarkerFor(ports, ctx, h, res, siblingMarkerReason(ctx, failedModel), siblingModel)
}

func switchMarkerFor(ports continuity.StrategyPorts, ctx context.Context, h http.Header, res turnLoopResult, reason, siblingModel string) string {
	return suppressMarkerIfRequested(ctx, h, modelSelectionMarkerForRequest(ports, ctx, res, siblingRoutingMarkerFor(res, reason, siblingModel), siblingModel, reason))
}

func siblingRoutingMarkerFor(res turnLoopResult, reason, siblingModel string) string {
	if res.SuggestionMode || isUnpinnedScoredTurn(res.TurnType) || res.TurnType == turntype.Probe || siblingModel == "" || baseModelOf(res.PriorServedModel) == siblingModel {
		return ""
	}
	return "✦ **Weave Router** → " + siblingModel + " · " + reason + "\n\n"
}

func subscriptionTargetMarker(ports continuity.StrategyPorts, ctx context.Context, h http.Header, res turnLoopResult, picked, target router.Decision, pickedMarker string) string {
	if target.Model == picked.Model {
		return pickedMarker
	}
	if _, ok := lastFailureOn(attemptTraceFromContext(ctx), picked.Model); !ok {
		return switchMarkerFor(ports, ctx, h, res, markerReasonSubscriptionCovered, target.Model)
	}
	return siblingMarkerFor(ports, ctx, h, res, picked.Model, target.Model)
}

func cyberRefusalRoutingMarkerFor(res turnLoopResult, fallbackModel string) string {
	if res.SuggestionMode || isUnpinnedScoredTurn(res.TurnType) || res.TurnType == turntype.Probe || fallbackModel == "" || baseModelOf(res.PriorServedModel) == fallbackModel {
		return ""
	}
	return "✦ **Weave Router** → " + fallbackModel + " · " + markerReasonCyberRefusal + "\n\n"
}

func routingReasonShort(res turnLoopResult) string {
	if res.PlannerDecision.Evaluated() {
		return humanReasonFromPlanner(res.PlannerDecision.Reason)
	}
	if res.UserForced {
		return markerReasonUserForced
	}
	return markerReasonBestPick
}

func humanReasonFromPlanner(code string) string {
	switch code {
	case planner.ReasonEVPositive:
		return markerReasonSwitched
	case planner.ReasonEVNegative, planner.ReasonNoPriorUsage:
		return markerReasonStayed
	case planner.ReasonTierUpgrade:
		return markerReasonTierUpgrade
	case planner.ReasonNoPin, planner.ReasonSameModel:
		return markerReasonBestPick
	default:
		return ""
	}
}

func installationExcludedModelsFromContext(ctx context.Context) []string {
	v := ctx.Value(InstallationExcludedModelsContextKey{})
	if v == nil {
		return nil
	}
	out, _ := v.([]string)
	return out
}

func installationAllowedModelsFromContext(ctx context.Context) []string {
	v := ctx.Value(InstallationAllowedModelsContextKey{})
	if v == nil {
		return nil
	}
	out, _ := v.([]string)
	return out
}

func installationSubscriptionModelsWhenActiveFromContext(ctx context.Context) []string {
	v := ctx.Value(InstallationSubscriptionModelsWhenActiveContextKey{})
	if v == nil {
		return nil
	}
	out, _ := v.([]string)
	return out
}

func installationSubscriptionModelsWhenInactiveFromContext(ctx context.Context) []string {
	out, _ := ctx.Value(InstallationSubscriptionModelsWhenInactiveContextKey{}).([]string)
	if openRouterOverflowEnabled(ctx) {
		return openRouterOverflowModels(out)
	}
	return out
}

func allowedModelsForRequest(ctx context.Context) map[string]struct{} {
	if planHasDedicatedServingPolicy(ctx) && !subscriptionStateModelsEnabled(ctx) {
		return nil
	}
	policy := installationAllowedModelSet(ctx)
	subset := requestAllowedModelSet(ctx)
	if subset == nil {
		return policy
	}
	if policy == nil {
		return subset
	}
	out := make(map[string]struct{}, len(subset))
	for m := range subset {
		if _, ok := policy[m]; ok {
			out[m] = struct{}{}
		}
	}
	return out
}

func installationAllowedModelSet(ctx context.Context) map[string]struct{} {
	var allowed map[string]struct{}
	if !planHasDedicatedServingPolicy(ctx) || subscriptionStateModelsEnabled(ctx) {
		if base := installationAllowedModelsFromContext(ctx); len(base) > 0 {
			allowed = modelSet(base)
		}
	}
	if !subscriptionStateModelsEnabled(ctx) {
		return allowed
	}
	stateModels, hasSelectedStateModels := ctx.Value(subscriptionStateAllowedModelsKey{}).(map[string]struct{})
	if !hasSelectedStateModels {
		return allowed
	}
	if allowed == nil {
		if stateModels == nil {
			return make(map[string]struct{})
		}
		return stateModels
	}
	intersection := make(map[string]struct{})
	for model := range stateModels {
		if _, permitted := allowed[model]; permitted {
			intersection[model] = struct{}{}
		}
	}
	return intersection
}

func modelPermittedByAllowlist(ctx context.Context, model string) bool {
	allowed := installationAllowedModelSet(ctx)
	if allowed == nil {
		return true
	}
	_, ok := allowed[model]
	return ok
}

func (s *Service) routableUniverse() map[string]struct{} {
	if s.availableModels != nil {
		return s.availableModels
	}
	out := make(map[string]struct{}, len(catalog.Models))
	for _, m := range catalog.Models {
		out[m.ID] = struct{}{}
	}
	return out
}

func subscriptionRoutingDisabledForRequest(ctx context.Context) bool {
	disabled, _ := ctx.Value(InstallationSubscriptionRoutingDisabledContextKey{}).(bool)
	return disabled
}

func subscriptionFundingOutOfPlayForRequest(ctx context.Context) bool {
	plan, ok := entitlement.ProductScopeFromContext(ctx)
	return ok && plan == entitlement.PlanMax
}

func hideTerminalSurfacesForRequest(ctx context.Context) bool {
	if _, active := auth.BlindExperimentFrom(ctx); active {
		return true
	}
	hide, _ := ctx.Value(InstallationHideTerminalSurfacesContextKey{}).(bool)
	return hide
}

func routingKnobsForRequest(ctx context.Context) *router.Overrides {
	if planHasDedicatedServingPolicy(ctx) {
		return nil
	}
	if k := router.RoutingKnobsFromContext(ctx); k != nil {
		return k
	}
	if v, ok := ctx.Value(InstallationRoutingKnobsContextKey{}).(*router.Overrides); ok {
		return v
	}
	return nil
}

func (s *Service) excludedModelsForRequest(ctx context.Context) map[string]struct{} {
	return s.excludedModelsFor(ctx, allowedModelsForRequest(ctx))
}

func (s *Service) policyExcludedModels(ctx context.Context) map[string]struct{} {
	return s.excludedModelsFor(ctx, installationAllowedModelSet(ctx))
}

func (s *Service) excludedModelsFor(ctx context.Context, allowed map[string]struct{}) map[string]struct{} {
	ineligible := s.productIneligibleModels(ctx)
	if override := s.routing.ExcludedModelsOverride(); override != nil {
		return candidates.MergeExcludedModels(override, ineligible)
	}
	excluded := installationExcludedModelsFromContext(ctx)
	out := make(map[string]struct{}, len(excluded))
	for _, m := range excluded {
		out[m] = struct{}{}
	}
	for m := range ineligible {
		out[m] = struct{}{}
	}

	if allowed != nil {
		for model := range s.routableUniverse() {
			if _, ok := allowed[model]; !ok {
				out[model] = struct{}{}
			}
		}
	}
	for model := range s.gatewayUnservedModelsForRequest(ctx) {
		out[model] = struct{}{}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (s *Service) productIneligibleModels(ctx context.Context) map[string]struct{} {
	return modelSet(catalog.IneligibleIDs(entitlement.ModelBoundaryFromContext(ctx)))
}

func installationExcludedProvidersFromContext(ctx context.Context) []string {
	if planHasDedicatedServingPolicy(ctx) && !subscriptionStateModelsEnabled(ctx) {
		return nil
	}
	v := ctx.Value(InstallationExcludedProvidersContextKey{})
	if v == nil {
		return nil
	}
	out, _ := v.([]string)
	return out
}

func sessionDisabledProvidersFromContext(ctx context.Context) []string {
	v := ctx.Value(SessionDisabledProvidersContextKey{})
	if v == nil {
		return nil
	}
	out, _ := v.([]string)
	return out
}

func sessionDemotedModelsFromContext(ctx context.Context) []string {
	v := ctx.Value(SessionDemotedModelsContextKey{})
	if v == nil {
		return nil
	}
	out, _ := v.([]string)
	return out
}

func observedOverflowModelsFromContext(ctx context.Context) []string {
	out, _ := ctx.Value(ObservedOverflowModelsContextKey{}).([]string)
	return out
}

func sessionStrikeReadmitModelsFromContext(ctx context.Context) []string {
	out, _ := ctx.Value(SessionStrikeReadmitModelsContextKey{}).([]string)
	return out
}

func sessionCooldownModelsFromContext(ctx context.Context) map[string]time.Time {
	v := ctx.Value(SessionCooldownModelsContextKey{})
	if v == nil {
		return nil
	}
	out, _ := v.(map[string]time.Time)
	return out
}

func (s *Service) excludedProvidersForRequest(ctx context.Context) map[string]struct{} {
	return s.credentials.ExcludedProviders(installationExcludedProvidersFromContext(ctx), sessionDisabledProvidersFromContext(ctx))
}

func installationPreferredModelsFromContext(ctx context.Context) []string {
	v := ctx.Value(InstallationPreferredModelsContextKey{})
	if v == nil {
		return nil
	}
	out, _ := v.([]string)
	return out
}

func installationFastModeModelsFromContext(ctx context.Context) []string {
	if planHasDedicatedServingPolicy(ctx) {
		return nil
	}
	v := ctx.Value(InstallationFastModeModelsContextKey{})
	if v == nil {
		return nil
	}
	out, _ := v.([]string)
	return out
}

func (s *Service) preferredModelsForRequest(ctx context.Context) []string {
	var preferred []string
	if subscriptionStateModelsEnabled(ctx) {
		preferred = slices.Clone(installationSubscriptionModelsWhenActiveFromContext(ctx))
	}
	if planHasDedicatedServingPolicy(ctx) {
		return preferred
	}
	for _, model := range installationPreferredModelsFromContext(ctx) {
		if !slices.Contains(preferred, model) {
			preferred = append(preferred, model)
		}
	}
	return preferred
}

func clusterArmOverridesForRequest(ctx context.Context) map[string][]string {
	if planHasDedicatedServingPolicy(ctx) {
		if !flags.BoolOr(ctx, flags.KeyPlanUserClusterLists, false) {
			return nil
		}
		return mergeClusterOverrides(nil, auth.UserClusterModelListsFrom(ctx))
	}
	v := ctx.Value(ClusterModelListsContextKey{})
	keyScoped, _ := v.(map[string][]string)
	userScoped := auth.UserClusterModelListsFrom(ctx)
	return mergeClusterOverrides(keyScoped, userScoped)
}

func memberSelectedModel(ctx context.Context, model string) bool {
	if !flags.BoolOr(ctx, flags.KeyPlanUserClusterLists, false) || billing.SubscriptionOnlyFromContext(ctx) && !linkedFirst(ctx) {
		return false
	}
	for _, models := range auth.UserClusterModelListsFrom(ctx) {
		if slices.Contains(models, model) {
			return true
		}
	}
	return false
}

func planHasDedicatedServingPolicy(ctx context.Context) bool {
	identity, managed := requestcontext.ServingIdentityFromContext(ctx)
	return managed && entitlement.PlanHasServingProfile(entitlement.Plan(identity.Plan))
}

const contextWindowOutputReserve = 8_000

const extendedContextTriggerTokens = 140_000

func shouldEnableExtendedContext(est, outputReserve int) bool {
	return est+outputReserve > extendedContextTriggerTokens
}

func (s *Service) restrictToTier(excluded map[string]struct{}, tier catalog.Tier) (map[string]struct{}, bool) {
	if tier == catalog.TierUnknown {
		return excluded, false
	}
	out := make(map[string]struct{}, len(excluded))
	for k := range excluded {
		out[k] = struct{}{}
	}
	inTierEligible := 0
	consider := func(model string) {
		if catalog.TierFor(model) == tier {
			if _, alreadyExcluded := excluded[model]; !alreadyExcluded {
				inTierEligible++
			}
			return
		}
		out[model] = struct{}{}
	}
	if s.availableModels != nil {
		for model := range s.availableModels {
			consider(model)
		}
	} else {
		for _, m := range catalog.Models {
			consider(m.ID)
		}
	}
	if inTierEligible == 0 {
		return excluded, false
	}
	return out, true
}

func subscriptionProvidersForRequest(ctx context.Context) map[string]struct{} {
	available := map[string]struct{}{}
	if subscriptionRoutingDisabledForRequest(ctx) || subscriptionFundingOutOfPlayForRequest(ctx) {
		return available
	}
	if !claudeSubscriptionSuppressed(ctx) && (anthropicSubscriptionFromContext(ctx) != "" || managedSubscriptionEnrolled(ctx, subscriptions.ProviderClaude)) {
		available[providers.ProviderAnthropic] = struct{}{}
	}
	if !codexSubscriptionSuppressed(ctx) && (openaiSubscriptionFromContext(ctx) != "" || managedSubscriptionEnrolled(ctx, subscriptions.ProviderCodex)) {
		available[providers.ProviderOpenAI] = struct{}{}
	}
	return available
}

func anthropicSubscriptionFromContext(ctx context.Context) string {
	if anthropicBYOKFirst(ctx) {
		return ""
	}
	v, _ := ctx.Value(AnthropicSubscriptionContextKey{}).(string)
	return v
}

type suppressClaudeSubscriptionContextKey struct{}

func withSuppressedClaudeSubscription(ctx context.Context) context.Context {
	return context.WithValue(ctx, suppressClaudeSubscriptionContextKey{}, true)
}

func claudeSubscriptionSuppressed(ctx context.Context) bool {
	v, _ := ctx.Value(suppressClaudeSubscriptionContextKey{}).(bool)
	return v
}

type suppressCodexSubscriptionContextKey struct{}

func withSuppressedCodexSubscription(ctx context.Context) context.Context {
	return context.WithValue(ctx, suppressCodexSubscriptionContextKey{}, true)
}

func codexSubscriptionSuppressed(ctx context.Context) bool {
	v, _ := ctx.Value(suppressCodexSubscriptionContextKey{}).(bool)
	return v
}

type suppressCodexModelContextKey struct{}

func withSuppressedCodexModel(ctx context.Context, model string) context.Context {
	previous, _ := ctx.Value(suppressCodexModelContextKey{}).(map[string]struct{})
	models := make(map[string]struct{}, len(previous))
	for id := range previous {
		models[id] = struct{}{}
	}
	models[router.StripDateSuffix(model)] = struct{}{}
	return context.WithValue(ctx, suppressCodexModelContextKey{}, models)
}

func codexModelSuppressed(ctx context.Context, model string) bool {
	models, _ := ctx.Value(suppressCodexModelContextKey{}).(map[string]struct{})
	_, suppressed := models[router.StripDateSuffix(model)]
	return suppressed
}

type codexChatEndpointContextKey struct{}

func withCodexChatEndpoint(ctx context.Context) context.Context {
	return context.WithValue(ctx, codexChatEndpointContextKey{}, true)
}

func codexChatEndpoint(ctx context.Context) bool {
	v, _ := ctx.Value(codexChatEndpointContextKey{}).(bool)
	return v
}

func (s *Service) avoidCodexOnChatEndpoint(ctx context.Context, provider, model string, endpointResponses bool, headers http.Header) (context.Context, error) {
	if provider != providers.ProviderOpenAI || endpointResponses ||
		subscriptionRoutingDisabledForRequest(ctx) || subscriptionFundingOutOfPlayForRequest(ctx) {
		return ctx, nil
	}
	personalSubscription := servedOnCodexSubscription(resolveAndInjectCredentials(ctx, provider, model, headers))
	managedSubscription := managedSubscriptionCanServe(ctx, provider, model)
	if !personalSubscription && !managedSubscription {
		return ctx, nil
	}
	if !s.openaiFallbackKeyAvailable(ctx) {
		return nil, subscriptionOnlyUnavailable(ctx)
	}
	return withCodexChatEndpoint(ctx), nil
}

func servedOnCodexSubscription(ctx context.Context) bool {
	creds := CredentialsFromContext(ctx)
	return creds != nil && creds.OAuth && len(creds.AccountID) > 0
}

func servedOnSubscription(ctx context.Context) bool {
	creds := CredentialsFromContext(ctx)
	return (creds != nil && creds.OAuth) || managedSubscriptionServed(ctx)
}

func servedOnBYOK(ctx context.Context) bool {
	creds := CredentialsFromContext(ctx)
	return creds != nil && creds.Source == credSourceBYOK
}

func byokServedForProvider(ctx context.Context, provider string) bool {
	if provider == "" {
		return false
	}
	for _, key := range externalKeysFromContext(ctx) {
		if key.Provider == provider && len(key.Plaintext) > 0 {
			return true
		}
	}
	return false
}

func openaiSubscriptionFromContext(ctx context.Context) string {
	if byokFirst(ctx, providers.ProviderOpenAI) {
		return ""
	}
	v, _ := ctx.Value(OpenAISubscriptionContextKey{}).(string)
	return v
}

func openaiAccountIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(OpenAIAccountIDContextKey{}).(string)
	return v
}

func codexSubscriptionFromContext(ctx context.Context) *Credentials {
	return codexSubscriptionCreds(openaiSubscriptionFromContext(ctx), openaiAccountIDFromContext(ctx))
}

func codexResponsesRequest(ctx context.Context, headers http.Header) bool {
	if subscriptionRoutingDisabledForRequest(ctx) || subscriptionFundingOutOfPlayForRequest(ctx) {
		return false
	}
	if codexSubscriptionFromContext(ctx) != nil {
		return true
	}
	if c := ExtractClientCredentials(providers.ProviderOpenAI, headers); c != nil && c.OAuth && !byokFirst(ctx, providers.ProviderOpenAI) {
		return true
	}
	return false
}

func NewService(r router.Router, providerMap map[string]providers.Client, emitter TelemetryEmitter, pinStore sessionpin.Store, hardPinExplore bool, hardPinProvider, hardPinModel string, telemetry TelemetryRepository) *Service {
	s := &Service{
		clients:                        dispatch.NewClients(providerMap),
		translationCompatibilityMode:   TranslationCompatibilityShadow,
		emitter:                        emitter,
		ingress:                        ingress.New(0),
		pinStore:                       pinStore,
		noProgress:                     conversation.NewNoProgressTracker(),
		searchUse:                      newSearchUseTracker(),
		searchRequirementDecayTurns:    DefaultSearchRequirementDecayTurns,
		compaction:                     conversation.NewCompactionTracker(),
		prefixTrimFreeSwitch:           true,
		turnSignalCaptureEnabled:       true,
		textRepetitionBreakEnabled:     true,
		ccOrchToolsCrossVendor:         true,
		hardPinExplore:                 hardPinExplore,
		hardPinProvider:                hardPinProvider,
		hardPinModel:                   hardPinModel,
		telemetry:                      telemetry,
		continuityConfig:               continuity.DefaultConfig(),
		hmmServing:                     hmmServing{hmmConfig: continuity.DefaultAuthoritativeConfig()},
		cyberRefusalRepin:              true,
		cyberRefusalRetry:              true,
		anthropicServerSideFallback:    true,
		siblingFailover:                true,
		openAIResponsesBroad:           true,
		nativeAnthropicResponseSignals: true,
		nativeOpenAIResponseSignals:    true,
		cyberRefusalFallbackModel:      "claude-sonnet-5",
	}
	s.routing = routingpolicy.New(nil)
	s.escalationHelper()
	if r == nil {
		return s
	}
	return s.WithPolicyStrategy(policy.StrategySpec{Strategy: router.StrategyHMMEmbedding, Router: r, Unavailable: router.ErrHMMUnavailable})
}

func (s *Service) WithTranslationCompatibilityMode(mode TranslationCompatibilityMode) *Service {
	switch mode {
	case TranslationCompatibilityOff, TranslationCompatibilityShadow, TranslationCompatibilityEnforce:
		s.translationCompatibilityMode = mode
	}
	return s
}

func (s *Service) WithScopedSearchRequirement(enabled bool, decayTurns int) *Service {
	s.scopedSearchRequirement = enabled
	if decayTurns > 0 {
		s.searchRequirementDecayTurns = decayTurns
	}
	return s
}

func (s *Service) WithContinuityConfig(cfg continuity.Config) *Service {
	s.continuityConfig = cfg
	return s
}

func (s *Service) WithCyberRefusalRepin(enabled bool) *Service {
	s.cyberRefusalRepin = enabled
	return s
}

func (s *Service) WithCyberRefusalRetry(enabled bool) *Service {
	s.cyberRefusalRetry = enabled
	return s
}

func (s *Service) WithAnthropicServerSideFallback(enabled bool) *Service {
	s.anthropicServerSideFallback = enabled
	return s
}

func (s *Service) WithSiblingFailover(enabled bool) *Service {
	s.siblingFailover = enabled
	return s
}

func (s *Service) WithOpenAIResponsesBroad(enabled bool) *Service {
	s.openAIResponsesBroad = enabled
	return s
}

func (s *Service) WithCommittedStreamArmDemotion(enabled bool) *Service {
	s.committedStreamArmDemotion = enabled
	return s
}

func (s *Service) WithRescuedFailureArmDemotion(enabled bool) *Service {
	s.rescuedFailureArmDemotion = enabled
	return s
}

func (s *Service) WithTransientRateLimit(enabled bool, cooldownSeconds int) *Service {
	s.transientRateLimit = enabled
	s.rateLimitCooldownSeconds = cooldownSeconds
	return s
}

const recoveryProbeLeaseDuration = 15 * time.Minute

type localRecoveryProbeLease struct {
	id    uint64
	until time.Time
}

func (s *Service) recoveryProbeLeaseFor(ctx context.Context) time.Duration {
	leaseFor := recoveryProbeLeaseDuration
	if deadline, hasDeadline := ctx.Deadline(); hasDeadline {
		if untilDeadline := deadline.Add(time.Minute).Sub(s.clock.Now()); untilDeadline > leaseFor {
			leaseFor = untilDeadline
		}
	}
	return leaseFor
}

func (s *Service) acquireRecoveryProbe(ctx context.Context, sessionKey [sessionpin.SessionKeyLen]byte, model string) (func(), bool) {
	if sessionKey == ([sessionpin.SessionKeyLen]byte{}) || model == "" {
		return func() {}, true
	}
	leaseFor := s.recoveryProbeLeaseFor(ctx)
	if store, ok := s.pinStore.(sessionpin.RecoveryProbeStore); ok {
		token := uuid.New()
		acquired, err := store.AcquireRecoveryProbe(ctx, sessionKey, model, token, leaseFor)
		if err != nil {
			observability.FromContext(ctx).Warn("failed to acquire recovery probe lease; keeping model out for this request", "model", model, "err", err)
			return nil, false
		}
		if !acquired {
			return nil, false
		}
		var once sync.Once
		return func() {
			once.Do(func() {
				releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
				defer cancel()
				if err := store.ReleaseRecoveryProbe(releaseCtx, sessionKey, model, token); err != nil {
					observability.FromContext(ctx).Warn("failed to release recovery probe lease; it will expire", "model", model, "err", err)
				}
			})
		}, true
	}
	key := string(sessionKey[:]) + "\x00" + model
	now := s.clock.Now()
	s.recoveryProbeMu.Lock()
	if lease, exists := s.recoveryProbeLeases[key]; exists && now.Before(lease.until) {
		s.recoveryProbeMu.Unlock()
		return nil, false
	}
	if s.recoveryProbeLeases == nil {
		s.recoveryProbeLeases = make(map[string]localRecoveryProbeLease)
	}
	s.nextRecoveryProbeID++
	leaseID := s.nextRecoveryProbeID
	s.recoveryProbeLeases[key] = localRecoveryProbeLease{id: leaseID, until: now.Add(leaseFor)}
	s.recoveryProbeMu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			s.recoveryProbeMu.Lock()
			if s.recoveryProbeLeases[key].id == leaseID {
				delete(s.recoveryProbeLeases, key)
			}
			s.recoveryProbeMu.Unlock()
		})
	}, true
}

func (s *Service) clearRecoveredCooldown(ctx context.Context, res turnLoopResult, servedModel string, proxyErr error) {
	observedUntil, probed := res.CooldownProbes[servedModel]
	if !probed || proxyErr != nil {
		return
	}
	store, ok := s.pinStore.(sessionpin.RecoveryProbeStore)
	if !ok {
		return
	}
	pinRole := res.PinRole
	if pinRole == "" {
		pinRole = sessionpin.DefaultRole
	}
	for _, role := range demotionRoles(pinRole, pinRole) {
		if err := store.ClearDemotionCooldown(context.WithoutCancel(ctx), res.SessionKey, role, servedModel, observedUntil); err != nil {
			observability.FromContext(ctx).Warn("failed to clear recovered cooldown; next turns keep probing", "model", servedModel, "role", role, "err", err)
		}
	}
}

func (s *Service) WithNativeAnthropicResponseSignals(enabled bool) *Service {
	s.nativeAnthropicResponseSignals = enabled
	return s
}

func (s *Service) WithNativeOpenAIResponseSignals(enabled bool) *Service {
	s.nativeOpenAIResponseSignals = enabled
	return s
}

func (s *Service) WithAllowedModelsHeader(enabled bool) *Service {
	s.allowedModelsHeader = enabled
	return s
}

func (s *Service) WithRetrySleep(sleep func(context.Context, time.Duration) error) *Service {
	s.retrySleep = sleep
	return s
}

func (s *Service) WithCyberRefusalFallbackModel(model string) *Service {
	if strings.TrimSpace(model) != "" {
		s.cyberRefusalFallbackModel = strings.TrimSpace(model)
	}
	return s
}

func (s *Service) WithHMMConfig(cfg continuity.AuthoritativeConfig) *Service {
	s.hmmConfig = cfg
	return s
}

func (s *Service) WithPolicyDeadlineFallback(enabled bool) *Service {
	s.policyDeadlineFallback = enabled
	return s
}

func (s *Service) WithPolicyDeadlineDefaultModel(model string) *Service {
	s.policyDeadlineDefaultModel = model
	return s
}

func (s *Service) WithPrefixTrimFreeSwitch(enabled bool) *Service {
	s.prefixTrimFreeSwitch = enabled
	return s
}

func (s *Service) WithEscapeNormalize(enabled bool) *Service {
	s.escapeNormalize = enabled
	return s
}

func (s *Service) WithEffortEscalation(enabled bool) *Service {
	s.effortEscalation = enabled
	return s
}

func (s *Service) WithCCOrchestrationToolsCrossVendor(enabled bool) *Service {
	s.ccOrchToolsCrossVendor = enabled
	return s
}

func (s *Service) WithCCTaskToolsCrossVendor(enabled bool) *Service {
	s.ccTaskToolsCrossVendor = enabled
	return s
}

func (s *Service) WithCCAutonomySystemAppend(enabled bool) *Service {
	s.ccAutonomySystemAppend = enabled
	return s
}

func (s *Service) WithEarlyStopRedo(mode flags.EarlyStopRedoMode) *Service {
	s.earlyStopRedo = mode
	return s
}

func (s *Service) WithCCWorkspaceSystemAppend(enabled bool) *Service {
	s.ccWorkspaceSystemAppend = enabled
	return s
}

func (s *Service) WithBandSwap(enabled bool) *Service {
	if !enabled {
		s.bandSwap = nil
		return s
	}
	clf, err := bandswap.New()
	if err != nil {
		observability.Get().Error("band swap head failed to load; per-turn swap disabled", "err", err)
		s.bandSwap = nil
		return s
	}
	s.bandSwap = clf
	return s
}

func (s *Service) WithTurnSignalCapture(enabled bool) *Service {
	s.turnSignalCaptureEnabled = enabled
	return s
}

func (s *Service) WithRouterFeedbackStore(store RouterFeedbackStore) *Service {
	s.feedbackStore = store
	return s
}

func (s *Service) WithContentCapture(mode ContentCaptureMode, maxBytes int, redactor Redactor) *Service {
	s.captureMode = mode
	if maxBytes > 0 {
		s.captureMaxBytes = maxBytes
	}
	s.redactor = redactor
	return s
}

func forcedReasoningEffort(model string, escalate bool) string {
	if strings.HasPrefix(model, "grok-") {
		return "low"
	}
	switch {
	case strings.HasPrefix(model, "gpt-5"):
		if escalate {
			return "high"
		}
		return "low"
	case strings.HasPrefix(model, "gemini-3"):
		return "low"
	default:
		return ""
	}
}

func (s *Service) WithSummarizer(sz handover.Summarizer) *Service {
	s.summarizer = sz
	return s
}

func (s *Service) WithCompactionHandoverSummarizer(sz handover.Summarizer) *Service {
	s.compactionHandoverSummarizer = sz
	return s
}

func (s *Service) WithWebSearchExecutor(ex websearch.Executor) *Service {
	s.webSearch = ex
	return s
}

func (s *Service) WithAvailableModels(models map[string]struct{}) *Service {
	if models == nil {
		s.availableModels = nil
		return s
	}
	copied := make(map[string]struct{}, len(models))
	for m := range models {
		copied[m] = struct{}{}
	}
	s.availableModels = copied
	return s
}

type HardPinRequest struct {
	EnabledProviders map[string]struct{}
	ExcludedModels   map[string]struct{}
	CustomBindings   map[string][]string
	GatewayProviders map[string]struct{}
}

type HardPinResolver func(HardPinRequest) (provider, model string, ok bool)

func (s *Service) WithExplicitUtilityHardPin(enabled bool) *Service {
	s.explicitUtilityHardPin = enabled
	return s
}

func (s *Service) WithHardPinResolver(resolver HardPinResolver) *Service {
	s.hardPinResolver = resolver
	return s
}

func (s *Service) WithDefaultBaselineModel(model string) *Service {
	s.defaultBaselineModel = model
	return s
}

func (s *Service) WithSubAgentOverride(provider, model string) *Service {
	s.subAgentProvider = provider
	s.subAgentModel = model
	return s
}

func clientRequestedModel(ctx context.Context, requested string) string {
	if !router.IsRoutingModel(requested) {
		return requested
	}
	if baseline := clientBaselineModel(ctx); baseline != "" {
		return baseline
	}
	return requested
}

func clientBaselineModel(ctx context.Context) string {
	switch ClientIdentityFrom(ctx).ClientApp {
	case ClientAppClaudeCode:
		return "claude-opus-5-5"
	case ClientAppCodex:
		return "gpt-6.1-sol"
	}
	return ""
}

func (s *Service) baselineFor(ctx context.Context, requested string) string {
	requested = clientRequestedModel(ctx, requested)
	if requested != "" {
		if _, ok := catalog.PrimaryPriceFor(requested); ok {
			return requested
		}
	}
	return s.defaultBaselineModel
}

func (s *Service) WithInferenceExecutor(executor *dispatch.Executor) *Service {
	s.executor = executor
	if executor != nil && executor.Clients() != nil {
		s.clients = executor.Clients()
	}
	return s
}

func (s *Service) InferenceExecutor() *dispatch.Executor {
	return s.executor
}

func (s *Service) WithByokOnly(byokOnly bool) *Service {
	s.credentials.BYOKOnly = byokOnly
	return s
}

func (s *Service) WithExcludedModelsOverride(models []string) *Service {
	s.routing = s.routing.WithExcludedModelsOverride(models)
	return s
}

func (s *Service) WithRoutingPolicy(p *routingpolicy.Policy) *Service {
	s.routing = p.WithExcludedModelsOverride(s.ExcludedModelsOverride())
	return s
}

func (s *Service) HasExcludedModelsOverride() bool {
	return s.routing.ExcludedModelsOverride() != nil
}

func (s *Service) WithGlobalAutomaticExclusions(store GlobalAutomaticExclusionStore) *Service {
	if store == nil {
		s.globalAutomaticExclusions = nil
		return s
	}
	s.globalAutomaticExclusions = newGlobalAutomaticExclusionCache(store)
	return s
}

func (s *Service) RoutableModels() map[string]struct{} {
	if s == nil {
		return nil
	}
	universe := s.routableUniverse()
	out := make(map[string]struct{}, len(universe))
	for m := range universe {
		out[m] = struct{}{}
	}
	return out
}

func (s *Service) ExcludedModelsOverride() []string {
	override := s.routing.ExcludedModelsOverride()
	if override == nil {
		return nil
	}
	out := make([]string, 0, len(override))
	for m := range override {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

func (s *Service) WithExcludedProvidersOverride(providerNames []string) *Service {
	if len(providerNames) == 0 {
		s.credentials.ExcludedProvidersOverride = nil
		return s
	}
	set := make(map[string]struct{}, len(providerNames))
	for _, p := range providerNames {
		set[p] = struct{}{}
	}
	s.credentials.ExcludedProvidersOverride = set
	return s
}

func (s *Service) HasExcludedProvidersOverride() bool {
	return s.credentials.ExcludedProvidersOverride != nil
}

func (s *Service) ExcludedProvidersOverride() []string {
	if s.credentials.ExcludedProvidersOverride == nil {
		return nil
	}
	out := make([]string, 0, len(s.credentials.ExcludedProvidersOverride))
	for p := range s.credentials.ExcludedProvidersOverride {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func (s *Service) usageRequired() bool {
	return s.pinStore != nil || s.emitter != nil || s.telemetry != nil || s.billing != nil
}

func gatewayResponsesKey(ctx context.Context, provider string) string {
	if !providers.IsGateway(provider) {
		return ""
	}
	return EffectiveBaseURL(ctx, provider)
}

func gatewayModelKey(endpoint, provider, model string) string {
	if !providers.IsGateway(provider) || model == "" {
		return ""
	}
	endpoint = strings.TrimRight(endpoint, "/")
	if endpoint == "" {
		endpoint = provider
	}
	return endpoint + "|" + model
}

func (s *Service) gatewayLacksModel(key string) bool {
	if key == "" {
		return false
	}
	_, ok := s.unservedGatewayModels.Load(key)
	return ok
}

func (s *Service) rememberGatewayLacksModel(ctx context.Context, provider, model string) {
	key := gatewayModelKey(EffectiveBaseURL(ctx, ""), provider, model)
	if key == "" {
		return
	}
	s.unservedGatewayModels.Store(key, struct{}{})
}

func (s *Service) newTelemetryRecorder() telemetry.Recorder {
	if s.emitter == nil {
		return nil
	}
	return s.emitter.NewRecorder()
}

func (s *Service) newUsageExtractor(inner http.ResponseWriter, provider string) (*telemetry.UsageExtractor, http.ResponseWriter) {
	if s.emitter == nil {
		return newFallbackUsageExtractor(inner, provider)
	}
	return s.emitter.NewUsageExtractor(inner, provider)
}

func (s *Service) WithTranslationMetrics(metrics telemetry.TranslationMetrics) *Service {
	s.translationMetrics = metrics
	return s
}

func (s *Service) WithBillingService(b *billing.Service) *Service {
	s.billing = b
	return s
}

func (s *Service) WithCodexExtraCredits(enabled bool) *Service {
	s.codexExtraCredits = enabled
	return s
}

func (s *Service) WithManagedSubscriptions(pool subscriptions.Leaser) *Service {
	s.managedSubscriptions = pool
	return s
}

func (s *Service) WithDeploymentKeyedProviders(set map[string]struct{}) *Service {
	if set == nil {
		s.credentials.KeyedProviders = nil
		return s
	}
	copied := make(map[string]struct{}, len(set))
	for p := range set {
		copied[p] = struct{}{}
	}
	s.credentials.KeyedProviders = copied
	return s
}

func (s *Service) WithPassthroughEligibleProviders(set map[string]struct{}) *Service {
	if set == nil {
		s.credentials.PassthroughEligible = nil
		return s
	}
	copied := make(map[string]struct{}, len(set))
	for p := range set {
		copied[p] = struct{}{}
	}
	s.credentials.PassthroughEligible = copied
	return s
}

func (s *Service) MetricsSummary(ctx context.Context, installationID string, from, to time.Time) (TelemetrySummary, error) {
	if s.telemetry == nil {
		return TelemetrySummary{}, nil
	}
	return s.telemetry.GetTelemetrySummary(ctx, installationID, from, to)
}

var (
	ErrSessionCostNotFound    = errors.New("no committed router telemetry for session")
	ErrInvalidSessionID       = errors.New("session id must be non-empty and at most 128 bytes")
	ErrSessionCostUnavailable = errors.New("router telemetry storage is not configured")
)

func (s *Service) SessionCost(ctx context.Context, installationID, sessionID string) (SessionCost, error) {
	if sessionID == "" || len(sessionID) > MaxClientIdentifierLen {
		return SessionCost{}, ErrInvalidSessionID
	}
	if s.telemetry == nil {
		return SessionCost{}, ErrSessionCostUnavailable
	}
	return s.telemetry.GetSessionCost(ctx, installationID, sessionID)
}

func (s *Service) MetricsTimeseries(ctx context.Context, installationID string, from, to time.Time, granularity string) ([]TelemetryBucket, error) {
	if s.telemetry == nil {
		return nil, nil
	}
	return s.telemetry.GetTelemetryTimeseries(ctx, installationID, from, to, granularity)
}

func (s *Service) MetricsSummaryAll(ctx context.Context, from, to time.Time) (TelemetrySummary, error) {
	if s.telemetry == nil {
		return TelemetrySummary{}, nil
	}
	return s.telemetry.GetTelemetrySummaryAll(ctx, from, to)
}

func (s *Service) MetricsTimeseriesAll(ctx context.Context, from, to time.Time, granularity string) ([]TelemetryBucket, error) {
	if s.telemetry == nil {
		return nil, nil
	}
	return s.telemetry.GetTelemetryTimeseriesAll(ctx, from, to, granularity)
}

func (s *Service) MetricsDashboardFacts(ctx context.Context, installationID string, from, to time.Time, granularity string) ([]TelemetryDashboardFact, error) {
	if s.telemetry == nil {
		return nil, nil
	}
	return s.telemetry.GetTelemetryDashboardFacts(ctx, installationID, from, to, granularity)
}

func (s *Service) MetricsActiveUsers(ctx context.Context, installationID string, from, to time.Time, granularity string) ([]TelemetryActiveUsers, error) {
	if s.telemetry == nil {
		return nil, nil
	}
	return s.telemetry.GetTelemetryDashboardActiveUsers(ctx, installationID, from, to, granularity)
}

func (s *Service) MetricsPeople(ctx context.Context, installationID string) ([]TelemetryPerson, error) {
	if s.telemetry == nil {
		return nil, nil
	}
	return s.telemetry.GetTelemetryDashboardPeople(ctx, installationID)
}

func (s *Service) MetricsDrillDown(ctx context.Context, installationID string, from, to time.Time) ([]TelemetryDrillDownRow, error) {
	if s.telemetry == nil {
		return nil, nil
	}
	return s.telemetry.GetTelemetryDashboardDrillDown(ctx, installationID, from, to)
}

func (s *Service) MetricsIndividualFacts(ctx context.Context, installationID string, from, to time.Time, granularity string) ([]TelemetryIndividualFact, error) {
	if s.telemetry == nil {
		return nil, nil
	}
	return s.telemetry.GetTelemetryDashboardIndividualFacts(ctx, installationID, from, to, granularity)
}

func (s *Service) MetricsSubscriptionPool(ctx context.Context, installationID string, servedSince, readingsSince time.Time) ([]TelemetrySubscriptionSeat, error) {
	if s.telemetry == nil {
		return nil, nil
	}
	return s.telemetry.GetTelemetrySubscriptionPoolSeats(ctx, installationID, servedSince, readingsSince)
}

func (s *Service) MetricsModelBreakdown(ctx context.Context, installationID string, from, to time.Time, granularity string) ([]TelemetryModelBucket, error) {
	if s.telemetry == nil {
		return nil, nil
	}
	return s.telemetry.GetTelemetryModelBreakdown(ctx, installationID, from, to, granularity)
}

func (s *Service) MetricsModelBreakdownAll(ctx context.Context, from, to time.Time, granularity string) ([]TelemetryModelBucket, error) {
	if s.telemetry == nil {
		return nil, nil
	}
	return s.telemetry.GetTelemetryModelBreakdownAll(ctx, from, to, granularity)
}

func (s *Service) MetricsRows(ctx context.Context, installationID string, from, to time.Time, limit int32) ([]TelemetryRow, error) {
	if s.telemetry == nil {
		return nil, nil
	}
	return s.telemetry.GetTelemetryRows(ctx, installationID, from, to, limit)
}

func (s *Service) MetricsRowsAll(ctx context.Context, from, to time.Time, limit int32) ([]TelemetryRow, error) {
	if s.telemetry == nil {
		return nil, nil
	}
	return s.telemetry.GetTelemetryRowsAll(ctx, from, to, limit)
}

var ErrProviderNotConfigured = dispatch.ErrProviderNotConfigured

var ErrRequestNotJSONObject = translate.ErrNotJSONObject

var stripRoutingMarkerFromMessages = translate.StripRoutingMarkerFromMessages

func (s *Service) provider(name string) (providers.Client, error) {
	return s.clients.Client(name)
}

func (s *Service) Clients() *dispatch.Clients {
	return s.clients
}

func (s *Service) WithPolicyStrategy(spec policy.StrategySpec) *Service {
	if spec.Strategy == "" {
		return s
	}
	if s.strategies == nil {
		s.strategies = make(map[router.Strategy]registeredStrategy)
	}
	registered := registeredStrategy{
		router:       spec.Router,
		unavailable:  spec.Unavailable,
		capabilities: spec.Capabilities,
	}
	registered.outcomes, _ = spec.Router.(policy.OutcomeReporter)
	registered.feedback, _ = spec.Router.(policy.FeedbackReporter)
	s.strategies[spec.Strategy] = registered
	return s
}

func (s *Service) PolicyCapabilities(strategy router.Strategy) (policy.Capabilities, bool) {
	registered, ok := s.strategies[strategy]
	if ok {
		if source, dynamic := registered.router.(policy.CapabilitySource); dynamic {
			return source.CurrentCapabilities(), true
		}
	}
	return registered.capabilities, ok
}

func (s *Service) PolicyCapabilitiesForRequest(ctx context.Context, strategy router.Strategy) (policy.Capabilities, bool) {
	if source, ok := s.strategies[strategy].router.(policy.RequestCapabilitySource); ok {
		return source.CapabilitiesForRequest(ctx), true
	}
	return s.PolicyCapabilities(strategy)
}

func (s *Service) authoritativePerTurnSelection(ctx context.Context, strategy router.Strategy) bool {
	if s == nil {
		return false
	}
	capabilities, ok := s.PolicyCapabilitiesForRequest(ctx, strategy)
	return ok && capabilities.AuthoritativePerTurnSelection
}

func (s *Service) PolicyStrategyAvailable(strategy router.Strategy) bool {
	registered, ok := s.strategies[strategy]
	if !ok || registered.router == nil {
		return false
	}
	if source, dynamic := registered.router.(policy.AvailabilitySource); dynamic {
		return source.Available()
	}
	return true
}

func (s *Service) RegisteredStrategies() []router.Strategy {
	if s == nil {
		return nil
	}
	strategies := make([]router.Strategy, 0, len(s.strategies))
	for strategy := range s.strategies {
		strategies = append(strategies, strategy)
	}
	sort.Slice(strategies, func(i, j int) bool { return strategies[i] < strategies[j] })
	return strategies
}

func (s *Service) WithHMMRouter(r router.Router) *Service {
	return s.WithPolicyStrategy(policy.StrategySpec{
		Strategy:    router.StrategyHMM,
		Router:      r,
		Unavailable: router.ErrHMMUnavailable,
		Capabilities: policy.Capabilities{
			SchemaVersion:            policy.SchemaVersionV1,
			ReportsOutcomes:          true,
			ReportsFeedback:          true,
			HonorsPreferredModels:    true,
			HonorsQualityPriceBias:   true,
			SupportsDebugRouteDetail: true,
			SupportsShadow:           true,
		},
	})
}

func (s *Service) routeFor(ctx context.Context, strategy router.Strategy, req router.Request) (router.Decision, error) {
	var err error
	if denied, _ := ctx.Value(SessionCooldownProbeDeniedModelsContextKey{}).([]string); len(denied) > 0 {
		for _, model := range denied {
			req.Candidates.ExcludedModels = candidates.ExcludingModel(req.Candidates.ExcludedModels, model)
		}
	}
	req, err = s.applyTranslationPlan(ctx, req)
	if err != nil {
		return router.Decision{}, err
	}
	ctx, decision, passthrough, err := s.passthroughDecision(ctx, req)
	if passthrough {
		return decision, err
	}
	req = s.withPolicyRequestContext(ctx, req)
	return s.routeWithStrategy(ctx, strategy, req)
}

func (s *Service) routeWithStrategy(ctx context.Context, strategy router.Strategy, req router.Request) (router.Decision, error) {
	decision, err := s.routeWithStrategyUnchecked(ctx, strategy, req)
	if err != nil {
		return decision, err
	}
	if decision, err = eligibleStrategyDecision(ctx, strategy, req, decision); err != nil {
		return router.Decision{}, err
	}
	if pin, pinned := router.HonouredPolicyPin(ctx); pinned && (decision.Metadata == nil || !decision.Metadata.PolicyPinHonoured) {
		return router.Decision{}, fmt.Errorf("strategy %q cannot serve policy pin %s: %w", strategy, pin, router.ErrPolicyPinUnavailable)
	}
	return decision, nil
}

func (s *Service) routeWithStrategyUnchecked(ctx context.Context, strategy router.Strategy, req router.Request) (router.Decision, error) {
	registered, ok := s.strategies[strategy]
	if !ok || registered.router == nil {
		unavailable := defaultStrategyUnavailable(strategy)
		if ok && registered.unavailable != nil {
			unavailable = registered.unavailable
		}
		return router.Decision{}, fmt.Errorf("strategy %q requested but no router configured: %w", strategy, unavailable)
	}
	if s.strategyPorts.Ownership.Owns(strategy) && req.ConversationMessages != nil && !hasTextUserBoundary(req.ConversationMessages) {
		req.Unscorable = true
	}
	return registered.router.Route(ctx, req)
}

func (s *Service) withPolicyRequestContext(ctx context.Context, req router.Request) router.Request {
	req.Candidates.AutomaticExcludedModels = candidates.MergeExcludedModels(req.Candidates.AutomaticExcludedModels, s.globalAutomaticExcludedModels(ctx))
	req.ProductEligibility = entitlement.ModelBoundaryFromContext(ctx)
	req.Candidates.ExcludedModels = candidates.MergeExcludedModels(req.Candidates.ExcludedModels, s.productIneligibleModels(ctx))
	req.OrganizationID, _ = ctx.Value(ExternalIDContextKey{}).(string)
	req.InstallationID = ""
	if installationID := installationIDFromContext(ctx); installationID != uuid.Nil {
		req.InstallationID = installationID.String()
	}
	clientIdentity := ClientIdentityFrom(ctx)
	req.ClientApp = clientIdentity.ClientApp
	req.ClientBudget = requestcontext.ClientBudgetFrom(ctx)
	req.RolloutID = policyRolloutIDFromContext(ctx)
	req.CaptureMode = s.effectiveCaptureMode(ctx).String()
	req.TrainingAllowed = policyTrainingAllowedForRequest(ctx)
	req.DebugEnabled, _ = ctx.Value(PolicyDebugEnabledContextKey{}).(bool)
	req.RoutingIntent, _ = ctx.Value(PolicyRoutingIntentContextKey{}).(string)
	return req
}

func policyRolloutIDFromContext(ctx context.Context) string {
	if rolloutID, ok := ctx.Value(PolicyRolloutIDContextKey{}).(string); ok && rolloutID != "" {
		return rolloutID
	}
	return ClientIdentityFrom(ctx).RolloutID
}

func defaultStrategyUnavailable(strategy router.Strategy) error {
	switch strategy {
	case router.StrategyRL:
		return router.ErrRLPolicyUnavailable
	case router.StrategyHMM, router.StrategyHMMEmbedding, router.StrategyHMMBeta, router.StrategyHMMQwen:
		return router.ErrHMMUnavailable
	default:
		return router.ErrStrategyUnavailable
	}
}

func (s *Service) Route(ctx context.Context, req router.Request) (router.Decision, error) {
	strategy := router.StrategyFromContext(ctx)
	routeCtx, span := startRoutingSpan(ctx, strategy, req)
	decision, err := s.routeFor(routeCtx, strategy, req)
	finishRoutingSpan(span, decision, err)
	return decision, err
}

func (s *Service) RouteAnthropicRequest(ctx context.Context, body []byte, headers http.Header) (decision router.Decision, err error) {
	ctx, req, err := s.anthropicRoutingRequest(ctx, body, headers, "anthropic_route")
	if err != nil {
		return decision, err
	}
	return s.Route(ctx, req)
}

func (s *Service) PassthroughToProvider(ctx context.Context, body []byte, w http.ResponseWriter, r *http.Request) error {
	if isCountTokensRequest(r) {
		return s.countTokens(ctx, body, w, r)
	}
	return s.PassthroughToNamedProvider(ctx, providers.ProviderAnthropic, body, w, r)
}

func (s *Service) anthropicCredentialReachable(ctx context.Context, headers http.Header) bool {
	if s.anthropicFallbackKeyAvailable(ctx) {
		return true
	}
	if s.credentials.KeyedProviders == nil && !s.credentials.BYOKOnly {
		if s.clients.Has(providers.ProviderAnthropic) {
			return true
		}
	}
	return ExtractClientCredentials(providers.ProviderAnthropic, headers) != nil
}

func (s *Service) PassthroughToNamedProvider(ctx context.Context, providerName string, body []byte, w http.ResponseWriter, r *http.Request) error {
	log := observability.FromContext(ctx)
	p, err := s.provider(providerName)
	if err != nil {
		return err
	}
	ctx = resolveAndInjectCredentials(ctx, providerName, "", r.Header)

	if providerName == providers.ProviderAnthropic && len(body) > 0 {
		if canon, had, cerr := translate.CanonicalizeModelInBody(body); cerr == nil && had {
			body = canon
		}
		if bare, had, cerr := translate.StripProviderPrefixInBody(body, providerName); cerr == nil && had {
			body = bare
		}
	}

	var prep providers.PreparedRequest
	if providerName == providers.ProviderAnthropic && len(body) > 0 {
		env, parseErr := translate.ParseAnthropic(body)
		if parseErr == nil {
			prep, err = env.PrepareAnthropicPassthrough(r.Header)
			if err != nil {
				return fmt.Errorf("prepare passthrough: %w", err)
			}
		} else {
			prep = providers.PreparedRequest{Body: body, Headers: translate.AnthropicPassthroughHeadersForBody(r.Header, body)}
		}
	} else if providerName == providers.ProviderAnthropic {
		prep = providers.PreparedRequest{Body: body, Headers: translate.AnthropicPassthroughHeadersForBody(r.Header, body)}
	} else {
		prep = providers.PreparedRequest{Body: body, Headers: make(http.Header)}
	}

	proxyStart := s.clock.Now()
	proxyErr := p.Passthrough(ctx, prep, w, r)
	proxyMs := s.clock.Now().Sub(proxyStart).Milliseconds()
	log.Info("PassthroughToProvider complete", "provider", providerName, "proxy_ms", proxyMs, "proxy_err", proxyErr)
	return proxyErr
}

func logUpstreamBody(log *slog.Logger, sessionKey [sessionpin.SessionKeyLen]byte, decision router.Decision, feats translate.RoutingFeatures, body []byte) {
	log.Info("upstream prepared request",
		"session_key", hex.EncodeToString(sessionKey[:8]),
		"decision_model", decision.Model,
		"decision_provider", decision.Provider,
		"message_count", feats.MessageCount,
		"body_len", len(body),
	)
}

func (s *Service) anthropicNativeAttempt(
	env *translate.RequestEnvelope,
	r *http.Request,
	prep providers.PreparedRequest,
	sink http.ResponseWriter,
	preludeBuf *preludeBuffer,
	preludeState *requestNoticeStream,
	streamCut *streamCutObserver,
	notice translate.RouterNotice,
	setExtractor func(*telemetry.UsageExtractor),
	setStreamCost func(router.Decision, bool, func() bool),
) dispatchAttempt {
	return func(actx context.Context, d router.Decision, p providers.Client) error {
		requestCacheTTL1h := func() bool { return translate.AnthropicRequestCacheTTL1h(prep.Body) }
		setStreamCost(d, false, requestCacheTTL1h)
		attemptSink, noticeErr := preludeState.prepare(actx, sink, preludeBuf, translate.FormatAnthropic, env.Stream(), reconnectNoticeForAttempt(actx, r.Header, notice))
		if noticeErr != nil {
			return fmt.Errorf("emit Anthropic router notice: %w", noticeErr)
		}
		ex, proxyWriter := s.newUsageExtractor(attemptSink, d.Provider)
		ex.SetRequestCacheTTL1h(requestCacheTTL1h)
		setExtractor(ex)
		var terminal *streamTerminalObserver
		if env.Stream() {
			terminal = newStreamTerminalObserver(proxyWriter, nativeStreamAnthropic)
			proxyWriter = terminal
		}
		if preludeBuf != nil {
			preludeBuf.Seal()
		}
		toolNames := translate.NewAnthropicToolNameWriter(proxyWriter, prep.ResponseToolNames)
		err := p.Proxy(actx, d, prep, streamCut.attach(toolNames), r)
		err = finalizeAfterProxy(err, toolNames.Finalize)
		if err == nil && terminal != nil {
			err = terminal.streamErr()
		}
		if err != nil && env.Stream() && preludeBuf.Committed() {
			streamCut.noteCut(err)
			err = emitAnthropicSSEErrorEvent(sink, attributeStreamError(actx, d.Provider, err))
		}
		return err
	}
}

func (s *Service) maybeRepinOnRefusal(ctx context.Context, strategy router.Strategy, obs *refusalObserver, sessionKey [sessionpin.SessionKeyLen]byte, role string, served router.Decision) {
	if obs == nil || !obs.refused {
		return
	}
	s.repinOffRefusingModel(ctx, strategy, sessionKey, role, served, obs.category, "")
}

func (s *Service) repinOffRefusingModel(ctx context.Context, strategy router.Strategy, sessionKey [sessionpin.SessionKeyLen]byte, role string, served router.Decision, category, avoidProvider string) {
	if s.pinStore == nil {
		return
	}
	if !s.ResolveCyberRefusalRepin(ctx) {
		return
	}
	installationID := installationIDFromContext(ctx)
	if installationID == uuid.Nil {
		return
	}
	if sessionKey == ([sessionpin.SessionKeyLen]byte{}) {
		return
	}
	if strings.HasPrefix(served.Reason, translate.ReasonUserForceModel) {
		return
	}
	log := observability.FromContext(ctx)
	fbModel, fbProvider, ok := s.cyberRefusalFallback(ctx, strategy, sessionKey, role, served, avoidProvider)
	if !ok {
		log.Warn("safety refusal observed but no distinct fallback model available; not re-pinning",
			"from_model", served.Model, "fallback_model", fbModel, "refusal_category", category)
		return
	}
	pin := sessionpin.Pin{
		SessionKey:     sessionKey,
		Role:           role,
		InstallationID: installationID,
		Provider:       fbProvider,
		Model:          fbModel,
		Reason:         reasonCyberRefusalRepin,
		Strategy:       strategy,
		TurnCount:      1,
		PinnedUntil:    pinExpiry(reasonCyberRefusalRepin, s.clock.Now()),
	}
	if err := s.pinStore.Upsert(context.WithoutCancel(ctx), pin); err != nil {
		log.Error("cyber-refusal re-pin: pin upsert failed", "err", err, "from_model", served.Model, "to_model", fbModel)
		return
	}
	log.Info("safety refusal — re-pinned session off refusing model",
		"session_key", shortSessionKey(sessionKey),
		"refusal_category", category,
		"from_model", served.Model,
		"to_model", fbModel,
		"to_provider", fbProvider)
}

func (s *Service) ProxyMessages(ctx context.Context, body []byte, w http.ResponseWriter, r *http.Request) (returnErr error) {
	ctx = withWeaveCreditsFunding(ctx, r.Header)
	w, finishBlindExperimentWriter := withBlindExperimentWriter(ctx, w, body)
	defer finishBlindExperimentWriter()
	if websearch.IsClaudeCodeWebSearchHelper(body) {
		r = r.Clone(ctx)
		r.Header.Del(ForceModelHeader)
	}
	ctx = context.WithValue(ctx, nativeSearchLoggerContextKey{}, observability.FromContext(ctx))
	ctx = s.withSubscriptionFundingAuthorization(ctx, r.Header)
	switch gjson.GetBytes(body, "thread.type").String() {
	case translate.MessageThreadContinue:
		observability.FromContext(ctx).Info("Rejecting message-thread continue; client resends full history")
		return writeMessageThreadUnsupported(w)
	case translate.MessageThreadCreate:
		var threadErr error
		if body, threadErr = sjson.DeleteBytes(body, "thread"); threadErr != nil {
			return fmt.Errorf("strip message thread: %w", threadErr)
		}
		translate.StripMessageThreadsBeta(r.Header)
	}
	ctx, returnErr = withTurnInput(ctx, s, body, r.Header, router.EndpointAnthropicMessages)
	if returnErr != nil {
		return returnErr
	}
	ctx, body, err := s.parseHandoff(ctx, body)
	if err != nil {
		return err
	}
	ctx, turn, err := admitTurn(ctx, s, s.clock, r)
	if err != nil {
		return err
	}
	rateLimit, facts, requestStart := turn.rateLimit, turn.facts, turn.start
	log := observability.FromContext(ctx)

	ctx = withSubscriptionOnlyWarningEcho(ctx, body)
	replayedModel := translate.LastAnnouncedMessagesModel(body)
	body, stripErr := stripRoutingMarkerFromMessages(body)
	if stripErr != nil {
		log.Error("Failed to strip routing marker from inbound messages", "err", stripErr)
		return fmt.Errorf("strip routing marker: %w", stripErr)
	}

	footerEchoed := translate.FeedbackFooterInHistory(body)
	if echoed, _ := ctx.Value(responsesFooterEchoedContextKey{}).(bool); echoed {
		footerEchoed = true
	}
	parsed, parseErr := s.ingress.Anthropic.ParseMessages(ctx, body)
	if parseErr != nil {
		log.Error("Failed to parse Anthropic request", "err", parseErr)
		return fmt.Errorf("parse request: %w", parseErr)
	}
	env, body, modelVariant1M := parsed.Envelope, parsed.Body, parsed.ModelVariant1M
	if claims := piSessionFromContext(ctx); claims != nil && !claims.matches(ctx, env) {
		return ErrHandoffInvalid
	}
	var responseBuffer *responseCostBuffer
	if !env.Stream() {
		responseBuffer = newResponseCostBuffer(w)
		w = responseBuffer
		defer func() {
			if flushErr := responseBuffer.FlushToClient(); flushErr != nil {
				log.Error("Failed to flush buffered response", "err", flushErr)
			}
		}()
	}

	clientBudget := resolveClientBudget(facts.Client, r.Header, env.Model(), modelVariant1M)
	ctx = requestcontext.WithClientBudget(ctx, clientBudget)
	agentShadowEval, agentShadowMode := AgentShadowEvalFromContext(ctx)

	var sessionKey [sessionpin.SessionKeyLen]byte
	ctx, log, sessionKey = bindRequestLogger(ctx, env, facts.APIKeyID, facts.RequestID, "anthropic_messages")
	log.Debug("Resolved client context budget", "budget_evidence", clientBudget.Evidence, "client_version", clientBudget.Version, "model_variant_1m", clientBudget.ModelVariant1M, "inbound_context_1m", clientBudget.InboundContext1M, "default_window", clientBudget.DefaultWindow, "default_compact_threshold", clientBudget.DefaultCompactThreshold)
	if err := env.StripCommandOutcomes(); err != nil {
		return fmt.Errorf("strip command outcomes: %w", err)
	}
	if removed, err := env.StripHistoricalCommands(); err != nil {
		return fmt.Errorf("strip historical router commands: %w", err)
	} else if removed > 0 {
		log.Info("Stripped historical router commands from Anthropic history", "removed_messages", removed)
	}
	if removed := env.StripRouterFeedbackArtifacts(); removed > 0 {
		log.Info("Stripped router-feedback artifacts from Anthropic history", "removed_messages", removed)
	}
	if removed := env.StripBetaArtifacts(); removed > 0 {
		ctx = withBetaArtifactHistory(ctx)
		log.Info("Stripped beta artifacts from Anthropic history", "removed_messages", removed)
	}

	feats := env.RoutingFeatures()
	ctx = withRequestImageCount(ctx, feats.ImageCount)
	if err := validateHandoffEnvelope(ctx, env, r.Header, body); err != nil {
		return err
	}
	if preparingHandoff(ctx) && !s.canPrepareHandoff(ctx, env, feats, r.Header, body) {
		return writePreparedHandoff(w, preparedHandoff{Bypass: true})
	}
	promptText := feats.PromptText
	embedInput := "concatenated_stream"
	if feats.OnlyUserMessageText != "" {
		promptText = feats.OnlyUserMessageText
		embedInput = "only_user_message"
	}

	log.Info("ProxyMessages start",
		"requested_model", feats.Model,
		"stream", env.Stream(),
		"message_count", feats.MessageCount,
		"has_tools", feats.HasTools,
		"total_input_tokens", feats.Tokens,
		"prompt_preview", s.zdrLogField(ctx, observability.Preview(promptText, 200)),
	)

	if !agentShadowMode {
		betaSessionKey := deriveBetaSessionKeyForRequest(ctx, env, facts.APIKeyID, sessionKey)
		if cmd, hasCmd := env.ExtractBetaCommand(); hasCmd {
			log.Info("ProxyMessages beta command")
			return s.handleBetaCommand(ctx, w, env, cmd, facts.InstallationID, sessionKey, betaSessionKey, feats.Tokens)
		}
		ctx, err = s.applySessionStrategy(ctx, facts.InstallationID, betaSessionKey)
		if err != nil {
			return err
		}
		*r = *r.WithContext(ctx)
	}
	strategy, forceModelSessionKey := router.StrategyFromContext(ctx), deriveForceModelSessionKeyForRequest(ctx, env, facts.APIKeyID, sessionKey)
	if preparingHandoff(ctx) && !s.authoritativePerTurnSelection(ctx, strategy) {
		return writePreparedHandoff(w, preparedHandoff{Bypass: true})
	}

	agentForceModel := ""
	requestBodyChanged := false
	if !agentShadowMode && s.pinStore != nil {
		if cmd, hasCmd := env.ExtractForceModelCommand(); hasCmd {
			log.Info("ProxyMessages force-model command", "force_model_cmd", cmd)
			if cmd.FromToolResult {
				var err error
				agentForceModel, _, err = s.applyForceModelCommand(ctx, strategy, cmd, facts.InstallationID, sessionKey, forceModelSessionKey)
				if err != nil {
					return err
				}
				requestBodyChanged = true
			} else {
				if err := s.handleForceModelCommand(ctx, w, env, cmd, sessionTarget{strategy: strategy, installationID: facts.InstallationID, sessionKey: sessionKey}, forceModelSessionKey, feats.Tokens); err != nil {
					return err
				}
				s.grantPostCommandContinuation(ctx, strategy, facts.InstallationID, sessionKey, roleForTier(catalog.TierFor(feats.Model)))
				return nil
			}
		}
	}
	if !agentShadowMode {
		if cmd, hasCmd := env.ExtractRouterFeedbackCommand(); hasCmd {
			log.Info("ProxyMessages router-feedback command")
			if err := s.handleRouterFeedbackCommand(ctx, w, env, cmd, sessionTarget{strategy: strategy, installationID: facts.InstallationID, sessionKey: sessionKey}, feats.Tokens, !cmd.FromToolResult); err != nil {
				return err
			}
			if !cmd.FromToolResult {
				s.grantPostCommandContinuation(ctx, strategy, facts.InstallationID, sessionKey, roleForTier(catalog.TierFor(feats.Model)))
				return nil
			}
			requestBodyChanged = true
		}
	}
	if !agentShadowMode && env.ExtractRouterSessionCommand() {
		log.Info("ProxyMessages router-session command")
		if err := s.handleRouterSessionCommand(ctx, w, env, feats.Tokens); err != nil {
			return err
		}
		s.grantPostCommandContinuation(ctx, strategy, facts.InstallationID, sessionKey, roleForTier(catalog.TierFor(feats.Model)))
		return nil
	}
	if !agentShadowMode && env.ExtractRouterModelsCommand() {
		log.Info("ProxyMessages router-models command")
		if err := s.handleRouterModelsCommand(ctx, w, env, feats.Tokens); err != nil {
			return err
		}
		s.grantPostCommandContinuation(ctx, strategy, facts.InstallationID, sessionKey, roleForTier(catalog.TierFor(feats.Model)))
		return nil
	}
	if !agentShadowMode && env.ExtractListModelsCommand() {
		log.Info("ProxyMessages list-models command")
		if err := s.handleListModelsCommand(ctx, w, env, feats.Tokens); err != nil {
			return err
		}
		s.grantPostCommandContinuation(ctx, strategy, facts.InstallationID, sessionKey, roleForTier(catalog.TierFor(feats.Model)))
		return nil
	}

	if coalesced := env.CoalesceDuplicateToolResults(); coalesced > 0 {
		log.Info("Coalesced duplicate tool results before dispatch", "coalesced", coalesced)
		requestBodyChanged = true
	}
	if sanitized := env.SanitizeOrphanedToolCalls(); sanitized > 0 {
		log.Info("Sanitized orphaned tool calls before dispatch", "sanitized", sanitized)
		requestBodyChanged = true
	}
	if requestBodyChanged {
		feats = env.RoutingFeatures()
		ctx = withRequestImageCount(ctx, feats.ImageCount)
		promptText = feats.PromptText
		embedInput = "concatenated_stream"
		if feats.OnlyUserMessageText != "" {
			promptText = feats.OnlyUserMessageText
			embedInput = "only_user_message"
		}
	}

	forceModel := agentForceModel
	forceCluster, taskArm := "", taskArmTurn{}
	if !agentShadowMode {
		var headerForceModel string
		var forceErr error
		ctx, headerForceModel, forceErr = s.applyForceModelHeader(ctx, r, facts.InstallationID, forceModelSessionKey)
		if forceErr != nil {
			return forceErr
		}
		if headerForceModel != "" {
			forceModel = headerForceModel
		}
		forceCluster, taskArm, forceErr = forceClusterForTurn(s.strategyPorts, ctx, turnRequest{Strategy: strategy, InstallationID: facts.InstallationID}, r, env, feats, forceModel)
		if forceErr != nil {
			return forceErr
		}
	}

	var inboundSpiralSignals conversation.SpiralSignals
	var inboundSpiralReasons []conversation.SpiralReason
	inboundSpiralComputed := s.ResolveTurnSignalCaptureEnabled(ctx)
	if inboundSpiralComputed {
		inboundSpiralSignals = conversation.ComputeSpiralSignals(env, feats.MessageCount)
		inboundSpiralReasons = conversation.SpiralReasons(inboundSpiralSignals)
	}

	if s.effectiveCaptureMode(ctx) != CaptureOff {
		logInboundRequestDiagnostics(log, env)
	}

	eligibility, eligibilityErr := requestEligibility(ctx, s, providers.ProviderAnthropic, r.Header)
	if eligibilityErr != nil {
		return eligibilityErr
	}
	enabledProviders := eligibility.EnabledProviders

	if !preparingHandoff(ctx) && s.serveNativeWebSearch(ctx, body, feats.Model, env.Stream(), feats.Tokens, enabledProviders, r.Header, w) {
		return nil
	}

	if !preparingHandoff(ctx) {
		if handled, searchErr := s.serveCodexWebSearch(ctx, body, feats.Model, env.Stream(), enabledProviders, r, w); handled {
			return searchErr
		}
	}

	outputReserve := contextWindowOutputReserve
	if feats.MaxTokens > outputReserve {
		outputReserve = feats.MaxTokens
	}
	baseExcluded := s.excludeCodexOAuthOnlyModels(ctx, r.Header, enabledProviders, s.excludedModelsForRequest(ctx))

	inboundToolCallCount := len(env.AssistantToolCallSignatures())
	inboundLastUser := env.LastUserMessage()
	inboundUserPrompt := routingEndsWithUserPrompt(ctx, env)
	inboundLatestToolCalls := latestToolCallCountsJSON(env)

	prefiltered := candidates.Prefilter(candidates.PrefilterRequest{
		Envelope: env, ImageCount: feats.ImageCount, OutputReserve: outputReserve, Eligibility: eligibility,
		Excluded: baseExcluded, Available: s.availableModels,
		AdvisorExclusions: advisorExclusions(env, s.routableUniverse()), ExcludeAdvisorOutranking: true,
	})
	logPrefilter(log, env, feats.ImageCount, outputReserve, prefiltered)
	excluded, overflowEstimate := prefiltered.Excluded, prefiltered.OverflowEstimate

	routeStart := s.clock.Now()
	routingPromptText, routingMessages := routingInputForRequest(ctx, env, promptText)
	req := router.Request{
		RequestedModel:               feats.Model,
		ClientBudget:                 clientBudget,
		ForceModel:                   forceModel,
		ForceCluster:                 forceCluster,
		EstimatedInputTokens:         feats.Tokens,
		HasTools:                     feats.HasTools,
		HasImages:                    feats.HasImages,
		ImageCount:                   feats.ImageCount,
		TranslationRequirements:      env.TranslationRequirements(router.EndpointAnthropicMessages),
		ReasoningConfigurationSHA256: env.ReasoningConfigurationSHA256(),
		ToolConfigurationSHA256:      env.ToolConfigurationSHA256(),
		PromptText:                   routingPromptText,
		ConversationMessages:         routingMessages,
		AvailableTools:               availableToolsForRouting(env),
		Tools:                        toolsForRouting(env),
		OrganizationID:               facts.ExternalID,
		FeedbackKey:                  hex.EncodeToString(sessionKey[:]),
		FeedbackRole:                 roleForTier(catalog.TierFor(feats.Model)),
		ClientSessionID:              clientSessionIDForRequest(ctx, env),
		EnabledProviders:             enabledProviders,
		CustomBindings:               eligibility.CustomBindings,
		GatewayProviders:             eligibility.GatewayProviders,
		SubscriptionProviders:        s.selectableSubscriptionProviders(ctx, r.Header),
		Candidates:                   prefilteredCandidates(ctx, excluded, prefiltered),
		PreferredModels:              s.preferredModelsForRequest(ctx),
		RoutingKnobs:                 routingKnobsForRequest(ctx),
		ClusterArmOverrides:          clusterArmOverridesForRequest(ctx),
		ProductEligibility:           entitlement.ModelBoundaryFromContext(ctx),
	}
	if facts.InstallationID != uuid.Nil {
		req.InstallationID = facts.InstallationID.String()
	}
	var routeRes turnLoopResult
	var routeErr error
	routeCtx, routeSpan := startRoutingSpan(ctx, strategy, req)
	if handoffFromContext(ctx) != nil {
		routeRes, routeErr = s.resumeHandoff(routeCtx, strategy, env, req, turntype.IsSubAgent(env, subAgentHintForRequest(routeCtx, r.Header.Get("x-weave-subagent-type")), openCodeCaller(ClientIdentityFrom(routeCtx))))
	} else if agentShadowMode {
		routeRes, routeErr = s.runAgentShadowEvaluationRoute(routeCtx, env, feats, facts.InstallationID, req, agentShadowEval)
	} else {
		routeRes, routeErr = taskArm.route(routeCtx, s.runTurnLoop)(env, feats, turnRequest{Strategy: strategy, APIKeyID: facts.APIKeyID, InstallationID: facts.InstallationID}, "", r.Header, req)
	}
	defer routeRes.releaseCooldownProbes()
	var escalationCapture *captureWriter
	defer func() {
		if !preparingHandoff(ctx) {
			s.completeEscalation(ctx, routeRes, returnErr, escalationCapture, translate.EscalationResponseAnthropic)
		}
	}()
	finishRoutingSpan(routeSpan, routeRes.Decision, routeErr)
	if routeErr != nil {
		log.Error("Routing failed", "err", routeErr, "route_ms", s.clock.Now().Sub(routeStart).Milliseconds(), "requested_model", feats.Model, "total_input_tokens", feats.Tokens)
		s.recordPolicyPinRouteFailure(ctx, routeFailureRequest{RequestID: facts.RequestID, RequestStart: requestStart, RequestedModel: feats.Model, Strategy: strategy}, routeRes.TurnType, inboundUserPrompt, routeErr)
		return routeErr
	}
	ctx = requestcontext.WithCallerModelPassthrough(ctx, routeRes.CallerModelPassthrough)
	ctx = withPickedModelFunding(ctx, routeRes.CallerModelPassthrough, routeRes.Decision.Provider)
	ctx = s.withTurnOverloadProbe(ctx, routeRes)
	if preparingHandoff(ctx) {
		return s.finishHandoffPreparation(ctx, strategy, w, env, req, routeRes)
	}
	if len(routeRes.SessionDisabledProviders) > 0 {
		ctx = context.WithValue(ctx, SessionDisabledProvidersContextKey{}, routeRes.SessionDisabledProviders)
	}
	if len(routeRes.ObservedOverflowModels) > 0 {
		ctx = context.WithValue(ctx, ObservedOverflowModelsContextKey{}, routeRes.ObservedOverflowModels)
	}
	if len(routeRes.SessionDemotedModels) > 0 {
		ctx = context.WithValue(ctx, SessionDemotedModelsContextKey{}, routeRes.SessionDemotedModels)
		if len(routeRes.SessionCooldownModels) > 0 {
			ctx = context.WithValue(ctx, SessionCooldownModelsContextKey{}, routeRes.SessionCooldownModels)
		}
		if len(routeRes.SessionStrikeReadmitModels) > 0 {
			ctx = context.WithValue(ctx, SessionStrikeReadmitModelsContextKey{}, routeRes.SessionStrikeReadmitModels)
		}
	}
	if len(routeRes.CooldownProbeDeniedModels) > 0 {
		ctx = context.WithValue(ctx, SessionCooldownProbeDeniedModelsContextKey{}, routeRes.CooldownProbeDeniedModels)
	}

	if routeRes.UsageBypass && routeRes.Decision.Provider == providers.ProviderAnthropic {
		err := s.bypassToAnthropic(ctx, env, feats, routeRes.modelSwitched(), requestStart, facts.RequestID, facts.ExternalID, routeRes.TurnType, routeRes.Decision.Reason, r, w)
		if !errors.Is(err, errBypassRetryable) {
			if !agentShadowMode {
				s.firePolicyShadowForServingDecision(ctx, strategy, routeRes.Decision, req)
			}
			return err
		}

		linkedSubscriptionRetry := false
		if billing.SubscriptionOnlyFromContext(ctx) {
			released, ok := releaseThrottledLinkedFirst(ctx)
			if ok {
				ctx = released
			} else if s.managedSubscriptions != nil && managedSubscriptionCanServe(ctx, routeRes.Decision.Provider, routeRes.Decision.Model) {
				ctx = withSuppressedClaudeSubscription(ctx)
				linkedSubscriptionRetry = true
				log.Info("Subscription-only bypass hit retryable error; trying linked Claude accounts",
					"request_id", facts.RequestID, "external_id", facts.ExternalID, "model", routeRes.Decision.Model)
			} else {
				log.Info("Subscription-only bypass hit retryable error; refusing instead of paid reroute",
					"request_id", facts.RequestID, "external_id", facts.ExternalID)
				return subscriptionOnlyUnavailable(ctx)
			}
		}

		if s.pinStore != nil && !isUnpinnedScoredTurn(routeRes.TurnType) {
			sessionKey := deriveSessionKeyForRequest(ctx, env, facts.APIKeyID)
			role := roleForTier(catalog.TierFor(feats.Model))
			pin, _ := routingpolicy.NewSessionPins(s.pinStore, s.clock.Now, strategy).Load(ctx, sessionKey, role)
			hmmHistory := routingpolicy.NewSessionPins(s.pinStore, s.clock.Now, strategy).LoadHMMHistory(ctx, sessionKey, role)
			forceHistory := s.loadForceModelHistory(ctx, sessionKey, role)
			routeRes.SessionKey = sessionKey
			routeRes.applySwitchHistory(pin, hmmHistory, forceHistory)
		}

		routeRes.UsageBypass = false
		if !linkedSubscriptionRetry {
			rerouteCtx, rerouteSpan := startRoutingSpan(ctx, strategy, req)
			decision, rerouteErr := s.routeFor(rerouteCtx, strategy, req)
			finishRoutingSpan(rerouteSpan, decision, rerouteErr)
			if rerouteErr != nil {
				log.Error("Reroute after usage-bypass failure failed", "err", rerouteErr)
				return rerouteErr
			}
			routeRes.Decision, routeRes.UserForced = decision, false
			routeRes.Fresh = decision
		}
	}

	routeRes.SuggestionMode = r.Header.Get("x-weave-suggestion-mode") == "true"
	decision := routeRes.Decision
	if !agentShadowMode {
		s.firePolicyShadowForServingDecision(ctx, strategy, decision, req)
	}
	tt := routeRes.TurnType
	stickyHit := routeRes.StickyHit
	pinTier := routeRes.PinTier
	pinAgeSec := routeRes.PinAgeSec
	routeMs := s.clock.Now().Sub(routeStart).Milliseconds()
	s.logPlannerOutcome(ctx, routeRes)

	toolBearingTurn := inboundToolCallCount > 0 || inboundLastUser.HasToolResult
	if !agentShadowMode && !routeRes.CallerModelPassthrough && !routeRes.AuthoritativePerTurn && toolBearingTurn && s.noProgress != nil {
		fp := conversation.ComputeNoProgressFingerprint(decision, promptText, feats.MessageCount, conversation.ToolProgressMarker(env))
		role := roleForTier(catalog.TierFor(feats.Model))
		if looped, count := s.noProgress.RecordAndDetect(routeRes.SessionKey, facts.InstallationID, role, fp, s.clock.Now()); looped {
			return s.handleNoProgressBreak(ctx, w, env, count, sessionTarget{strategy: strategy, installationID: facts.InstallationID, sessionKey: routeRes.SessionKey}, role, decision.Model, decision.Provider, feats.Tokens, routeRes.Decision.Reason)
		}
	}

	if !agentShadowMode && !routeRes.CallerModelPassthrough && !routeRes.AuthoritativePerTurn && s.ResolveTextRepetitionBreakEnabled(ctx) && (turntype.DetectFromEnvelope(env, feats, "") == turntype.MainLoop || turntype.DetectFromEnvelope(env, feats, "") == turntype.ToolResult) {
		if looped, count, sampleHash := conversation.DetectTextRepetition(env); looped {
			role := roleForTier(catalog.TierFor(feats.Model))
			return s.handleTextRepetitionBreak(ctx, w, env, count, sampleHash, sessionTarget{strategy: strategy, installationID: facts.InstallationID, sessionKey: routeRes.SessionKey}, role, decision.Model, decision.Provider, feats.Tokens)
		}
	}

	if !agentShadowMode && !callerModelPassthroughActive(ctx) {
		if _, recoveryErr := applyClientCompactionRecovery(ctx, env, clientBudget, tt, req.ConversationMessages); recoveryErr != nil {
			return recoveryErr
		}
	}

	var compactionHandoverOutcome handoverOutcome
	if !agentShadowMode && !subscriptionStateModelsEnabled(ctx) && !routeRes.AuthoritativePerTurn && decision.Provider != providers.ProviderAnthropic && !routeRes.HardPinned && !routeRes.Handover.Invoked && routeRes.PrefixTrimmed {
		log.Info("Context trimming detected on non-Anthropic route; rewriting context with handover summary",
			"message_count", feats.MessageCount,
			"tool_call_count", inboundToolCallCount,
			"decision_model", decision.Model,
			"decision_provider", decision.Provider,
		)
		compactionHandoverOutcome = s.runCompactionHandover(ctx, env, r.Header, decision.Model, req)
	}

	credentialCtx := resolveAndInjectCredentials(ctx, decision.Provider, decision.Model, r.Header)

	w.Header().Set(HeaderRouterDecision, decision.Reason)
	w.Header().Set(HeaderRouterProvider, decision.Provider)
	w.Header().Set(HeaderRouterModel, decision.Model)
	w.Header().Set(HeaderRouterContextWindow, strconv.Itoa(contextWindowForRequest(decision.Model, decision.Provider)))
	setContextEstimateHeaders(w.Header(), overflowEstimate, outputReserve)
	if !agentShadowMode {
		s.setFeedbackLinkHeader(ctx, w, facts.InstallationID, facts.ExternalID, facts.RequestID, auth.UserIDFrom(ctx))
	}

	if _, err := s.provider(decision.Provider); err != nil {
		return err
	}

	reqPricing, _ := catalog.PrimaryPriceFor(s.baselineFor(ctx, feats.Model))
	actPricing, _ := catalog.PrimaryPriceFor(decision.Model)
	reqDecisionPricing := reqPricing.ForInputTokens(feats.Tokens)
	actDecisionPricing := actPricing.ForInputTokens(feats.Tokens)
	decisionBuilder := telemetry.NewAttrBuilder(46).
		String("request_id", facts.RequestID).
		String("external_id", facts.ExternalID).
		String("router_user_id", auth.UserIDFrom(ctx)).
		String("client.device_id", facts.Client.DeviceID).
		String("client.account_id", facts.Client.AccountID).
		String("client.session_id", facts.Client.SessionID).
		String("client.user_agent", facts.Client.UserAgent).
		String("client.app", facts.Client.TelemetryClientApp()).
		String("rollout_id", policyRolloutIDFromContext(ctx)).
		String("requested.model", feats.Model).
		String("decision.model", decision.Model).
		String("decision.provider", decision.Provider).
		String("decision.reason", decision.Reason).
		Bool("routing.policy_fallback", routeRes.PolicyFallback).
		Bool("routing.sticky_hit", stickyHit).
		Bool("routing.session_pin_hit", pinTier == "in_proc" || pinTier == "postgres").
		String("routing.session_pin_tier", pinTier).
		Int64("routing.session_pin_age_s", pinAgeSec).
		String("routing.turn_type", string(tt)).
		String("routing.embed_input", embedInput).
		Int64("routing.estimated_input_tokens", int64(feats.Tokens)).
		Float64("catalog.requested_input_per_1m", reqDecisionPricing.InputUSDPer1M).
		Float64("catalog.requested_output_per_1m", reqDecisionPricing.OutputUSDPer1M).
		Float64("catalog.actual_input_per_1m", actDecisionPricing.InputUSDPer1M).
		Float64("catalog.actual_output_per_1m", actDecisionPricing.OutputUSDPer1M).
		Int64("latency.route_ms", routeMs)
	applySubscriptionDecisionSpanTelemetry(ctx, decisionBuilder, feats.Model, decision.Model)
	applyServingSpanAttrs(ctx, decisionBuilder)
	applySidecarAttrs(decisionBuilder, routeRes)
	applyPlannerAttrs(decisionBuilder, routeRes)
	applyRoutingStateAttrs(decisionBuilder, routeRes, decision.ServedIdentity(), sessionKey)
	applyCallerRoutingAttrs(ctx, decisionBuilder)
	applyExperimentAttrs(ctx, decisionBuilder, &routeRes, taskArm, feats.Model, routeRes.Decision.Model)
	telemetry.Record(ctx, telemetry.Span{
		Name:  "router.decision",
		Start: requestStart,
		End:   s.clock.Now(),
		Attrs: decisionBuilder.Build(),
	})
	telemetry.Flush(ctx)

	opts := translate.EmitOptions{
		TargetModel:                       decision.Model,
		TargetProvider:                    decision.Provider,
		Capabilities:                      router.Lookup(decision.Model),
		IncludeStreamUsage:                s.usageRequired(),
		SessionAffinity:                   sessionAffinityHint(routeRes.SessionKey),
		ModelSwitched:                     routeRes.modelSwitched(),
		EnableExtendedContext:             shouldEnableExtendedContext(env.FullTokenEstimate(), outputReserve),
		EnableServerSideFallback:          s.ResolveAnthropicServerSideFallback(ctx),
		KeepCrossVendorOrchestrationTools: flags.BoolOr(ctx, flags.KeyCCOrchToolsCrossVendor, s.ccOrchToolsCrossVendor),
		KeepCrossVendorTaskTools:          s.ResolveCCTaskToolsCrossVendor(ctx),
		AppendAutonomySystem:              s.autonomySystemAppendApplies(ctx, body, env, routeRes.TurnType),
		AppendWorkspaceSystem:             s.workspaceSystemAppendApplies(ctx, body, env, routeRes.TurnType),
	}
	effortServed := s.resolveEffort(ctx, decision, opts.Capabilities, routeRes.EscalateEffort)
	effortServed.apply(&opts)

	if s.claudeSubscriptionExhausted(ctx, r.Header) {
		ctx = withSuppressedClaudeSubscription(ctx)
		if s.linkedSeatCanCover(ctx, decision.Provider, decision.Model) {
			ctx = withDeferredReconnectNudge(ctx, claudeSubscriptionReconnectNudge(ctx, r.Header, s.usageObserver))
		} else {
			ctx = withSubscriptionReconnectNudge(ctx, claudeSubscriptionReconnectNudge(ctx, r.Header, s.usageObserver))
		}
	}
	ctx = s.resolveCredentials(ctx, decision.Provider, decision.Model, r.Header)
	opts.FastMode = fastModeForAttempt(ctx, decision.Model, decision.Provider)

	bindings := s.resolveBindingsForDispatch(ctx, decision)

	if billing.SubscriptionOnlyFromContext(ctx) && !routeRes.UsageBypass && !subscriptionStateModelsEnabled(ctx) {
		switch {
		case subscriptionOnlyUnservable(ctx, decision.Provider, decision.Model):
			released, ok := releaseUnservableLinkedFirst(ctx, decision)
			if !ok {
				log.Info("Subscription-only request cannot be served on the subscription; refusing",
					"requested_model", feats.Model, "external_id", facts.ExternalID, "decision_provider", decision.Provider)
				return subscriptionOnlyUnavailable(ctx)
			}
			ctx = released
		case !servedOnCustomerKey(ctx) && s.anthropicSubscriptionObservedExhausted(ctx, r.Header) && !claudeSubscriptionSuppressed(ctx):
			log.Info("Subscription-only request cannot be served on the subscription; refusing",
				"requested_model", feats.Model, "external_id", facts.ExternalID, "decision_provider", decision.Provider)
			return subscriptionOnlyUnavailable(ctx)
		default:
			bindings = []catalog.ProviderBinding{{Provider: decision.Provider}}
		}
	}
	clientSink := w
	var streamCost *streamCostWriter
	if env.Stream() && !agentShadowMode {
		streamCost = newStreamCostWriter(clientSink)
		clientSink = streamCost
		if s.ingress.Keepalive > 0 {
			keepalive := sse.NewKeepaliveWriter(clientSink, ingress.AnthropicPingFrame, s.ingress.Keepalive)
			defer keepalive.Close()
			clientSink = keepalive
		}
		if footer := s.feedbackFooter(ctx, ClientIdentityFrom(ctx).ClientApp, routeRes.TurnType, footerEchoed); footer != "" {
			clientSink = translate.NewAnthropicRoutingFooterWriter(clientSink, footer)
		}
	}
	clientSink, escalationCapture = s.captureEscalationResponse(clientSink, routeRes)
	contentSink, contentCap := s.maybeCaptureResponse(ctx, clientSink)
	var policyOutcomeCap *captureWriter
	if !agentShadowMode {
		contentSink, policyOutcomeCap = s.capturePolicyOutcomeResponse(ctx, contentSink, routeRes, decision)
	}
	preludeBuf := newPreludeBuffer(contentSink)
	var rootSink http.ResponseWriter = preludeBuf
	var sink = rootSink
	refusalObs := newRefusalObserver(sink)
	sink = refusalObs
	toolUseObs := newToolUseObserver(sink)
	sink = toolUseObs
	earlyStop := dispatch.NewEarlyStopGate(sink, log)
	sink = earlyStop
	preludeBuf.OnDiscard = earlyStop.ResetAttempt
	streamCut := newStreamCutObserver(s.clock.Now)
	streamCut.describeRequest(len(body), env)

	proxyStart := s.clock.Now()
	inferenceParentCtx := ctx
	ctx, inferenceSpan := startInferenceSpan(ctx, decision, clientSessionIDForRequest(ctx, env))
	defer inferenceSpan.End()
	var proxyErr error
	crossFormat := false
	var extractor *telemetry.UsageExtractor
	var respSummary translate.ResponseSummary
	var reqStats providers.RequestMutationStats

	announcementTurn := announcementTurnFor(routeRes, replayedModel)
	marker := suppressMarkerIfRequested(ctx, r.Header, modelSelectionMarkerForRequest(s.strategyPorts, ctx, announcementTurn, routingMarkerFor(announcementTurn), decision.Model, ""))
	muteSlowStartFor(ctx, routeRes)
	fundingCtx := ctx
	if CredentialsFromContext(fundingCtx) == nil {
		fundingCtx = credentialCtx
	}
	notice := routerNoticeForRequest(ctx, r.Header, subscriptions.ProviderClaude, decision.Model, marker)
	notice = addRoutingFundingSource(fundingCtx, seatAdmissionOf(s), decision.Provider, decision.Model, notice)
	marker = notice.Text
	toolValidator := env.ToolValidator()
	setExtractor := func(e *telemetry.UsageExtractor) { extractor = e }
	anthropicPrelude := &requestNoticeStream{now: s.clock.Now}
	fastServed := false
	setStreamCost := func(d router.Decision, inputIncludesCache bool, requestCacheTTL1h func() bool) {
		if streamCost != nil {
			streamCost.SetCostCalculator(routerCostCalculatorFor(d.Model, d.Provider, fastServed), inputIncludesCache, requestCacheTTL1h)
			streamCost.source = func() providerusage.Facts { return extractor.UsageFacts() }
		}
	}
	anthropicTierAttemptFor := func(targetOpts translate.EmitOptions, prep providers.PreparedRequest, targetNotice translate.RouterNotice) *anthropicTierAttempt {
		return &anthropicTierAttempt{
			s:             s,
			log:           log,
			env:           env,
			r:             r,
			opts:          targetOpts,
			native:        s.anthropicNativeAttempt(env, r, prep, sink, preludeBuf, anthropicPrelude, streamCut, targetNotice, setExtractor, setStreamCost),
			sink:          sink,
			preludeBuf:    preludeBuf,
			preludeState:  anthropicPrelude,
			streamCut:     streamCut,
			notice:        targetNotice,
			setExtractor:  setExtractor,
			setStreamCost: setStreamCost,
			logBody: func(d router.Decision, body []byte) {
				logUpstreamBody(log, routeRes.SessionKey, d, feats, body)
			},
		}
	}
	recordFastServed := func(fast bool) { fastServed = fast }

	attemptDeps := messagesAttemptDeps{
		request:                 r,
		svc:                     s,
		env:                     env,
		routeRes:                &routeRes,
		log:                     log,
		crossFormat:             &crossFormat,
		feats:                   feats,
		anthropicTierAttemptFor: anthropicTierAttemptFor,
		recordFastServed:        recordFastServed,
		preludeBuf:              preludeBuf,
		sink:                    sink,
		anthropicPrelude:        anthropicPrelude,
		streamCut:               streamCut,
		setExtractor:            setExtractor,
		setStreamCost:           setStreamCost,
		fastServed:              &fastServed,
		respSummary:             &respSummary,
		reqStats:                &reqStats,
		streamCost:              streamCost,
		extractor:               &extractor,
		toolValidator:           toolValidator,
	}
	buildAttempt := func(target router.Decision, targetOpts translate.EmitOptions, targetMarker string, fundingCtx context.Context) (dispatchAttempt, error) {
		return buildMessagesAttempt(ctx, attemptDeps, target, targetOpts, targetMarker, fundingCtx)
	}
	attempt, attemptBuildErr := buildAttempt(decision, opts, marker, fundingCtx)
	if attemptBuildErr != nil && !translate.IsIntrinsicallyIncompatible(attemptBuildErr) {
		finishInferenceSpan(inferenceSpan, decision, decision.Provider, -1, attemptBuildErr)
		return attemptBuildErr
	}

	rescue := &messagesRescue{
		rescueChain: &rescueChain{
			svc: s, strategyPorts: s.strategyPorts, seats: seatAdmissionOf(s), clock: s.clock, usageObserver: s.usageObserver,
			r:                r,
			log:              log,
			env:              env,
			routeRes:         routeRes,
			req:              &req,
			opts:             opts,
			contentSink:      contentSink,
			preludeBuf:       preludeBuf,
			attempt:          attempt,
			buildAttempt:     buildAttempt,
			surface:          inference.PurposeAnthropicMessages,
			respSummary:      &respSummary,
			reqStats:         &reqStats,
			decision:         decision,
			bindings:         bindings,
			marker:           marker,
			proxyErr:         proxyErr,
			effortServed:     effortServed,
			overflowEstimate: overflowEstimate,
			outputReserve:    outputReserve,
		},
		feats:           feats,
		agentShadowMode: agentShadowMode,

		earlyStop:                 earlyStop,
		streamCut:                 streamCut,
		attemptBuildErr:           attemptBuildErr,
		anthropicTierAttemptFor:   anthropicTierAttemptFor,
		recordFastServed:          recordFastServed,
		extractor:                 &extractor,
		fastServed:                &fastServed,
		crossFormat:               &crossFormat,
		compactionHandoverOutcome: compactionHandoverOutcome,
	}
	rescue.flushErr = rescue.flushErrAsAnthropic
	rescue.armBaseline(ctx)
	rescue.armSubscriptionRetry(ctx)
	ctx = rescue.armRescues(ctx)
	rescue.armEarlyStop(ctx)
	rescue.holdPrimary()
	rescue.dispatchPrimary(ctx)
	rescue.redoIfStoppedEarly(ctx)
	rescue.notePrimaryFailure(ctx)
	rescue.classifyPrimaryFailure()
	ctx = rescue.baselineRescue(ctx)
	rescue.subscriptionRetry(ctx)
	rescue.siblingRescue(ctx)
	rescue.finish(ctx)
	decision, marker, proxyErr, effortServed = rescue.decision, rescue.marker, rescue.proxyErr, rescue.effortServed
	compactionHandoverOutcome = rescue.compactionHandoverOutcome
	winnerIdx, finalProvider := rescue.winnerIdx, rescue.finalProvider

	if rescue.subscriptionFailoverUsed {
		ctx = withSuppressedClaudeSubscription(ctx)
	}
	ctx = resolveAndInjectCredentials(ctx, finalProvider, decision.Model, r.Header)

	if extractor.Speed() == catalog.SpeedFast {
		fastServed = true
	}
	if actBindingPricing, ok := servedPricing(finalProvider, decision.Model, fastServed); ok {
		actPricing = actBindingPricing
	}

	proxyMs := s.clock.Now().Sub(proxyStart).Milliseconds()
	finishInferenceSpan(inferenceSpan, decision, finalProvider, winnerIdx, proxyErr)
	ctx = restoreParentSpan(ctx, inferenceParentCtx)

	if refusalObs.refused && respSummary.StopReason == "" {
		respSummary.StopReason = anthropicRefusalStopReason
	}

	nativeRespSummary := false
	if respSummary.StopReason == "" && s.ResolveNativeAnthropicResponseSignals(ctx) {
		if stopReason, toolUseBlocks, observed := extractor.AnthropicResponse(); observed {
			respSummary.UpstreamFinishReason = stopReason
			respSummary.StopReason = stopReason
			respSummary.ToolUseBlocks = toolUseBlocks
			nativeRespSummary = true
		}
	}
	terminalFinishReason := respSummary.UpstreamFinishReason
	if nativeRespSummary {
		terminalFinishReason = ""
	}
	terminalToolUse, endedOnToolUse := toolUseObs.terminal()
	toolErrorTally := toolErrorCounts(env.AssistantToolCallOutcomes())

	in, out := extractor.Tokens()
	cacheCreation, cacheRead := extractor.CacheTokens()
	usageMods := extractor.UsageModifiers()
	baselineWarmPrefill := routeRes.baselineWarmPrefillTokens(requestStart, cacheCreation, cacheRead, decision.Model, s.baselineFor(ctx, feats.Model), req.HistoryTruncated)
	requestedInputCost := catalog.CounterfactualInputCost(in, cacheCreation, cacheRead, baselineWarmPrefill, reqPricing, decision.Provider, usageMods)
	if responseBuffer != nil && proxyErr == nil {
		setRouterCostHeaders(w.Header(), routerResponseCostFromPricing(actPricing, decision.Provider, in, out, cacheCreation, cacheRead, usageMods))
	}
	upstreamBuilder := telemetry.NewAttrBuilder(41).
		String("request_id", facts.RequestID).
		String("external_id", facts.ExternalID).
		String("router_user_id", auth.UserIDFrom(ctx)).
		String("client.device_id", facts.Client.DeviceID).
		String("client.account_id", facts.Client.AccountID).
		String("client.session_id", facts.Client.SessionID).
		String("client.user_agent", facts.Client.UserAgent).
		String("client.app", facts.Client.TelemetryClientApp()).
		String("rollout_id", policyRolloutIDFromContext(ctx)).
		String("requested.model", feats.Model).
		String("decision.model", decision.Model).
		String("decision.provider", finalProvider).
		String("decision.reason", decision.Reason).
		String("routing.turn_type", string(routeRes.TurnType)).
		String("upstream.finish_reason", respSummary.UpstreamFinishReason).
		String("upstream.stop_reason", respSummary.StopReason).
		Bool("upstream.refusal", refusalObs.refused).
		String("upstream.refusal_category", refusalObs.category).
		Int64("usage.input_tokens", int64(in)).
		Int64("usage.output_tokens", int64(out)).
		Int64("usage.cache_creation_input_tokens", int64(cacheCreation)).
		Int64("usage.cache_read_input_tokens", int64(cacheRead)).
		Int64("usage.cache_creation_1h_input_tokens", int64(usageMods.CacheCreation1h)).
		String("usage.speed", string(extractor.Speed())).
		String("usage.inference_geo", string(extractor.InferenceGeo())).
		Float64("cost.requested_input_usd", requestedInputCost).
		Int64("cost.baseline_warm_prefill_tokens", int64(baselineWarmPrefill)).
		Float64("cost.requested_output_usd", catalog.EffectiveOutputCost(catalog.PromptTokens(in, cacheCreation, cacheRead, decision.Provider), out, reqPricing, usageMods)).
		Float64("cost.actual_input_usd", catalog.EffectiveInputCost(in, cacheCreation, cacheRead, actPricing, decision.Provider, usageMods)).
		Float64("cost.actual_output_usd", catalog.EffectiveOutputCost(catalog.PromptTokens(in, cacheCreation, cacheRead, decision.Provider), out, actPricing, usageMods)).
		Bool("cost.subscription_served", s.costNeutralSubscriptionServed(ctx)).
		Bool("cost.fast_mode", fastServed).
		Int64("latency.upstream_ms", proxyMs).
		Int64("latency.total_ms", s.clock.Now().Sub(requestStart).Milliseconds()).
		Int64("upstream.status_code", int64(upstreamStatus(proxyErr))).
		Bool("routing.cross_format", crossFormat).
		String("dispatch.primary_provider", rescue.primaryProvider).
		String("dispatch.primary_model", rescue.primaryModel).
		String("dispatch.final_provider", finalProvider).
		Int64("dispatch.fallback_attempts", int64(winnerIdx)).
		Bool("dispatch.failover_used", rescue.failoverUsed).
		Bool("dispatch.failover_attempted", rescue.failoverAttempted).
		Bool("dispatch.baseline_failover", rescue.baselineFailoverUsed).
		Bool("dispatch.subscription_failover", rescue.subscriptionFailoverUsed).
		Bool("dispatch.sibling_failover", rescue.siblingFailoverUsed).
		Bool("dispatch.early_stop_redo", rescue.earlyStopRedone)
	if s.effectiveCaptureMode(ctx) == CaptureOff {
		upstreamBuilder.Int64("request.message_count", int64(feats.MessageCount)).
			Bool("request.has_tools", feats.HasTools)
	}
	applyPlannerAttrs(upstreamBuilder, routeRes)
	applyRoutingStateAttrs(upstreamBuilder, routeRes, decision.ServedIdentity(), sessionKey)
	applyCallerRoutingAttrs(ctx, upstreamBuilder)
	applyExperimentAttrs(ctx, upstreamBuilder, &routeRes, taskArm, feats.Model, decision.Model)
	applyEffortAttrs(upstreamBuilder, effortServed)
	s.applySubscriptionSpanTelemetry(ctx, upstreamBuilder, feats.Model, decision.Model)
	addTimingAttrs(ctx, upstreamBuilder)

	obs := buildObservationContext(s.strategyPorts, ctx, strategy, decision, routeRes.Fresh, s.effectiveCaptureMode(ctx))
	obs.applySpanAttrs(upstreamBuilder)

	telemetry.Record(ctx, telemetry.Span{
		Name:  "router.upstream",
		Start: proxyStart,
		End:   s.clock.Now(),
		Attrs: upstreamBuilder.Build(),
	})
	respBody, respTrunc := capturedResponse(contentCap)
	if !agentShadowMode {
		s.recordCallLog(ctx, upstreamBuilder.Build(), routeMs, proxyErr, body, respBody, respTrunc)
	}
	telemetry.Flush(ctx)

	if !agentShadowMode {
		s.recordTurnUsage(ctx, routeRes, finalProvider, decision.ServedIdentity(), in, out, cacheCreation, cacheRead, extractor.OutputLimitReached())
		s.clearRecoveredCooldown(ctx, routeRes, decision.Model, proxyErr)
	}

	var subscriberTelemetry *InsertTelemetryParams
	if !agentShadowMode && facts.InstallationID != uuid.Nil {
		credentialKeyPrefix, credentialKeySuffix, credSource := s.credentialKeyParts(ctx)
		s.markServedExternalKeyUsed(ctx, proxyErr)
		degenerateShadow := proxyErr == nil && !nativeRespSummary && isDegenerateResponse(out, respSummary.ToolUseBlocks, respSummary.StopReason, respSummary.StopReasonDemoted)
		if degenerateShadow && !agentShadowMode {
			log.Info("router.degenerate_shadow",
				"model", decision.Model,
				"provider", finalProvider,
				"output_tokens", out,
				"tool_use_blocks", respSummary.ToolUseBlocks,
				"stop_reason", respSummary.StopReason,
				"upstream_finish_reason", respSummary.UpstreamFinishReason,
				"would_failover", true,
			)
			s.evictPinAfterDegenerateResponse(ctx, strategy, stickyHit, decision.Reason, facts.InstallationID, routeRes.SessionKey, stickyStateRole(routeRes))
		}
		tel := InsertTelemetryParams{
			InstallationID:           facts.InstallationID.String(),
			APIKeyID:                 apiKeyIDFromContext(ctx),
			RequestID:                facts.RequestID,
			SpanType:                 "router.upstream",
			TraceID:                  facts.RequestID,
			Timestamp:                requestStart,
			RequestedModel:           feats.Model,
			DecisionModel:            decision.Model,
			DecisionProvider:         decision.Provider,
			DecisionReason:           telemetryDecisionReason(ctx, decision.Reason),
			RequestedAllowedModels:   requestedAllowedModelsForTelemetry(ctx),
			EstimatedInputTokens:     int32(feats.Tokens),
			ContextSnapshot:          contextSnapshotJSONForDecision(w.Header(), facts.RequestID, feats.Model, decision.Model, decision.Provider, requestStart, s.clock.Now()),
			StickyHit:                stickyHit,
			PinTier:                  routeRes.PinTier,
			EmbedInput:               embedInput,
			InputTokens:              int32(in),
			OutputTokens:             int32(out),
			RequestedInputCostUSD:    requestedInputCost,
			RequestedOutputCostUSD:   catalog.EffectiveOutputCost(catalog.PromptTokens(in, cacheCreation, cacheRead, decision.Provider), out, reqPricing, usageMods),
			ActualInputCostUSD:       catalog.EffectiveInputCost(in, cacheCreation, cacheRead, actPricing, decision.Provider, usageMods),
			ActualOutputCostUSD:      catalog.EffectiveOutputCost(catalog.PromptTokens(in, cacheCreation, cacheRead, decision.Provider), out, actPricing, usageMods),
			RouteLatencyMs:           routeMs,
			UpstreamLatencyMs:        proxyMs,
			TotalLatencyMs:           s.clock.Now().Sub(requestStart).Milliseconds(),
			CrossFormat:              crossFormat,
			UpstreamStatusCode:       int32(upstreamStatus(proxyErr)),
			CandidateModels:          obs.CandidateModels,
			ChosenScore:              obs.ChosenScore,
			CandidateScores:          obs.CandidateScores,
			Propensity:               obs.Propensity,
			Strategy:                 obs.Strategy,
			RouteID:                  obs.RouteID,
			PolicyRouteKey:           obs.PolicyRouteKey,
			PolicyArtifactID:         obs.PolicyArtifactID,
			PolicyArtifactSHA256:     obs.PolicyArtifactSHA256,
			RosterVersion:            obs.RosterVersion,
			ClassifierArtifactID:     obs.ClassifierArtifactID,
			ClassifierArtifactSHA256: obs.ClassifierArtifactSHA256,
			ClassifierPredictedLabel: obs.ClassifierPredictedLabel,
			ClassifierClassOrder:     obs.ClassifierClassOrder,
			ClassifierProbabilities:  obs.ClassifierProbabilities,
			SelectionPolicyReleaseID: obs.SelectionPolicyReleaseID,
			SelectionPolicySHA256:    obs.SelectionPolicySHA256,
			SelectionHeadGeneration:  int64PtrIf(obs.SelectionHeadGeneration > 0, obs.SelectionHeadGeneration),
			SelectionTrace:           obs.SelectionTrace,
			SidecarSchemaVersion:     obs.SidecarSchemaVersion,
			TrainingAllowed:          obs.TrainingAllowed,
			CaptureMode:              obs.CaptureMode,
			DebugRef:                 obs.DebugRef,
			TTFTMs:                   obs.TTFTMs,
			CacheCreationTokens:      cacheTokenPtr(cacheCreation),
			CacheCreation1hTokens:    cacheCreation1hPtr(cacheCreation, usageMods.CacheCreation1h),
			CacheReadTokens:          cacheTokenPtr(cacheRead),
			Speed:                    string(extractor.Speed()),
			InferenceGeo:             string(extractor.InferenceGeo()),
			ReasoningTokens:          cacheTokenPtr(extractor.ReasoningTokens()),
			DeviceID:                 facts.Client.DeviceID,
			SessionID:                facts.Client.SessionID,
			RouterUserID:             auth.UserIDFrom(ctx),
			ClientApp:                facts.Client.TelemetryClientApp(),
			TurnType:                 string(routeRes.TurnType),
			RolloutID:                obs.RolloutID,
			UpstreamFinishReason:     stringPtrOrEmpty(respSummary.UpstreamFinishReason),
			StopReason:               stringPtrOrEmpty(respSummary.StopReason),
			ToolUseBlocks:            int32PtrIfKnown(int32(respSummary.ToolUseBlocks), respSummary.StopReason != ""),
			InvalidToolArgsBlocks:    int32PtrIfKnown(int32(respSummary.InvalidToolArgsBlocks), respSummary.StopReason != "" && !nativeRespSummary),
			LastToolUseName:          terminalToolUse.Name,
			LastToolUseInputBytes:    int32PtrIfKnown(int32(terminalToolUse.InputBytes), endedOnToolUse),
			ToolErrorCounts:          toolErrorCountsJSON(toolErrorTally),
			AutonomyAppendFired:      boolPtrOrNil(autonomyAppendFired(opts, finalProvider)),
			WorkspaceAppendFired:     boolPtrOrNil(workspaceAppendFired(opts, finalProvider)),
			FailoverUsed:             boolPtrTrue(rescue.failoverUsed),
			FailoverAttempted:        boolPtrTrue(rescue.failoverAttempted),
			DegenerateShadow:         boolPtrOrNil(degenerateShadow),
			SessionKey:               sessionKey[:],
			Role:                     routeRes.PinRole,
			FreshDecisionModel:       obs.FreshDecisionModel,
			FreshCandidateScores:     obs.FreshCandidateScores,
			PinAgeSec:                int64PtrIf(stickyHit && pinAgeSec > 0, pinAgeSec),
			ToolResultBytes:          toolResultBytesPtr(inboundLastUser, tt),
			UserPrompt:               &inboundUserPrompt,
			ErrorClass:               classifyTurnError(proxyErr, respSummary.StopReason, respSummary.InvalidToolArgsBlocks),
			FailureOwner:             turnFailureOwnerAfterFinish(ctx, proxyErr, classifyTurnError(proxyErr, respSummary.StopReason, respSummary.InvalidToolArgsBlocks), terminalFinishReason),
			LatestToolCallCounts:     inboundLatestToolCalls,
			CredentialKeyPrefix:      credentialKeyPrefix,
			CredentialKeySuffix:      credentialKeySuffix,
			CredentialSource:         credSource,
			UnifiedLimitHeaders:      unifiedLimitHeadersJSON(ctx),
			CodexLimitHeaders:        codexLimitHeadersJSON(ctx),
		}
		applyServedGroupTelemetry(s.strategyPorts, ctx, &tel, routeRes, decision)
		applyPlannerTelemetry(&tel, routeRes)
		applyEffortTelemetry(&tel, effortServed)
		applyAuthorityShadowTelemetry(&tel, routeRes)
		applyBlindExperimentTelemetry(ctx, &tel, &routeRes)
		applyPolicyPinTelemetry(ctx, &tel, decision.Metadata)
		applySubscriberTelemetry(ctx, &tel)
		applyAgreementShadowTelemetry(s.strategyPorts, ctx, &tel, routeRes)
		signalTurn := tt == turntype.MainLoop || tt == turntype.ToolResult
		applyTurnSignalTelemetry(&tel, inboundSpiralSignals, inboundSpiralReasons,
			inboundSpiralComputed && signalTurn,
			obs.TrainingAllowed,
			s.effectiveCaptureMode(ctx))
		applyClientGitContextTelemetry(ctx, &tel, routeRes.SessionFirstTurn, env.SystemBlocks())
		subscriberTelemetry = &tel
	}

	var subscriberSettlement subscriberSettlementState
	if turnUsageBillable(ctx, proxyErr, in, out, cacheCreation, cacheRead) && !agentShadowMode {
		subscriberSettlement = s.emitBilling(ctx, facts.RequestID, facts.ExternalID, feats.Model, decision, actPricing, routeRes, in, out, cacheCreation, cacheRead, usageMods)
		if compactionHandoverOutcome.Invoked && !compactionHandoverOutcome.FallbackToFullHistory {
			s.billAuxiliaryInference(ctx, facts.RequestID, auxSuffixCompactionHandoverSummry, facts.ExternalID, compactionHandoverOutcome.SummaryUsage)
		}
	}
	if subscriberTelemetry != nil {
		if turnUsageBillable(ctx, proxyErr, in, out, cacheCreation, cacheRead) {
			applySubscriberSettlementTelemetry(subscriberSettlement, subscriberTelemetry)
		} else {
			markSubscriberTelemetryUnsettled(subscriberTelemetry)
		}
		s.fireTelemetry(ctx, *subscriberTelemetry)
	}

	armDemoted := ""
	var armDemotionReasonValue sessionpin.DemotionReason
	rescuedArmDemoted := ""
	var rescuedArmDemotionReason sessionpin.DemotionReason
	if !agentShadowMode && !routeRes.CallerModelPassthrough {
		s.maybeEvictPinAfterUpstreamErr(ctx, stickyHit, proxyErr, decision.Reason, sessionTarget{strategy: strategy, installationID: facts.InstallationID, sessionKey: routeRes.SessionKey}, stickyStateRole(routeRes))

		armDemoted = s.maybeDemoteArmAfterCommittedStreamFailure(ctx, preludeBuf.Committed(), routeRes.HardPinned, proxyErr, decision.Model, decision.Reason, sessionTarget{strategy: strategy, installationID: facts.InstallationID, sessionKey: routeRes.SessionKey}, stickyStateRole(routeRes), routeRes.PinRole)
		if armDemoted != "" {
			armDemotionReasonValue = sessionpin.DemotionReasonCommittedStreamFailure
		}

		primaryModelRescueRan := rescue.siblingRescueRan || decision.Model != rescue.primaryModel
		if providers.IsResponseHeaderTimeout(rescue.primaryFailureErr) && (primaryModelRescueRan || proxyErr != nil) {
			rescuedArmDemoted, rescuedArmDemotionReason = s.maybeStrikeArmAfterRescuedFailure(ctx, primaryModelRescueRan, routeRes.HardPinned, rescue.primaryFailureErr, rescue.primaryDecision, sessionTarget{strategy: strategy, installationID: facts.InstallationID, sessionKey: routeRes.SessionKey}, stickyStateRole(routeRes), routeRes.PinRole)
		} else {
			rescuedArmDemoted, rescuedArmDemotionReason = s.maybeStrikeArmAfterRescuedFailure(ctx, rescue.siblingRescueRan, routeRes.HardPinned, rescue.rescuedPrimaryErr, rescue.rescuedPrimary, sessionTarget{strategy: strategy, installationID: facts.InstallationID, sessionKey: routeRes.SessionKey}, stickyStateRole(routeRes), routeRes.PinRole)
		}
		s.strikeRescuedModelsForTurn(ctx, proxyErr, decision, routeRes.HardPinned, routeRes.SessionKey, stickyStateRole(routeRes), routeRes.PinRole)
		if proxyErr != nil {
			unrescuedStallDemoted := s.maybeDemoteArmAfterUnrescuedStall(ctx, rescue.siblingRescueRan, preludeBuf.Committed(), routeRes.HardPinned, rescue.primarySubscriptionArmFailure, rescue.primaryDecision, sessionTarget{strategy: strategy, installationID: facts.InstallationID, sessionKey: routeRes.SessionKey}, stickyStateRole(routeRes), routeRes.PinRole)
			if armDemoted == "" && unrescuedStallDemoted != "" {
				armDemoted = unrescuedStallDemoted
				armDemotionReasonValue = sessionpin.DemotionReasonUnrescuedStall
			}
		}

		s.maybeExpireDeadArmPin(ctx, strategy, rescue.deadArmRejected, decision.Reason, facts.InstallationID, routeRes.SessionKey, stickyStateRole(routeRes))
		s.maybeExpireSubscriptionArmPin(ctx, strategy, rescue.primarySubscriptionArmFailure, decision.Reason, facts.InstallationID, routeRes.SessionKey, stickyStateRole(routeRes))

		if !rescue.baselineAttempted {
			s.maybeDisableProviderAfterOverload(ctx, stickyHit, proxyErr, finalProvider, decision.Reason, sessionTarget{strategy: strategy, installationID: facts.InstallationID, sessionKey: routeRes.SessionKey}, stickyStateRole(routeRes), routeRes.PinRole)
		}

		s.maybeRepinOnRefusal(ctx, strategy, refusalObs, routeRes.SessionKey, stickyStateRole(routeRes), decision)
	}

	for _, iss := range respSummary.ToolCallIssues {
		log.Info("router.tool_call_invalid",
			"tool_name", iss.ToolName,
			"failure_bucket", string(iss.Bucket),
			"detail", iss.Detail,
			"repaired", iss.Repaired,
			"repair_actions", iss.Actions,
			"model", decision.Model,
			"provider", finalProvider,
			"session_key_prefix", shortSessionKey(routeRes.SessionKey),
		)
	}

	if preludeBuf.Committed() {
		streamCut.noteCut(proxyErr)
	}
	servedCredentialSource, servedSubscriptionTier := s.servedCredentialLogValues(ctx)
	log.Info("ProxyMessages complete", append(append([]any{"requested_model", feats.Model, "baseline_model", s.baselineFor(ctx, feats.Model), "decision_model", decision.Model, "decision_provider", decision.Provider, "primary_provider", rescue.primaryProvider, "primary_model", rescue.primaryModel, "fallback_attempts", winnerIdx, "failover_used", rescue.failoverUsed, "failover_attempted", rescue.failoverAttempted, "subscription_failover", rescue.subscriptionFailoverUsed, "decision_reason", decision.Reason, "requested_tier", routeRes.RequestedTier.String(), "decision_tier", catalog.TierFor(decision.Model).String(), "embedded_tokens", len(promptText) / 4, "total_input_tokens", feats.Tokens, "context_estimate_tokens", overflowEstimate, "has_tools", feats.HasTools, "message_count", feats.MessageCount, "last_kind", feats.LastKind, "last_preview", s.zdrLogField(ctx, feats.LastPreview), "embed_input", embedInput, "cross_format", crossFormat, "sticky_hit", stickyHit, "route_ms", routeMs, "proxy_ms", proxyMs, "proxy_err", proxyErr, "error_class", classifyTurnError(proxyErr, respSummary.StopReason, respSummary.InvalidToolArgsBlocks), "failure_owner", turnFailureOwnerAfterFinish(ctx, proxyErr, classifyTurnError(proxyErr, respSummary.StopReason, respSummary.InvalidToolArgsBlocks), terminalFinishReason), "upstream_err_body", s.zdrLogField(ctx, providers.UpstreamErrorBodyMessage(proxyErr)), "upstream_status", upstreamStatus(proxyErr), "upstream_finish_reason", respSummary.UpstreamFinishReason, "resp_stop_reason", respSummary.StopReason, "resp_refusal", refusalObs.refused, "resp_refusal_category", refusalObs.category, "stop_reason_promoted", respSummary.StopReasonPromoted, "tool_use_blocks", respSummary.ToolUseBlocks, "invalid_tool_args_blocks", respSummary.InvalidToolArgsBlocks, "last_tool_use_name", terminalToolUse.Name, "last_tool_use_input_bytes", terminalToolUse.InputBytes, "ended_on_tool_use", endedOnToolUse, "tool_error_counts", toolErrorTally, "text_only_turn_nudged", respSummary.TextOnlyTurnNudged, "stop_reason_demoted", respSummary.StopReasonDemoted, "suppressed_tool_calls", respSummary.SuppressedToolCalls, "tool_call_invalid_blocks", len(respSummary.ToolCallIssues), "cc_only_tools_stripped", reqStats.CCOnlyToolsStripped, "cc_task_reminders_stripped", reqStats.CCTaskRemindersStripped, "gemini_reminder_injected", reqStats.GeminiReminderInjected, "gemini_validated_tool_mode", reqStats.GeminiValidatedToolMode, "resp_output_tokens", respSummary.OutputTokens, "resp_reasoning_tokens", extractor.ReasoningTokens(), "prelude_committed", preludeBuf.Committed(), "routing_marker", marker, "prior_served_model", routeRes.PriorServedModel, "hard_pinned", routeRes.HardPinned, "credential_source", servedCredentialSource, "client_app", completionLogClientApp(facts.Client), "subscription_tier", servedSubscriptionTier}, append(append(append(armStrikeLogFieldsWithPrimaryReason(armDemoted, armDemotionReasonValue, rescuedArmDemoted, rescuedArmDemotionReason), plannerLogFields(routeRes)...), streamCut.completionLogFields()...), rateLimit.CompletionLogFields()...)...), downgradeShadowLogFields(routeRes)...)...)
	policyRespBody, policyRespTrunc := capturedResponse(policyOutcomeCap)
	var policyResp *policyOutcomeResponse
	if policyOutcomeCap != nil {
		policyResp = &policyOutcomeResponse{Body: policyRespBody, Truncated: policyRespTrunc}
	}
	if !agentShadowMode {
		s.reportPolicyOutcome(ctx, routeRes, decision, effortServed, finalProvider, fastServed, feats.Tokens, in, out, cacheCreation, cacheRead, usageMods, routeMs, proxyMs, proxyErr, policyResp)
	}
	return proxyErr
}

func applyPlannerAttrs(b *telemetry.AttrBuilder, res turnLoopResult) {
	if res.PlannerDecision.Evaluated() {
		b.String("planner.outcome", plannerOutcomeAttr(res)).
			String("planner.reason", res.PlannerDecision.Reason).
			Float64("planner.expected_savings_usd", res.PlannerDecision.ExpectedSavingsUSD).
			Float64("planner.eviction_cost_usd", res.PlannerDecision.EvictionCostUSD).
			Float64("planner.threshold_usd", res.PlannerDecision.ThresholdUSD).
			String("planner.pin_model", res.PinModel).
			String("planner.fresh_model", res.Fresh.Model).
			String("planner.chosen_model", res.Decision.Model).
			Bool("planner.pin_cache_warm", !res.PlannerDecision.PinCacheCold).
			String("cache.pin_provider", res.PinProvider).
			Bool("cache.prefix_stable", !res.PrefixBroken).
			Bool("planner.pin_price_fallback", res.PlannerDecision.PinPriceFallback).
			Bool("planner.fresh_price_fallback", res.PlannerDecision.FreshPriceFallback)
		if res.PriorTurnGapMS != nil {
			b.Int64("cache.prior_turn_gap_ms", *res.PriorTurnGapMS)
		}
		if res.PlannerDecision.ShadowComputed {
			b.String("planner.shadow_outcome", plannerOutcome(res.PlannerDecision.ShadowOutcome)).
				Float64("planner.shadow_expected_savings_usd", res.PlannerDecision.ShadowExpectedSavingsUSD).
				Float64("planner.shadow_stay_cost_usd", res.PlannerDecision.ShadowStayCostUSD).
				Float64("planner.shadow_switch_cost_usd", res.PlannerDecision.ShadowSwitchCostUSD)
		}
	}
	b.Bool("handover.invoked", res.Handover.Invoked).
		Int64("handover.latency_ms", res.Handover.LatencyMS).
		Int64("handover.summary_tokens", int64(res.Handover.SummaryTokens)).
		Bool("handover.fallback_to_full_history", res.Handover.FallbackToFullHistory)
}

func plannerOutcome(outcome planner.Outcome) string {
	if outcome == planner.OutcomeSwitch {
		return "switch"
	}
	return "stay"
}

func applySidecarAttrs(b *telemetry.AttrBuilder, res turnLoopResult) {
	if res.Fresh.Metadata == nil {
		return
	}
	if st := res.Fresh.Metadata.SidecarTimings; st != nil {
		if st.EmbedMs != nil {
			b.Int64("latency.embed_ms", int64(math.Round(*st.EmbedMs)))
		}
		if st.SelectMs != nil {
			b.Int64("latency.sidecar_select_ms", int64(math.Round(*st.SelectMs)))
		}
		if st.OtherMs != nil {
			b.Int64("latency.sidecar_other_ms", int64(math.Round(*st.OtherMs)))
		}
	}
	if ss := res.Fresh.Metadata.SidecarStats; ss != nil {
		if ss.EmbedCacheHits != nil {
			b.Int64("routing.embed_cache_hits", *ss.EmbedCacheHits)
		}
		if ss.EmbedCacheMisses != nil {
			b.Int64("routing.embed_cache_misses", *ss.EmbedCacheMisses)
		}
		if ss.EmbedCacheEvictions != nil {
			b.Int64("routing.embed_cache_evictions", *ss.EmbedCacheEvictions)
		}
		if ss.RoutesInflight != nil {
			b.Int64("routing.sidecar_inflight", *ss.RoutesInflight)
		}
		if ss.OverrunsLive != nil {
			b.Int64("routing.sidecar_overruns_live", *ss.OverrunsLive)
		}
	}
}

func applyRoutingStateAttrs(
	b *telemetry.AttrBuilder,
	res turnLoopResult,
	servedIdentity string,
	sessionKey [sessionpin.SessionKeyLen]byte,
) {
	b.String("routing.session_key", sessionKeyHex(sessionKey)).
		String("routing.pin_role", res.PinRole).
		String("routing.prior_served_model", res.PriorServedModel).
		Bool("routing.model_changed", res.PriorServedModel != "" && res.PriorServedModel != servedIdentity).
		Bool("routing.llm_escalation_applied", res.escalation.LLMApplied())
}

func plannerOutcomeAttr(res turnLoopResult) string {
	if !res.PlannerDecision.Evaluated() {
		return "skipped"
	}
	switch res.PlannerDecision.Outcome {
	case planner.OutcomeStay:
		return "stay"
	case planner.OutcomeSwitch:
		return "switch"
	default:
		return "skipped"
	}
}

func plannerLogFields(res turnLoopResult) []any {
	var pinCacheWarm any
	if res.PlannerDecision.Evaluated() {
		pinCacheWarm = !res.PlannerDecision.PinCacheCold
	}
	return []any{
		"planner_outcome", plannerOutcomeAttr(res),
		"planner_reason", res.PlannerDecision.Reason,
		"planner_expected_savings_usd", res.PlannerDecision.ExpectedSavingsUSD,
		"planner_eviction_cost_usd", res.PlannerDecision.EvictionCostUSD,
		"planner_threshold_usd", res.PlannerDecision.ThresholdUSD,
		"planner_pin_model", res.PinModel,
		"planner_fresh_model", res.Fresh.Model,
		"planner_chosen_model", res.Decision.Model,
		"planner_pin_cache_warm", pinCacheWarm,
		"cache_pin_provider", res.PinProvider,
		"cache_prefix_stable", !res.PrefixBroken,
		"cache_prior_turn_gap_ms", res.PriorTurnGapMS,
		"planner_pin_price_fallback", res.PlannerDecision.PinPriceFallback,
		"planner_fresh_price_fallback", res.PlannerDecision.FreshPriceFallback,
	}
}

func stickyStateRole(res turnLoopResult) string {
	if res.StickyHit && res.StickyRole != "" {
		return res.StickyRole
	}
	return res.PinRole
}

func (s *Service) logPlannerOutcome(ctx context.Context, res turnLoopResult) {
	if !res.PlannerDecision.Evaluated() {
		return
	}
	log := observability.FromContext(ctx)
	if res.PlannerDecision.Outcome == planner.OutcomeSwitch {
		log.Info("router switched models",
			"from", res.PinModel,
			"to", res.Decision.Model,
			"reason", res.PlannerDecision.Reason,
			"expected_savings_usd", res.PlannerDecision.ExpectedSavingsUSD,
			"eviction_cost_usd", res.PlannerDecision.EvictionCostUSD,
			"threshold_usd", res.PlannerDecision.ThresholdUSD,
			"pin_cache_warm", !res.PlannerDecision.PinCacheCold,
			"handover_invoked", res.Handover.Invoked,
			"handover_fallback_to_full_history", res.Handover.FallbackToFullHistory,
			"handover_latency_ms", res.Handover.LatencyMS,
		)
		return
	}
	message := "router stayed on pinned model"
	if isHMMDecision(s.strategyPorts, res.Fresh) {
		message = "router stayed on previous HMM model"
	}
	log.Info(message,
		"model", res.Decision.Model,
		"pin_model", res.PinModel,
		"reason", res.PlannerDecision.Reason,
		"expected_savings_usd", res.PlannerDecision.ExpectedSavingsUSD,
		"eviction_cost_usd", res.PlannerDecision.EvictionCostUSD,
		"threshold_usd", res.PlannerDecision.ThresholdUSD,
		"pin_cache_warm", !res.PlannerDecision.PinCacheCold,
	)
}

func isHMMTurn(ports continuity.StrategyPorts, res turnLoopResult) bool {
	return isHMMDecision(ports, res.Decision) || isHMMDecision(ports, res.Fresh)
}

func (s *Service) recordHMMTurnHistory(ctx context.Context, res turnLoopResult, servedProvider, servedModel string, in, out, cacheCreation, cacheRead int, outputLimitReached bool) {
	sessionPins := routingpolicy.NewSessionPins(s.pinStore, s.clock.Now, strategyForTurnLoopResult(res))
	if servedModel == "" || res.InstallationID == uuid.Nil {
		return
	}
	var zeroKey [sessionpin.SessionKeyLen]byte
	if res.SessionKey == zeroKey {
		return
	}
	hasUsage := in != 0 || out != 0 || cacheCreation != 0 || cacheRead != 0
	strategyCtx := strategyContext(ctx, strategyForTurnLoopResult(res))
	historyProvider := servedProvider
	if !hasUsage {
		if prior := sessionPins.LoadHMMHistory(strategyCtx, res.SessionKey, res.PinRole); prior.Provider != "" {
			historyProvider = prior.Provider
		}
	}
	role := hmmHistoryRole(res.PinRole)
	sessionPins.Upsert(strategyCtx, sessionpin.Pin{
		SessionKey:     res.SessionKey,
		Role:           role,
		InstallationID: res.InstallationID,
		Provider:       historyProvider,
		Reason:         hmmHistoryStoredReason(s.strategyPorts, res),
		Strategy:       strategyForTurnLoopResult(res),
		TurnCount:      1,
		PinnedUntil:    pinExpiry(s.strategyPorts.CostGate.HistoryReason(), s.clock.Now()),
	})
	if !hasUsage {
		return
	}
	now := s.clock.Now()
	if err := s.pinStore.UpdateUsage(strategyCtx, res.SessionKey, role, sessionpin.Usage{
		Strategy:            strategyForTurnLoopResult(res),
		InputTokens:         in,
		CachedReadTokens:    cacheRead,
		CachedWriteTokens:   cacheCreation,
		OutputTokens:        out,
		OutputLimitReached:  outputLimitReached,
		EndedAt:             now,
		ServedModel:         servedModel,
		ServedProvider:      servedProvider,
		PriorServedModel:    res.PriorServedModel,
		SessionEverSwitched: res.SessionEverSwitched,
	}); err != nil {
		observability.Get().Error("HMM switch-history writeback failed", "err", err)
	}
	s.recordTokenizerObservation(ctx, res, role, servedProvider, in, cacheCreation, cacheRead)
}

func hmmHistoryStoredReason(ports continuity.StrategyPorts, res turnLoopResult) string {
	if isHMMDecision(ports, res.Fresh) {
		return res.Fresh.Reason
	}
	if isHMMDecision(ports, res.Decision) {
		return res.Decision.Reason
	}
	return ports.CostGate.HistoryReason()
}

func (s *Service) policyOutcomeRoute(res turnLoopResult, decision router.Decision) (router.Decision, *router.RoutingMetadata, policy.OutcomeReporter, bool) {
	for _, routeDecision := range []router.Decision{decision, res.Fresh} {
		routeMetadata := routeDecision.Metadata
		if routeMetadata == nil || routeMetadata.Strategy == "" || routeMetadata.RouteID == "" {
			continue
		}
		registered, ok := s.strategies[router.Strategy(routeMetadata.Strategy)]
		if !ok || registered.outcomes == nil {
			continue
		}
		return routeDecision, routeMetadata, registered.outcomes, true
	}
	return router.Decision{}, nil, nil, false
}

func (s *Service) capturePolicyOutcomeResponse(ctx context.Context, w http.ResponseWriter, res turnLoopResult, decision router.Decision) (http.ResponseWriter, *captureWriter) {
	if !policyTrainingAllowedForRequest(ctx) {
		return w, nil
	}
	if _, _, _, ok := s.policyOutcomeRoute(res, decision); !ok {
		return w, nil
	}
	capture := newCaptureWriter(w, policyOutcomeResponseMaxBytes)
	return capture, capture
}

func isAuthoritativePinHoldMismatch(res turnLoopResult, served router.Decision) bool {
	if served.Metadata != nil {
		return false
	}
	switch pinTier(res.PinTier) {
	case pinTierAuthoritativeExcludedPin,
		pinTierAuthoritativeUpgradeConfidenceLow,
		pinTierAuthoritativeDowngradeConfidenceLow,
		pinTierAuthoritativeDowngradeHysteresis:
		return true
	default:
		return false
	}
}

func (s *Service) reportPolicyOutcome(ctx context.Context, res turnLoopResult, decision router.Decision, effort effortResolution, finalProvider string, servedFast bool, estimatedInputTokens, inputTokens, outputTokens, cacheCreation, cacheRead int, usageMods catalog.UsageModifiers, routeMs, proxyMs int64, proxyErr error, response *policyOutcomeResponse) {
	routeDecision, routeMetadata, reporter, ok := s.policyOutcomeRoute(res, decision)
	if !ok {
		return
	}
	organizationID, _ := ctx.Value(ExternalIDContextKey{}).(string)
	installationID, _ := ctx.Value(InstallationIDContextKey{}).(string)
	trainingAllowed := policyTrainingAllowedForRequest(ctx)
	clientIdentity := ClientIdentityFrom(ctx)
	selectedServedModelMatch := routeDecision.Model == decision.Model
	authoritativeModelMismatch := routeMetadata.AuthoritativePerTurnSelection &&
		!selectedServedModelMatch
	if authoritativeModelMismatch {
		trainingAllowed = false
		log := observability.FromContext(ctx)
		if isAuthoritativePinHoldMismatch(res, decision) {
			log.Info(
				"Authoritative policy model did not match served model",
				"route_id", routeMetadata.RouteID,
				"selected_model", routeDecision.Model,
				"served_model", decision.Model,
				"pin_tier", res.PinTier,
			)
		} else {
			log.Error(
				"Authoritative policy model did not match served model",
				"route_id", routeMetadata.RouteID,
				"selected_model", routeDecision.Model,
				"served_model", decision.Model,
			)
		}
	}
	effortMismatch := effort.Mismatch()
	if effortMismatch {
		trainingAllowed = false
		observability.FromContext(ctx).Warn(
			"Selected effort did not match the effort sent upstream",
			"route_id", routeMetadata.RouteID,
			"served_model", decision.Model,
			"arm_effort", effort.Arm,
			"selected_effort", effort.Selected,
			"sent_effort", effort.Sent,
			"effort_source", effort.Source,
		)
	}
	payload := map[string]interface{}{
		"route_id":                         routeMetadata.RouteID,
		"strategy":                         routeMetadata.Strategy,
		"organization_id":                  organizationID,
		"installation_id":                  installationID,
		"client_app":                       clientIdentity.TelemetryClientApp(),
		"rollout_id":                       policyRolloutIDFromContext(ctx),
		"training_allowed":                 trainingAllowed,
		"capture_mode":                     s.effectiveCaptureMode(ctx).String(),
		"policy_route_key":                 routeMetadata.PolicyRouteKey,
		"policy_artifact_id":               routeMetadata.PolicyArtifactID,
		"policy_artifact_sha256":           routeMetadata.PolicyArtifactSHA256,
		"roster_version":                   routeMetadata.RosterVersion,
		"classifier_artifact_id":           s.strategyPorts.Facts.ClassifierTelemetryOf(routeMetadata).ArtifactID,
		"classifier_artifact_sha256":       s.strategyPorts.Facts.ClassifierTelemetryOf(routeMetadata).ArtifactSHA256,
		"selection_policy_release_id":      routeMetadata.SelectionPolicyReleaseID,
		"selection_policy_sha256":          routeMetadata.SelectionPolicySHA256,
		"selection_head_generation":        routeMetadata.SelectionHeadGeneration,
		"sidecar_schema_version":           routeMetadata.SidecarSchemaVersion,
		"selected_model":                   routeDecision.Model,
		"selected_provider":                routeDecision.Provider,
		"served_model":                     decision.Model,
		"served_provider":                  finalProvider,
		"decision_model":                   routeDecision.Model,
		"decision_provider":                routeDecision.Provider,
		"selected_served_model_match":      selectedServedModelMatch,
		"arm_effort":                       effort.Arm,
		"selected_effort":                  effort.Selected,
		"sent_effort":                      effort.Sent,
		"effort_source":                    effort.Source,
		"selected_sent_effort_match":       !effortMismatch,
		"authoritative_per_turn_selection": routeMetadata.AuthoritativePerTurnSelection,
		"status":                           upstreamStatus(proxyErr),
		"error":                            "",
		"estimated_input_tokens":           estimatedInputTokens,
		"input_tokens":                     inputTokens,
		"output_tokens":                    outputTokens,
		"cache_creation_tokens":            cacheCreation,
		"cache_read_tokens":                cacheRead,
		"route_latency_ms":                 routeMs,
		"upstream_latency_ms":              proxyMs,
		"turn_type":                        string(res.TurnType),
		"sticky_hit":                       res.StickyHit,
	}
	switch {
	case authoritativeModelMismatch:
		payload["training_exclusion_reason"] = "selected_served_model_mismatch"
	case effortMismatch:
		payload["training_exclusion_reason"] = "selected_sent_effort_mismatch"
	}
	if trainingAllowed && response != nil {
		payload["response_body_truncated"] = response.Truncated
		if !response.Truncated {
			payload["response_text"] = translate.AnthropicClientResponseText(response.Body)
		}
	}
	if proxyErr != nil {
		payload["error"] = proxyErr.Error()
	}
	if price, ok := servedPricing(finalProvider, decision.Model, servedFast); ok {
		inputCost := catalog.EffectiveInputCost(inputTokens, cacheCreation, cacheRead, price, finalProvider, usageMods)
		outputCost := catalog.EffectiveOutputCost(catalog.PromptTokens(inputTokens, cacheCreation, cacheRead, finalProvider), outputTokens, price, usageMods)
		payload["cost_usd"] = inputCost + outputCost
	}
	log := observability.FromContext(ctx).With("route_id", routeMetadata.RouteID)
	if err := ctx.Err(); err != nil {
		log.Debug("Skipping policy outcome report for canceled request", "err", err)
		return
	}
	if identity, managed := requestcontext.ServingIdentityFromContext(ctx); managed {
		payload["serving_identity"] = identity
	}
	observability.SafeGoContext(ctx, log, policyOutcomeReportTimeout, "reportPolicyOutcome", func(reportCtx context.Context) {
		if err := reporter.ReportOutcome(reportCtx, payload); err != nil {
			log.Error("Policy outcome report failed", "strategy", routeMetadata.Strategy, "err", err)
		}
	})
}

func pinDecision(p sessionpin.Pin) router.Decision {
	return router.Decision{
		Provider: p.Provider,
		Model:    p.Model,
		Effort:   p.Effort,
		Reason:   p.Reason,
	}
}

func (s *Service) policyDeadlineDefaultDecision(req router.Request) (router.Decision, bool) {
	if s.policyDeadlineDefaultModel == "" {
		return router.Decision{}, false
	}
	if _, excluded := req.Candidates.ExcludedModels[s.policyDeadlineDefaultModel]; excluded {
		return router.Decision{}, false
	}
	if _, excluded := req.Candidates.SafetyExcludedModels[s.policyDeadlineDefaultModel]; excluded {
		return router.Decision{}, false
	}

	providerSet := make(map[string]struct{}, s.clients.Len())
	for _, provider := range s.clients.Names() {
		if req.EnabledProviders != nil {
			if _, enabled := req.EnabledProviders[provider]; !enabled {
				continue
			}
		}
		providerSet[provider] = struct{}{}
	}
	binding, ok := catalog.ResolveBinding(s.policyDeadlineDefaultModel, providerSet)
	if !ok {
		return router.Decision{}, false
	}
	return router.Decision{
		Provider: binding.Provider,
		Model:    s.policyDeadlineDefaultModel,
		Reason:   policyDeadlineDefaultReason,
	}, true
}

func (s *Service) bandSwapServed(ctx context.Context, turnType turntype.TurnType, pin sessionpin.Pin, fresh router.Decision, hasImages bool, imageCount int, enabledProviders, excludedModels map[string]struct{}) router.Decision {
	anchor := pinDecision(pin)
	if s.bandSwap == nil || pin.PairedModel == "" || turnType != turntype.MainLoop {
		return anchor
	}
	if fresh.Metadata == nil || len(fresh.Metadata.Embedding) != bandswap.EmbedDim {
		return anchor
	}
	action, band, ok := s.bandSwap.PredictBand(fresh.Metadata.Embedding)
	if !ok {
		return anchor
	}
	large, small := orderBandPair(pin)
	served := large
	if band == bandswap.Small {
		served = small
	}
	if served.Model != pin.Model {
		if _, available := s.availableModels[served.Model]; !available {
			return anchor
		}
		if hasImages && (!catalog.AcceptsImages(served.Model) || imageCount > 0 && !catalog.AcceptsImageCount(served.Model, imageCount, enabledProviders)) {
			return anchor
		}
		if _, excluded := excludedModels[served.Model]; excluded {
			return anchor
		}
		if _, disabled := s.globalAutomaticExcludedModels(ctx)[served.Model]; disabled {
			return anchor
		}
		if !s.clients.Has(served.Provider) {
			return anchor
		}
		if enabledProviders != nil {
			if _, ok := enabledProviders[served.Provider]; !ok {
				return anchor
			}
		}
	}
	observability.FromContext(ctx).Info("band swap served",
		"predicted_action", action,
		"band", band,
		"served_model", served.Model,
		"served_provider", served.Provider,
		"anchor_model", pin.Model,
		"paired_model", pin.PairedModel,
	)
	return served
}

func orderBandPair(pin sessionpin.Pin) (large, small router.Decision) {
	a := pinDecision(pin)
	b := router.Decision{Provider: pin.PairedProvider, Model: pin.PairedModel, Reason: pin.Reason}
	ta, tb := catalog.TierFor(a.Model), catalog.TierFor(b.Model)
	if ta != tb {
		if ta > tb {
			return a, b
		}
		return b, a
	}
	if primaryInputPrice(a.Model) >= primaryInputPrice(b.Model) {
		return a, b
	}
	return b, a
}

func primaryInputPrice(model string) float64 {
	pricing, ok := catalog.PrimaryPriceFor(model)
	if !ok {
		return 0
	}
	return pricing.InputUSDPer1M
}

func pinAge(p sessionpin.Pin, now time.Time) int64 {
	if p.FirstPinnedAt.IsZero() {
		return 0
	}
	d := now.Sub(p.FirstPinnedAt)
	if d < 0 {
		return 0
	}
	return int64(d.Seconds())
}

func externalKeysFromContext(ctx context.Context) []*auth.ExternalAPIKey {
	v := ctx.Value(ExternalAPIKeysContextKey{})
	if v == nil {
		return nil
	}
	keys, _ := v.([]*auth.ExternalAPIKey)
	return keys
}

func (s *Service) requestUsesNonDeploymentCreds(ctx context.Context, headers http.Header) bool {
	return s.credentials.UsesNonDeploymentCreds(externalKeysFromContext(ctx), headers)
}

func (s *Service) enabledProvidersForRequest(ctx context.Context, surfaceProvider string, headers http.Header) map[string]struct{} {
	subscriptionLanes := make(map[string]struct{}, 2)
	if subscriptionCredsFromToken(anthropicSubscriptionFromContext(ctx)) != nil ||
		managedSubscriptionEnrolled(ctx, subscriptions.ProviderClaude) && s.supportsSubscriptionTransport(providers.ProviderAnthropic) {
		subscriptionLanes[providers.ProviderAnthropic] = struct{}{}
	}
	if codexSubscriptionFromContext(ctx) != nil ||
		managedSubscriptionEnrolled(ctx, subscriptions.ProviderCodex) && !codexChatEndpoint(ctx) && s.supportsSubscriptionTransport(providers.ProviderOpenAI) {
		subscriptionLanes[providers.ProviderOpenAI] = struct{}{}
	}
	return s.credentials.EnabledProviders(credentials.EligibilityRequest{
		Registered:                    s.clients.NameSet(),
		SurfaceProvider:               surfaceProvider,
		Headers:                       headers,
		ExternalKeys:                  externalKeysFromContext(ctx),
		RouterKeyAuthed:               installationIDFromContext(ctx) != (uuid.UUID{}),
		SubscriptionLanes:             subscriptionLanes,
		InstallationExcludedProviders: installationExcludedProvidersFromContext(ctx),
		SessionDisabledProviders:      sessionDisabledProvidersFromContext(ctx),
	})
}

func (s *Service) hasOpenAIInfrastructureCredential(ctx context.Context, headers http.Header) bool {
	if byokServedForProvider(ctx, providers.ProviderOpenAI) {
		return true
	}
	if !s.credentials.BYOKOnly {
		if s.credentials.KeyedProviders == nil {
			if s.clients.Has(providers.ProviderOpenAI) {
				return true
			}
		} else if _, keyed := s.credentials.KeyedProviders[providers.ProviderOpenAI]; keyed {
			return true
		}
	}
	if installationIDFromContext(ctx) == uuid.Nil {
		if client := ExtractClientCredentials(providers.ProviderOpenAI, headers); client != nil && !client.OAuth {
			return true
		}
	}
	return false
}

func (s *Service) openAIPaidCredentialAvailable(ctx context.Context, headers http.Header) bool {
	if weaveCreditsDenied(ctx) {
		return usableBYOK(ctx, providers.ProviderOpenAI) != nil
	}
	return s.hasOpenAIInfrastructureCredential(ctx, headers)
}

func (s *Service) excludeCodexOAuthOnlyModels(
	ctx context.Context,
	headers http.Header,
	enabledProviders map[string]struct{},
	excluded map[string]struct{},
) map[string]struct{} {
	_, codexAvailable := subscriptionServableProviders(ctx, headers)[providers.ProviderOpenAI]
	if !codexAvailable || (!paidFallbackForbiddenFor(ctx, providers.ProviderOpenAI) && s.openAIPaidCredentialAvailable(ctx, headers)) {
		return excluded
	}
	for _, model := range catalog.Models {
		if codexSubscriptionCanAttemptModel(model.ID) {
			continue
		}
		for _, binding := range model.Providers {
			if enabledProviders != nil {
				if _, enabled := enabledProviders[binding.Provider]; !enabled {
					continue
				}
			}
			if binding.Provider == providers.ProviderOpenAI {
				excluded = candidates.ExcludingModel(excluded, model.ID)
			}
			break
		}
	}
	return excluded
}

func resolveAndInjectCredentials(ctx context.Context, provider, model string, headers http.Header) context.Context {
	byok := BuildCredentialsMap(externalKeysFromContext(ctx))
	if byok[provider] != nil && byok[provider].FundingPriority == auth.FundingPriorityKeyFirst {
		return context.WithValue(ctx, CredentialsContextKey{}, byok[provider])
	}
	routerKeyed := installationIDFromContext(ctx) != (uuid.UUID{})
	subDisabled := subscriptionRoutingDisabledForRequest(ctx) || subscriptionFundingOutOfPlayForRequest(ctx)
	suppressClaudeSub := claudeSubscriptionSuppressed(ctx) || subDisabled || claudeModelSuppressed(ctx, model)
	suppressCodexSub := codexSubscriptionSuppressed(ctx) || codexModelSuppressed(ctx, model) || codexChatEndpoint(ctx) || subDisabled || !codexSubscriptionCanAttemptModel(model)
	if provider == providers.ProviderAnthropic && !suppressClaudeSub {
		if sub := subscriptionCredsFromToken(anthropicSubscriptionFromContext(ctx)); sub != nil {
			return context.WithValue(ctx, CredentialsContextKey{}, sub)
		}
		if inbound := ExtractClientCredentials(provider, headers); inbound != nil && inbound.OAuth {
			observability.FromContext(ctx).Info("Resolved Claude subscription credential",
				"credential_source", inbound.Source)
			return context.WithValue(ctx, CredentialsContextKey{}, inbound)
		}
	}
	if provider == providers.ProviderOpenAI && !suppressCodexSub {
		if sub := codexSubscriptionFromContext(ctx); sub != nil {
			return context.WithValue(ctx, CredentialsContextKey{}, sub)
		}
		if inbound := ExtractClientCredentials(provider, headers); inbound != nil && inbound.OAuth {
			observability.FromContext(ctx).Debug("Resolved Codex subscription credential for OpenAI turn", "credential_source", inbound.Source)
			return context.WithValue(ctx, CredentialsContextKey{}, inbound)
		}
	}
	var creds *Credentials
	if byok != nil {
		creds = byok[provider]
	}
	if creds == nil && !routerKeyed {
		client := ExtractClientCredentials(provider, headers)
		if client != nil && client.OAuth &&
			((provider == providers.ProviderAnthropic && suppressClaudeSub) ||
				(provider == providers.ProviderOpenAI && suppressCodexSub)) {
			client = nil
		}
		creds = client
	}
	if creds != nil {
		return context.WithValue(ctx, CredentialsContextKey{}, creds)
	}
	if (suppressClaudeSub && provider == providers.ProviderAnthropic) ||
		(suppressCodexSub && provider == providers.ProviderOpenAI) {
		return clearCredentials(ctx)
	}
	return ctx
}

func addTimingAttrs(ctx context.Context, b *telemetry.AttrBuilder) {
	applyServingSpanAttrs(ctx, b)
	t := timing.TimingFrom(ctx)
	if t == nil {
		return
	}
	upstreamTotal := t.Ms(&t.UpstreamRequestNanos, &t.UpstreamEOFNanos)
	fullE2E := t.MsSince(&t.EntryNanos)

	var overhead int64
	if upstreamTotal > 0 {
		overhead = fullE2E - upstreamTotal
	}

	b.Int64("latency.full_e2e_ms", fullE2E).
		Int64("latency.preupstream_ms", t.Ms(&t.EntryNanos, &t.UpstreamRequestNanos)).
		Int64("latency.upstream_headers_ms", t.Ms(&t.UpstreamRequestNanos, &t.UpstreamHeadersNanos)).
		Int64("latency.upstream_first_byte_ms", t.Ms(&t.UpstreamRequestNanos, &t.UpstreamFirstByteNanos)).
		Int64("latency.upstream_total_ms", upstreamTotal).
		Int64("latency.postupstream_ms", t.MsSince(&t.UpstreamEOFNanos)).
		Int64("latency.router_overhead_ms", overhead)
}

func cacheCreation1hPtr(cacheCreation, oneHour int) *int32 {
	if cacheCreation <= 0 {
		return nil
	}
	v := int32(oneHour)
	return &v
}

func cacheTokenPtr(n int) *int32 {
	if n <= 0 {
		return nil
	}
	v := int32(n)
	return &v
}

func int32PtrIfKnown(v int32, known bool) *int32 {
	if !known {
		return nil
	}
	return &v
}

func boolPtrOrNil(v bool) *bool {
	if !v {
		return nil
	}
	return &v
}

func boolPtrTrue(v bool) *bool {
	return &v
}

func int64PtrIf(known bool, v int64) *int64 {
	if !known {
		return nil
	}
	return &v
}

func toolResultBytesPtr(inbound translate.LastUserMessageInfo, tt turntype.TurnType) *int32 {
	if tt != turntype.ToolResult || !inbound.HasToolResult {
		return nil
	}
	v := int32(inbound.ToolResultBytes)
	return &v
}

func stringPtrOrEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

const degenerateOutputThreshold = 10

func isDegenerateResponse(outputTokens, toolUseBlocks int, stopReason string, stopReasonDemoted bool) bool {
	return outputTokens < degenerateOutputThreshold &&
		toolUseBlocks == 0 &&
		stopReason == "end_turn" &&
		!stopReasonDemoted
}

func (s *Service) fireTelemetry(ctx context.Context, p InsertTelemetryParams) {
	if p.UpstreamStatusCode != http.StatusOK || !conversationContextTurn(turntype.TurnType(p.TurnType)) {
		p.ContextSnapshot = nil
	}
	if s.telemetry == nil {
		return
	}
	log := observability.Get().With("request_id", p.RequestID)
	observability.SafeGoContext(ctx, log, 5*time.Second, "fireTelemetry", func(ctx context.Context) {
		s.applyUserPromptGap(ctx, log, &p)
		if err := s.telemetry.InsertRequestTelemetry(ctx, p); err != nil {
			log.Warn("Telemetry insert failed", "err", err)
		}
		s.recordAgreementShadow(ctx, log, p)
	})
}

func (s *Service) emitBilling(ctx context.Context, requestID, externalID, requestedModel string, decision router.Decision, actPricing catalog.Pricing, routeRes turnLoopResult, in, out, cacheCreation, cacheRead int, usageMods catalog.UsageModifiers) subscriberSettlementState {
	if s.billing == nil || externalID == "" {
		return captureSubscriberSettlementState(ctx)
	}
	hasOverride := billing.HasOverrideFromContext(ctx)
	apiKeyID, _ := ctx.Value(APIKeyIDContextKey{}).(string)
	s.fireBilling(ctx, billing.DebitInferenceParams{
		OrganizationID:     externalID,
		RouterRequestID:    requestID,
		Model:              decision.Model,
		RequestedModel:     requestedModel,
		Provider:           decision.Provider,
		InputTokens:        in,
		OutputTokens:       out,
		CacheCreation:      cacheCreation,
		CacheRead:          cacheRead,
		Pricing:            actPricing,
		UsageModifiers:     usageMods,
		HasOverride:        hasOverride,
		SubscriptionServed: routeRes.UsageBypass || servedOnSubscription(ctx),
		ByokServed:         servedOnBYOK(ctx),
		APIKeyID:           apiKeyID,
		RouterUserID:       auth.UserIDFrom(ctx),
	})
	settlement := captureSubscriberSettlementState(ctx)

	if routeRes.Handover.Invoked && !routeRes.Handover.FallbackToFullHistory {
		s.billAuxiliaryInference(ctx, requestID, auxSuffixHandoverSummary, externalID, routeRes.Handover.SummaryUsage)
	}
	return settlement
}

func (s *Service) fireBilling(ctx context.Context, p billing.DebitInferenceParams) {
	if s.billing == nil {
		return
	}
	if session := auth.DelegatedSessionFrom(ctx); session != nil {
		p.DelegatedSessionID, p.DelegatedTaskID = session.SessionID, session.TaskID
	}
	if p.OrganizationID == "" {
		observability.FromContext(ctx).Debug("Billing debit skipped: no organization_id on request")
		return
	}
	dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	balance, err := s.billing.DebitForInference(dbCtx, p)
	if err == nil {
		observability.FromContext(ctx).Debug("Billing debit complete",
			"organization_id", p.OrganizationID,
			"router_request_id", p.RouterRequestID,
			"model", p.Model,
			"balance_usd_micros", balance,
			"override", p.HasOverride,
			"subscription_served", p.SubscriptionServed,
			"byok_served", p.ByokServed,
		)
		return
	}
	logBillingDebitFailure(ctx, p, err)
}

func logBillingDebitFailure(ctx context.Context, p billing.DebitInferenceParams, err error) {
	observability.FromContext(ctx).Error("router_billing_debit_failed",
		"err", err,
		"organization_id", p.OrganizationID,
		"router_request_id", p.RouterRequestID,
		"model", p.Model,
		"provider", p.Provider,
		"input_tokens", p.InputTokens,
		"output_tokens", p.OutputTokens,
		"cache_creation_tokens", p.CacheCreation,
		"cache_read_tokens", p.CacheRead,
		"has_override", p.HasOverride,
		"subscription_served", p.SubscriptionServed,
		"byok_served", p.ByokServed,
	)
}

func upstreamStatus(err error) int {
	if err == nil {
		return http.StatusOK
	}
	var statusErr *providers.UpstreamStatusError
	if errors.As(err, &statusErr) {
		return statusErr.Status
	}
	var bufferedErr *providers.UpstreamErrorResponse
	if errors.As(err, &bufferedErr) {
		return bufferedErr.Status
	}
	return 0
}

func finalizeAfterProxy(proxyErr error, fn func() error) error {
	var statusErr *providers.UpstreamStatusError
	isStatus := errors.As(proxyErr, &statusErr)
	if proxyErr != nil && !isStatus {
		return proxyErr
	}
	finErr := fn()
	if isStatus {
		return proxyErr
	}
	return finErr
}

func (s *Service) ProxyOpenAIChatCompletion(ctx context.Context, body []byte, w http.ResponseWriter, r *http.Request) (returnErr error) {
	ctx = withWeaveCreditsFunding(ctx, r.Header)
	w, finishBlindExperimentWriter := withBlindExperimentWriter(ctx, w, body)
	defer finishBlindExperimentWriter()
	ctx = s.withSubscriptionFundingAuthorization(ctx, r.Header)
	ctx, returnErr = withTurnInput(ctx, s, body, r.Header, router.EndpointOpenAIChat)
	if returnErr != nil {
		return returnErr
	}
	ctx, turn, err := admitTurn(ctx, s, s.clock, r)
	if err != nil {
		return err
	}
	rateLimit, facts, requestStart := turn.rateLimit, turn.facts, turn.start
	log := observability.FromContext(ctx)

	ctx = withSubscriptionOnlyWarningEcho(ctx, body)
	replayedModel := translate.LastAnnouncedMessagesModel(body)
	if replayedModel == "" {
		replayedModel, _ = ctx.Value(responsesReplayedModelContextKey{}).(string)
	}
	strippedBody, stripErr := stripRoutingMarkerFromMessages(body)
	if stripErr != nil {
		log.Error("Failed to strip routing marker from OpenAI messages", "err", stripErr)
		return fmt.Errorf("strip routing marker: %w", stripErr)
	}
	body = strippedBody

	footerEchoed := translate.FeedbackFooterInHistory(body)
	if echoed, _ := ctx.Value(responsesFooterEchoedContextKey{}).(bool); echoed {
		footerEchoed = true
	}
	strippedBody, stripErr = translate.StripFeedbackFooterFromMessages(body)
	if stripErr != nil {
		log.Error("Failed to strip feedback footer from OpenAI messages", "err", stripErr)
	} else {
		body = strippedBody
	}
	env, parseErr := translate.ParseOpenAI(body)
	if parseErr != nil {
		log.Error("Failed to parse OpenAI request", "err", parseErr)
		return fmt.Errorf("parse request: %w", parseErr)
	}
	var responseBuffer *responseCostBuffer
	_, isResponsesWriter := w.(*translate.ResponsesWriter)
	if !env.Stream() && !isResponsesWriter {
		responseBuffer = newResponseCostBuffer(w)
		w = responseBuffer
		defer func() {
			if flushErr := responseBuffer.FlushToClient(); flushErr != nil {
				log.Error("Failed to flush buffered response", "err", flushErr)
			}
		}()
	}

	var sessionKey [sessionpin.SessionKeyLen]byte
	ctx, log, sessionKey = bindRequestLogger(ctx, env, facts.APIKeyID, facts.RequestID, "openai_chat_completions")
	if err := env.StripCommandOutcomes(); err != nil {
		return fmt.Errorf("strip command outcomes: %w", err)
	}
	if removed, err := env.StripHistoricalCommands(); err != nil {
		return fmt.Errorf("strip historical router commands: %w", err)
	} else if removed > 0 {
		log.Info("Stripped historical router commands from OpenAI history", "removed_messages", removed)
	}
	if removed := env.StripRouterFeedbackArtifacts(); removed > 0 {
		log.Info("Stripped router-feedback artifacts from OpenAI history", "removed_messages", removed)
	}
	if removed := env.StripBetaArtifacts(); removed > 0 {
		ctx = withBetaArtifactHistory(ctx)
		log.Info("Stripped beta artifacts from OpenAI history", "removed_messages", removed)
	}
	codexTitleGen, _ := ctx.Value(codexTitleGenerationContextKey{}).(bool)
	feats := env.RoutingFeatures()
	ctx = withRequestImageCount(ctx, feats.ImageCount)
	if codexTitleGen {
		feats.TitleGenHint = true
	}
	promptText := feats.PromptText
	embedInput := "concatenated_stream"
	if feats.OnlyUserMessageText != "" {
		promptText = feats.OnlyUserMessageText
		embedInput = "only_user_message"
	}

	log.Info("ProxyOpenAIChatCompletion start",
		"requested_model", feats.Model,
		"stream", env.Stream(),
		"message_count", feats.MessageCount,
		"has_tools", feats.HasTools,
		"total_input_tokens", feats.Tokens,
		"prompt_preview", s.zdrLogField(ctx, observability.Preview(promptText, 200)),
	)

	betaSessionKey := deriveBetaSessionKeyForRequest(ctx, env, facts.APIKeyID, sessionKey)
	if cmd, hasCmd := env.ExtractBetaCommand(); hasCmd {
		log.Info("ProxyOpenAIChatCompletion beta command")
		return s.handleBetaCommand(ctx, w, env, cmd, facts.InstallationID, sessionKey, betaSessionKey, feats.Tokens)
	}
	ctx, err = s.applySessionStrategy(ctx, facts.InstallationID, betaSessionKey)
	if err != nil {
		return err
	}
	*r = *r.WithContext(ctx)
	strategy, forceModelSessionKey := router.StrategyFromContext(ctx), deriveForceModelSessionKeyForRequest(ctx, env, facts.APIKeyID, sessionKey)

	agentForceModel := ""
	requestBodyChanged := false
	if s.pinStore != nil {
		if cmd, hasCmd := env.ExtractForceModelCommand(); hasCmd {
			if routingEnv := routingEnvelopeForRequest(ctx, env); routingEnv != env {
				routingEnv.ExtractForceModelCommand()
			}
			log.Info("ProxyOpenAIChatCompletion force-model command", "force_model_cmd", cmd)
			if agentForceModelContinuesTurn(ctx, cmd) {
				var err error
				agentForceModel, _, err = s.applyForceModelCommand(ctx, strategy, cmd, facts.InstallationID, sessionKey, forceModelSessionKey)
				if err != nil {
					return err
				}
				requestBodyChanged = true
			} else {
				if err := s.handleForceModelCommand(ctx, w, env, cmd, sessionTarget{strategy: strategy, installationID: facts.InstallationID, sessionKey: sessionKey}, forceModelSessionKey, feats.Tokens); err != nil {
					return err
				}
				s.grantPostCommandContinuation(ctx, strategy, facts.InstallationID, sessionKey, roleForTier(catalog.TierFor(feats.Model)))
				return nil
			}
		}
	}
	if cmd, hasCmd := env.ExtractRouterFeedbackCommand(); hasCmd {
		if routingEnv := routingEnvelopeForRequest(ctx, env); routingEnv != env {
			routingEnv.ExtractRouterFeedbackCommand()
		}
		log.Info("ProxyOpenAIChatCompletion router-feedback command")
		skillFeedback := codexSkillTurnFrom(ctx) == translate.CodexSkillTurnFeedback
		synthetic := !cmd.FromToolResult || skillFeedback
		if err := s.handleRouterFeedbackCommand(ctx, w, env, cmd, sessionTarget{strategy: strategy, installationID: facts.InstallationID, sessionKey: sessionKey}, feats.Tokens, synthetic); err != nil {
			return err
		}
		if synthetic {
			s.grantPostCommandContinuation(ctx, strategy, facts.InstallationID, sessionKey, roleForTier(catalog.TierFor(feats.Model)))
			return nil
		}
		requestBodyChanged = true
	}
	if env.ExtractRouterSessionCommand() {
		log.Info("ProxyOpenAIChatCompletion router-session command")
		if err := s.handleRouterSessionCommand(ctx, w, env, feats.Tokens); err != nil {
			return err
		}
		s.grantPostCommandContinuation(ctx, strategy, facts.InstallationID, sessionKey, roleForTier(catalog.TierFor(feats.Model)))
		return nil
	}
	if env.ExtractRouterModelsCommand() {
		log.Info("ProxyOpenAIChatCompletion router-models command")
		if err := s.handleRouterModelsCommand(ctx, w, env, feats.Tokens); err != nil {
			return err
		}
		s.grantPostCommandContinuation(ctx, strategy, facts.InstallationID, sessionKey, roleForTier(catalog.TierFor(feats.Model)))
		return nil
	}
	if env.ExtractListModelsCommand() {
		log.Info("ProxyOpenAIChatCompletion list-models command")
		if err := s.handleListModelsCommand(ctx, w, env, feats.Tokens); err != nil {
			return err
		}
		s.grantPostCommandContinuation(ctx, strategy, facts.InstallationID, sessionKey, roleForTier(catalog.TierFor(feats.Model)))
		return nil
	}

	if coalesced := env.CoalesceDuplicateToolResults(); coalesced > 0 {
		log.Info("Coalesced duplicate tool results before dispatch", "coalesced", coalesced)
		requestBodyChanged = true
	}
	if sanitized := env.SanitizeOrphanedToolCalls(); sanitized > 0 {
		log.Info("Sanitized orphaned tool calls before dispatch", "sanitized", sanitized)
		requestBodyChanged = true
	}
	if routingEnv := routingEnvelopeForRequest(ctx, env); routingEnv != env {
		routingEnv.CoalesceDuplicateToolResults()
		routingEnv.SanitizeOrphanedToolCalls()
	}
	if requestBodyChanged {
		feats = env.RoutingFeatures()
		ctx = withRequestImageCount(ctx, feats.ImageCount)
		if codexTitleGen {
			feats.TitleGenHint = true
		}
		promptText = feats.PromptText
		embedInput = "concatenated_stream"
		if feats.OnlyUserMessageText != "" {
			promptText = feats.OnlyUserMessageText
			embedInput = "only_user_message"
		}
	}

	forceModel := agentForceModel
	ctx, headerForceModel, forceErr := s.applyForceModelHeader(ctx, r, facts.InstallationID, forceModelSessionKey)
	if forceErr != nil {
		return forceErr
	}
	if headerForceModel != "" {
		forceModel = headerForceModel
	}
	if forceModel == "" {
		forceModel = codexSelectedModel(ctx)
	}
	forceCluster, taskArm, forceErr := forceClusterForTurn(s.strategyPorts, ctx, turnRequest{Strategy: strategy, InstallationID: facts.InstallationID}, r, env, feats, forceModel)
	if forceErr != nil {
		return forceErr
	}

	if s.effectiveCaptureMode(ctx) != CaptureOff {
		logInboundRequestDiagnostics(log, env)
	}

	subAgentHint := r.Header.Get("x-weave-subagent-type")

	eligibility, eligibilityErr := requestEligibility(ctx, s, providers.ProviderOpenAI, r.Header)
	if eligibilityErr != nil {
		return eligibilityErr
	}
	enabledProviders := eligibility.EnabledProviders

	responsesBody, _ := ctx.Value(codexResponsesBodyContextKey{}).([]byte)
	reasoningConfigurationHash := env.ReasoningConfigurationSHA256()
	if nativeResponsesHash, ok := ctx.Value(nativeResponsesReasoningHashContextKey{}).(string); ok {
		reasoningConfigurationHash = nativeResponsesHash
	}
	toolConfigurationHash := env.ToolConfigurationSHA256()
	if nativeResponsesHash, ok := ctx.Value(nativeResponsesToolHashContextKey{}).(string); ok {
		toolConfigurationHash = nativeResponsesHash
	}

	outputReserveOAI := contextWindowOutputReserve
	if feats.MaxTokens > outputReserveOAI {
		outputReserveOAI = feats.MaxTokens
	}
	baseExcludedOAI := s.excludeCodexOAuthOnlyModels(ctx, r.Header, enabledProviders, s.excludedModelsForRequest(ctx))

	inboundLastUser := env.LastUserMessage()
	inboundUserPromptOpenAI := routingEndsWithUserPrompt(ctx, env)

	prefiltered := candidates.Prefilter(candidates.PrefilterRequest{
		Envelope: env, ImageCount: feats.ImageCount, OutputReserve: outputReserveOAI, Eligibility: eligibility,
		Excluded: baseExcludedOAI, Available: s.availableModels,
		AdvisorExclusions: advisorExclusions(env, s.routableUniverse()),
	})
	logPrefilter(log, env, feats.ImageCount, outputReserveOAI, prefiltered)
	excludedOAI, overflowEstimateOAI := prefiltered.Excluded, prefiltered.OverflowEstimate
	feats, routingPromptText, routingMessages := routingTurnInputForRequest(ctx, env, feats, promptText)
	routeRequest := router.Request{
		RequestedModel:               feats.Model,
		ForceModel:                   forceModel,
		ForceCluster:                 forceCluster,
		EstimatedInputTokens:         feats.Tokens,
		HasTools:                     feats.HasTools,
		HasImages:                    feats.HasImages,
		ImageCount:                   feats.ImageCount,
		TranslationRequirements:      env.TranslationRequirements(router.EndpointOpenAIChat),
		ReasoningConfigurationSHA256: reasoningConfigurationHash,
		ToolConfigurationSHA256:      toolConfigurationHash,
		PromptText:                   routingPromptText,
		ConversationMessages:         routingMessages,
		AvailableTools:               availableToolsForRouting(env),
		Tools:                        toolsForRouting(env),
		FeedbackKey:                  hex.EncodeToString(sessionKey[:]),
		FeedbackRole:                 roleForTier(catalog.TierFor(feats.Model)),
		ClientSessionID:              clientSessionIDForRequest(ctx, env),
		EnabledProviders:             enabledProviders,
		CustomBindings:               eligibility.CustomBindings,
		GatewayProviders:             s.gatewayProvidersForRequest(ctx),
		SubscriptionProviders:        s.selectableSubscriptionProviders(ctx, r.Header),
		Candidates:                   prefilteredCandidates(ctx, excludedOAI, prefiltered),
		PreferredModels:              s.preferredModelsForRequest(ctx),
		RoutingKnobs:                 routingKnobsForRequest(ctx),
		ClusterArmOverrides:          clusterArmOverridesForRequest(ctx),
		ProductEligibility:           entitlement.ModelBoundaryFromContext(ctx),
	}
	routeStart := s.clock.Now()
	routeCtx, routeSpan := startRoutingSpan(ctx, strategy, routeRequest)
	routeRes, err := taskArm.route(routeCtx, s.runTurnLoop)(env, feats, turnRequest{Strategy: strategy, APIKeyID: facts.APIKeyID, InstallationID: facts.InstallationID}, subAgentHint, r.Header, routeRequest)
	defer routeRes.releaseCooldownProbes()
	var escalationCapture *captureWriter
	defer func() {
		s.completeEscalation(ctx, routeRes, returnErr, escalationCapture, translate.EscalationResponseChat)
	}()
	finishRoutingSpan(routeSpan, routeRes.Decision, err)
	routeMs := s.clock.Now().Sub(routeStart).Milliseconds()
	if err != nil {
		log.Error("Routing failed for OpenAI request", "err", err, "route_ms", routeMs, "requested_model", feats.Model, "total_input_tokens", feats.Tokens)
		s.recordPolicyPinRouteFailure(ctx, routeFailureRequest{RequestID: facts.RequestID, RequestStart: requestStart, RequestedModel: feats.Model, Strategy: strategy}, routeRes.TurnType, inboundUserPromptOpenAI, err)
		return err
	}
	ctx = requestcontext.WithCallerModelPassthrough(ctx, routeRes.CallerModelPassthrough)
	ctx = withPickedModelFunding(ctx, routeRes.CallerModelPassthrough, routeRes.Decision.Provider)
	ctx = withCodexPickFunding(ctx, routeRes.Decision.Model, routeRes.Decision.Provider)
	ctx = s.withTurnOverloadProbe(ctx, routeRes)
	if len(routeRes.SessionDisabledProviders) > 0 {
		ctx = context.WithValue(ctx, SessionDisabledProvidersContextKey{}, routeRes.SessionDisabledProviders)
	}
	if len(routeRes.ObservedOverflowModels) > 0 {
		ctx = context.WithValue(ctx, ObservedOverflowModelsContextKey{}, routeRes.ObservedOverflowModels)
	}
	if len(routeRes.SessionDemotedModels) > 0 {
		ctx = context.WithValue(ctx, SessionDemotedModelsContextKey{}, routeRes.SessionDemotedModels)
		if len(routeRes.SessionCooldownModels) > 0 {
			ctx = context.WithValue(ctx, SessionCooldownModelsContextKey{}, routeRes.SessionCooldownModels)
		}
		if len(routeRes.SessionStrikeReadmitModels) > 0 {
			ctx = context.WithValue(ctx, SessionStrikeReadmitModelsContextKey{}, routeRes.SessionStrikeReadmitModels)
		}
	}
	if len(routeRes.CooldownProbeDeniedModels) > 0 {
		ctx = context.WithValue(ctx, SessionCooldownProbeDeniedModelsContextKey{}, routeRes.CooldownProbeDeniedModels)
	}
	routeRes.SuggestionMode = r.Header.Get("x-weave-suggestion-mode") == "true"
	decision := routeRes.Decision
	s.firePolicyShadowForServingDecision(ctx, strategy, decision, routeRequest)
	tt := routeRes.TurnType
	stickyHit := routeRes.StickyHit
	pinTier := routeRes.PinTier
	pinAgeSec := routeRes.PinAgeSec
	s.logPlannerOutcome(ctx, routeRes)

	if facts.Client.ClientApp == ClientAppOpencode && !routeRes.CallerModelPassthrough && !routeRes.AuthoritativePerTurn && s.ResolveTextRepetitionBreakEnabled(ctx) && (tt == turntype.MainLoop || tt == turntype.ToolResult) {
		if looped, count, sampleHash := conversation.DetectTextRepetition(env); looped {
			role := roleForTier(catalog.TierFor(feats.Model))
			return s.handleTextRepetitionBreak(ctx, w, env, count, sampleHash, sessionTarget{strategy: strategy, installationID: facts.InstallationID, sessionKey: sessionKey}, role, decision.Model, decision.Provider, feats.Tokens)
		}
	}

	credentialCtx := resolveAndInjectCredentials(ctx, decision.Provider, decision.Model, r.Header)

	if _, err := s.provider(decision.Provider); err != nil {
		return err
	}

	w.Header().Set(HeaderRouterDecision, decision.Reason)
	w.Header().Set(HeaderRouterProvider, decision.Provider)
	w.Header().Set(HeaderRouterModel, decision.Model)
	w.Header().Set(HeaderRouterContextWindow, strconv.Itoa(contextWindowForRequest(decision.Model, decision.Provider)))
	setContextEstimateHeaders(w.Header(), env.ContextOverflowTokenEstimate(), outputReserveOAI)
	s.setFeedbackLinkHeader(ctx, w, facts.InstallationID, facts.ExternalID, facts.RequestID, auth.UserIDFrom(ctx))

	reqPricing, _ := catalog.PrimaryPriceFor(s.baselineFor(ctx, feats.Model))
	actPricing, _ := catalog.PrimaryPriceFor(decision.Model)
	reqDecisionPricing := reqPricing.ForInputTokens(feats.Tokens)
	actDecisionPricing := actPricing.ForInputTokens(feats.Tokens)
	openaiDecisionBuilder := telemetry.NewAttrBuilder(46).
		String("request_id", facts.RequestID).
		String("external_id", facts.ExternalID).
		String("router_user_id", auth.UserIDFrom(ctx)).
		String("client.device_id", facts.Client.DeviceID).
		String("client.account_id", facts.Client.AccountID).
		String("client.session_id", facts.Client.SessionID).
		String("client.user_agent", facts.Client.UserAgent).
		String("client.app", facts.Client.TelemetryClientApp()).
		String("rollout_id", policyRolloutIDFromContext(ctx)).
		String("requested.model", feats.Model).
		String("decision.model", decision.Model).
		String("decision.provider", decision.Provider).
		String("decision.reason", decision.Reason).
		Bool("routing.policy_fallback", routeRes.PolicyFallback).
		Bool("routing.sticky_hit", stickyHit).
		Bool("routing.session_pin_hit", pinTier == "in_proc" || pinTier == "postgres").
		String("routing.session_pin_tier", pinTier).
		Int64("routing.session_pin_age_s", pinAgeSec).
		String("routing.turn_type", string(tt)).
		String("routing.embed_input", embedInput).
		Int64("routing.estimated_input_tokens", int64(feats.Tokens)).
		Float64("catalog.requested_input_per_1m", reqDecisionPricing.InputUSDPer1M).
		Float64("catalog.requested_output_per_1m", reqDecisionPricing.OutputUSDPer1M).
		Float64("catalog.actual_input_per_1m", actDecisionPricing.InputUSDPer1M).
		Float64("catalog.actual_output_per_1m", actDecisionPricing.OutputUSDPer1M).
		Int64("latency.route_ms", routeMs)
	applySubscriptionDecisionSpanTelemetry(ctx, openaiDecisionBuilder, feats.Model, decision.Model)
	applyServingSpanAttrs(ctx, openaiDecisionBuilder)
	applySidecarAttrs(openaiDecisionBuilder, routeRes)
	applyPlannerAttrs(openaiDecisionBuilder, routeRes)
	applyRoutingStateAttrs(openaiDecisionBuilder, routeRes, decision.ServedIdentity(), sessionKey)
	applyCallerRoutingAttrs(ctx, openaiDecisionBuilder)
	applyExperimentAttrs(ctx, openaiDecisionBuilder, &routeRes, taskArm, feats.Model, routeRes.Decision.Model)
	telemetry.Record(ctx, telemetry.Span{
		Name:  "router.decision",
		Start: requestStart,
		End:   s.clock.Now(),
		Attrs: openaiDecisionBuilder.Build(),
	})
	telemetry.Flush(ctx)

	opts := translate.EmitOptions{
		TargetModel:              decision.Model,
		TargetProvider:           decision.Provider,
		Capabilities:             router.Lookup(decision.Model),
		IncludeStreamUsage:       s.usageRequired(),
		SessionAffinity:          sessionAffinityHint(routeRes.SessionKey),
		EnableServerSideFallback: s.ResolveAnthropicServerSideFallback(ctx),
		ModelSwitched:            routeRes.modelSwitched(),
		EnableExtendedContext:    shouldEnableExtendedContext(env.FullTokenEstimate(), outputReserveOAI),
	}
	effortServed := s.resolveEffort(ctx, decision, opts.Capabilities, routeRes.EscalateEffort)
	effortServed.apply(&opts)

	if s.codexSubscriptionExhausted(ctx, r.Header) {
		nudge := codexSubscriptionReconnectNudge(ctx, r.Header, s.usageObserver)
		ctx = withSuppressedCodexSubscription(ctx)
		if codexSeatsCoverOrThrottled(ctx, s.linkedSeatCanCover(ctx, decision.Provider, decision.Model), s.clock.Now()) {
			ctx = withDeferredReconnectNudge(ctx, nudge)
		} else {
			ctx = withSubscriptionReconnectNudge(ctx, nudge)
		}
	}
	resolvedCtx := s.resolveCredentials(ctx, decision.Provider, decision.Model, r.Header)
	responsesEndpointKey := EffectiveBaseURL(resolvedCtx, decision.Provider)
	openAIResponsesEndpoint := len(responsesBody) > 0
	if !openAIResponsesEndpoint && decision.Provider == providers.ProviderOpenAI {
		openAIResponsesEndpoint = translate.UseOpenAIResponsesAPI(translate.ResponsesRoute{
			Provider:       decision.Provider,
			Capabilities:   opts.Capabilities,
			HasTools:       feats.HasTools,
			ChatOnlyParams: env.RequiresChatCompletionsParams(opts.Capabilities),
			Broad:          s.ResolveOpenAIResponsesBroad(ctx),
		}) && !s.gatewayCaps.LacksResponses(responsesEndpointKey)
		if !env.RequiresChatCompletionsParams(opts.Capabilities) && !s.gatewayCaps.LacksResponses(responsesEndpointKey) && s.supportsSubscriptionTransport(decision.Provider) &&
			(servedOnCodexSubscription(resolvedCtx) || managedSubscriptionCanServe(ctx, decision.Provider, decision.Model)) {
			openAIResponsesEndpoint = true
		}
	}
	endpointCtx, endpointErr := s.avoidCodexOnChatEndpoint(ctx, decision.Provider, decision.Model, openAIResponsesEndpoint, r.Header)
	if endpointErr != nil {
		return endpointErr
	}
	if codexChatEndpoint(endpointCtx) {
		ctx = s.resolveCredentials(endpointCtx, decision.Provider, decision.Model, r.Header)
	} else {
		ctx = resolvedCtx
	}
	opts.FastMode = fastModeForAttempt(ctx, decision.Model, decision.Provider)
	fastServed := false

	bindings := s.resolveBindingsForDispatch(ctx, decision)

	if billing.SubscriptionOnlyFromContext(ctx) && !subscriptionStateModelsEnabled(ctx) {
		if subscriptionOnlyUnservable(ctx, decision.Provider, decision.Model) {
			released, ok := releaseUnservableLinkedFirst(ctx, decision)
			if !ok {
				log.Info("Subscription-only request cannot be served on the subscription; refusing",
					"requested_model", feats.Model, "external_id", facts.ExternalID, "decision_provider", decision.Provider)
				return subscriptionOnlyUnavailable(ctx)
			}
			ctx = released
		} else {
			bindings = []catalog.ProviderBinding{{Provider: decision.Provider}}
		}
	}
	clientSink := w
	if _, isResponses := w.(*translate.ResponsesWriter); env.Stream() && !isResponses {
		if footer := s.feedbackFooter(ctx, ClientIdentityFrom(ctx).ClientApp, routeRes.TurnType, footerEchoed); footer != "" {
			clientSink = translate.NewOpenAIRoutingFooterWriter(w, footer)
		}
	}
	clientSink, escalationCapture = s.captureEscalationResponse(clientSink, routeRes)
	contentSink, contentCap := s.maybeCaptureResponse(ctx, clientSink)

	announcementTurn := announcementTurnFor(routeRes, replayedModel)
	if announcedModel, ok := ctx.Value(responsesLastAnnouncedModelContextKey{}).(string); ok {
		announcementTurn.PriorServedModel = announcedModel
	}
	marker := suppressMarkerIfRequested(ctx, r.Header, modelSelectionMarkerForRequest(s.strategyPorts, ctx, announcementTurn, routingMarkerFor(announcementTurn), decision.Model, ""))
	muteSlowStartFor(ctx, routeRes)
	fundingCtx := ctx
	if CredentialsFromContext(fundingCtx) == nil {
		fundingCtx = credentialCtx
	}
	notice := routerNoticeForDecision(ctx, r.Header, decision, marker)
	notice = addRoutingFundingSource(fundingCtx, seatAdmissionOf(s), decision.Provider, decision.Model, notice)
	marker = notice.Text

	promotedToResponses := false
	if len(responsesBody) == 0 && !routeRes.Handover.Invoked && openAIResponsesEndpoint {
		if native, ok := ctx.Value(nativeResponsesBodyContextKey{}).([]byte); ok && len(native) > 0 {
			responsesBody = native
			promotedToResponses = true
		}
	}

	responsesWriter, isResponses := w.(*translate.ResponsesWriter)
	responsesNotices := &requestNoticeStream{now: s.clock.Now}
	responseSwitch := responseProtocolSwitch{
		notices:             responsesNotices,
		writer:              responsesWriter,
		nativeRequest:       len(responsesBody) > 0,
		translatedResponses: !isResponses && len(responsesBody) == 0 && openAIResponsesEndpoint,
		promotedToResponses: promotedToResponses,
	}
	initialNativeResponses := responseSwitch.nativeRequest && decision.Provider == providers.ProviderOpenAI
	if footer := s.feedbackFooter(ctx, facts.Client.ClientApp, routeRes.TurnType, footerEchoed); isResponses && footer != "" {
		responsesWriter.SetFooterText(footer)
	}
	responsesPreludeWillEmit := env.Stream() && !initialNativeResponses && (len(bindings) <= 1 || marker != "")
	if initialNativeResponses {
		responsesPreludeWillEmit = env.Stream() && supportsResponsesTerminalSurfaces(facts.Client.ClientApp) && marker != ""
	}

	var responsesPreludeBuf *preludeBuffer
	if rw, ok := contentSink.(*translate.ResponsesWriter); ok {
		var noticeErr error
		rw.WrapInner(func(inner http.ResponseWriter) http.ResponseWriter {
			responsesPreludeBuf = newPreludeBuffer(inner)
			preludeNotice := notice
			if initialNativeResponses && !supportsResponsesTerminalSurfaces(facts.Client.ClientApp) {
				preludeNotice.Text = ""
			}
			noticeSink, prepareErr := responsesNotices.prepare(ctx, responsesPreludeBuf, responsesPreludeBuf, translate.FormatOpenAIResponses, env.Stream(), preludeNotice)
			noticeErr = prepareErr
			if prepareErr != nil {
				return responsesPreludeBuf
			}
			return noticeSink
		})
		if noticeErr != nil {
			return fmt.Errorf("emit Responses router notice: %w", noticeErr)
		}
	}
	var preludeBuf *preludeBuffer
	var rootSink http.ResponseWriter
	if responsesPreludeBuf != nil {
		preludeBuf = responsesPreludeBuf
		rootSink = contentSink
	} else {
		preludeBuf = newPreludeBuffer(contentSink)
		rootSink = preludeBuf
	}

	responseSwitch.buffer = preludeBuf
	if isResponses {
		if initialNativeResponses {
			responseSwitch.native(supportsResponsesTerminalSurfaces(facts.Client.ClientApp) &&
				(marker != "" || s.feedbackFooter(ctx, facts.Client.ClientApp, routeRes.TurnType, footerEchoed) != ""))
		}
		if responsesPreludeWillEmit {
			if err := responsesWriter.Prelude(true); err != nil {
				log.Error("Responses prelude failed", "err", err)
			} else {
				responsesWriter.Flush()
				if err := responsesPreludeBuf.CommitPrelude(); err != nil {
					return fmt.Errorf("commit Responses prelude: %w", err)
				}
			}
		}
	}

	var sink = rootSink
	openAIPrelude := &requestNoticeStream{now: s.clock.Now}

	makeMarkerSink := func(notice translate.RouterNotice) (http.ResponseWriter, error) {
		if isResponses || responseSwitch.passthrough() {
			if isResponses && responsesPreludeBuf != nil && notice.Kind == translate.NoticeSubscriptionReconnect && (!responseSwitch.passthrough() || supportsResponsesTerminalSurfaces(facts.Client.ClientApp)) {
				return sink, responsesNotices.deliver(notice)
			}
			return sink, nil
		}
		noticeSink, noticeErr := openAIPrelude.prepare(ctx, sink, preludeBuf, translate.FormatOpenAI, env.Stream(), notice)
		return noticeSink, noticeErr
	}

	toolValidator := env.ToolValidator()

	proxyStart := s.clock.Now()
	inferenceParentCtx := ctx
	ctx, inferenceSpan := startInferenceSpan(ctx, decision, clientSessionIDForRequest(ctx, env))
	defer inferenceSpan.End()
	var proxyErr error
	crossFormat := false
	var extractor *telemetry.UsageExtractor

	var respSummary translate.ResponseSummary
	nativeRespSummary := false
	cyberRetryArmed := false
	preOutputRescueArmed := false
	cyberRefusalSeen := false
	buildProviderAttempt := func(target router.Decision, targetOpts translate.EmitOptions, targetMarker string, fundingCtx context.Context) (dispatchAttempt, error) {
		targetNotice := routerNoticeForDecision(withOwnCodexRejectionNudge(ctx, r.Header, s.usageObserver), r.Header, target, targetMarker)
		targetNotice = addRoutingFundingSource(fundingCtx, seatAdmissionOf(s), target.Provider, target.Model, targetNotice)
		if isResponses {
			if targetMarker != "" && (!responseSwitch.passthrough() || supportsResponsesTerminalSurfaces(facts.Client.ClientApp)) {
				_, noticeErr := responsesNotices.prepare(ctx, responsesPreludeBuf, responsesPreludeBuf, translate.FormatOpenAIResponses, env.Stream(), targetNotice)
				if noticeErr != nil {
					return nil, fmt.Errorf("emit Responses router notice: %w", noticeErr)
				}
			}
		}
		switch providers.FamilyFor(target.Provider) {
		case providers.FamilyOpenAICompat:
			dispatchOpenAI := func(actx context.Context, d router.Decision, p providers.Client, surface openAISurface, stripPromptCacheKey bool) error {
				resetResponsesAttempt(w)
				var prep providers.PreparedRequest
				switch surface {
				case surfaceResponsesNative:
					outBody, setErr := sjson.SetBytes(responsesBody, "model", d.Model)
					if setErr != nil {
						log.Error("Failed to set routed model on Codex Responses body", "err", setErr, "decision_model", d.Model)
						return fmt.Errorf("set codex model: %w", setErr)
					}
					outBody, setErr = translate.ClampResponsesInputCallIDs(outBody)
					if setErr != nil {
						return fmt.Errorf("clamp codex call_id: %w", setErr)
					}
					nativeOpts := targetOpts
					nativeOpts.TargetProvider = d.Provider
					nativeOpts.FastMode = fastModeForAttempt(actx, d.Model, d.Provider)
					fastServed = nativeOpts.FastMode
					outBody, setErr = codexNativeFastMode(outBody, nativeOpts, routeRes.TurnType)
					if setErr != nil {
						return fmt.Errorf("set codex service_tier: %w", setErr)
					}
					outBody, setErr = codexNativeEffort(outBody, nativeOpts, routeRes.TurnType)
					if setErr != nil {
						return fmt.Errorf("set codex reasoning effort: %w", setErr)
					}
					prep = providers.PreparedRequest{
						Body:     outBody,
						Endpoint: providers.EndpointResponses,
						Headers:  make(http.Header),
						Stats: providers.RequestMutationStats{
							Transformations: responseTransformationsFromContext(actx),
						},
					}
				default:
					attemptOpts := targetOpts
					attemptOpts.TargetProvider = d.Provider
					attemptOpts.StripPromptCacheKey = stripPromptCacheKey
					attemptOpts.FastMode = fastModeForAttempt(actx, d.Model, d.Provider)
					attemptOpts.ReasoningReplayScope = s.reasoningReplayScope(actx, d)
					fastServed = attemptOpts.FastMode
					var emitErr error
					if surface == surfaceResponsesTranslated {
						prep, emitErr = env.PrepareOpenAIResponses(r.Header, attemptOpts)
					} else {
						prep, emitErr = prepareOpenAIForTurn(env, r.Header, attemptOpts, routeRes.TurnType)
					}
					if emitErr != nil {
						log.Error("Failed to emit OpenAI body", "err", emitErr,
							"decision_provider", d.Provider, "endpoint", prep.Endpoint)
						return fmt.Errorf("emit body: %w", emitErr)
					}
				}
				noticeSink, noticeErr := makeMarkerSink(reconnectNoticeForAttempt(actx, r.Header, targetNotice))
				if noticeErr != nil {
					return fmt.Errorf("emit OpenAI router notice: %w", noticeErr)
				}
				refusalGate := newCyberRefusalGate(noticeSink,
					cyberRetryArmed && d.Provider == providers.ProviderOpenAI)
				if surface == surfaceResponsesNative && env.Stream() && preOutputRescueArmed {
					refusalGate.holdPreOutputFailure()
				}
				var attemptSink http.ResponseWriter = refusalGate
				var seedToolCalls *translate.SeedToolCallChatWriter
				if surface == surfaceChat {
					if names := env.SeedToolCallNames(prep.ResponseToolNames); len(names) > 0 {
						seedToolCalls = translate.NewSeedToolCallChatWriter(attemptSink, names)
						attemptSink = seedToolCalls
					}
				}
				proxyWriter := attemptSink
				var translator *translate.ResponsesToOpenAIChatWriter
				var toolNames *translate.ToolNameWriter
				var nativeTerminal *ingress.ResponsesTerminalObserver
				switch {
				case surface == surfaceResponsesTranslated:
					var usage translate.UsageSink
					if s.usageRequired() {
						extractor = telemetry.NewUsageExtractor(d.Provider)
						usage = extractor
					}
					translator = translate.NewResponsesToOpenAIChatWriter(attemptSink, d.Model, usage).
						WithLogger(log).
						WithToolValidator(toolValidator)
					if providers.OutputExcludesReasoning(d.Provider) {
						translator.SetUsageOutputBasis(providerusage.OutputExcludesReasoning)
					}
					if isResponses {
						responsesWriter.SetUsageSource(translator.UsageFacts)
					}
					if err := translator.Prelude(env.Stream()); err != nil {
						log.Error("chat/completions prelude failed (Responses upstream)", "err", err)
					}
					toolNames = translate.NewOpenAIToolNameWriter(translator, prep.ResponseToolNames)
					proxyWriter = toolNames
				case surface == surfaceResponsesNative:
					nativeTerminal = ingress.NewResponsesTerminalObserver(attemptSink)
					proxyWriter = nativeTerminal
					if s.usageRequired() {
						extractor, proxyWriter = s.newUsageExtractor(nativeTerminal, d.Provider)
					}
				case s.usageRequired() || isResponses:
					extractor, proxyWriter = s.newUsageExtractor(attemptSink, d.Provider)
				}
				if surface == surfaceChat && isResponses {
					responsesWriter.SetUsageSource(extractor.UsageFacts)
				}
				var chatTerminal *streamTerminalObserver
				if surface == surfaceChat && !isResponses && env.Stream() {
					chatTerminal = newStreamTerminalObserver(proxyWriter, nativeStreamOpenAIChat)
					proxyWriter = chatTerminal
				}
				if preludeBuf != nil {
					preludeBuf.Seal()
				}
				err := p.Proxy(actx, d, prep, proxyWriter, r)
				if nativeTerminal != nil && nativeTerminal.Observed() && errors.Is(err, context.Canceled) {
					err = nil
				}
				if seedToolCalls != nil {
					err = finalizeAfterProxy(err, seedToolCalls.Finalize)
					if n := seedToolCalls.ConvertedToolCalls(); n > 0 {
						log.Warn("Recovered tool calls from Seed text markup",
							"model", d.Model,
							"provider", d.Provider,
							"tool_calls", n)
					}
				}
				if err == nil && chatTerminal != nil {
					err = chatTerminal.streamErr()
				}
				if err != nil && surface != surfaceResponsesNative && env.Stream() && preludeBuf.Committed() {
					_ = emitOpenAISSEErrorEvent(sink, attributeStreamError(actx, d.Provider, err))
				}
				if translator != nil {
					err = finalizeAfterProxy(finalizeAfterProxy(err, toolNames.Finalize), translator.Finalize)
					if err == nil {
						err = translator.UpstreamError()
					}
					respSummary = translator.Summary()
					nativeRespSummary = false
				} else if nativeTerminal != nil {
					nativeTerminal.Finalize()
					if rw, ok := w.(*translate.ResponsesWriter); ok {
						err = finalizeAfterProxy(err, rw.Finalize)
					}
					if err == nil && env.Stream() {
						err = nativeTerminal.Err()
					}
					if isStreamTerminalMissing(err) {
						refusalGate.Abandon()
					}
					if nativeTerminal.Observed() && nativeTerminal.Signals().OutputLimitReached {
						extractor.RecordOutputLimitReached()
					}
					respSummary = translate.ResponseSummary{UpstreamFinishReason: nativeTerminal.Signals().FinishReason}
					nativeRespSummary = false
					if nativeTerminal.Observed() && s.ResolveNativeOpenAIResponseSignals(actx) {
						respSummary.StopReason = nativeTerminal.Signals().FinishReason
						respSummary.ToolUseBlocks = nativeTerminal.Signals().ToolCalls
						nativeRespSummary = true
					}
				}
				if releaseErr := refusalGate.finalizeAttempt(err); releaseErr != nil && (err == nil || providers.IsUpstreamCyberPolicyRefusal(releaseErr)) {
					err = releaseErr
				}
				if err != nil && refusalGate.holdFailure && refusalGate.releaseEvent != "" {
					log.Warn("Held Responses stream released before upstream failure",
						"model", d.Model,
						"provider", d.Provider,
						"release_event", refusalGate.releaseEvent,
						"release_item_type", refusalGate.releaseItemType,
						"err", err)
				}
				if refusalGate.refused {
					cyberRefusalSeen = true
				}
				if refusalGate.withheld {
					err = providers.CyberPolicyRefusalError()
				}

				return err
			}
			return func(actx context.Context, d router.Decision, p providers.Client) error {
				surface := responseSwitch.surface(d.Provider, servedOnCodexSubscription(actx))
				gatewayKey := gatewayResponsesKey(actx, d.Provider)
				stripPCK := s.gatewayCaps.RejectsPromptCacheKey(gatewayKey)
				err := dispatchOpenAI(actx, d, p, surface, stripPCK)
				if err != nil && !stripPCK && gatewayKey != "" && !committed(preludeBuf) &&
					providers.IsUpstreamPromptCacheKeyRejection(err) {
					s.gatewayCaps.RememberRejectsPromptCacheKey(gatewayKey)
					stripPCK = true
					log.Warn("Gateway rejected prompt_cache_key; retrying without the affinity hint",
						"model", d.Model,
						"decision_provider", d.Provider,
						"request_id", facts.RequestID)
					if preludeBuf != nil {
						preludeBuf.Discard()
					}
					err = dispatchOpenAI(actx, d, p, surface, true)
				}
				if err == nil || surface == surfaceChat ||
					committed(preludeBuf) || !providers.IsUpstreamResponsesUnsupported(err) ||
					codexSubscriptionModelRejected(err) {
					return err
				}
				if responseSwitch.fallbackToChat(surface) != nil {
					return err
				}
				s.gatewayCaps.RememberLacksResponses(responsesEndpointKey)
				log.Warn("OpenAI endpoint rejected the Responses API; retrying on chat/completions",
					"model", d.Model,
					"decision_provider", d.Provider,
					"request_id", facts.RequestID)
				if preludeBuf != nil {
					preludeBuf.Discard()
				}
				return dispatchOpenAI(actx, d, p, surfaceChat, stripPCK)
			}, nil
		case providers.FamilyGemini:
			crossFormat = true
			prep, emitErr := env.PrepareGemini(r.Header, targetOpts)
			if emitErr != nil {
				log.Error("Failed to translate OpenAI request to Gemini format", "err", emitErr)
				return nil, fmt.Errorf("translate openai request to gemini: %w", emitErr)
			}
			dispatchGemini := func(actx context.Context, d router.Decision, p providers.Client, pr providers.PreparedRequest) (func(error) error, error) {
				resetResponsesAttempt(w)
				var usage translate.UsageSink
				if s.usageRequired() {
					extractor = telemetry.NewUsageExtractor(d.Provider)
					usage = extractor
				}
				attemptSink, noticeErr := makeMarkerSink(reconnectNoticeForAttempt(actx, r.Header, targetNotice))
				if noticeErr != nil {
					return func(err error) error { return err }, noticeErr
				}
				translator := translate.NewGeminiToOpenAISSETranslator(attemptSink, d.Model, usage)
				if isResponses {
					responsesWriter.SetUsageSource(translator.UsageFacts)
				}
				if preludeBuf != nil {
					preludeBuf.Seal()
				}
				rawErr := p.Proxy(actx, d, pr, translator, r)
				finalize := func(err error) error {
					if err != nil && env.Stream() && preludeBuf.Committed() {
						_ = emitOpenAISSEErrorEvent(sink, attributeStreamError(actx, d.Provider, err))
					}
					return finalizeAfterProxy(err, translator.Finalize)
				}
				return finalize, rawErr
			}
			return func(actx context.Context, d router.Decision, p providers.Client) error {
				retry := geminiToolModeAttempt{log: log, buffer: preludeBuf, env: env, headers: r.Header, opts: targetOpts}
				return retry.dispatch(prep, func(prepared providers.PreparedRequest) (func(error) error, error) {
					return dispatchGemini(actx, d, p, prepared)
				})
			}, nil
		case providers.FamilyAnthropic:
			crossFormat = true
			prep, emitErr := env.PrepareAnthropic(r.Header, targetOpts)
			if emitErr != nil {
				log.Error("Failed to translate OpenAI request to Anthropic format", "err", emitErr)
				return nil, fmt.Errorf("translate openai request: %w", emitErr)
			}
			dispatchAnthropic := func(actx context.Context, d router.Decision, p providers.Client, attemptPrep providers.PreparedRequest, fast bool) (func(error) error, error) {
				resetResponsesAttempt(w)
				fastServed = fast
				var usage translate.UsageSink
				if s.usageRequired() {
					extractor = telemetry.NewUsageExtractor(providers.ProviderAnthropic)
					extractor.SetRequestCacheTTL1h(func() bool { return translate.AnthropicRequestCacheTTL1h(attemptPrep.Body) })
					usage = extractor
				}
				attemptSink, noticeErr := makeMarkerSink(reconnectNoticeForAttempt(actx, r.Header, targetNotice))
				if noticeErr != nil {
					return func(err error) error { return err }, noticeErr
				}
				translator := translate.NewSSETranslator(attemptSink, d.Model, usage)
				if isResponses {
					responsesWriter.SetUsageSource(translator.UsageFacts)
				}
				if preludeBuf != nil {
					preludeBuf.Seal()
				}
				toolNames := translate.NewAnthropicToolNameWriter(translator, attemptPrep.ResponseToolNames)
				rawErr := p.Proxy(actx, d, attemptPrep, toolNames, r)
				finalize := func(err error) error {
					if err != nil && env.Stream() && preludeBuf.Committed() {
						_ = emitOpenAISSEErrorEvent(sink, attributeStreamError(actx, d.Provider, err))
					}
					err = finalizeAfterProxy(finalizeAfterProxy(err, toolNames.Finalize), translator.Finalize)
					respSummary = translator.Summary()
					nativeRespSummary = false
					return err
				}
				return finalize, rawErr
			}
			return func(actx context.Context, d router.Decision, p providers.Client) error {
				attemptOpts := targetOpts
				attemptOpts.TargetProvider = d.Provider
				attemptOpts.FastMode = fastModeForAttempt(actx, d.Model, d.Provider)
				attemptPrep := prep
				if attemptOpts.FastMode != targetOpts.FastMode {
					var emitErr error
					attemptPrep, emitErr = env.PrepareAnthropic(r.Header, attemptOpts)
					if emitErr != nil {
						log.Error("Failed to re-translate OpenAI request to Anthropic format for fast-tier change", "err", emitErr)
						return fmt.Errorf("translate openai request: %w", emitErr)
					}
				}
				finalize, rawErr := dispatchAnthropic(actx, d, p, attemptPrep, attemptOpts.FastMode)
				rawErr, _ = retryAnthropicStandardSpeed(log, preludeBuf, d, attemptOpts, rawErr, func(standardOpts translate.EmitOptions) (func() error, error) {
					standardPrep, emitErr := env.PrepareAnthropic(r.Header, standardOpts)
					if emitErr != nil {
						return nil, emitErr
					}
					return func() error {
						var standardErr error
						finalize, standardErr = dispatchAnthropic(actx, d, p, standardPrep, false)
						return standardErr
					}, nil
				})
				return finalize(rawErr)
			}, nil
		default:
			return nil, fmt.Errorf("%w: %s (no translation path defined)", ErrProviderNotConfigured, target.Provider)
		}
	}

	buildAttempt := responseSwitch.wrapBuilder(buildProviderAttempt)
	attempt, attemptBuildErr := buildAttempt(decision, opts, marker, fundingCtx)
	if attemptBuildErr != nil {
		finishInferenceSpan(inferenceSpan, decision, decision.Provider, -1, attemptBuildErr)
		return attemptBuildErr
	}

	surfacePurpose := inference.PurposeOpenAIChatCompletions
	if isResponsesWriter {
		surfacePurpose = inference.PurposeOpenAIResponses
	}
	rescue := &chatRescue{
		rescueChain: &rescueChain{
			svc: s, strategyPorts: s.strategyPorts, seats: seatAdmissionOf(s), clock: s.clock,
			r:                r,
			log:              log,
			env:              env,
			routeRes:         routeRes,
			req:              &routeRequest,
			opts:             opts,
			contentSink:      contentSink,
			preludeBuf:       preludeBuf,
			attempt:          attempt,
			buildAttempt:     buildAttempt,
			surface:          surfacePurpose,
			overflowEstimate: overflowEstimateOAI,
			outputReserve:    outputReserveOAI,
			respSummary:      &respSummary,
			decision:         decision,
			bindings:         bindings,
			marker:           marker,
			proxyErr:         proxyErr,
			effortServed:     effortServed,
		},
		isResponses:          isResponses,
		responsesPreludeBuf:  responsesPreludeBuf,
		cyberRefusalSeen:     &cyberRefusalSeen,
		cyberRetryArmed:      &cyberRetryArmed,
		preOutputRescueArmed: &preOutputRescueArmed,
	}
	rescue.flushErr = rescue.flushErrAsOpenAI
	rescue.armCyberRetry(ctx, strategy)
	rescue.armSubscriptionRetries(ctx)
	ctx = rescue.armRescues(ctx)
	rescue.holdPrimary()
	rescue.dispatchPrimary(ctx)
	rescue.notePrimaryFailure(ctx)
	rescue.codexRetry(ctx)
	rescue.claudeRetry(ctx)
	rescue.cyberRetry(ctx)
	rescue.siblingRescue(ctx)
	rescue.finish(ctx)
	decision, marker, proxyErr, effortServed = rescue.decision, rescue.marker, rescue.proxyErr, rescue.effortServed
	winnerIdx, finalProvider := rescue.winnerIdx, rescue.finalProvider

	if rescue.codexFailoverUsed {
		ctx = withSuppressedCodexSubscription(ctx)
	} else if rescue.claudeFailoverUsed {
		ctx = withSuppressedClaudeSubscription(ctx)
	}
	ctx = s.resolveCredentials(ctx, finalProvider, decision.Model, r.Header)

	if extractor.Speed() == catalog.SpeedFast {
		fastServed = true
	}
	if actBindingPricing, ok := servedPricing(finalProvider, decision.Model, fastServed); ok {
		actPricing = actBindingPricing
	}

	proxyMs := s.clock.Now().Sub(proxyStart).Milliseconds()
	finishInferenceSpan(inferenceSpan, decision, finalProvider, winnerIdx, proxyErr)
	ctx = restoreParentSpan(ctx, inferenceParentCtx)

	in, out := extractor.Tokens()
	cacheCreation, cacheRead := extractor.CacheTokens()
	usageMods := extractor.UsageModifiers()
	baselineWarmPrefill := routeRes.baselineWarmPrefillTokens(requestStart, cacheCreation, cacheRead, decision.Model, s.baselineFor(ctx, feats.Model), routeRequest.HistoryTruncated)
	requestedInputCost := catalog.CounterfactualInputCost(in, cacheCreation, cacheRead, baselineWarmPrefill, reqPricing, decision.Provider, usageMods)
	if !env.Stream() && proxyErr == nil {
		setRouterCostHeaders(w.Header(), routerResponseCostFromPricing(actPricing, decision.Provider, in, out, cacheCreation, cacheRead, usageMods))
	}

	if respSummary.StopReason == "" && s.ResolveNativeOpenAIResponseSignals(ctx) {
		if finishReason, toolCalls, observed := extractor.OpenAIChatResponse(); observed {
			respSummary.UpstreamFinishReason = finishReason
			respSummary.StopReason = finishReason
			respSummary.ToolUseBlocks = toolCalls
			nativeRespSummary = true
		}
	}
	openaiUpstreamBuilder := telemetry.NewAttrBuilder(41).
		String("request_id", facts.RequestID).
		String("external_id", facts.ExternalID).
		String("router_user_id", auth.UserIDFrom(ctx)).
		String("client.device_id", facts.Client.DeviceID).
		String("client.account_id", facts.Client.AccountID).
		String("client.session_id", facts.Client.SessionID).
		String("client.user_agent", facts.Client.UserAgent).
		String("client.app", facts.Client.TelemetryClientApp()).
		String("rollout_id", policyRolloutIDFromContext(ctx)).
		String("requested.model", feats.Model).
		String("decision.model", decision.Model).
		String("decision.provider", finalProvider).
		String("decision.reason", decision.Reason).
		String("routing.turn_type", string(routeRes.TurnType)).
		String("upstream.finish_reason", respSummary.UpstreamFinishReason).
		String("upstream.stop_reason", respSummary.StopReason).
		Int64("usage.input_tokens", int64(in)).
		Int64("usage.output_tokens", int64(out)).
		Int64("usage.cache_creation_input_tokens", int64(cacheCreation)).
		Int64("usage.cache_read_input_tokens", int64(cacheRead)).
		Int64("usage.cache_creation_1h_input_tokens", int64(usageMods.CacheCreation1h)).
		String("usage.speed", string(extractor.Speed())).
		String("usage.inference_geo", string(extractor.InferenceGeo())).
		Float64("cost.requested_input_usd", requestedInputCost).
		Int64("cost.baseline_warm_prefill_tokens", int64(baselineWarmPrefill)).
		Float64("cost.requested_output_usd", catalog.EffectiveOutputCost(catalog.PromptTokens(in, cacheCreation, cacheRead, decision.Provider), out, reqPricing, usageMods)).
		Float64("cost.actual_input_usd", catalog.EffectiveInputCost(in, cacheCreation, cacheRead, actPricing, decision.Provider, usageMods)).
		Float64("cost.actual_output_usd", catalog.EffectiveOutputCost(catalog.PromptTokens(in, cacheCreation, cacheRead, decision.Provider), out, actPricing, usageMods)).
		Bool("cost.subscription_served", s.costNeutralSubscriptionServed(ctx)).
		Bool("cost.fast_mode", fastServed).
		Int64("latency.upstream_ms", proxyMs).
		Int64("latency.total_ms", s.clock.Now().Sub(requestStart).Milliseconds()).
		Int64("upstream.status_code", int64(upstreamStatus(proxyErr))).
		Bool("routing.cross_format", crossFormat).
		String("dispatch.primary_provider", rescue.primaryProvider).
		String("dispatch.primary_model", rescue.primaryModel).
		String("dispatch.final_provider", finalProvider).
		Int64("dispatch.fallback_attempts", int64(winnerIdx)).
		Bool("dispatch.failover_used", rescue.failoverUsed).
		Bool("dispatch.failover_attempted", rescue.failoverAttempted).
		Bool("dispatch.subscription_failover", rescue.codexFailoverUsed || rescue.claudeFailoverUsed).
		Bool("dispatch.cyber_refusal_retry", rescue.cyberRetryRan).
		Bool("dispatch.sibling_failover", rescue.siblingFailoverUsed)
	if responsesSurface, _ := ctx.Value(responsesSurfaceContextKey{}).(bool); responsesSurface {
		openaiUpstreamBuilder.String("request.api_surface", string(requestAPISurfaceResponses))
	}
	if s.effectiveCaptureMode(ctx) == CaptureOff {
		openaiUpstreamBuilder.Int64("request.message_count", int64(feats.MessageCount)).
			Bool("request.has_tools", feats.HasTools)
	}
	applyPlannerAttrs(openaiUpstreamBuilder, routeRes)
	applyRoutingStateAttrs(openaiUpstreamBuilder, routeRes, decision.ServedIdentity(), sessionKey)
	applyCallerRoutingAttrs(ctx, openaiUpstreamBuilder)
	applyExperimentAttrs(ctx, openaiUpstreamBuilder, &routeRes, taskArm, feats.Model, decision.Model)
	applyEffortAttrs(openaiUpstreamBuilder, effortServed)
	s.applySubscriptionSpanTelemetry(ctx, openaiUpstreamBuilder, feats.Model, decision.Model)
	addTimingAttrs(ctx, openaiUpstreamBuilder)

	openaiObs := buildObservationContext(s.strategyPorts, ctx, strategy, decision, routeRes.Fresh, s.effectiveCaptureMode(ctx))
	openaiObs.applySpanAttrs(openaiUpstreamBuilder)

	telemetry.Record(ctx, telemetry.Span{
		Name:  "router.upstream",
		Start: proxyStart,
		End:   s.clock.Now(),
		Attrs: openaiUpstreamBuilder.Build(),
	})
	callLogBase := openaiUpstreamBuilder.Build()
	emitCallLog := func() {
		reqBody := body
		if h := deferredCallLogFrom(ctx); h != nil && h.requestBody != nil {
			reqBody = h.requestBody
		}
		respBody, respTrunc := capturedResponse(contentCap)
		s.recordCallLog(ctx, callLogBase, routeMs, proxyErr, reqBody, respBody, respTrunc)
		telemetry.Flush(ctx)
	}
	if h := deferredCallLogFrom(ctx); h != nil {
		h.fn = emitCallLog
	} else {
		emitCallLog()
	}

	s.recordTurnUsage(ctx, routeRes, finalProvider, decision.ServedIdentity(), in, out, cacheCreation, cacheRead, extractor.OutputLimitReached())
	s.clearRecoveredCooldown(ctx, routeRes, decision.Model, proxyErr)

	var subscriberSettlement subscriberSettlementState
	if turnUsageBillable(ctx, proxyErr, in, out, cacheCreation, cacheRead) {
		subscriberSettlement = s.emitBilling(ctx, facts.RequestID, facts.ExternalID, feats.Model, decision, actPricing, routeRes, in, out, cacheCreation, cacheRead, usageMods)
	}

	armDemotedOAI := ""
	var armDemotionReasonOAI sessionpin.DemotionReason
	rescuedArmDemotedOAI := ""
	var rescuedArmDemotionReasonOAI sessionpin.DemotionReason
	if !routeRes.CallerModelPassthrough {
		s.maybeEvictPinAfterUpstreamErr(ctx, stickyHit, proxyErr, decision.Reason, sessionTarget{strategy: strategy, installationID: installationIDFromContext(ctx), sessionKey: routeRes.SessionKey}, stickyStateRole(routeRes))
		armDemotedOAI = s.maybeDemoteArmAfterCommittedStreamFailure(ctx, committed(preludeBuf) || committed(responsesPreludeBuf), routeRes.HardPinned, proxyErr, decision.Model, decision.Reason, sessionTarget{strategy: strategy, installationID: installationIDFromContext(ctx), sessionKey: routeRes.SessionKey}, stickyStateRole(routeRes), routeRes.PinRole)
		if armDemotedOAI != "" {
			armDemotionReasonOAI = sessionpin.DemotionReasonCommittedStreamFailure
		}
		primaryModelRescueRan := rescue.siblingRescueRan || decision.Model != rescue.primaryModel
		if providers.IsResponseHeaderTimeout(rescue.primaryFailureErr) && (primaryModelRescueRan || proxyErr != nil) {
			rescuedArmDemotedOAI, rescuedArmDemotionReasonOAI = s.maybeStrikeArmAfterRescuedFailure(ctx, primaryModelRescueRan, routeRes.HardPinned, rescue.primaryFailureErr, rescue.primaryDecision, sessionTarget{strategy: strategy, installationID: installationIDFromContext(ctx), sessionKey: routeRes.SessionKey}, stickyStateRole(routeRes), routeRes.PinRole)
		} else {
			rescuedArmDemotedOAI, rescuedArmDemotionReasonOAI = s.maybeStrikeArmAfterRescuedFailure(ctx, rescue.siblingRescueRan, routeRes.HardPinned, rescue.rescuedPrimaryErr, rescue.rescuedPrimary, sessionTarget{strategy: strategy, installationID: installationIDFromContext(ctx), sessionKey: routeRes.SessionKey}, stickyStateRole(routeRes), routeRes.PinRole)
		}
		s.strikeRescuedModelsForTurn(ctx, proxyErr, decision, routeRes.HardPinned, routeRes.SessionKey, stickyStateRole(routeRes), routeRes.PinRole)
		if proxyErr != nil {
			unrescuedStallDemoted := s.maybeDemoteArmAfterUnrescuedStall(ctx, rescue.siblingRescueRan, committed(preludeBuf) || committed(responsesPreludeBuf), routeRes.HardPinned, rescue.primarySubscriptionArmFailure, rescue.primaryDecision, sessionTarget{strategy: strategy, installationID: installationIDFromContext(ctx), sessionKey: routeRes.SessionKey}, stickyStateRole(routeRes), routeRes.PinRole)
			if armDemotedOAI == "" && unrescuedStallDemoted != "" {
				armDemotedOAI = unrescuedStallDemoted
				armDemotionReasonOAI = sessionpin.DemotionReasonUnrescuedStall
			}
		}
		s.maybeExpireSubscriptionArmPin(ctx, strategy, rescue.primarySubscriptionArmFailure, decision.Reason, installationIDFromContext(ctx), routeRes.SessionKey, stickyStateRole(routeRes))
		s.maybeDisableProviderAfterOverload(ctx, stickyHit, proxyErr, finalProvider, decision.Reason, sessionTarget{strategy: strategy, installationID: installationIDFromContext(ctx), sessionKey: routeRes.SessionKey}, stickyStateRole(routeRes), routeRes.PinRole)
	}

	installationIDOAI, _ := ctx.Value(InstallationIDContextKey{}).(string)
	if installationIDOAI != "" {
		credentialKeyPrefix, credentialKeySuffix, credSource := s.credentialKeyParts(ctx)
		s.markServedExternalKeyUsed(ctx, proxyErr)
		telOAI := InsertTelemetryParams{
			InstallationID:           installationIDOAI,
			APIKeyID:                 apiKeyIDFromContext(ctx),
			RequestID:                facts.RequestID,
			SpanType:                 "router.upstream",
			TraceID:                  facts.RequestID,
			Timestamp:                requestStart,
			RequestedModel:           feats.Model,
			DecisionModel:            decision.Model,
			DecisionProvider:         decision.Provider,
			DecisionReason:           telemetryDecisionReason(ctx, decision.Reason),
			RequestedAllowedModels:   requestedAllowedModelsForTelemetry(ctx),
			EstimatedInputTokens:     int32(feats.Tokens),
			ContextSnapshot:          contextSnapshotJSONForDecision(w.Header(), facts.RequestID, feats.Model, decision.Model, decision.Provider, requestStart, s.clock.Now()),
			StickyHit:                stickyHit,
			PinTier:                  routeRes.PinTier,
			EmbedInput:               embedInput,
			InputTokens:              int32(in),
			OutputTokens:             int32(out),
			RequestedInputCostUSD:    requestedInputCost,
			RequestedOutputCostUSD:   catalog.EffectiveOutputCost(catalog.PromptTokens(in, cacheCreation, cacheRead, decision.Provider), out, reqPricing, usageMods),
			ActualInputCostUSD:       catalog.EffectiveInputCost(in, cacheCreation, cacheRead, actPricing, decision.Provider, usageMods),
			ActualOutputCostUSD:      catalog.EffectiveOutputCost(catalog.PromptTokens(in, cacheCreation, cacheRead, decision.Provider), out, actPricing, usageMods),
			RouteLatencyMs:           routeMs,
			UpstreamLatencyMs:        proxyMs,
			TotalLatencyMs:           s.clock.Now().Sub(requestStart).Milliseconds(),
			CrossFormat:              crossFormat,
			UpstreamStatusCode:       int32(upstreamStatus(proxyErr)),
			CandidateModels:          openaiObs.CandidateModels,
			ChosenScore:              openaiObs.ChosenScore,
			CandidateScores:          openaiObs.CandidateScores,
			Propensity:               openaiObs.Propensity,
			Strategy:                 openaiObs.Strategy,
			RouteID:                  openaiObs.RouteID,
			PolicyRouteKey:           openaiObs.PolicyRouteKey,
			PolicyArtifactID:         openaiObs.PolicyArtifactID,
			PolicyArtifactSHA256:     openaiObs.PolicyArtifactSHA256,
			RosterVersion:            openaiObs.RosterVersion,
			ClassifierArtifactID:     openaiObs.ClassifierArtifactID,
			ClassifierArtifactSHA256: openaiObs.ClassifierArtifactSHA256,
			ClassifierPredictedLabel: openaiObs.ClassifierPredictedLabel,
			ClassifierClassOrder:     openaiObs.ClassifierClassOrder,
			ClassifierProbabilities:  openaiObs.ClassifierProbabilities,
			SelectionPolicyReleaseID: openaiObs.SelectionPolicyReleaseID,
			SelectionPolicySHA256:    openaiObs.SelectionPolicySHA256,
			SelectionHeadGeneration:  int64PtrIf(openaiObs.SelectionHeadGeneration > 0, openaiObs.SelectionHeadGeneration),
			SelectionTrace:           openaiObs.SelectionTrace,
			SidecarSchemaVersion:     openaiObs.SidecarSchemaVersion,
			TrainingAllowed:          openaiObs.TrainingAllowed,
			CaptureMode:              openaiObs.CaptureMode,
			DebugRef:                 openaiObs.DebugRef,
			TTFTMs:                   openaiObs.TTFTMs,
			CacheCreationTokens:      cacheTokenPtr(cacheCreation),
			CacheCreation1hTokens:    cacheCreation1hPtr(cacheCreation, usageMods.CacheCreation1h),
			CacheReadTokens:          cacheTokenPtr(cacheRead),
			Speed:                    string(extractor.Speed()),
			InferenceGeo:             string(extractor.InferenceGeo()),
			ReasoningTokens:          cacheTokenPtr(extractor.ReasoningTokens()),
			DeviceID:                 facts.Client.DeviceID,
			SessionID:                facts.Client.SessionID,
			RouterUserID:             auth.UserIDFrom(ctx),
			ClientApp:                facts.Client.TelemetryClientApp(),
			TurnType:                 string(routeRes.TurnType),
			RolloutID:                openaiObs.RolloutID,
			UpstreamFinishReason:     stringPtrOrEmpty(respSummary.UpstreamFinishReason),
			StopReason:               stringPtrOrEmpty(respSummary.StopReason),
			ToolUseBlocks:            int32PtrIfKnown(int32(respSummary.ToolUseBlocks), respSummary.StopReason != ""),
			InvalidToolArgsBlocks:    int32PtrIfKnown(int32(respSummary.InvalidToolArgsBlocks), respSummary.StopReason != "" && !nativeRespSummary),
			FailoverUsed:             boolPtrTrue(rescue.failoverUsed),
			FailoverAttempted:        boolPtrTrue(rescue.failoverAttempted),
			SessionKey:               sessionKey[:],
			Role:                     routeRes.PinRole,
			FreshDecisionModel:       openaiObs.FreshDecisionModel,
			FreshCandidateScores:     openaiObs.FreshCandidateScores,
			PinAgeSec:                int64PtrIf(stickyHit && pinAgeSec > 0, pinAgeSec),
			ToolResultBytes:          toolResultBytesPtr(inboundLastUser, tt),
			UserPrompt:               &inboundUserPromptOpenAI,
			ErrorClass:               classifyTurnError(proxyErr, respSummary.StopReason, respSummary.InvalidToolArgsBlocks),
			FailureOwner:             turnFailureOwnerAfterFinish(ctx, proxyErr, classifyTurnError(proxyErr, respSummary.StopReason, respSummary.InvalidToolArgsBlocks), respSummary.UpstreamFinishReason),
			CredentialKeyPrefix:      credentialKeyPrefix,
			CredentialKeySuffix:      credentialKeySuffix,
			CredentialSource:         credSource,
			UnifiedLimitHeaders:      unifiedLimitHeadersJSON(ctx),
			CodexLimitHeaders:        codexLimitHeadersJSON(ctx),
		}
		applyServedGroupTelemetry(s.strategyPorts, ctx, &telOAI, routeRes, decision)
		applyPlannerTelemetry(&telOAI, routeRes)
		applyEffortTelemetry(&telOAI, effortServed)
		applyAuthorityShadowTelemetry(&telOAI, routeRes)
		applyBlindExperimentTelemetry(ctx, &telOAI, &routeRes)
		applyPolicyPinTelemetry(ctx, &telOAI, decision.Metadata)
		applySubscriberTelemetry(ctx, &telOAI)
		applyAgreementShadowTelemetry(s.strategyPorts, ctx, &telOAI, routeRes)
		if turnUsageBillable(ctx, proxyErr, in, out, cacheCreation, cacheRead) {
			applySubscriberSettlementTelemetry(subscriberSettlement, &telOAI)
		} else {
			markSubscriberTelemetryUnsettled(&telOAI)
		}
		s.fireTelemetry(ctx, telOAI)
	}

	if cyberRefusalSeen && !routeRes.CallerModelPassthrough {
		s.repinOffRefusingModel(ctx, strategy, routeRes.SessionKey, stickyStateRole(routeRes), rescue.primaryDecision, providers.CyberPolicyErrorCode, rescue.primaryDecision.Provider)
	}

	for _, iss := range respSummary.ToolCallIssues {
		log.Info("router.tool_call_invalid",
			"tool_name", iss.ToolName,
			"failure_bucket", string(iss.Bucket),
			"detail", iss.Detail,
			"repaired", iss.Repaired,
			"repair_actions", iss.Actions,
			"model", decision.Model,
			"provider", finalProvider,
			"session_key_prefix", shortSessionKey(routeRes.SessionKey),
		)
	}

	servedCredentialSource, servedSubscriptionTier := s.servedCredentialLogValues(ctx)
	log.Info("ProxyOpenAIChatCompletion complete", append(append([]any{"requested_model", feats.Model, "baseline_model", s.baselineFor(ctx, feats.Model), "decision_model", decision.Model, "decision_provider", decision.Provider, "primary_provider", rescue.primaryProvider, "primary_model", rescue.primaryModel, "fallback_attempts", winnerIdx, "failover_used", rescue.failoverUsed, "failover_attempted", rescue.failoverAttempted, "subscription_failover", rescue.codexFailoverUsed || rescue.claudeFailoverUsed, "decision_reason", decision.Reason, "requested_tier", routeRes.RequestedTier.String(), "decision_tier", catalog.TierFor(decision.Model).String(), "embedded_tokens", len(promptText) / 4, "total_input_tokens", feats.Tokens, "has_tools", feats.HasTools, "embed_input", embedInput, "cross_format", crossFormat, "sticky_hit", stickyHit, "pin_tier", pinTier, "turn_type", string(tt), "route_ms", routeMs, "proxy_ms", proxyMs, "proxy_err", proxyErr, "error_class", classifyTurnError(proxyErr, respSummary.StopReason, respSummary.InvalidToolArgsBlocks), "failure_owner", turnFailureOwnerAfterFinish(ctx, proxyErr, classifyTurnError(proxyErr, respSummary.StopReason, respSummary.InvalidToolArgsBlocks), respSummary.UpstreamFinishReason), "upstream_err_body", s.zdrLogField(ctx, providers.UpstreamErrorBodyMessage(proxyErr)), "upstream_status", upstreamStatus(proxyErr), "upstream_finish_reason", respSummary.UpstreamFinishReason, "resp_stop_reason", respSummary.StopReason, "stop_reason_promoted", respSummary.StopReasonPromoted, "tool_use_blocks", respSummary.ToolUseBlocks, "invalid_tool_args_blocks", respSummary.InvalidToolArgsBlocks, "tool_call_invalid_blocks", len(respSummary.ToolCallIssues), "routing_marker", marker, "prior_served_model", routeRes.PriorServedModel, "hard_pinned", routeRes.HardPinned, "credential_source", servedCredentialSource, "client_app", completionLogClientApp(facts.Client), "subscription_tier", servedSubscriptionTier}, append(append(armStrikeLogFieldsWithPrimaryReason(armDemotedOAI, armDemotionReasonOAI, rescuedArmDemotedOAI, rescuedArmDemotionReasonOAI), plannerLogFields(routeRes)...), rateLimit.CompletionLogFields()...)...), downgradeShadowLogFields(routeRes)...)...)
	s.reportPolicyOutcome(ctx, routeRes, decision, effortServed, finalProvider, fastServed, feats.Tokens, in, out, cacheCreation, cacheRead, usageMods, routeMs, proxyMs, proxyErr, nil)

	return proxyErr
}

func stripResponsesVisualArtifacts(body []byte) ([]byte, error) {
	stripped, err := translate.StripCommandOutcomesFromResponsesInput(body)
	if err != nil {
		return nil, fmt.Errorf("strip Responses command outcome: %w", err)
	}
	stripped, err = translate.StripRoutingBadgeFromResponsesInput(stripped)
	if err != nil {
		return nil, fmt.Errorf("strip native Responses routing badge: %w", err)
	}
	stripped, err = translate.StripFeedbackFooterFromResponsesInput(stripped)
	if err != nil {
		return nil, fmt.Errorf("strip native Responses feedback footer: %w", err)
	}
	return stripped, nil
}

func stripResponsesTerminalArtifacts(body []byte) ([]byte, error) {
	stripped, err := translate.StripRouterCommandsFromResponsesInput(body)
	if err != nil {
		return nil, fmt.Errorf("strip Responses router command: %w", err)
	}
	stripped, err = translate.StripHistoricalCommandsFromResponsesInput(stripped)
	if err != nil {
		return nil, err
	}
	return stripResponsesVisualArtifacts(stripped)
}

func (s *Service) ProxyOpenAIResponses(ctx context.Context, body []byte, w http.ResponseWriter, r *http.Request) error {
	w, finishBlindExperimentWriter := withBlindExperimentWriter(ctx, w, body)
	defer finishBlindExperimentWriter()
	ctx = withAttemptTrace(ctx, body, r.Header)
	ctx = context.WithValue(ctx, responsesSurfaceContextKey{}, true)
	ctx, inputErr := s.withClassifierInput(ctx, body, router.EndpointOpenAIResponses)
	if inputErr != nil {
		return inputErr
	}
	ctx = s.withUsageObserver(ctx, r.Header)
	clientApp := ClientIdentityFrom(ctx).ClientApp
	portableCodex := clientApp == ClientAppCodex
	terminalResponses := supportsResponsesTerminalSurfaces(clientApp)
	if translate.FeedbackFooterInResponsesHistory(body) {
		ctx = context.WithValue(ctx, responsesFooterEchoedContextKey{}, true)
	}
	ctx = withSubscriptionOnlyWarningEcho(ctx, body)
	nativeBody, conversionBody, err := responsesDispatchBodies(body, terminalResponses)
	if err != nil {
		return err
	}
	if terminalResponses {
		ctx = context.WithValue(ctx, responsesLastAnnouncedModelContextKey{}, translate.LastAnnouncedResponsesModel(body))
	} else {
		ctx = context.WithValue(ctx, responsesReplayedModelContextKey{}, translate.LastReplayedResponsesModel(body))
	}
	conversion, err := convertCodexResponses(ctx, conversionBody, portableCodex, codexLossyRescueNeeded(ctx, s, r.Header))
	if err != nil {
		return fmt.Errorf("translate responses request: %w", err)
	}
	chatBody, model := conversion.Body, conversion.Model
	if len(conversion.RoutingBody) > 0 {
		ctx, err = withResponsesRoutingEnvelope(ctx, conversion.RoutingBody)
		if err != nil {
			return err
		}
	}
	if conversion.Requirements.NativeOnly {
		observability.FromContext(ctx).Info("Responses request restricted to its native wire family",
			"native_only_reasons", ingress.NativeOnlyReasons(conversion.Report, conversionBody))
	}
	if portableCodex && codexModelSwitched(r.Header, conversion) {
		ctx = withCodexSelectedModel(ctx, model, nativeBody)
	}
	ctx = context.WithValue(ctx, codexSkillContextKey{}, conversion.CodexSkill)
	codexNativeRequest := codexResponsesRequest(ctx, r.Header)
	if conversion.Requirements.NativeOnly || codexNativeRequest {
		ctx = context.WithValue(ctx, codexResponsesBodyContextKey{}, nativeBody)
	}
	ctx = context.WithValue(ctx, nativeResponsesBodyContextKey{}, nativeBody)
	if conversion.Requirements.NativeOnly || (portableCodex && codexNativeRequest) {
		originalEnvelope, parseErr := translate.ParseOpenAI(nativeBody)
		if parseErr != nil {
			return fmt.Errorf("parse native Responses request: %w", parseErr)
		}
		ctx = context.WithValue(ctx, nativeResponsesReasoningHashContextKey{}, originalEnvelope.ReasoningConfigurationSHA256())
		ctx = context.WithValue(ctx, nativeResponsesToolHashContextKey{}, originalEnvelope.ToolConfigurationSHA256())
	}
	ctx = context.WithValue(ctx, responsesRequirementsContextKey{}, conversion.Requirements)
	if portableCodex && conversion.TitleGeneration {
		ctx = context.WithValue(ctx, codexTitleGenerationContextKey{}, true)
	}
	ctx = context.WithValue(ctx, responsesTransformsContextKey{}, conversion.Report)
	wrapper := translate.NewResponsesWriter(w, model)
	if terminalResponses {
		wrapper.EnableTerminalBadgeProvenance()
	}
	wrapper.SetToolMappings(conversion.ToolMappings)
	ctx, deferredLog := withDeferredCallLog(ctx)
	deferredLog.requestBody = body
	proxyErr := s.ProxyOpenAIChatCompletion(ctx, chatBody, wrapper, r)
	if proxyErr != nil {
		if msg := TerminalErrorMessage(proxyErr); msg != upstreamCallFailedMessage {
			wrapper.SetUpstreamFailureMessage(msg)
		}
		finalize := func() error { return wrapper.FinalizeError(proxyErr) }
		if isContextOverflow(proxyErr) {
			finalize = func() error {
				return wrapper.FailContextOverflow(gjson.GetBytes(chatBody, "stream").Bool(), openAIContextOverflowCode, contextOverflowMessage)
			}
		}
		if finErr := finalize(); finErr != nil {
			observability.FromContext(ctx).Error("Failed to finalize Responses error stream", "err", finErr)
		}
		if deferredLog.escalation != nil {
			deferredLog.escalation(proxyErr)
		}
		deferredLog.run()
		return proxyErr
	}
	finErr := wrapper.Finalize()
	if deferredLog.escalation != nil {
		deferredLog.escalation(finErr)
	}
	deferredLog.run()
	return finErr
}
