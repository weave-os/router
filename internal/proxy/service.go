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
	"strings"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/conversation"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/ingress"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/bandswap"
	"weave-os/router/internal/router/candidates"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/continuity"
	"weave-os/router/internal/router/escalation/floor"
	"weave-os/router/internal/router/handover"
	"weave-os/router/internal/router/planner"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/routingpolicy"
	"weave-os/router/internal/routingpolicy/overrides"
	"weave-os/router/internal/subscriptions"
	"weave-os/router/internal/subscriptions/entitlement"
	"weave-os/router/internal/telemetry"
	"weave-os/router/internal/telemetry/turnrecord"
	"weave-os/router/internal/timing"
	"weave-os/router/internal/translate"
	providerusage "weave-os/router/internal/usage"
	"weave-os/router/internal/websearch"

	"github.com/google/uuid"
)

type Service struct {
	recoveryProbes routingpolicy.RecoveryProbeLeases

	strategies                   map[router.Strategy]registeredStrategy
	engine                       *dispatch.Engine
	translationCompatibilityMode TranslationCompatibilityMode
	scopedSearchRequirement      bool
	searchRequirementDecayTurns  int
	searchUse                    *searchUseTracker
	turnTelemetry                turnTelemetry
	translationMetrics           telemetry.TranslationMetrics
	pinStore                     sessionpin.Store
	escalation                   *floor.Helper
	lifecycle                    *conversation.Lifecycle
	prefixTrimFreeSwitch         bool
	escapeNormalize              bool
	captureMode                  ContentCaptureMode
	captureMaxBytes              int
	redactor                     Redactor
	routing                      *routingpolicy.Policy
	credentials                  credentials.Brokerage
	strategyServing

	rescueConfig                   dispatch.RescueConfig
	openAIResponsesBroad           bool
	nativeAnthropicResponseSignals bool
	nativeOpenAIResponseSignals    bool
	armDemotion                    dispatch.ArmDemotionPolicy
	throttling                     dispatch.Throttling
	ingress                        ingress.Formats
	effortEscalation               bool
	ccOrchToolsCrossVendor         bool
	ccTaskToolsCrossVendor         bool
	ccAutonomySystemAppend         bool
	ccWorkspaceSystemAppend        bool

	turnSignalCaptureEnabled bool
	webSearch                websearch.Executor
	billing                  *billing.Service
	clock                    auth.Clock
	feedbackLinks            feedbackLinks
}

type registeredStrategy struct {
	router       router.Router
	unavailable  error
	capabilities policy.Capabilities
	outcomes     policy.OutcomeReporter
	feedback     policy.FeedbackReporter
}

func apiKeyIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(requestcontext.APIKeyIDContextKey{}).(string)
	return id
}

type codexResponsesBodyContextKey struct{}

type nativeResponsesBodyContextKey struct{}

type responsesLastAnnouncedModelContextKey struct{}

type responsesReplayedModelContextKey struct{}

type nativeResponsesReasoningHashContextKey struct{}

type nativeResponsesToolHashContextKey struct{}

type codexSkillContextKey struct{}

type responsesFooterEchoedContextKey struct{}

type codexTitleGenerationContextKey struct{}

type SessionDisabledProvidersContextKey struct{}

type SessionDemotedModelsContextKey struct{}

type ObservedOverflowModelsContextKey struct{}

type SessionCooldownModelsContextKey struct{}

type SessionCooldownProbeDeniedModelsContextKey struct{}

type SessionStrikeReadmitModelsContextKey struct{}

type UsageBypassConfig struct {
	Enabled   bool
	Threshold *float64
}

func usageBypassFromContext(ctx context.Context) (UsageBypassConfig, bool) {
	cfg, ok := ctx.Value(requestcontext.InstallationUsageBypassContextKey{}).(UsageBypassConfig)
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

func machineReadTurn(res routingpolicy.TurnRoute) bool {
	return res.SuggestionMode || res.CallerModelPassthrough || isUnpinnedScoredTurn(res.TurnType) ||
		res.TurnType == turntype.Probe || res.TurnType == turntype.Compaction
}

func muteSlowStartFor(ctx context.Context, res routingpolicy.TurnRoute) {
	if machineReadTurn(res) || res.HardPinned || res.TurnType == turntype.SubAgentDispatch {
		attemptTraceFromContext(ctx).MuteSlowStart()
	}
}

func announcementTurnFor(res routingpolicy.TurnRoute, replayedModel string) routingpolicy.TurnRoute {
	if res.PriorServedModel == "" {
		res.PriorServedModel = replayedModel
	}
	return res
}

func routingMarkerFor(res routingpolicy.TurnRoute) string {
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

func modelSelectionReason(ports continuity.StrategyPorts, res routingpolicy.TurnRoute, failoverReason string) string {
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

func baselineRoutingMarkerFor(res routingpolicy.TurnRoute, baselineModel string) string {
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

func siblingMarkerFor(ports continuity.StrategyPorts, ctx context.Context, h http.Header, res routingpolicy.TurnRoute, failedModel, siblingModel string) string {
	return switchMarkerFor(ports, ctx, h, res, siblingMarkerReason(ctx, failedModel), siblingModel)
}

func switchMarkerFor(ports continuity.StrategyPorts, ctx context.Context, h http.Header, res routingpolicy.TurnRoute, reason, siblingModel string) string {
	return suppressMarkerIfRequested(ctx, h, modelSelectionMarkerForRequest(ports, ctx, res, siblingRoutingMarkerFor(res, reason, siblingModel), siblingModel, reason))
}

func siblingRoutingMarkerFor(res routingpolicy.TurnRoute, reason, siblingModel string) string {
	if res.SuggestionMode || isUnpinnedScoredTurn(res.TurnType) || res.TurnType == turntype.Probe || siblingModel == "" || baseModelOf(res.PriorServedModel) == siblingModel {
		return ""
	}
	return "✦ **Weave Router** → " + siblingModel + " · " + reason + "\n\n"
}

func subscriptionTargetMarker(ports continuity.StrategyPorts, ctx context.Context, h http.Header, res routingpolicy.TurnRoute, picked, target router.Decision, pickedMarker string) string {
	if target.Model == picked.Model {
		return pickedMarker
	}
	if _, ok := lastFailureOn(attemptTraceFromContext(ctx), picked.Model); !ok {
		return switchMarkerFor(ports, ctx, h, res, markerReasonSubscriptionCovered, target.Model)
	}
	return siblingMarkerFor(ports, ctx, h, res, picked.Model, target.Model)
}

func cyberRefusalRoutingMarkerFor(res routingpolicy.TurnRoute, fallbackModel string) string {
	if res.SuggestionMode || isUnpinnedScoredTurn(res.TurnType) || res.TurnType == turntype.Probe || fallbackModel == "" || baseModelOf(res.PriorServedModel) == fallbackModel {
		return ""
	}
	return "✦ **Weave Router** → " + fallbackModel + " · " + markerReasonCyberRefusal + "\n\n"
}

func routingReasonShort(res routingpolicy.TurnRoute) string {
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
	v := ctx.Value(requestcontext.InstallationExcludedModelsContextKey{})
	if v == nil {
		return nil
	}
	out, _ := v.([]string)
	return out
}

func installationAllowedModelsFromContext(ctx context.Context) []string {
	v := ctx.Value(requestcontext.InstallationAllowedModelsContextKey{})
	if v == nil {
		return nil
	}
	out, _ := v.([]string)
	return out
}

func installationSubscriptionModelsWhenActiveFromContext(ctx context.Context) []string {
	v := ctx.Value(requestcontext.InstallationSubscriptionModelsWhenActiveContextKey{})
	if v == nil {
		return nil
	}
	out, _ := v.([]string)
	return out
}

func installationSubscriptionModelsWhenInactiveFromContext(ctx context.Context) []string {
	out, _ := ctx.Value(requestcontext.InstallationSubscriptionModelsWhenInactiveContextKey{}).([]string)
	if openRouterOverflowEnabled(ctx) {
		return openRouterOverflowModels(out)
	}
	return out
}

func allowedModelsForRequest(ctx context.Context) map[string]struct{} {
	if planHasDedicatedServingPolicy(ctx) && !subscriptionStateModelsEnabled(ctx) {
		return nil
	}
	return candidates.IntersectAllowed(installationAllowedModelSet(ctx), requestAllowedModelSet(ctx))
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
	if stateModels == nil {
		stateModels = make(map[string]struct{})
	}
	return candidates.IntersectAllowed(allowed, stateModels)
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
	return candidates.RoutableUniverse(s.routing.AvailableModels())
}

func subscriptionRoutingDisabledForRequest(ctx context.Context) bool {
	disabled, _ := ctx.Value(requestcontext.InstallationSubscriptionRoutingDisabledContextKey{}).(bool)
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
	hide, _ := ctx.Value(requestcontext.InstallationHideTerminalSurfacesContextKey{}).(bool)
	return hide
}

func routingKnobsForRequest(ctx context.Context) *router.Overrides {
	if planHasDedicatedServingPolicy(ctx) {
		return nil
	}
	if k := router.RoutingKnobsFromContext(ctx); k != nil {
		return k
	}
	if v, ok := ctx.Value(requestcontext.InstallationRoutingKnobsContextKey{}).(*router.Overrides); ok {
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
	return candidates.ExcludedModels(candidates.ExclusionSources{
		Override:             s.routing.ExcludedModelsOverride(),
		ProductIneligible:    s.productIneligibleModels(ctx),
		InstallationExcluded: installationExcludedModelsFromContext(ctx),
		Allowed:              allowed,
		Available:            s.routing.AvailableModels(),
		GatewayUnserved:      func() map[string]struct{} { return s.gatewayUnservedModelsForRequest(ctx) },
	})
}

func (s *Service) productIneligibleModels(ctx context.Context) map[string]struct{} {
	return modelSet(catalog.IneligibleIDs(entitlement.ModelBoundaryFromContext(ctx)))
}

func installationExcludedProvidersFromContext(ctx context.Context) []string {
	if planHasDedicatedServingPolicy(ctx) && !subscriptionStateModelsEnabled(ctx) {
		return nil
	}
	v := ctx.Value(requestcontext.InstallationExcludedProvidersContextKey{})
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
	v := ctx.Value(requestcontext.InstallationPreferredModelsContextKey{})
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
	v := ctx.Value(requestcontext.InstallationFastModeModelsContextKey{})
	if v == nil {
		return nil
	}
	out, _ := v.([]string)
	return out
}

func preferredModelsForRequest(ctx context.Context) []string {
	var subscriptionModels, installationPreferred []string
	if subscriptionStateModelsEnabled(ctx) {
		subscriptionModels = installationSubscriptionModelsWhenActiveFromContext(ctx)
	}
	if !planHasDedicatedServingPolicy(ctx) {
		installationPreferred = installationPreferredModelsFromContext(ctx)
	}
	return candidates.PreferredModels(subscriptionModels, installationPreferred)
}

func clusterArmOverridesForRequest(ctx context.Context) map[string][]string {
	if planHasDedicatedServingPolicy(ctx) {
		if !flags.BoolOr(ctx, flags.KeyPlanUserClusterLists, false) {
			return nil
		}
		return mergeClusterOverrides(nil, auth.UserClusterModelListsFrom(ctx))
	}
	v := ctx.Value(requestcontext.ClusterModelListsContextKey{})
	keyScoped, _ := v.(map[string][]string)
	userScoped := auth.UserClusterModelListsFrom(ctx)
	return mergeClusterOverrides(keyScoped, userScoped)
}

func memberSelectedModel(ctx context.Context, model string) bool {
	if !flags.BoolOr(ctx, flags.KeyPlanUserClusterLists, false) || billing.SubscriptionOnlyFromContext(ctx) && !credentials.LinkedFirst(ctx) {
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

func subscriptionProvidersForRequest(ctx context.Context) map[string]struct{} {
	available := map[string]struct{}{}
	if subscriptionRoutingDisabledForRequest(ctx) || subscriptionFundingOutOfPlayForRequest(ctx) {
		return available
	}
	if !credentials.ClaudeSubscriptionSuppressed(ctx) && (anthropicSubscriptionFromContext(ctx) != "" || managedSubscriptionEnrolled(ctx, subscriptions.ProviderClaude)) {
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
	v, _ := ctx.Value(credentials.AnthropicSubscriptionContextKey{}).(string)
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
		return nil, credentials.SubscriptionOnlyUnavailable(ctx)
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

func openaiSubscriptionFromContext(ctx context.Context) string {
	if byokFirst(ctx, providers.ProviderOpenAI) {
		return ""
	}
	v, _ := ctx.Value(credentials.OpenAISubscriptionContextKey{}).(string)
	return v
}

func openaiAccountIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(credentials.OpenAIAccountIDContextKey{}).(string)
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

func NewService(r router.Router, clients *dispatch.Clients, emitter telemetry.Emitter, pinStore sessionpin.Store, hardPinExplore bool, hardPinProvider, hardPinModel string, telemetryRepo turnrecord.TelemetryRepository) *Service {
	s := &Service{
		engine:                       dispatch.NewEngine(clients),
		translationCompatibilityMode: TranslationCompatibilityShadow,
		turnTelemetry:                turnTelemetry{emitter: emitter, repo: telemetryRepo},
		ingress:                      ingress.New(0),
		pinStore:                     pinStore,
		lifecycle:                    conversation.NewLifecycle(),
		searchUse:                    newSearchUseTracker(),
		searchRequirementDecayTurns:  DefaultSearchRequirementDecayTurns,
		prefixTrimFreeSwitch:         true,
		turnSignalCaptureEnabled:     true,
		ccOrchToolsCrossVendor:       true,
		strategyServing:              strategyServing{continuityConfig: continuity.DefaultConfig(), hmmConfig: continuity.DefaultAuthoritativeConfig()},
		rescueConfig: dispatch.RescueConfig{
			SiblingFailover:             true,
			CyberRefusalRepin:           true,
			CyberRefusalRetry:           true,
			CyberRefusalFallbackModel:   "claude-sonnet-5",
			AnthropicServerSideFallback: true,
		},
		openAIResponsesBroad:           true,
		nativeAnthropicResponseSignals: true,
		nativeOpenAIResponseSignals:    true,
	}
	s.routing = routingpolicy.New(nil).WithHardPins(overrides.HardPins{Explore: hardPinExplore, Provider: hardPinProvider, Model: hardPinModel})
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
	s.rescueConfig.CyberRefusalRepin = enabled
	return s
}

func (s *Service) WithCyberRefusalRetry(enabled bool) *Service {
	s.rescueConfig.CyberRefusalRetry = enabled
	return s
}

func (s *Service) WithAnthropicServerSideFallback(enabled bool) *Service {
	s.rescueConfig.AnthropicServerSideFallback = enabled
	return s
}

func (s *Service) WithSiblingFailover(enabled bool) *Service {
	s.rescueConfig.SiblingFailover = enabled
	return s
}

func (s *Service) WithOpenAIResponsesBroad(enabled bool) *Service {
	s.openAIResponsesBroad = enabled
	return s
}

func (s *Service) WithCommittedStreamArmDemotion(enabled bool) *Service {
	s.armDemotion.CommittedStream = enabled
	return s
}

func (s *Service) WithRescuedFailureArmDemotion(enabled bool) *Service {
	s.armDemotion.RescuedFailure = enabled
	return s
}

func (s *Service) WithTransientRateLimit(enabled bool, cooldownSeconds int) *Service {
	s.throttling.RateLimit = dispatch.NewRateLimitPolicy(enabled, cooldownSeconds)
	return s
}

func (s *Service) WithNativeAnthropicResponseSignals(enabled bool) *Service {
	s.nativeAnthropicResponseSignals = enabled
	return s
}

func (s *Service) WithNativeOpenAIResponseSignals(enabled bool) *Service {
	s.nativeOpenAIResponseSignals = enabled
	return s
}

func (s *Service) WithRetrySleep(sleep func(context.Context, time.Duration) error) *Service {
	s.engine.SetRetrySleep(sleep)
	return s
}

func (s *Service) WithCyberRefusalFallbackModel(model string) *Service {
	if strings.TrimSpace(model) != "" {
		s.rescueConfig.CyberRefusalFallbackModel = strings.TrimSpace(model)
	}
	return s
}

func (s *Service) WithHMMConfig(cfg continuity.AuthoritativeConfig) *Service {
	s.hmmConfig = cfg
	return s
}

func (s *Service) WithPolicyDeadlineFallback(enabled bool) *Service {
	deadline := s.routing.Deadline()
	deadline.Fallback = enabled
	s.routing = s.routing.WithDeadline(deadline)
	return s
}

func (s *Service) WithPolicyDeadlineDefaultModel(model string) *Service {
	deadline := s.routing.Deadline()
	deadline.DefaultModel = model
	s.routing = s.routing.WithDeadline(deadline)
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
	s.rescueConfig.EarlyStopRedo = mode
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

func (s *Service) WithWebSearchExecutor(ex websearch.Executor) *Service {
	s.webSearch = ex
	return s
}

func (s *Service) WithAvailableModels(models map[string]struct{}) *Service {
	s.routing = s.routing.WithAvailableModels(models)
	return s
}

type HardPinRequest = overrides.HardPinRequest

type HardPinResolver = overrides.HardPinResolver

func (s *Service) WithExplicitUtilityHardPin(enabled bool) *Service {
	hardPins := s.routing.HardPins()
	hardPins.ExplicitUtility = enabled
	s.routing = s.routing.WithHardPins(hardPins)
	return s
}

func (s *Service) WithHardPinResolver(resolver HardPinResolver) *Service {
	hardPins := s.routing.HardPins()
	hardPins.Resolver = resolver
	s.routing = s.routing.WithHardPins(hardPins)
	return s
}

func (s *Service) WithDefaultBaselineModel(model string) *Service {
	s.routing = s.routing.WithDefaultBaselineModel(model)
	return s
}

func (s *Service) WithSubAgentOverride(provider, model string) *Service {
	hardPins := s.routing.HardPins()
	hardPins.SubAgentProvider, hardPins.SubAgentModel = provider, model
	s.routing = s.routing.WithHardPins(hardPins)
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
	case requestcontext.ClientAppClaudeCode:
		return "claude-opus-5-5"
	case requestcontext.ClientAppCodex:
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
	return s.routing.DefaultBaselineModel()
}

func (s *Service) WithInferenceExecutor(executor *dispatch.Executor) *Service {
	s.engine.SetExecutor(executor)
	return s
}

func (s *Service) InferenceExecutor() *dispatch.Executor {
	return s.engine.Executor()
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
	s.routing = s.routing.WithPlansFrom(p)
	return s
}

func (s *Service) HasExcludedModelsOverride() bool {
	return s.routing.ExcludedModelsOverride() != nil
}

func (s *Service) WithGlobalAutomaticExclusions(store candidates.GlobalAutomaticExclusionStore) *Service {
	s.routing = s.routing.WithGlobalAutomaticExclusions(store)
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
	return s.pinStore != nil || s.turnTelemetry.emitter != nil || s.turnTelemetry.repo != nil || s.billing != nil
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
	return s.engine.GatewayLacksModel(key)
}

func (s *Service) rememberGatewayLacksModel(ctx context.Context, provider, model string) {
	key := gatewayModelKey(EffectiveBaseURL(ctx, ""), provider, model)
	if key == "" {
		return
	}
	s.engine.RememberGatewayLacksModel(key)
}

func (s *Service) newTelemetryRecorder() telemetry.Recorder {
	if s.turnTelemetry.emitter == nil {
		return nil
	}
	return s.turnTelemetry.emitter.NewRecorder()
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
	s.credentials.CodexExtraCredits = enabled
	return s
}

func (s *Service) WithManagedSubscriptions(pool subscriptions.Leaser) *Service {
	s.credentials.ManagedSubscriptions = pool
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

func (s *Service) MetricsSummary(ctx context.Context, installationID string, from, to time.Time) (turnrecord.TelemetrySummary, error) {
	if s.turnTelemetry.repo == nil {
		return turnrecord.TelemetrySummary{}, nil
	}
	return s.turnTelemetry.repo.GetTelemetrySummary(ctx, installationID, from, to)
}

var (
	ErrInvalidSessionID       = errors.New("session id must be non-empty and at most 128 bytes")
	ErrSessionCostUnavailable = errors.New("router telemetry storage is not configured")
)

func (s *Service) SessionCost(ctx context.Context, installationID, sessionID string) (turnrecord.SessionCost, error) {
	if sessionID == "" || len(sessionID) > MaxClientIdentifierLen {
		return turnrecord.SessionCost{}, ErrInvalidSessionID
	}
	if s.turnTelemetry.repo == nil {
		return turnrecord.SessionCost{}, ErrSessionCostUnavailable
	}
	return s.turnTelemetry.repo.GetSessionCost(ctx, installationID, sessionID)
}

func (s *Service) MetricsTimeseries(ctx context.Context, installationID string, from, to time.Time, granularity string) ([]turnrecord.TelemetryBucket, error) {
	if s.turnTelemetry.repo == nil {
		return nil, nil
	}
	return s.turnTelemetry.repo.GetTelemetryTimeseries(ctx, installationID, from, to, granularity)
}

func (s *Service) MetricsSummaryAll(ctx context.Context, from, to time.Time) (turnrecord.TelemetrySummary, error) {
	if s.turnTelemetry.repo == nil {
		return turnrecord.TelemetrySummary{}, nil
	}
	return s.turnTelemetry.repo.GetTelemetrySummaryAll(ctx, from, to)
}

func (s *Service) MetricsTimeseriesAll(ctx context.Context, from, to time.Time, granularity string) ([]turnrecord.TelemetryBucket, error) {
	if s.turnTelemetry.repo == nil {
		return nil, nil
	}
	return s.turnTelemetry.repo.GetTelemetryTimeseriesAll(ctx, from, to, granularity)
}

func (s *Service) MetricsDashboardFacts(ctx context.Context, installationID string, from, to time.Time, granularity string) ([]turnrecord.TelemetryDashboardFact, error) {
	if s.turnTelemetry.repo == nil {
		return nil, nil
	}
	return s.turnTelemetry.repo.GetTelemetryDashboardFacts(ctx, installationID, from, to, granularity)
}

func (s *Service) MetricsActiveUsers(ctx context.Context, installationID string, from, to time.Time, granularity string) ([]turnrecord.TelemetryActiveUsers, error) {
	if s.turnTelemetry.repo == nil {
		return nil, nil
	}
	return s.turnTelemetry.repo.GetTelemetryDashboardActiveUsers(ctx, installationID, from, to, granularity)
}

func (s *Service) MetricsPeople(ctx context.Context, installationID string) ([]turnrecord.TelemetryPerson, error) {
	if s.turnTelemetry.repo == nil {
		return nil, nil
	}
	return s.turnTelemetry.repo.GetTelemetryDashboardPeople(ctx, installationID)
}

func (s *Service) MetricsDrillDown(ctx context.Context, installationID string, from, to time.Time) ([]turnrecord.TelemetryDrillDownRow, error) {
	if s.turnTelemetry.repo == nil {
		return nil, nil
	}
	return s.turnTelemetry.repo.GetTelemetryDashboardDrillDown(ctx, installationID, from, to)
}

func (s *Service) MetricsIndividualFacts(ctx context.Context, installationID string, from, to time.Time, granularity string) ([]turnrecord.TelemetryIndividualFact, error) {
	if s.turnTelemetry.repo == nil {
		return nil, nil
	}
	return s.turnTelemetry.repo.GetTelemetryDashboardIndividualFacts(ctx, installationID, from, to, granularity)
}

func (s *Service) MetricsSubscriptionPool(ctx context.Context, installationID string, servedSince, readingsSince time.Time) ([]turnrecord.TelemetrySubscriptionSeat, error) {
	if s.turnTelemetry.repo == nil {
		return nil, nil
	}
	return s.turnTelemetry.repo.GetTelemetrySubscriptionPoolSeats(ctx, installationID, servedSince, readingsSince)
}

func (s *Service) MetricsModelBreakdown(ctx context.Context, installationID string, from, to time.Time, granularity string) ([]turnrecord.TelemetryModelBucket, error) {
	if s.turnTelemetry.repo == nil {
		return nil, nil
	}
	return s.turnTelemetry.repo.GetTelemetryModelBreakdown(ctx, installationID, from, to, granularity)
}

func (s *Service) MetricsModelBreakdownAll(ctx context.Context, from, to time.Time, granularity string) ([]turnrecord.TelemetryModelBucket, error) {
	if s.turnTelemetry.repo == nil {
		return nil, nil
	}
	return s.turnTelemetry.repo.GetTelemetryModelBreakdownAll(ctx, from, to, granularity)
}

func (s *Service) MetricsRows(ctx context.Context, installationID string, from, to time.Time, limit int32) ([]turnrecord.TelemetryRow, error) {
	if s.turnTelemetry.repo == nil {
		return nil, nil
	}
	return s.turnTelemetry.repo.GetTelemetryRows(ctx, installationID, from, to, limit)
}

func (s *Service) MetricsRowsAll(ctx context.Context, from, to time.Time, limit int32) ([]turnrecord.TelemetryRow, error) {
	if s.turnTelemetry.repo == nil {
		return nil, nil
	}
	return s.turnTelemetry.repo.GetTelemetryRowsAll(ctx, from, to, limit)
}

var ErrProviderNotConfigured = dispatch.ErrProviderNotConfigured

var ErrRequestNotJSONObject = translate.ErrNotJSONObject

var stripRoutingMarkerFromMessages = translate.StripRoutingMarkerFromMessages

func (s *Service) provider(name string) (providers.Client, error) {
	return s.engine.Clients().Client(name)
}

func (s *Service) Clients() *dispatch.Clients {
	return s.engine.Clients()
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
		unavailable := router.StrategyUnavailableError(strategy)
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
	req.OrganizationID, _ = ctx.Value(requestcontext.ExternalIDContextKey{}).(string)
	req.InstallationID = ""
	if installationID := requestcontext.InstallationIDFromContext(ctx); installationID != uuid.Nil {
		req.InstallationID = installationID.String()
	}
	clientIdentity := ClientIdentityFrom(ctx)
	req.ClientApp = clientIdentity.ClientApp
	req.ClientBudget = requestcontext.ClientBudgetFrom(ctx)
	req.RolloutID = policyRolloutIDFromContext(ctx)
	req.CaptureMode = s.effectiveCaptureMode(ctx).String()
	req.TrainingAllowed = policyTrainingAllowedForRequest(ctx)
	req.DebugEnabled, _ = ctx.Value(requestcontext.PolicyDebugEnabledContextKey{}).(bool)
	req.RoutingIntent, _ = ctx.Value(requestcontext.PolicyRoutingIntentContextKey{}).(string)
	return req
}

func policyRolloutIDFromContext(ctx context.Context) string {
	if rolloutID, ok := ctx.Value(requestcontext.PolicyRolloutIDContextKey{}).(string); ok && rolloutID != "" {
		return rolloutID
	}
	return ClientIdentityFrom(ctx).RolloutID
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

func (s *Service) PassthroughToNamedProvider(ctx context.Context, providerName string, body []byte, w http.ResponseWriter, r *http.Request) error {
	log := observability.FromContext(ctx)
	p, err := s.engine.Clients().Upstream(providerName)
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
) dispatch.UpstreamAttempt {
	return func(actx context.Context, d router.Decision, p dispatch.Upstream) error {
		requestCacheTTL1h := func() bool { return translate.AnthropicRequestCacheTTL1h(prep.Body) }
		setStreamCost(d, false, requestCacheTTL1h)
		attemptSink, noticeErr := preludeState.prepare(actx, sink, preludeBuf, translate.FormatAnthropic, env.Stream(), reconnectNoticeForAttempt(actx, r.Header, notice))
		if noticeErr != nil {
			return fmt.Errorf("emit Anthropic router notice: %w", noticeErr)
		}
		ex, proxyWriter := s.turnTelemetry.newUsageExtractor(attemptSink, d.Provider)
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
	installationID := requestcontext.InstallationIDFromContext(ctx)
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
	if err := routingpolicy.NewSessionPins(s.pinStore, s.clock.Now, strategy).WriteRepin(ctx, installationID, sessionKey, role, fbProvider, fbModel, reasonCyberRefusalRepin); err != nil {
		log.Error("cyber-refusal re-pin: pin upsert failed", "err", err, "from_model", served.Model, "to_model", fbModel)
		return
	}
	log.Info("safety refusal — re-pinned session off refusing model",
		"session_key", conversation.ShortSessionKey(sessionKey),
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
		r.Header.Del(requestcontext.ForceModelHeader)
	}
	ctx = context.WithValue(ctx, nativeSearchLoggerContextKey{}, observability.FromContext(ctx))
	ctx = s.withSubscriptionFundingAuthorization(ctx, r.Header)
	body, threadContinued, threadErr := s.ingress.Anthropic.StripMessageThread(body, r.Header)
	if threadErr != nil {
		return fmt.Errorf("strip message thread: %w", threadErr)
	}
	if threadContinued {
		observability.FromContext(ctx).Info("Rejecting message-thread continue; client resends full history")
		return ingress.WriteMessageThreadUnsupported(w)
	}
	ctx, strategy, returnErr := withTurnInput(ctx, s, body, r.Header, router.EndpointAnthropicMessages)
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

	ctx, body, replayedModel, footerEchoed, err := stripInboundEchoes(ctx, log, body, "inbound")
	if err != nil {
		return err
	}
	parsed, parseErr := s.ingress.Anthropic.ParseMessages(ctx, body)
	if parseErr != nil {
		log.Error("Failed to parse Anthropic request", "err", parseErr)
		return fmt.Errorf("parse request: %w", parseErr)
	}
	env, body, modelVariant1M := parsed.Envelope, parsed.Body, parsed.ModelVariant1M
	if claims := conversation.PiSessionFromContext(ctx); claims != nil && !piSessionMatches(ctx, claims, env) {
		return conversation.ErrHandoffInvalid
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
	agentShadowEval, agentShadowMode := requestcontext.AgentShadowEvalFromContext(ctx)

	var sessionKey [sessionpin.SessionKeyLen]byte
	ctx, log, sessionKey = bindRequestLogger(ctx, env, facts.APIKeyID, facts.RequestID, "anthropic_messages")
	log.Debug("Resolved client context budget", "budget_evidence", clientBudget.Evidence, "client_version", clientBudget.Version, "model_variant_1m", clientBudget.ModelVariant1M, "inbound_context_1m", clientBudget.InboundContext1M, "default_window", clientBudget.DefaultWindow, "default_compact_threshold", clientBudget.DefaultCompactThreshold)
	if ctx, err = stripClientHistory(ctx, log, env, "Anthropic"); err != nil {
		return err
	}

	feats := env.RoutingFeatures()
	ctx = withRequestImageCount(ctx, feats.ImageCount)
	if err := validateHandoffEnvelope(ctx, env, r.Header, body); err != nil {
		return err
	}
	if conversation.PreparingHandoff(ctx) && !s.canPrepareHandoff(ctx, env, feats, r.Header, body) {
		return writePreparedHandoff(w, preparedHandoff{Bypass: true})
	}
	promptText, embedInput := turnPromptText(feats)
	logTurnStart(log, "ProxyMessages start", env.Stream(), feats, s.zdrLogField(ctx, observability.Preview(promptText, 200)))

	if !agentShadowMode {
		betaSessionKey := deriveBetaSessionKeyForRequest(ctx, env, facts.APIKeyID, sessionKey)
		if cmd, hasCmd := env.ExtractBetaCommand(); hasCmd {
			log.Info("ProxyMessages beta command")
			return (betaCommands{s}).handle(ctx, w, env, cmd, facts.InstallationID, sessionKey, betaSessionKey, feats.Tokens)
		}
		ctx, strategy, err = s.applySessionStrategy(ctx, strategy, facts.InstallationID, betaSessionKey)
		if err != nil {
			return err
		}
		*r = *r.WithContext(ctx)
	}
	forceModelSessionKey := deriveForceModelSessionKeyForRequest(ctx, env, facts.APIKeyID, sessionKey)
	if conversation.PreparingHandoff(ctx) && !s.authoritativePerTurnSelection(ctx, strategy) {
		return writePreparedHandoff(w, preparedHandoff{Bypass: true})
	}

	agentForceModel := ""
	requestBodyChanged := false
	if !agentShadowMode {
		outcome, err := conversation.CommandDispatch{
			Source:                  env,
			Handlers:                turnCommands{s: s, w: w, env: env, target: sessionTarget{strategy: strategy, installationID: facts.InstallationID, sessionKey: sessionKey}, forceModelSessionKey: forceModelSessionKey, inputTokens: feats.Tokens, requestedModel: feats.Model},
			Protocol:                "ProxyMessages",
			ForceModelEnabled:       s.pinStore != nil,
			ForceModelContinuesTurn: func(cmd translate.ForceModelResult) bool { return cmd.FromToolResult },
			SyntheticFeedback:       func(cmd translate.RouterFeedbackResult) bool { return !cmd.FromToolResult },
		}.Run(ctx)
		if err != nil || outcome.Handled {
			return err
		}
		agentForceModel, requestBodyChanged = outcome.AgentForceModel, outcome.BodyChanged
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
		promptText, embedInput = turnPromptText(feats)
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

	if !conversation.PreparingHandoff(ctx) && s.serveNativeWebSearch(ctx, body, feats.Model, env.Stream(), feats.Tokens, enabledProviders, r.Header, w) {
		return nil
	}

	if !conversation.PreparingHandoff(ctx) {
		if handled, searchErr := s.serveCodexWebSearch(ctx, body, feats.Model, env.Stream(), enabledProviders, r, w); handled {
			return searchErr
		}
	}

	outputReserve, prefiltered := prefilterTurn(ctx, s, turnPrefilterInput{
		env: env, feats: feats, eligibility: eligibility, header: r.Header, excludeAdvisorOutranking: true, log: log,
	})
	inboundToolCallCount := len(env.AssistantToolCallSignatures())
	inboundLastUser := env.LastUserMessage()
	inboundUserPrompt := routingEndsWithUserPrompt(ctx, env)
	inboundLatestToolCalls := latestToolCallCountsJSON(env)
	overflowEstimate := prefiltered.OverflowEstimate

	routeStart := s.clock.Now()
	routingPromptText, routingMessages := routingInputForRequest(ctx, env, promptText)
	req := preRouteRequest(ctx, s, preRouteRequestInput{
		env: env, feats: feats, forceModel: forceModel, forceCluster: forceCluster,
		endpoint: router.EndpointAnthropicMessages, reasoningConfigHash: env.ReasoningConfigurationSHA256(), toolConfigHash: env.ToolConfigurationSHA256(),
		promptText: routingPromptText, messages: routingMessages, feedbackKey: hex.EncodeToString(sessionKey[:]),
		eligibility: eligibility, gatewayProviders: eligibility.GatewayProviders, header: r.Header, prefiltered: prefiltered,
	})
	req.ClientBudget, req.OrganizationID = clientBudget, facts.ExternalID
	if facts.InstallationID != uuid.Nil {
		req.InstallationID = facts.InstallationID.String()
	}
	routeRes, _, routeErr := routeTurn(ctx, s, s.clock, log, routeTurnRequest{
		Request:        req,
		Started:        routeStart,
		Failure:        routeFailureRequest{RequestID: facts.RequestID, RequestStart: requestStart, RequestedModel: feats.Model, Strategy: strategy},
		FailureMessage: "Routing failed",
		IsUserPrompt:   inboundUserPrompt,
	}, func(routeCtx context.Context) (routingpolicy.TurnRoute, error) {
		switch {
		case conversation.HandoffFromContext(ctx) != nil:
			return s.resumeHandoff(routeCtx, strategy, env, req, turntype.IsSubAgent(env, subAgentHintForRequest(routeCtx, r.Header.Get("x-weave-subagent-type")), openCodeCaller(ClientIdentityFrom(routeCtx))))
		case agentShadowMode:
			return s.runAgentShadowEvaluationRoute(routeCtx, env, feats, facts.InstallationID, req, agentShadowEval)
		}
		return taskArm.route(routeCtx, s.runTurnLoop)(env, feats, turnRequest{Strategy: strategy, APIKeyID: facts.APIKeyID, InstallationID: facts.InstallationID}, "", r.Header, req)
	})
	defer routeRes.ReleaseCooldownProbes()
	var escalationCapture *captureWriter
	defer func() {
		if !conversation.PreparingHandoff(ctx) {
			s.completeEscalation(ctx, routeRes, returnErr, escalationCapture, translate.EscalationResponseAnthropic)
		}
	}()
	if routeErr != nil {
		return routeErr
	}
	ctx = requestcontext.WithCallerModelPassthrough(ctx, routeRes.CallerModelPassthrough)
	ctx = withPickedModelFunding(ctx, routeRes.CallerModelPassthrough, routeRes.Decision.Provider)
	ctx = s.withTurnOverloadProbe(ctx, routeRes)
	if conversation.PreparingHandoff(ctx) {
		return s.finishHandoffPreparation(ctx, strategy, w, env, req, routeRes)
	}
	ctx = withRouteSessionState(ctx, routeRes)

	if routeRes.UsageBypass && routeRes.Decision.Provider == providers.ProviderAnthropic {
		err := s.bypassToAnthropic(ctx, env, feats, routeRes.ModelSwitched(), requestStart, facts.RequestID, facts.ExternalID, routeRes.TurnType, routeRes.Decision.Reason, r, w)
		if !errors.Is(err, errBypassRetryable) {
			if !agentShadowMode {
				s.firePolicyShadowForServingDecision(ctx, strategy, routeRes.Decision, req)
			}
			return err
		}

		var linkedSubscriptionRetry bool
		ctx, linkedSubscriptionRetry, err = s.credentials.RetryAfterSubscriptionOnlyBypass(ctx, log, facts.RequestID, facts.ExternalID, routeRes.Decision, managedSubscriptionCanServe)
		if err != nil {
			return err
		}

		reloadUsageBypassSession(ctx, s, strategy, env, facts.APIKeyID, feats.Model, &routeRes)
		routeRes.UsageBypass = false
		if !linkedSubscriptionRetry {
			if err := rerouteAfterUsageBypass(ctx, s, log, strategy, req, &routeRes); err != nil {
				return err
			}
		}
	}

	routeRes.SuggestionMode = r.Header.Get("x-weave-suggestion-mode") == "true"
	decision := routeRes.Decision
	if !agentShadowMode {
		s.firePolicyShadowForServingDecision(ctx, strategy, decision, req)
	}
	tt := routeRes.TurnType
	stickyHit := routeRes.StickyHit
	pinAgeSec := routeRes.PinAgeSec
	routeMs := s.clock.Now().Sub(routeStart).Milliseconds()
	logPlannerOutcome(ctx, s.strategyPorts, routeRes)

	toolBearingTurn := inboundToolCallCount > 0 || inboundLastUser.HasToolResult
	if !agentShadowMode && !routeRes.CallerModelPassthrough && !routeRes.AuthoritativePerTurn && toolBearingTurn && s.lifecycle.NoProgress() != nil {
		fp := conversation.ComputeNoProgressFingerprint(decision, promptText, feats.MessageCount, conversation.ToolProgressMarker(env))
		role := roleForTier(catalog.TierFor(feats.Model))
		if looped, count := s.lifecycle.NoProgress().RecordAndDetect(routeRes.SessionKey, facts.InstallationID, role, fp, s.clock.Now()); looped {
			return writeLoopStop(ctx, w, env, newLoopBreak(s, strategy, facts.InstallationID, routeRes.SessionKey, role, decision).NoProgress(ctx, env, count, routeRes.Decision.Reason), feats.Tokens)
		}
	}

	if !agentShadowMode && !routeRes.CallerModelPassthrough && !routeRes.AuthoritativePerTurn && s.ResolveTextRepetitionBreakEnabled(ctx) && (turntype.DetectFromEnvelope(env, feats, "") == turntype.MainLoop || turntype.DetectFromEnvelope(env, feats, "") == turntype.ToolResult) {
		if looped, count, sampleHash := conversation.DetectTextRepetition(env); looped {
			role := roleForTier(catalog.TierFor(feats.Model))
			return writeLoopStop(ctx, w, env, newLoopBreak(s, strategy, facts.InstallationID, routeRes.SessionKey, role, decision).TextRepetition(ctx, env, count, sampleHash), feats.Tokens)
		}
	}

	if !agentShadowMode && !callerModelPassthroughActive(ctx) {
		if _, recoveryErr := conversation.ApplyClientCompactionRecovery(ctx, env, clientBudget, tt, req.ConversationMessages, DefaultCompactionMaxTokens, claudeCodeDefaultWindow); recoveryErr != nil {
			return recoveryErr
		}
	}

	var compactionHandoverOutcome handover.Outcome
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

	setDecisionHeaders(w.Header(), decision)
	setContextEstimateHeaders(w.Header(), overflowEstimate, outputReserve)
	if !agentShadowMode {
		s.setFeedbackLinkHeader(ctx, w, facts.InstallationID, facts.ExternalID, facts.RequestID, auth.UserIDFrom(ctx))
	}

	if _, err := s.provider(decision.Provider); err != nil {
		return err
	}

	reqPricing, _ := catalog.PrimaryPriceFor(s.baselineFor(ctx, feats.Model))
	actPricing, _ := catalog.PrimaryPriceFor(decision.Model)
	recordDecisionSpan(ctx, s.clock, decisionSpan{
		surface: decisionSpanWithTaskArmAndSubscriber, facts: facts, feats: feats, decision: decision, routeRes: &routeRes, taskArm: taskArm,
		sessionKey: sessionKey, embedInput: embedInput, routeMs: routeMs,
		reqPricing: reqPricing, actPricing: actPricing, start: requestStart,
	})

	opts := translate.EmitOptions{
		TargetModel:                       decision.Model,
		TargetProvider:                    decision.Provider,
		Capabilities:                      router.Lookup(decision.Model),
		IncludeStreamUsage:                s.usageRequired(),
		SessionAffinity:                   sessionAffinityHint(routeRes.SessionKey),
		ModelSwitched:                     routeRes.ModelSwitched(),
		EnableExtendedContext:             shouldEnableExtendedContext(env.FullTokenEstimate(), outputReserve),
		EnableServerSideFallback:          s.ResolveAnthropicServerSideFallback(ctx),
		KeepCrossVendorOrchestrationTools: flags.BoolOr(ctx, flags.KeyCCOrchToolsCrossVendor, s.ccOrchToolsCrossVendor),
		KeepCrossVendorTaskTools:          s.ResolveCCTaskToolsCrossVendor(ctx),
		AppendAutonomySystem:              s.autonomySystemAppendApplies(ctx, body, env, routeRes.TurnType),
		AppendWorkspaceSystem:             s.workspaceSystemAppendApplies(ctx, body, env, routeRes.TurnType),
	}
	effortServed := s.resolveEffort(ctx, decision, opts.Capabilities, routeRes.EscalateEffort)
	effortServed.Apply(&opts)

	if s.claudeSubscriptionExhausted(ctx, r.Header) {
		ctx = credentials.WithSuppressedClaudeSubscription(ctx)
		if s.linkedSeatCanCover(ctx, decision.Provider, decision.Model) {
			ctx = withDeferredReconnectNudge(ctx, claudeSubscriptionReconnectNudge(ctx, r.Header, s.credentials.UsageObserver))
		} else {
			ctx = withSubscriptionReconnectNudge(ctx, claudeSubscriptionReconnectNudge(ctx, r.Header, s.credentials.UsageObserver))
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
				return credentials.SubscriptionOnlyUnavailable(ctx)
			}
			ctx = released
		case !servedOnCustomerKey(ctx) && s.anthropicSubscriptionObservedExhausted(ctx, r.Header) && !credentials.ClaudeSubscriptionSuppressed(ctx):
			log.Info("Subscription-only request cannot be served on the subscription; refusing",
				"requested_model", feats.Model, "external_id", facts.ExternalID, "decision_provider", decision.Provider)
			return credentials.SubscriptionOnlyUnavailable(ctx)
		default:
			bindings = []catalog.ProviderBinding{{Provider: decision.Provider}}
		}
	}
	clientSink := w
	var streamCost *streamCostWriter
	if env.Stream() && !agentShadowMode {
		var closeKeepalive func()
		clientSink, streamCost, closeKeepalive = anthropicStreamSink(w, s.ingress.Keepalive, func() string {
			return s.feedbackFooter(ctx, ClientIdentityFrom(ctx).ClientApp, routeRes.TurnType, footerEchoed)
		})
		defer closeKeepalive()
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
	buildAttempt := func(target router.Decision, targetOpts translate.EmitOptions, targetMarker string, fundingCtx context.Context) (dispatch.UpstreamAttempt, error) {
		return buildMessagesAttempt(ctx, attemptDeps, target, targetOpts, targetMarker, fundingCtx)
	}
	attempt, attemptBuildErr := buildAttempt(decision, opts, marker, fundingCtx)
	if attemptBuildErr != nil && !translate.IsIntrinsicallyIncompatible(attemptBuildErr) {
		finishInferenceSpan(inferenceSpan, decision, decision.Provider, -1, attemptBuildErr)
		return attemptBuildErr
	}

	rescue := &dispatch.MessagesRescue{
		RescueChain: &dispatch.RescueChain{
			RescuePorts: rescuePortsFor(s), Markers: rescueMarkers{s.strategyPorts, r.Header, routeRes, s.credentials.UsageObserver},
			HTTPRequest:      r,
			Log:              log,
			Env:              env,
			RouteRes:         rescueTurnOf(routeRes),
			Request:          &req,
			Opts:             opts,
			ContentSink:      contentSink,
			PreludeBuf:       preludeBuf,
			Attempt:          attempt,
			BuildAttempt:     buildAttempt,
			Surface:          inference.PurposeAnthropicMessages,
			RespSummary:      &respSummary,
			ReqStats:         &reqStats,
			Decision:         decision,
			Bindings:         bindings,
			Marker:           marker,
			ProxyErr:         proxyErr,
			EffortServed:     effortServed,
			OverflowEstimate: overflowEstimate,
			OutputReserve:    outputReserve,
		},
		Feats:           feats,
		AgentShadowMode: agentShadowMode,

		EarlyStop:                 earlyStop,
		NoteStreamCut:             streamCut.noteCut,
		AttemptBuildErr:           attemptBuildErr,
		AnthropicTierAttempt:      boundTierAttempt(anthropicTierAttemptFor, recordFastServed),
		Extractor:                 &extractor,
		FastServed:                &fastServed,
		CrossFormat:               &crossFormat,
		CompactionHandoverOutcome: compactionHandoverOutcome,
	}
	rescue.FlushErr = rescue.FlushErrAsAnthropic
	rescue.ArmBaseline(ctx)
	rescue.ArmSubscriptionRetry(ctx)
	ctx = rescue.ArmRescues(ctx)
	rescue.ArmEarlyStop(ctx)
	rescue.HoldPrimary()
	rescue.DispatchPrimary(ctx)
	rescue.RedoIfStoppedEarly(ctx)
	rescue.NotePrimaryFailure(ctx)
	rescue.ClassifyPrimaryFailure()
	ctx = rescue.BaselineRescue(ctx)
	rescue.SubscriptionRetry(ctx)
	rescue.SiblingRescue(ctx)
	rescue.Finish(ctx)
	decision, marker, proxyErr, effortServed = rescue.Decision, rescue.Marker, rescue.ProxyErr, rescue.EffortServed
	compactionHandoverOutcome = rescue.CompactionHandoverOutcome
	winnerIdx, finalProvider := rescue.WinnerIdx, rescue.FinalProvider

	if rescue.SubscriptionFailoverUsed {
		ctx = credentials.WithSuppressedClaudeSubscription(ctx)
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

	return finishMessagesTurn(ctx, s, &messagesFinishedTurn{
		w: w, body: body, facts: facts, requestStart: requestStart, rateLimit: rateLimit, log: log,
		env: env, responseBuffer: responseBuffer, agentShadowMode: agentShadowMode, sessionKey: sessionKey, feats: feats, promptText: promptText,
		embedInput: embedInput, strategy: strategy, taskArm: taskArm, inboundSpiralSignals: inboundSpiralSignals, inboundSpiralReasons: inboundSpiralReasons, inboundSpiralComputed: inboundSpiralComputed,
		inboundLastUser: inboundLastUser, inboundUserPrompt: inboundUserPrompt, inboundLatestToolCalls: inboundLatestToolCalls, overflowEstimate: overflowEstimate, req: req, routeRes: routeRes,
		decision: decision, tt: tt, stickyHit: stickyHit, pinAgeSec: pinAgeSec, routeMs: routeMs, compactionHandoverOutcome: compactionHandoverOutcome,
		reqPricing: reqPricing, actPricing: actPricing, opts: opts, effortServed: effortServed, contentCap: contentCap, policyOutcomeCap: policyOutcomeCap,
		preludeBuf: preludeBuf, refusalObs: refusalObs, toolUseObs: toolUseObs, streamCut: streamCut, proxyStart: proxyStart, proxyErr: proxyErr,
		crossFormat: crossFormat, extractor: extractor, respSummary: respSummary, reqStats: reqStats, marker: marker, fastServed: fastServed,
		rescue: rescue, winnerIdx: winnerIdx, finalProvider: finalProvider, proxyMs: proxyMs,
	})
}

func applyPlannerAttrs(b *telemetry.AttrBuilder, res routingpolicy.TurnRoute) {
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

func applySidecarAttrs(b *telemetry.AttrBuilder, res routingpolicy.TurnRoute) {
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
	res routingpolicy.TurnRoute,
	servedIdentity string,
	sessionKey [sessionpin.SessionKeyLen]byte,
) {
	b.String("routing.session_key", sessionKeyHex(sessionKey)).
		String("routing.pin_role", res.PinRole).
		String("routing.prior_served_model", res.PriorServedModel).
		Bool("routing.model_changed", res.PriorServedModel != "" && res.PriorServedModel != servedIdentity).
		Bool("routing.llm_escalation_applied", res.Escalation().LLMApplied())
}

func plannerOutcomeAttr(res routingpolicy.TurnRoute) string {
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

func plannerLogFields(res routingpolicy.TurnRoute) []any {
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

func stickyStateRole(res routingpolicy.TurnRoute) string {
	if res.StickyHit && res.StickyRole != "" {
		return res.StickyRole
	}
	return res.PinRole
}

func logPlannerOutcome(ctx context.Context, ports continuity.StrategyPorts, res routingpolicy.TurnRoute) {
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
	if isHMMDecision(ports, res.Fresh) {
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

func isHMMTurn(ports continuity.StrategyPorts, res routingpolicy.TurnRoute) bool {
	return isHMMDecision(ports, res.Decision) || isHMMDecision(ports, res.Fresh)
}

func (s *Service) recordHMMTurnHistory(ctx context.Context, res routingpolicy.TurnRoute, servedProvider, servedModel string, in, out, cacheCreation, cacheRead int, outputLimitReached bool) {
	if servedModel == "" || res.InstallationID == uuid.Nil || res.SessionKey == ([sessionpin.SessionKeyLen]byte{}) {
		return
	}
	strategy := strategyForTurnLoopResult(res)
	role, usageRecorded := routingpolicy.NewSessionPins(s.pinStore, s.clock.Now, strategy).RecordHMMTurn(strategyContext(ctx, strategy), routingpolicy.HMMTurnRecord{
		SessionKey:     res.SessionKey,
		InstallationID: res.InstallationID,
		PinRole:        res.PinRole,
		Reason:         hmmHistoryStoredReason(s.strategyPorts, res),
		HistoryReason:  s.strategyPorts.CostGate.HistoryReason(),
		Usage: sessionpin.Usage{
			InputTokens:         in,
			CachedReadTokens:    cacheRead,
			CachedWriteTokens:   cacheCreation,
			OutputTokens:        out,
			OutputLimitReached:  outputLimitReached,
			ServedModel:         servedModel,
			ServedProvider:      servedProvider,
			PriorServedModel:    res.PriorServedModel,
			SessionEverSwitched: res.SessionEverSwitched,
		},
	})
	if usageRecorded {
		s.recordTokenizerObservation(ctx, res, role, servedProvider, in, cacheCreation, cacheRead)
	}
}

func hmmHistoryStoredReason(ports continuity.StrategyPorts, res routingpolicy.TurnRoute) string {
	if isHMMDecision(ports, res.Fresh) {
		return res.Fresh.Reason
	}
	if isHMMDecision(ports, res.Decision) {
		return res.Decision.Reason
	}
	return ports.CostGate.HistoryReason()
}

func (s *Service) policyOutcomeRoute(res routingpolicy.TurnRoute, decision router.Decision) (router.Decision, *router.RoutingMetadata, policy.OutcomeReporter, bool) {
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

func (s *Service) capturePolicyOutcomeResponse(ctx context.Context, w http.ResponseWriter, res routingpolicy.TurnRoute, decision router.Decision) (http.ResponseWriter, *captureWriter) {
	if !policyTrainingAllowedForRequest(ctx) {
		return w, nil
	}
	if _, _, _, ok := s.policyOutcomeRoute(res, decision); !ok {
		return w, nil
	}
	capture := newCaptureWriter(w, policyOutcomeResponseMaxBytes)
	return capture, capture
}

func isAuthoritativePinHoldMismatch(res routingpolicy.TurnRoute, served router.Decision) bool {
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

func (s *Service) reportPolicyOutcome(ctx context.Context, res routingpolicy.TurnRoute, decision router.Decision, effort dispatch.EffortResolution, finalProvider string, servedFast bool, estimatedInputTokens, inputTokens, outputTokens, cacheCreation, cacheRead int, usageMods catalog.UsageModifiers, routeMs, proxyMs int64, proxyErr error, response *policyOutcomeResponse) {
	routeDecision, routeMetadata, reporter, ok := s.policyOutcomeRoute(res, decision)
	if !ok {
		return
	}
	organizationID, _ := ctx.Value(requestcontext.ExternalIDContextKey{}).(string)
	installationID, _ := ctx.Value(requestcontext.InstallationIDContextKey{}).(string)
	classifier := s.strategyPorts.Facts.ClassifierTelemetryOf(routeMetadata)
	outcome := routingpolicy.PolicyOutcome{
		Route: routeMetadata, Selected: routeDecision, ServedModel: decision.Model, ServedProvider: finalProvider,
		OrganizationID: organizationID, InstallationID: installationID,
		ClientApp: ClientIdentityFrom(ctx).TelemetryClientApp(), RolloutID: policyRolloutIDFromContext(ctx),
		CaptureMode: s.effectiveCaptureMode(ctx).String(), RequestTrainingAllowed: policyTrainingAllowedForRequest(ctx),
		ClassifierArtifactID: classifier.ArtifactID, ClassifierArtifactSHA256: classifier.ArtifactSHA256,
		Effort: routingpolicy.PolicyOutcomeEffort{Arm: effort.Arm, Selected: effort.Selected, Sent: effort.Sent, Source: effort.Source, Mismatch: effort.Mismatch()},
		Status: upstreamStatus(proxyErr), Err: proxyErr,
		EstimatedInputTokens: estimatedInputTokens, InputTokens: inputTokens, OutputTokens: outputTokens,
		CacheCreationTokens: cacheCreation, CacheReadTokens: cacheRead,
		RouteLatencyMs: routeMs, UpstreamLatencyMs: proxyMs, TurnType: string(res.TurnType), StickyHit: res.StickyHit,
	}
	if outcome.AuthoritativeModelMismatch() {
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
	if outcome.Effort.Mismatch {
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
	payload := outcome.Payload()
	if outcome.TrainingAllowed() && response != nil {
		payload["response_body_truncated"] = response.Truncated
		if !response.Truncated {
			payload["response_text"] = translate.AnthropicClientResponseText(response.Body)
		}
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

func (s *Service) bandSwapServed(ctx context.Context, turnType turntype.TurnType, pin sessionpin.Pin, fresh router.Decision, hasImages bool, imageCount int, enabledProviders, excludedModels map[string]struct{}) router.Decision {
	anchor := pin.Decision()
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
	served := candidates.BandSwapTarget(pin, band)
	if served.Model != pin.Model {
		eligibility := candidates.BandSwapEligibility{Available: s.routing.AvailableModels(), Excluded: excludedModels, EnabledProviders: enabledProviders, HasImages: hasImages, ImageCount: imageCount}
		if !eligibility.Admits(served) {
			return anchor
		}
		if _, disabled := s.globalAutomaticExcludedModels(ctx)[served.Model]; disabled {
			return anchor
		}
		if !s.engine.Clients().Has(served.Provider) {
			return anchor
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
		Registered:                    s.engine.Clients().NameSet(),
		SurfaceProvider:               surfaceProvider,
		Headers:                       headers,
		ExternalKeys:                  credentials.ExternalKeysFromContext(ctx),
		RouterKeyAuthed:               requestcontext.InstallationIDFromContext(ctx) != (uuid.UUID{}),
		SubscriptionLanes:             subscriptionLanes,
		InstallationExcludedProviders: installationExcludedProvidersFromContext(ctx),
		SessionDisabledProviders:      sessionDisabledProvidersFromContext(ctx),
	})
}

func (s *Service) openAIPaidCredentialAvailable(ctx context.Context, headers http.Header) bool {
	if billing.WeaveCreditsDeniedFromContext(ctx) {
		return usableBYOK(ctx, providers.ProviderOpenAI) != nil
	}
	return s.credentials.HasOpenAIInfrastructureCredential(ctx, headers, s.engine.Clients().Has)
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
	return candidates.ExcludeCodexOAuthOnlyModels(enabledProviders, excluded)
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

func (s *Service) fireTelemetry(ctx context.Context, p turnrecord.InsertTelemetryParams) {
	if p.UpstreamStatusCode != http.StatusOK || !conversationContextTurn(turntype.TurnType(p.TurnType)) {
		p.ContextSnapshot = nil
	}
	if s.turnTelemetry.repo == nil {
		return
	}
	log := observability.Get().With("request_id", p.RequestID)
	observability.SafeGoContext(ctx, log, 5*time.Second, "fireTelemetry", func(ctx context.Context) {
		s.turnTelemetry.applyUserPromptGap(ctx, log, &p)
		if err := s.turnTelemetry.repo.InsertRequestTelemetry(ctx, p); err != nil {
			log.Warn("Telemetry insert failed", "err", err)
		}
		s.recordAgreementShadow(ctx, log, p)
	})
}

func (s *Service) emitBilling(ctx context.Context, requestID, externalID, requestedModel string, decision router.Decision, actPricing catalog.Pricing, routeRes routingpolicy.TurnRoute, in, out, cacheCreation, cacheRead int, usageMods catalog.UsageModifiers) subscriberSettlementState {
	if s.billing == nil || externalID == "" {
		return captureSubscriberSettlementState(ctx)
	}
	hasOverride := billing.HasOverrideFromContext(ctx)
	apiKeyID, _ := ctx.Value(requestcontext.APIKeyIDContextKey{}).(string)
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

func upstreamStatus(err error) int { return dispatch.UpstreamStatus(err) }

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
