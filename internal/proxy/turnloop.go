package proxy

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"weave-os/router/internal/flags"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/breaker"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/handover"
	"weave-os/router/internal/router/hmm"
	"weave-os/router/internal/router/planner"
	"weave-os/router/internal/router/policy"

	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/telemetry"
	"weave-os/router/internal/translate"

	"github.com/google/uuid"
)

func addToSet(set map[string]struct{}, model string) map[string]struct{} {
	out := make(map[string]struct{}, len(set)+1)
	for k := range set {
		out[k] = struct{}{}
	}
	out[model] = struct{}{}
	return out
}

func mergeSessionStrikes(a, b []string) []string {
	if len(a) == 0 {
		return b
	}
	if len(b) == 0 {
		return a
	}
	seen := make(map[string]struct{}, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, p := range a {
		if _, ok := seen[p]; !ok {
			seen[p] = struct{}{}
			out = append(out, p)
		}
	}
	for _, p := range b {
		if _, ok := seen[p]; !ok {
			seen[p] = struct{}{}
			out = append(out, p)
		}
	}
	return out
}

func installationIDFromContext(ctx context.Context) uuid.UUID {
	raw, _ := ctx.Value(InstallationIDContextKey{}).(string)
	if raw == "" {
		return uuid.Nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil
	}
	return id
}

func cacheWarm(pin sessionpin.Pin, now time.Time) bool {
	if pin.LastTurnEndedAt.IsZero() {
		return false
	}
	return now.Sub(pin.LastTurnEndedAt) < providers.CacheTTLFor(pin.Provider)
}

func pinCacheCold(pin sessionpin.Pin, prefixBroken bool, now time.Time) bool {
	return !cacheWarm(pin, now) || prefixBroken
}

func applyPinEvidence(res *turnLoopResult, pin sessionpin.Pin, now time.Time) {
	res.PinModel = pin.Model
	res.PinProvider = pin.Provider
	res.PinPolicyGroup = pin.PolicyGroup
	res.PinAgeSec = pinAge(pin, now)
	if !pin.LastTurnEndedAt.IsZero() {
		gapMS := now.Sub(pin.LastTurnEndedAt).Milliseconds()
		res.PriorTurnGapMS = &gapMS
	}
}

func clearPinEvidence(res *turnLoopResult) {
	res.PinModel = ""
	res.PinProvider = ""
	res.PinPolicyGroup = ""
	res.PinAgeSec = 0
	res.PriorTurnGapMS = nil
}

func cacheablePrefixTokens(pin sessionpin.Pin, total int, prefixBroken bool) (int, bool) {
	if prefixBroken {
		return 0, true
	}
	cached := pin.LastCachedReadTokens + pin.LastCachedWriteTokens
	prior := priorPromptTokens(pin)
	if prior <= 0 {
		return 0, false
	}
	share := min(1.0, float64(cached)/float64(prior))
	return int(share * float64(total)), true
}

func priorPromptTokens(pin sessionpin.Pin) int {
	if providers.FamilyFor(pin.Provider) == providers.FamilyAnthropic {
		return pin.LastInputTokens + pin.LastCachedReadTokens + pin.LastCachedWriteTokens
	}
	return pin.LastInputTokens
}

func plannerInputTokens(env *translate.RequestEnvelope, feats translate.RoutingFeatures) int {
	if env == nil {
		return feats.Tokens
	}
	if full := env.FullTokenEstimate(); full > feats.Tokens {
		return full
	}
	return feats.Tokens
}

func (s *Service) plannerTokensFor(ctx context.Context, env *translate.RequestEnvelope, feats translate.RoutingFeatures) int {
	if !plannerConfigFor(ctx, s.planner).CorrectedEconomics {
		return feats.Tokens
	}
	return plannerInputTokens(env, feats)
}

const policyPinTier = "policy_pin"

type pinTier string

const (
	pinTierAuthoritativeUpgradeEvidence         pinTier = "authoritative_upgrade_evidence"
	pinTierAuthoritativeSlidingThresholdUpgrade pinTier = "authoritative_sliding_threshold_upgrade"
	pinTierAuthoritativeUpgradeConfidenceLow    pinTier = "authoritative_hmm_upgrade_confidence_low"
	pinTierAuthoritativeDowngradeConfidenceLow  pinTier = "authoritative_hmm_downgrade_confidence_low"
	pinTierAuthoritativeDowngradeHysteresis     pinTier = "authoritative_hmm_downgrade_hysteresis"
	pinTierAuthoritativeExcludedPin             pinTier = "authoritative_excluded_pin"
	pinTierAuthoritativeExcludedReroute         pinTier = "authoritative_excluded_reroute"
)

type turnLoopResult struct {
	EscalationShadowMarked bool
	EscalationScope        [32]byte
	EscalationOrdinal      int64
	escalationActivation   [32]byte
	escalationObservation  translate.EscalationObservation
	llmEscalation          *llmEscalationTurn

	Decision                    router.Decision
	SessionKey                  [sessionpin.SessionKeyLen]byte
	InstallationID              uuid.UUID
	Strategy                    router.Strategy
	TurnType                    turntype.TurnType
	StickyHit                   bool
	HardPinned                  bool
	SessionFirstTurn            bool
	Purpose                     inference.Purpose
	Origin                      policy.OverrideSource
	AuthoritativePerTurn        bool
	UsageBypass                 bool
	CallerModelPassthrough      bool
	PinTier                     string
	PinAgeSec                   int64
	ForcedPinDropped            bool
	ForcedPinDropReason         string
	ForcedPinModel              string
	PinProvider                 string
	PrefixBroken                bool
	PriorTurnGapMS              *int64
	PolicyFallback              bool
	RequestedTier               catalog.Tier
	PinRole                     string
	StickyRole                  string
	Fresh                       router.Decision
	PlannerDecision             planner.Decision
	PinModel                    string
	PinPolicyGroup              string
	PriorServedModel            string
	SessionEverSwitched         bool
	PriorServedEndedAt          time.Time
	PriorPromptTokens           int
	TokenizerCalibration        sessionpin.TokenizerCalibration
	ContextEstimate             int
	StripThinkingBlocks         bool
	Handover                    handoverOutcome
	SuggestionMode              bool
	PrefixTrimmed               bool
	EscalateEffort              bool
	PinTurnCount                int
	PinFirstPinnedAt            time.Time
	SessionDisabledProviders    []string
	TranslationEnabledProviders map[string]struct{}
	SessionDemotedModels        []string
	ObservedOverflowModels      []string
	SessionCooldownModels       map[string]time.Time
	CooldownProbeReleases       []func()
	OverloadProbe               *breaker.Probe
	CooldownProbes              map[string]time.Time
	CooldownProbeDeniedModels   []string
	SessionStrikeReadmitModels  []string
	AuthorityShadow             authorityCacheShadow
	UpgradeShadow               *authoritativeUpgradeDecision
	DowngradeShadow             downgradeGuardShadow
}

func (r turnLoopResult) releaseCooldownProbes() {
	for _, release := range r.CooldownProbeReleases {
		release()
	}
}

type downgradeGuardShadow struct {
	Served              bool
	Votes               int
	HysteresisWouldHold bool
	ConfidenceWouldHold bool
}

func downgradeShadowLogFields(res turnLoopResult) []any {
	return []any{
		"authoritative_downgrade_served", res.DowngradeShadow.Served,
		"downgrade_votes", res.DowngradeShadow.Votes,
		"downgrade_shadow_hysteresis_would_hold", res.DowngradeShadow.HysteresisWouldHold,
		"downgrade_shadow_confidence_would_hold", res.DowngradeShadow.ConfidenceWouldHold,
	}
}

type authorityCacheShadow struct {
	Computed     bool
	Decision     planner.Decision
	StayModel    string
	StayProvider string
	Sticky       bool
	StayScore    *float64
	FreshScore   *float64
}

func (a authorityCacheShadow) Reason() string {
	if !a.Computed {
		return ""
	}
	return a.Decision.Reason
}

func (a authorityCacheShadow) EVRan() bool {
	return a.Computed && a.Decision.ShadowComputed
}

func (r turnLoopResult) modelSwitched() bool {
	transition := r.PriorServedModel != "" && r.PriorServedModel != r.Decision.ServedIdentity()
	return transition || r.SessionEverSwitched || r.StripThinkingBlocks
}

func (r turnLoopResult) baselineWarmPrefillTokens(requestStart time.Time, cacheCreation, cacheRead int, servedModel, baselineModel string, historyTruncated bool) int {
	if cacheCreation <= 0 || r.PriorServedModel == "" || r.PrefixTrimmed || historyTruncated {
		return 0
	}
	if baseModelOf(r.PriorServedModel) == servedModel {
		return 0
	}
	baseline, ok := catalog.ByID(baselineModel)
	if !ok {
		return 0
	}
	if requestStart.Sub(r.PriorServedEndedAt) >= providers.CacheTTLFor(baseline.PrimaryProvider()) {
		return 0
	}
	return min(cacheCreation, max(r.PriorPromptTokens-cacheRead, 0))
}

func isHMMDecision(dec router.Decision) bool {
	if dec.Metadata != nil && router.IsHMMStrategy(router.Strategy(dec.Metadata.Strategy)) {
		return true
	}
	return strings.HasPrefix(strings.TrimSpace(dec.Reason), "hmm_policy")
}

const hmmHistoryReason = "hmm_history"

const DefaultHMMUpgradeConfidenceThreshold = 0.50

const DefaultHMMDowngradeHysteresisTurns = 2

const (
	hmmReasonConfidentUpgrade        = "hmm_confident_upgrade"
	hmmReasonUpgradeConfidenceLow    = "hmm_upgrade_confidence_low"
	hmmReasonDowngradeConfidenceLow  = "hmm_downgrade_confidence_low"
	hmmReasonDowngradeHysteresis     = "hmm_downgrade_hysteresis"
	hmmReasonPhaseChange             = "hmm_phase_change"
	nativeWebSearchPassthroughReason = "native_web_search_passthrough"
)

func decisionPolicyGroup(dec router.Decision) string {
	if dec.Metadata == nil {
		return ""
	}
	return dec.Metadata.PolicyGroup
}

const policyDeadlineFallbackReason = "policy_deadline_last_known_good"

const policyDeadlineDefaultReason = "policy_deadline_default_model"

const unscorableHMMStickyReason = "hmm_no_user_boundary_sticky"

func isPolicyDeadlineErr(err error) bool {
	if err == nil {
		return false
	}
	if !errors.Is(err, hmm.ErrHMMUnavailable) {
		return false
	}
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}

func hmmHistoryRole(role string) string {
	if role == "" {
		role = sessionpin.DefaultRole
	}
	return role + "_hmm_history"
}

type handoverOutcome struct {
	Invoked               bool
	LatencyMS             int64
	SummaryTokens         int
	FallbackToFullHistory bool
	SummaryUsage          handover.Usage
}

func (s *Service) hasSubAgentOverride() bool {
	return s.subAgentProvider != "" && s.subAgentModel != ""
}

var utilityPurposes = map[turntype.TurnType]inference.Purpose{
	turntype.TitleGen:         inference.PurposeTitleGeneration,
	turntype.Probe:            inference.PurposeProbe,
	turntype.SubAgentDispatch: inference.PurposeSubAgentDispatch,
}

func (r turnLoopResult) dispatchPurpose(surface inference.Purpose) inference.Purpose {
	if len(r.Purpose) > 0 {
		return r.Purpose
	}
	return surface
}

func (r turnLoopResult) dispatchOrigin(decision router.Decision) policy.OverrideSource {
	if r.Origin != "" && decision.Model == r.Decision.Model && decision.Provider == r.Decision.Provider {
		return r.Origin
	}
	return routedOrigin(decision, r.HardPinned, r.StickyHit)
}

func (r turnLoopResult) rescueOrigin() policy.OverrideSource {
	if len(r.Purpose) > 0 {
		return policy.OverrideSourceDeployment
	}
	return ""
}

func (s *Service) isHardPinnedTurn(ctx context.Context, tt turntype.TurnType) bool {
	switch tt {
	case turntype.Probe:
		return true
	case turntype.TitleGen:
		return s.explicitUtilityHardPin
	case turntype.SubAgentDispatch:
		if s.hasSubAgentOverride() {
			return true
		}
		return flags.BoolOr(ctx, flags.KeyHardPinExplore, s.hardPinExplore) && !router.IsHMMStrategy(router.StrategyFromContext(ctx))
	default:
		return false
	}
}

func isUnpinnedScoredTurn(tt turntype.TurnType) bool {
	return tt == turntype.Classifier || tt == turntype.Recap || tt == turntype.TitleGen
}

func (s *Service) routeWithoutPin(
	ctx context.Context,
	req router.Request,
	res turnLoopResult,
	reqHeaders http.Header,
	forceModelFound bool,
	forceModelPin sessionpin.Pin,
) (turnLoopResult, error) {
	if forceModelFound && res.TurnType != turntype.Classifier && forcedPinEligible(forceModelPin, req) {
		res.Decision = pinDecision(forceModelPin)
		res.Decision.Reason = translate.ReasonUserForceModel
		res.StickyHit = true
		res.PinTier = translate.ReasonUserForceModel
		return res, nil
	}
	req.PolicyTurnContext = buildPolicyTurnContext(req, res, sessionpin.Pin{}, sessionpin.Pin{}, s.clock.Now())
	if dec, ok := s.usageBypassDecision(ctx, reqHeaders, req, nil, res.TurnType); ok {
		res.Decision = dec
		res.UsageBypass = true
		return res, nil
	}
	if dec, ok := s.classifierModelDecision(ctx, req, res.TurnType); ok {
		res.Decision = dec
		res.CallerModelPassthrough = true
		res.Origin = policy.OverrideSourceDeployment
		return res, nil
	}
	decision, err := s.routeFor(ctx, req)
	if err != nil {
		return res, err
	}
	res.Decision = decision
	res.Fresh = decision
	return res, nil
}

func (s *Service) classifierModelDecision(ctx context.Context, req router.Request, tt turntype.TurnType) (router.Decision, bool) {
	if tt != turntype.Classifier || subscriptionStateModelsEnabled(ctx) || !catalog.PermittedBy(req.ProductEligibility, req.RequestedModel) {
		return router.Decision{}, false
	}
	dec, err := s.callerModelPassthroughDecision(ctx, req)
	if err != nil || !s.clients.Has(dec.Provider) {
		return router.Decision{}, false
	}
	dec.Reason = reasonClassifierRequestedModel
	return dec, true
}

func authoritativePolicyTurn(tt turntype.TurnType) bool {
	return tt == turntype.MainLoop || tt == turntype.ToolResult
}

func pinServesImages(pin sessionpin.Pin, req router.Request) bool {
	return !req.HasImages || catalog.AcceptsImages(pin.Model) && catalog.AcceptsImageCount(pin.Model, req.ImageCount, req.EnabledProviders)
}

func forcedPinServesImages(pin sessionpin.Pin, req router.Request) bool {
	return !req.HasImages || catalog.AcceptsImages(pin.Model)
}

func excludingModel(excluded map[string]struct{}, model string) map[string]struct{} {
	if _, ok := excluded[model]; ok {
		return excluded
	}
	out := make(map[string]struct{}, len(excluded)+1)
	for k := range excluded {
		out[k] = struct{}{}
	}
	out[model] = struct{}{}
	return out
}

func pinEligible(pin sessionpin.Pin, req router.Request) bool {
	if pin.Model == "" || pin.Provider == "" {
		return false
	}
	if _, excluded := req.ExcludedModels[pin.Model]; excluded {
		return false
	}
	if !pinServesImages(pin, req) {
		return false
	}
	if req.EnabledProviders == nil {
		return true
	}
	_, ok := req.EnabledProviders[pin.Provider]
	return ok
}

func automaticallyDisabled(req router.Request, model string) bool {
	_, disabled := req.AutomaticExcludedModels[model]
	return disabled
}

func automaticPinEligible(pin sessionpin.Pin, req router.Request) bool {
	return pinEligible(pin, req) && !automaticallyDisabled(req, pin.Model)
}

func withAutomaticExclusions(req HardPinRequest, automaticExcluded map[string]struct{}) HardPinRequest {
	req.ExcludedModels = mergeExcludedModels(req.ExcludedModels, automaticExcluded)
	return req
}

func forcedPinEligible(pin sessionpin.Pin, req router.Request) bool {
	req = readmitForcedContextEstimate(req, pin.Model)
	req = readmitForcedImageLimit(req, pin.Model)
	return pinEligible(pin, req)
}

func forcedPinIneligibilityReason(pin sessionpin.Pin, req router.Request) string {
	if req.EnabledProviders != nil {
		if _, enabled := req.EnabledProviders[pin.Provider]; !enabled {
			return "provider_not_enabled"
		}
	}
	if !forcedPinServesImages(pin, req) {
		return "not_image_capable"
	}
	return "excluded"
}

func policyPinServed(ctx context.Context, res turnLoopResult) error {
	pin, pinned := router.HonouredPolicyPin(ctx)
	if !pinned || len(res.Purpose) > 0 {
		return nil
	}
	if res.Decision.Metadata == nil || !res.Decision.Metadata.PolicyPinHonoured {
		return fmt.Errorf("turn served by %q (%s), not by policy pin %s: %w", res.Decision.Model, res.PinTier, pin, router.ErrPolicyPinUnavailable)
	}
	return nil
}

func (s *Service) runTurnLoop(
	ctx context.Context,
	env *translate.RequestEnvelope,
	feats translate.RoutingFeatures,
	apiKeyID string,
	installationID uuid.UUID,
	subAgentHint string,
	reqHeaders http.Header,
	req router.Request,
) (res turnLoopResult, routeErr error) {
	subAgentHint = subAgentHintForRequest(ctx, subAgentHint)
	shadowStartedAt := s.clock.Now()
	defer func() {
		if routeErr == nil {
			routeErr = policyPinServed(ctx, res)
			if routeErr == nil {
				logAuthoritativeUpgrade(ctx, res)
			}
		}
		if routeErr != nil && agreementShadowEnabled(ctx) {
			key := deriveSessionKeyForRequest(ctx, env, apiKeyID)
			params := InsertTelemetryParams{InstallationID: installationID.String(), RequestID: observability.RequestIDFromContext(ctx),
				Timestamp: shadowStartedAt, SessionKey: key[:], Role: res.PinRole,
				TurnType: string(turntype.Detect(env, feats, subAgentHint, openCodeCaller(ClientIdentityFrom(ctx)))), ErrorClass: TurnErrorRoutingRefused}
			applyAgreementShadowTelemetry(ctx, &params, res)
			if params.agreementShadow != nil {
				log := observability.FromContext(ctx)
				observability.SafeGoContext(ctx, log, 5*time.Second, "agreementShadowRoutingFailure", func(shadowCtx context.Context) {
					s.recordAgreementShadow(shadowCtx, log, params)
				})
			}
		}
	}()
	log := observability.FromContext(ctx)
	req = withRoutingTranslationRequirements(ctx, env, req)
	isolatedSearch, _ := ctx.Value(codexWebSearchContextKey{}).(bool)
	var threadSessionKey [sessionpin.SessionKeyLen]byte
	if !isolatedSearch {
		threadSessionKey = deriveSessionKeyForRequest(ctx, env, apiKeyID)
		req.TranslationRequirements = s.scopeSearchRequirement(threadSessionKey, searchUseEnvelope(ctx, env), req.TranslationRequirements)
	}
	req, compatibilityErr := s.applyTranslationPlan(ctx, req)
	if compatibilityErr != nil {
		return turnLoopResult{}, compatibilityErr
	}
	if planHasDedicatedServingPolicy(ctx) {
		req.ForceCluster = ""
	}
	ctx = context.WithValue(ctx, translationPlanAppliedContextKey{}, true)
	req.AutomaticExcludedModels = s.globalAutomaticExcludedModels(ctx)
	req.ClientApp = ClientIdentityFrom(ctx).ClientApp
	if transforms, ok := ctx.Value(responsesTransformsContextKey{}).([]translate.ResponseTransform); ok {
		for _, transform := range transforms {
			if metrics := s.translationMetrics; metrics != nil {
				metrics.RecordTranslationTransform(
					ctx,
					transform.Code,
					transform.Action,
					string(req.TranslationRequirements.SourceFormat),
					string(s.translationCompatibilityMode),
				)
			}
			telemetry.RecordLog(ctx, telemetry.LogRecord{
				Name: "translation.transform",
				Time: s.clock.Now(),
				Attrs: telemetry.NewAttrBuilder(5).
					String("translation.code", transform.Code).
					String("translation.action", transform.Action).
					String("translation.source_format", string(req.TranslationRequirements.SourceFormat)).
					String("translation.mode", string(s.translationCompatibilityMode)).
					String("translation.path", transform.Path).
					Build(),
			})
		}
	}
	req.OrganizationID, _ = ctx.Value(ExternalIDContextKey{}).(string)
	if installationID != uuid.Nil {
		req.InstallationID = installationID.String()
	}
	res = turnLoopResult{
		ContextEstimate:             env.ContextOverflowTokenEstimate(),
		TranslationEnabledProviders: req.EnabledProviders,
		InstallationID:              installationID,
		Strategy:                    router.StrategyFromContext(ctx),
		TurnType:                    turntype.Detect(env, feats, subAgentHint, openCodeCaller(ClientIdentityFrom(ctx))),
		PinTier:                     "miss",
		RequestedTier:               catalog.TierFor(feats.Model),
		StripThinkingBlocks:         stripsThinkingHistory(ctx),
	}
	res.AuthoritativePerTurn = authoritativePolicyTurn(res.TurnType) &&
		s.authoritativePerTurnSelection(ctx)
	if !isolatedSearch {
		req.TaskDomain = taskDomainInput(ctx, env, apiKeyID, res.TurnType)
	}
	res.PinRole = roleForTier(res.RequestedTier)

	if isolatedSearch {
		req.ForceModel = ""
		res.HardPinned = true
		ctx, decision, passthrough, err := s.passthroughDecision(ctx, req)
		if passthrough {
			res.Decision = decision
			res.CallerModelPassthrough = true
			return res, err
		}
		return s.routeWithoutPin(ctx, req, res, reqHeaders, false, sessionpin.Pin{})
	}
	forceModelSessionKey := deriveForceModelSessionKeyForRequest(ctx, env, apiKeyID, threadSessionKey)
	forceModelPin := sessionpin.Pin{}
	forceModelFound := false
	forceModelCleared := false
	if req.ForceModel != "" {
		canonicalModel, provider, known, effort := resolveForceModelWithEffort(req.ForceModel)
		if !known {
			return res, &ForcedModelUnknownError{Model: req.ForceModel}
		}
		forceModelPin = sessionpin.Pin{
			SessionKey:     forceModelSessionKey,
			Role:           forceModelSessionRole,
			InstallationID: installationID,
			Provider:       provider,
			Model:          canonicalModel,
			Effort:         effort,
			Reason:         translate.ReasonUserForceModel,
			PinnedUntil:    pinNeverExpires,
		}
		forceModelFound = true
	}
	if !forceModelFound {
		forceModelPin, forceModelFound, forceModelCleared = s.loadForceModelSessionPin(ctx, forceModelSessionKey, threadSessionKey, turntype.IsSubAgent(env, subAgentHint, openCodeCaller(ClientIdentityFrom(ctx))))
	}
	if forcePinBindsNonClassifierTurn(forceModelFound, res.TurnType) {
		binding, reason := s.forcedModelBinding(ctx, forceModelPin.Model, forceModelPin.Provider)
		if reason != "" {
			return res, &ForcedModelExcludedError{Model: forceModelPin.Model, Reason: reason}
		}
		forceModelPin.Provider = binding
		req.ExcludedModels = s.readmitForcedModel(ctx, req, env, feats, forceModelPin)
	}
	if !forceModelFound && authoritativePolicyTurn(res.TurnType) {
		if probe, release, claimed := s.claimOverloadProbe(ctx, req); claimed {
			ctx = withOverloadProbe(ctx, probe)
			res.OverloadProbe = &probe
			res.CooldownProbeReleases = append(res.CooldownProbeReleases, release)
			req.AutomaticExcludedModels = s.globalAutomaticExcludedModels(ctx)
		}
	}
	if !forceModelFound {
		var decision router.Decision
		var passthrough bool
		var err error
		ctx, decision, passthrough, err = s.passthroughDecision(ctx, req)
		if err != nil {
			return res, err
		}
		if passthrough {
			res.Decision = decision
			res.CallerModelPassthrough = true
			return res, nil
		}
	}
	if res.Strategy == router.StrategyLLMClassifier {
		return s.runClassifierTurn(ctx, req, res, threadSessionKey)
	}
	log.Info("turnloop classified",
		"turn_type", string(res.TurnType),
		"requested_tier", res.RequestedTier.String(),
		"pin_role", res.PinRole,
		"sub_agent_hint", subAgentHint,
	)

	automaticProbe := res.TurnType == turntype.Probe && !s.explicitUtilityHardPin &&
		(req.RequestedModel == "" || req.RequestedModel == automaticProbeModel || router.IsRoutingModel(req.RequestedModel))
	if _, pinned := router.HonouredPolicyPin(ctx); pinned && (!s.isHardPinnedTurn(ctx, res.TurnType) || automaticProbe) {
		if s.pinStore != nil && !isUnpinnedScoredTurn(res.TurnType) && !automaticProbe {
			res.SessionKey = threadSessionKey
			_, _, res.SessionFirstTurn = s.loadPinWithStoreState(ctx, res.SessionKey, res.PinRole)
		}
		req.PolicyTurnContext = buildPolicyTurnContext(req, res, sessionpin.Pin{}, sessionpin.Pin{}, s.clock.Now())
		decision, err := s.routeFor(ctx, req)
		if err != nil {
			return res, err
		}
		res.Decision = decision
		res.Fresh = decision
		res.PinTier = policyPinTier
		log.Info("turnloop served by policy pin", "decision_model", decision.Model, "decision_provider", decision.Provider)
		return res, nil
	}

	sessionForceControlFound := forceModelFound

	hardPinnedTurn := s.isHardPinnedTurn(ctx, res.TurnType)
	if s.pinStore != nil && hardPinnedTurn && !forceModelFound && !forceModelCleared {
		legacyPin, found := s.loadPin(ctx, threadSessionKey, res.PinRole)
		if found && isUserForcedReason(legacyPin.Reason) {
			binding, reason := s.forcedModelBinding(ctx, legacyPin.Model, legacyPin.Provider)
			if reason != "" {
				return res, &ForcedModelExcludedError{Model: legacyPin.Model, Reason: reason}
			}
			legacyPin.Provider = binding
			forceModelPin, forceModelFound = legacyPin, true
			req.ExcludedModels = s.readmitForcedModel(ctx, req, env, feats, forceModelPin)
		}
	}
	if _, policyPinned := router.HonouredPolicyPin(ctx); !forceModelFound && !policyPinned {
		decision, passthrough, err := s.blindExperimentPassthroughDecision(ctx, req)
		if err != nil {
			return res, err
		}
		if passthrough {
			res.Decision = decision
			res.CallerModelPassthrough = true
			return res, nil
		}
	}
	if hardPinnedTurn && !automaticProbe {
		purpose, registered := utilityPurposes[res.TurnType]
		if !registered {
			return res, fmt.Errorf("hard-pinned turn type %q has no inference purpose", res.TurnType)
		}
		res.Purpose = purpose
	}
	if forceModelFound && hardPinnedTurn {
		if forcedPinEligible(forceModelPin, req) {
			res.Purpose = utilityPurposes[res.TurnType]
			threadPin, hmmHistory, forceHistory := sessionpin.Pin{}, sessionpin.Pin{}, sessionpin.Pin{}
			if s.pinStore != nil {
				threadPin, _ = s.loadPin(ctx, threadSessionKey, res.PinRole)
				hmmHistory = s.loadHMMHistory(ctx, threadSessionKey, res.PinRole)
				forceHistory = s.loadForceModelHistory(ctx, threadSessionKey, res.PinRole)
			}
			res.SessionKey = threadSessionKey
			res.PinModel = forceModelPin.Model
			res.PinAgeSec = pinAge(forceModelPin, s.clock.Now())
			res.applySwitchHistory(threadPin, hmmHistory, forceHistory)
			res.EscalateEffort = !forceHistory.LastTurnEndedAt.IsZero() &&
				(forceHistory.LastOutputTokens == 0 || forceHistory.ConsecutiveUpstreamErrors > 0)
			res.Decision = pinDecision(forceModelPin)
			res.Decision.Reason = translate.ReasonUserForceModel
			res.StickyHit = true
			res.PinTier = translate.ReasonUserForceModel
			s.anchorForceModelHistory(ctx, installationID, threadSessionKey, res.PinRole, forceModelPin)
			return res, nil
		}
		res.SessionKey = threadSessionKey
		res.ForcedPinDropped = true
		res.ForcedPinModel = forceModelPin.Model
		res.ForcedPinDropReason = forcedPinIneligibilityReason(forceModelPin, req)
		log.Info("Forced session pin dropped for hard-pinned turn",
			"pin_model", forceModelPin.Model,
			"pin_provider", forceModelPin.Provider,
			"drop_reason", res.ForcedPinDropReason,
			"enabled_providers", sortedEnabledKeys(req.EnabledProviders),
			"role", res.PinRole,
		)
	}

	if res.TurnType == turntype.Probe && !s.explicitUtilityHardPin {
		res.SessionKey = [sessionpin.SessionKeyLen]byte{}
		if automaticProbe {
			return s.routeWithoutPin(ctx, req, res, reqHeaders, forceModelFound, forceModelPin)
		}
		decision, err := s.callerModelPassthroughDecision(ctx, req)
		if err != nil {
			return res, err
		}
		res.Decision = decision
		res.CallerModelPassthrough = true
		res.Origin = policy.OverrideSourceRequest
		return res, nil
	}

	if hardPinnedTurn {
		provider, model := s.hardPinProvider, s.hardPinModel
		useSubAgentOverride := res.TurnType == turntype.SubAgentDispatch && s.hasSubAgentOverride()
		if useSubAgentOverride {
			provider, model = s.subAgentProvider, s.subAgentModel
		}
		hardPinReq := HardPinRequest{
			EnabledProviders: req.EnabledProviders,
			ExcludedModels:   req.ExcludedModels,
			CustomBindings:   req.CustomBindings,
			GatewayProviders: req.GatewayProviders,
		}
		switch {
		case s.hardPinResolver != nil && !useSubAgentOverride:
			p, m, ok := s.hardPinResolver(withAutomaticExclusions(hardPinReq, req.AutomaticExcludedModels))
			if !ok {
				p, m, ok = s.hardPinResolver(hardPinReq)
				if ok {
					log.Warn("Hard-pin: automatic-routing exclusions left no candidate; ignoring them for this turn",
						"turn_type", string(res.TurnType),
						"hard_pin_model", m,
						"hard_pin_provider", p,
					)
				}
			}
			if !ok {
				hardPinErr := router.ErrNoEligibleProvider
				switch {
				case s.hardPinBlockedByContextWindow(hardPinReq, req):
					hardPinErr = policy.ErrContextWindowExceeded
				case len(req.GatewayProviders) > 0:
					hardPinErr = policy.ErrGatewayServesNoDeployedModel
				}
				log.Warn(
					"Hard-pin: no eligible provider for request",
					"turn_type", string(res.TurnType),
					"enabled_providers", sortedEnabledKeys(req.EnabledProviders),
					"err", hardPinErr,
				)
				return res, fmt.Errorf("hard-pin: no eligible provider for %s: %w", res.TurnType, hardPinErr)
			}
			provider, model = p, m
		default:
			if req.EnabledProviders != nil {
				if _, enabled := req.EnabledProviders[provider]; !enabled {
					return res, fmt.Errorf("hard-pin provider %q is ineligible for %s: %w", provider, res.TurnType, router.ErrNoEligibleProvider)
				}
			}
			if _, excluded := req.ExcludedModels[model]; excluded {
				return res, fmt.Errorf("hard-pin model %q is ineligible for %s: %w", model, res.TurnType, router.ErrNoEligibleProvider)
			}
			if automaticallyDisabled(req, model) {
				log.Warn("Hard-pin model is disabled for automatic routing; serving it anyway",
					"turn_type", string(res.TurnType),
					"hard_pin_model", model,
					"hard_pin_provider", provider,
				)
			}
		}
		hardDecision := router.Decision{
			Provider: provider,
			Model:    model,
			Reason:   string(res.TurnType) + "_hard_pin",
		}
		res.Decision = hardDecision
		res.StickyHit = true
		res.HardPinned = true
		res.Origin = policy.OverrideSourceDeployment
		res.PinTier = string(res.TurnType) + "_hard_pin"
		return res, nil
	}

	if !forceModelFound && env.IsNativeWebSearchSubTurn() {
		if decision, ok := s.usageBypassDecision(ctx, reqHeaders, req, nil, res.TurnType); ok {
			res.SessionKey = threadSessionKey
			res.Decision = decision
			res.UsageBypass = true
			return res, nil
		}

		passthroughModel := s.baselineFor(ctx, req.RequestedModel)
		_, excluded := req.ExcludedModels[passthroughModel]
		_, safetyExcluded := req.SafetyExcludedModels[passthroughModel]
		_, allowlisted := req.AllowedModels[passthroughModel]
		allowed := req.AllowedModels == nil || allowlisted
		binding, bindingFound := catalog.ResolveBindingWithCustom(
			passthroughModel,
			req.EnabledProviders,
			req.CustomBindings,
		)
		if !excluded && !safetyExcluded && allowed && bindingFound && binding.Provider == providers.ProviderAnthropic {
			res.SessionKey = threadSessionKey
			res.Decision = router.Decision{
				Provider: binding.Provider,
				Model:    passthroughModel,
				Reason:   nativeWebSearchPassthroughReason,
			}
			res.HardPinned = true
			res.PinTier = nativeWebSearchPassthroughReason
			log.Info("Native web-search sub-turn passed through to requested model",
				"model", passthroughModel,
				"provider", binding.Provider,
			)
			return res, nil
		}
	}

	if isUnpinnedScoredTurn(res.TurnType) {
		return s.routeWithoutPin(ctx, req, res, reqHeaders, forceModelFound, forceModelPin)
	}
	if res.TurnType == turntype.Compaction {
		return s.routeCompaction(ctx, req, res, reqHeaders, threadSessionKey, installationID, forceModelFound, forceModelPin)
	}

	sessionKey := threadSessionKey

	res.PrefixTrimmed = s.compaction.CheckAndRecord(
		sessionKey, installationID, res.PinRole,
		feats.MessageCount, len(env.AssistantToolCallSignatures()),
	)
	prefixBroken := s.ResolvePrefixTrimFreeSwitch(ctx) && res.PrefixTrimmed
	res.PrefixBroken = prefixBroken
	if res.PrefixTrimmed {
		log.Info("turnloop detected client history trim",
			"message_count", feats.MessageCount,
			"free_switch_armed", prefixBroken,
		)
	}

	if s.pinStore == nil {
		return s.routeWithoutPin(ctx, req, res, reqHeaders, forceModelFound, forceModelPin)
	}

	res.SessionKey = sessionKey

	pin, pinFound, noStoredState := s.loadPinWithStoreState(ctx, res.SessionKey, res.PinRole)
	res.SessionFirstTurn = noStoredState
	clearedPinReason := ""
	if !pinFound && pin.Model == "" && pin.Provider == "" && pin.Reason != "" {
		clearedPinReason = pin.Reason
	}
	hmmHistory := s.loadHMMHistory(ctx, res.SessionKey, res.PinRole)
	forceHistory := sessionpin.Pin{}
	if forceModelFound || forceModelCleared {
		forceHistory = s.loadForceModelHistory(ctx, res.SessionKey, res.PinRole)
	}
	if !forceModelFound && !forceModelCleared && pinFound && isUserForcedReason(pin.Reason) {
		binding, reason := s.forcedModelBinding(ctx, pin.Model, pin.Provider)
		if reason != "" {
			return res, &ForcedModelExcludedError{Model: pin.Model, Reason: reason}
		}
		pin.Provider = binding
		forceModelPin, forceModelFound = pin, true
		req.ExcludedModels = s.readmitForcedModel(ctx, req, env, feats, forceModelPin)
	}
	if forceModelCleared && pinFound && isUserForcedReason(pin.Reason) {
		pinFound = false
		pin = sessionpin.Pin{}
	}
	commandContinuation := sessionpin.Pin{}
	commandContinuationFound := false
	if res.TurnType == turntype.MainLoop {
		commandContinuation, commandContinuationFound = s.consumePostCommandContinuation(ctx, res.SessionKey, res.PinRole)
	}
	if pinFound && isUserForcedReason(pin.Reason) {
		binding, reason := s.forcedModelBinding(ctx, pin.Model, pin.Provider)
		if reason != "" {
			log.Warn("turnloop: forced pin refers to an excluded model",
				"pin_model", pin.Model,
				"pin_provider", pin.Provider,
				"reason", reason,
			)
			return res, &ForcedModelExcludedError{Model: pin.Model, Reason: reason}
		}
		pin.Provider = binding
	}
	disabledProviders := mergeSessionStrikes(pin.DisabledProviders, hmmHistory.DisabledProviders)
	forcedProvider := ""
	if forceModelFound {
		forcedProvider = forceModelPin.Provider
	} else if pinFound && isUserForcedReason(pin.Reason) {
		forcedProvider = pin.Provider
	}
	if forcedProvider != "" {
		filtered := make([]string, 0, len(disabledProviders))
		for _, provider := range disabledProviders {
			if provider != forcedProvider {
				filtered = append(filtered, provider)
			}
		}
		disabledProviders = filtered
	}
	if len(disabledProviders) > 0 {
		res.SessionDisabledProviders = disabledProviders
		if req.EnabledProviders != nil {
			filtered := make(map[string]struct{}, len(req.EnabledProviders))
			for p := range req.EnabledProviders {
				filtered[p] = struct{}{}
			}
			for _, p := range disabledProviders {
				delete(filtered, p)
			}
			req.EnabledProviders = filtered
		}
	}
	demoted := mergeSessionStrikes(pin.DemotedModels, hmmHistory.DemotedModels)
	if turnStrikes := mergeSessionStrikes(pin.TurnStrikes, hmmHistory.TurnStrikes); len(turnStrikes) > 0 {
		if routingEndsWithUserPrompt(ctx, env) {
			s.clearTurnStrikes(ctx, res.SessionKey, res.PinRole)
		} else {
			demoted = mergeSessionStrikes(demoted, turnStrikes)
		}
	}
	res.SessionStrikeReadmitModels = harnessSafeModels(imageSafeModels(demoted, req.ImageCount), req.HasTools)
	if s.ResolveTransientRateLimit(ctx) && (!forceModelFound || !forcedPinEligible(forceModelPin, req)) {
		cooldowns := mergeDemotionCooldowns(pin.DemotionCooldowns, hmmHistory.DemotionCooldowns)
		now := s.clock.Now()
		cooling := activeDemotionCooldowns(cooldowns, now)
		if len(cooling) > 0 {
			res.SessionCooldownModels = readmittableCooldowns(cooling, demoted, req.ImageCount)
			demoted = mergeSessionStrikes(demoted, cooldownsByExpiry(cooling))
		}
		expired := expiredDemotionCooldowns(cooldowns, now)
		probed := false
		for _, model := range cooldownsByExpiry(expired) {
			if slices.Contains(demoted, model) ||
				modelExcludedForRecoveryProbe(req, model) {
				continue
			}
			if !probed {
				if release, acquired := s.acquireRecoveryProbe(ctx, res.SessionKey, model); acquired {
					res.CooldownProbeReleases = append(res.CooldownProbeReleases, release)
					res.CooldownProbes = map[string]time.Time{model: expired[model]}
					probed = true
					continue
				}
			}
			demoted = mergeSessionStrikes(demoted, []string{model})
			res.CooldownProbeDeniedModels = append(res.CooldownProbeDeniedModels, model)
			req.ExcludedModels = excludingModel(req.ExcludedModels, model)
			ctx = context.WithValue(ctx, SessionCooldownProbeDeniedModelsContextKey{}, res.CooldownProbeDeniedModels)
		}
	}
	if len(demoted) > 0 {
		res.SessionDemotedModels = demoted
		for _, model := range demoted {
			req.AutomaticExcludedModels = addToSet(req.AutomaticExcludedModels, model)
		}
	}
	res.applySwitchHistory(pin, hmmHistory, forceHistory)
	req, res.ObservedOverflowModels = s.excludeObservedOverflowModels(ctx, req, res, max(contextWindowOutputReserve, feats.MaxTokens))
	req.PolicyTurnContext = buildPolicyTurnContext(req, res, pin, hmmHistory, s.clock.Now())
	res.EscalateEffort = pinFound && !pin.LastTurnEndedAt.IsZero() &&
		(pin.LastOutputTokens == 0 || pin.ConsecutiveUpstreamErrors > 0)
	if pinFound {
		res.PinTurnCount = pin.TurnCount + 1
		res.PinFirstPinnedAt = pin.FirstPinnedAt
		applyPinEvidence(&res, pin, s.clock.Now())
		log.Info("turnloop pin lookup hit",
			"pin_model", pin.Model,
			"pin_provider", pin.Provider,
			"pin_reason", pin.Reason,
			"pin_age_s", res.PinAgeSec,
			"pin_cache_warm", cacheWarm(pin, s.clock.Now()),
			"last_output_tokens", pin.LastOutputTokens,
			"session_ever_switched", pin.HasEverSwitched,
		)
	} else {
		log.Info("turnloop pin lookup miss", "role", res.PinRole)
	}

	forcedTierFloor := catalog.TierUnknown
	if forceModelFound {
		_, excluded := req.ExcludedModels[forceModelPin.Model]
		_, providerEnabled := req.EnabledProviders[forceModelPin.Provider]
		providerEligible := req.EnabledProviders == nil || providerEnabled
		imageCapable := forcedPinServesImages(forceModelPin, req)
		if _, estimateOnly := req.ContextWindowExcludedModels[forceModelPin.Model]; excluded && estimateOnly {
			log.Info("Forced session pin dispatched past the context-window estimate; the provider decides",
				"pin_model", forceModelPin.Model,
				"pin_provider", forceModelPin.Provider,
				"role", res.PinRole,
			)
			excluded = false
		}
		if _, imageLimitExcluded := req.ImageLimitExcludedModels[forceModelPin.Model]; imageLimitExcluded {
			log.Info("Forced session pin dispatched past the image-count cap; the provider decides",
				"pin_model", forceModelPin.Model,
				"pin_provider", forceModelPin.Provider,
				"role", res.PinRole,
			)
			excluded = false
		}
		if !excluded && providerEligible && imageCapable {
			res.PinModel = forceModelPin.Model
			res.PinAgeSec = pinAge(forceModelPin, s.clock.Now())
			res.EscalateEffort = !forceHistory.LastTurnEndedAt.IsZero() &&
				(forceHistory.LastOutputTokens == 0 || forceHistory.ConsecutiveUpstreamErrors > 0)
			res.Decision = pinDecision(forceModelPin)
			res.Decision.Reason = translate.ReasonUserForceModel
			res.StickyHit = true
			res.PinTier = translate.ReasonUserForceModel
			s.anchorForceModelHistory(ctx, installationID, res.SessionKey, res.PinRole, forceModelPin)
			return res, nil
		}
		res.ForcedPinDropped = true
		res.ForcedPinDropReason = forcedPinIneligibilityReason(forceModelPin, req)
		res.ForcedPinModel = forceModelPin.Model
		log.Info("Forced session pin dropped for this turn",
			"pin_model", forceModelPin.Model,
			"pin_provider", forceModelPin.Provider,
			"drop_reason", res.ForcedPinDropReason,
			"enabled_providers", sortedEnabledKeys(req.EnabledProviders),
			"role", res.PinRole,
		)
		if excluded || !imageCapable {
			forcedTierFloor = catalog.TierFor(forceModelPin.Model)
		}
		if !imageCapable {
			req.ExcludedModels = excludingModel(req.ExcludedModels, forceModelPin.Model)
		}
		if !sessionForceControlFound || isUserForcedReason(pin.Reason) {
			pinFound = false
			pin = sessionpin.Pin{}
		}
	}
	if pinFound && (isUserForcedReason(pin.Reason) || pin.Reason == translate.ReasonLoopEscalation) {
		_, excluded := req.ExcludedModels[pin.Model]
		_, providerEnabled := req.EnabledProviders[pin.Provider]
		providerEligible := req.EnabledProviders == nil || providerEnabled
		imageCapable := pinServesImages(pin, req)
		autoDisabled := !isUserForcedReason(pin.Reason) && automaticallyDisabled(req, pin.Model)
		if !excluded && !autoDisabled && providerEligible && imageCapable {
			decision := pinDecision(pin)
			decision.Reason = pin.Reason
			res.PinTier = pin.Reason
			res.Decision = decision
			res.StickyHit = true
			s.refreshPin(ctx, installationID, res.SessionKey, pin, res.PinRole, pinDecision(pin))
			return res, nil
		}
		dropReason := "excluded"
		switch {
		case autoDisabled:
			dropReason = "automatic_routing_disabled"
		case !providerEligible:
			dropReason = "provider_not_enabled"
		case !imageCapable:
			dropReason = "not_image_capable"
		}
		log.Info("Forced session pin dropped for this turn",
			"pin_model", pin.Model,
			"pin_provider", pin.Provider,
			"pin_reason", pin.Reason,
			"drop_reason", dropReason,
			"enabled_providers", sortedEnabledKeys(req.EnabledProviders),
			"role", res.PinRole,
		)
		if isUserForcedReason(pin.Reason) {
			res.ForcedPinDropped = true
			res.ForcedPinDropReason = dropReason
			res.ForcedPinModel = pin.Model
			if excluded || !imageCapable {
				forcedTierFloor = catalog.TierFor(pin.Model)
			}
		} else if excluded || autoDisabled {
			evictReason := "escalation_pin_excluded"
			if autoDisabled {
				evictReason = "escalation_pin_automatic_routing_disabled"
			}
			if err := s.expireSessionPin(ctx, installationID, res.SessionKey, res.PinRole, evictReason); err != nil {
				log.Error("ineligible escalation pin eviction failed", "err", err, "pin_model", pin.Model, "role", res.PinRole, "evict_reason", evictReason)
			}
		}
		if !imageCapable {
			req.ExcludedModels = excludingModel(req.ExcludedModels, pin.Model)
		}
		pinFound = false
		pin = sessionpin.Pin{}
	}

	if pinFound && !isUserForcedReason(pin.Reason) && automaticallyDisabled(req, pin.Model) {
		reason, _ := s.globalAutomaticExclusionReason(ctx, pin.Model)
		log.Info("Session pin model is disabled for automatic routing; falling through to scorer",
			"pin_model", pin.Model,
			"pin_provider", pin.Provider,
			"pin_reason", pin.Reason,
			"disable_reason", reason,
			"role", res.PinRole,
		)
		pinFound = false
		pin = sessionpin.Pin{}
	}

	if maxedModel := maxedOutServedModel(pin); pinFound && maxedModel != "" {
		log.Info("Session pin maxed out on previous turn; excluding for this turn",
			"pin_model", pin.Model,
			"pin_provider", pin.Provider,
			"maxed_model", maxedModel,
			"last_output_tokens", pin.LastOutputTokens,
		)
		excluded := make(map[string]struct{}, len(req.ExcludedModels)+1)
		for k := range req.ExcludedModels {
			excluded[k] = struct{}{}
		}
		excluded[maxedModel] = struct{}{}
		req.ExcludedModels = excluded
		req.SafetyExcludedModels = addToSet(req.SafetyExcludedModels, maxedModel)
		pinFound = false
		pin = sessionpin.Pin{}
	}
	if maxedModel := maxedOutServedModel(hmmHistory); maxedModel != "" {
		log.Info("HMM history maxed out on previous turn; excluding for this turn",
			"history_provider", hmmHistory.Provider,
			"maxed_model", maxedModel,
			"last_output_tokens", hmmHistory.LastOutputTokens,
		)
		excluded := make(map[string]struct{}, len(req.ExcludedModels)+1)
		for k := range req.ExcludedModels {
			excluded[k] = struct{}{}
		}
		excluded[maxedModel] = struct{}{}
		req.ExcludedModels = excluded
		req.SafetyExcludedModels = addToSet(req.SafetyExcludedModels, maxedModel)
		if pinFound && baseModelOf(pin.Model) == maxedModel {
			pinFound = false
			pin = sessionpin.Pin{}
		}
	}

	if pinFound && advisorRejectsModel(env, pin.Model) {
		log.Info("Session pin dropped: the request's advisor tool cannot advise the pinned model",
			"pin_model", pin.Model,
			"advisor_model", env.AdvisorToolModel(),
		)
		pinFound = false
		pin = sessionpin.Pin{}
	}

	if pinFound {
		if _, overCapacity := req.ExcludedModels[pin.Model]; overCapacity {
			outputReserveForPin := contextWindowOutputReserve
			if feats.MaxTokens > outputReserveForPin {
				outputReserveForPin = feats.MaxTokens
			}
			pinTokenEstimate := env.ContextOverflowTokenEstimate()
			if modelStripsAnthropicSignatures(pin.Model) {
				pinTokenEstimate -= env.SignatureTokenSavings()
			}
			if projected, ok := res.TokenizerCalibration.ProjectedPrompt(tokenizerFamilyFor(pin.Provider), res.ContextEstimate); ok && projected > pinTokenEstimate {
				pinTokenEstimate = projected
			}
			needed := pinTokenEstimate + outputReserveForPin
			modelCW := gatewayContextWindow(cortexGatewayProvidersForRequest(ctx), pin.Model, pin.Provider)
			if needed > modelCW {
				log.Info("Session pin excluded by context-window pre-filter; falling through to scorer",
					"pin_model", pin.Model,
					"pin_provider", pin.Provider,
					"token_estimate", pinTokenEstimate,
					"needed", needed,
					"model_context_window", modelCW,
				)
				pinFound = false
				pin = sessionpin.Pin{}
			} else {
				policyExcluded := s.excludedModelsForRequest(ctx)
				_, policyExcludes := policyExcluded[pin.Model]
				if !policyExcludes && !compatibilityExcludesModel(req, pin.Model) {
					if len(req.ExcludedModels) > 0 {
						pruned := make(map[string]struct{}, len(req.ExcludedModels)-1)
						for k := range req.ExcludedModels {
							if k != pin.Model {
								pruned[k] = struct{}{}
							}
						}
						req.ExcludedModels = pruned
					}
					delete(req.SafetyExcludedModels, pin.Model)
				}
				log.Info("Session pin preserved despite context-window pre-filter exclusion",
					"pin_model", pin.Model,
					"token_estimate", pinTokenEstimate,
					"needed", needed,
					"model_context_window", modelCW,
				)
			}
		}
	}

	if pinFound && !modelInRequestSubset(ctx, pin.Model) {
		log.Info("Session pin outside request allowed-models subset; falling through to scorer",
			"pin_model", pin.Model,
			"pin_provider", pin.Provider,
		)
		pinFound = false
	}

	if pinFound && req.EnabledProviders != nil {
		if _, ok := req.EnabledProviders[pin.Provider]; !ok {
			log.Info("Session pin provider not in enabled set; falling through to scorer",
				"pin_model", pin.Model,
				"pin_provider", pin.Provider,
			)
			pinFound = false
			pin = sessionpin.Pin{}
		}
	}

	if pinFound && req.HasImages && (!catalog.AcceptsImages(pin.Model) ||
		req.ImageCount > 0 && !catalog.AcceptsImageCount(pin.Model, req.ImageCount, req.EnabledProviders)) {
		log.Info("Session pin is text-only for image-bearing turn; falling through to scorer",
			"pin_model", pin.Model,
			"pin_provider", pin.Provider,
		)
		pinFound = false
		pin = sessionpin.Pin{}
	}
	if !pinFound {
		clearPinEvidence(&res)
	}

	if commandContinuationFound && clearedPinReason != "" {
		log.Info("discarding post-command continuation after source pin clear",
			"clear_reason", clearedPinReason,
			"pin_model", commandContinuation.Model,
			"pin_provider", commandContinuation.Provider,
		)
		commandContinuationFound = false
	}
	if commandContinuationFound && automaticPinEligible(commandContinuation, req) {
		decision := pinDecision(commandContinuation)
		res.Decision = decision
		res.StickyHit = true
		res.PinTier = "post_command_continuation"
		res.PinModel = commandContinuation.Model
		res.PinAgeSec = pinAge(commandContinuation, s.clock.Now())
		if forceModelCleared {
			res.applySwitchHistory(commandContinuation, hmmHistory, forceHistory, forceModelPin)
		} else {
			res.applySwitchHistory(commandContinuation, hmmHistory, forceHistory)
		}
		res.EscalateEffort = !commandContinuation.LastTurnEndedAt.IsZero() &&
			(commandContinuation.LastOutputTokens == 0 || commandContinuation.ConsecutiveUpstreamErrors > 0)
		log.Info("turnloop used one-shot post-command continuation",
			"pin_model", commandContinuation.Model,
			"pin_provider", commandContinuation.Provider,
		)
		s.refreshPin(ctx, installationID, res.SessionKey, commandContinuation, res.PinRole, decision)
		return res, nil
	}

	if dec, ok := s.usageBypassDecision(ctx, reqHeaders, req, res.SessionDemotedModels, res.TurnType); ok {
		res.Decision = dec
		res.UsageBypass = true
		return res, nil
	}

	if pinFound && res.AuthoritativePerTurn && req.ForceCluster == "" &&
		escalation.Rank(escalation.Group(pin.PolicyGroup)) >= 0 && s.ResolveAuthoritativeUpgradeSlidingThreshold(ctx) {
		req.SlidingThresholdPinGroup = escalation.Group(pin.PolicyGroup)
	}
	baselineRequest := req
	baselineTurn := res
	escalationTurn := s.beginEscalation(ctx, env, req, &res, apiKeyID)
	if escalationTurn != nil {
		req.Escalation = escalationTurn.constraint()
	}
	llmTurn := s.beginLLMEscalation(ctx, env, req, &res, apiKeyID)
	res.llmEscalation = llmTurn
	if llmTurn != nil && llmTurn.active {
		req.Escalation = llmTurn.constraint()
	}
	if pinFound {
		req.PreviousPolicyGroup = escalation.Group(pin.PolicyGroup)
	}

	routeRemaining := func() (turnLoopResult, error) {
		holdToolResult := res.AuthoritativePerTurn && req.Escalation == nil &&
			res.TurnType == turntype.ToolResult && s.ResolveHMMToolResultHold(ctx)
		if holdToolResult {
			res.AuthoritativePerTurn = false
		}
		if req.Escalation == nil && pinFound && automaticPinEligible(pin, req) && req.ConversationMessages != nil &&
			router.IsHMMStrategy(router.StrategyFromContext(ctx)) &&
			!hasTextUserBoundary(req.ConversationMessages) {
			if _, honoured := router.HonouredPolicyPin(ctx); !honoured {
				decision := pinDecision(pin)
				decision.Reason = unscorableHMMStickyReason
				res.Decision = decision
				res.StickyHit = true
				res.PinTier = unscorableHMMStickyReason
				log.Info("HMM turn has no user text; preserving eligible session pin",
					"pin_model", pin.Model, "pin_provider", pin.Provider)
				s.refreshPin(ctx, installationID, res.SessionKey, pin, res.PinRole, pinDecision(pin))
				return res, nil
			}
		}
		if req.Escalation == nil && !res.AuthoritativePerTurn &&
			(!s.ResolveScoreToolResultTurns(ctx) || holdToolResult) &&
			res.TurnType == turntype.ToolResult &&
			pinFound {
			decision := pinDecision(pin)
			res.Decision = decision
			res.StickyHit = true
			res.PinTier = "postgres_tool_result_sc"
			s.refreshPin(ctx, installationID, res.SessionKey, pin, res.PinRole, decision)
			return res, nil
		}

		if req.Escalation == nil && !res.AuthoritativePerTurn && !s.ResolvePlannerEnabled(ctx) && pinFound {
			decision := pinDecision(pin)
			res.Decision = decision
			res.StickyHit = true
			res.PinTier = "postgres"
			s.refreshPin(ctx, installationID, res.SessionKey, pin, res.PinRole, decision)
			return res, nil
		}

		var fresh router.Decision
		routed := false
		if forcedTierFloor != catalog.TierUnknown {
			if constrained, ok := s.restrictToTier(req.ExcludedModels, forcedTierFloor); ok {
				tierReq := req
				tierReq.ExcludedModels = constrained
				if dec, derr := s.routeFor(ctx, tierReq); derr == nil {
					fresh, routed = dec, true
					log.Info("user-forced model evicted; rerouted to next-best in same tier",
						"forced_tier", forcedTierFloor.String(),
						"fresh_model", dec.Model,
						"fresh_provider", dec.Provider,
					)
				} else if res.AuthoritativePerTurn {
					return res, derr
				} else {
					log.Info("tier-constrained reroute found no candidate; using unconstrained scorer",
						"forced_tier", forcedTierFloor.String(), "err", derr)
				}
			}
		}
		if !routed {
			dec, err := s.routeFor(ctx, req)
			if err != nil {
				if s.policyDeadlineFallback && isPolicyDeadlineErr(err) {
					if pinFound && pin.Model != "" {
						decision := pinDecision(pin)
						decision.Reason = policyDeadlineFallbackReason
						res.Decision = decision
						res.StickyHit = true
						res.PinTier = policyDeadlineFallbackReason
						res.PolicyFallback = true
						log.Warn("policy sidecar missed its deadline; serving session pin",
							"err", err,
							"pin_model", pin.Model,
							"pin_provider", pin.Provider,
							"pin_policy_group", pin.PolicyGroup,
							"requested_model", req.RequestedModel,
						)
						refreshed := decision
						refreshed.Reason = pin.Reason
						s.refreshPin(ctx, installationID, res.SessionKey, pin, res.PinRole, refreshed)
						return res, nil
					}
					if decision, ok := s.policyDeadlineDefaultDecision(req); ok {
						res.Decision = decision
						res.PinTier = policyDeadlineFallbackReason
						res.PolicyFallback = true
						log.Warn("policy sidecar missed its deadline; no session pin, serving tier-3 default",
							"err", err,
							"default_model", decision.Model,
							"default_provider", decision.Provider,
							"requested_model", req.RequestedModel,
						)
						s.writeNewPin(ctx, installationID, res.SessionKey, res.PinRole, decision)
						return res, nil
					}
				}
				log.Error("turnloop scorer failed", "err", err, "requested_model", req.RequestedModel)
				return res, err
			}
			fresh = dec
		}
		log.Info("turnloop scorer decision",
			"fresh_model", fresh.Model,
			"fresh_provider", fresh.Provider,
			"fresh_reason", fresh.Reason,
		)
		res.Fresh = fresh
		if escalationRoutingApplied(fresh) {
			res.Decision = fresh
			res.PinTier = "escalation_xgb"
			if llmTurn != nil && llmTurn.active {
				res.PinTier = llmEscalationPinTier
				if llmTurn.session.Config.EffectiveClassifier() == flags.EscalationClassifierLLM {
					res.PinTier = qwenEscalationPinTier
				}
			}
			return res, nil
		}
		if res.AuthoritativePerTurn {
			activePin := sessionpin.Pin{}
			if pinFound {
				activePin = pin
			}
			plannerTokens := s.plannerTokensFor(ctx, env, feats)
			res.UpgradeShadow = s.authoritativeUpgradeFor(ctx, req, activePin, fresh, res, plannerTokens)
			res.AuthorityShadow = s.authorityCacheShadowFor(
				ctx, req, activePin, hmmHistory, fresh, plannerTokens, prefixBroken,
			)
			s.logAuthorityCacheShadow(ctx, res)
			if res.UpgradeShadow != nil && res.UpgradeShadow.Verdict.Reason == upgradeDemotedModel {
				if pinFound && automaticPinEligible(pin, req) {
					decision := pinDecision(pin)
					res.Decision = decision
					res.StickyHit = true
					res.PinTier = string(pinTierAuthoritativeExcludedPin)
					log.Warn("authoritative policy returned an excluded model; keeping session pin",
						"pin_model", pin.Model,
						"fresh_model", fresh.Model,
						"reason", res.UpgradeShadow.Verdict.Reason,
					)
					s.refreshPin(ctx, installationID, res.SessionKey, pin, res.PinRole, decision)
					return res, nil
				}

				rerouteReq := req
				rerouted, rerouteErr := s.routeFor(ctx, rerouteReq)
				if rerouteErr != nil {
					return res, rerouteErr
				}
				reroutedPin := sessionpin.Pin{Model: rerouted.Model, Provider: rerouted.Provider}
				available := s.availableModels == nil
				if s.availableModels != nil {
					_, available = s.availableModels[rerouted.Model]
				}
				if !available || !pinEligible(reroutedPin, rerouteReq) {
					return res, fmt.Errorf("authoritative policy returned excluded model %q after reroute: %w", rerouted.Model, router.ErrNoEligibleProvider)
				}
				res.Fresh = rerouted
				res.Decision = rerouted
				res.PinTier = string(pinTierAuthoritativeExcludedReroute)
				log.Warn("authoritative policy returned an excluded model; serving rerouted decision",
					"excluded_model", fresh.Model,
					"rerouted_model", rerouted.Model,
					"reason", res.UpgradeShadow.Verdict.Reason,
				)
				s.writeNewPin(ctx, installationID, res.SessionKey, res.PinRole, rerouted)
				return res, nil
			}
			slidingServed, slidingProbability := slidingThresholdServes(req, pin, fresh)
			if slidingServed {
				log.Info("authoritative sliding threshold serving fresh model",
					"pin_model", pin.Model,
					"pin_group", req.SlidingThresholdPinGroup,
					"fresh_model", fresh.Model,
					"fresh_group", fresh.Metadata.PolicyGroup,
					"classified_group", fresh.Metadata.ClassifierPredictedLabel,
					"probability", slidingProbability,
				)
			}
			if !slidingServed && s.evidenceUpgradeApplies(ctx, res.SessionKey) && res.UpgradeShadow != nil {
				res.UpgradeShadow.Applied = true
				switch res.UpgradeShadow.Verdict.Outcome {
				case upgradeAllow, upgradeExistingPolicy, upgradeNotApplicable, upgradeExcluded:
				case upgradeHold:
					decision := pinDecision(pin)
					res.Decision = decision
					res.StickyHit = true
					res.PinTier = string(pinTierAuthoritativeUpgradeEvidence)
					votes := *res.UpgradeShadow.Evidence.VoteCount
					log.Info("turnloop held authoritative upgrade on evidence; keeping session pin",
						"pin_model", pin.Model,
						"fresh_model", fresh.Model,
						"reason", res.UpgradeShadow.Verdict.Reason,
						"upgrade_votes", votes,
					)
					s.refreshPinVotes(ctx, installationID, res.SessionKey, pin, res.PinRole, decision, 0, votes)
					return res, nil
				}
			}
			evidenceServedFresh := s.evidenceUpgradeApplies(ctx, res.SessionKey) && res.UpgradeShadow != nil && res.UpgradeShadow.Verdict.Outcome == upgradeAllow
			upgradeGateActive := s.ResolveAuthoritativeUpgradeGate(ctx) && s.resolveUpgradePolicyMode(ctx) != flags.AuthoritativeUpgradePolicyOff && !evidenceServedFresh && !slidingServed
			if upgradeGateActive && pinFound && pin.Model != "" && pin.Model != fresh.Model &&
				hmmFreshIsMoreExpensive(pin.Model, fresh.Model, plannerTokens) {
				if confidence, ok := hmmDecisionConfidence(fresh); ok && confidence < s.hmmUpgradeConfidenceThreshold {
					decision := pinDecision(pin)
					res.Decision = decision
					res.StickyHit = true
					res.PinTier = string(pinTierAuthoritativeUpgradeConfidenceLow)
					log.Info("turnloop suppressed low-confidence authoritative upgrade; keeping session pin",
						"pin_model", pin.Model,
						"pin_provider", pin.Provider,
						"fresh_model", fresh.Model,
						"fresh_provider", fresh.Provider,
						"confidence", confidence,
						"threshold", s.hmmUpgradeConfidenceThreshold,
					)
					s.refreshPin(ctx, installationID, res.SessionKey, pin, res.PinRole, decision)
					return res, nil
				}
			}
			if pinFound && pin.Model != "" && pin.Model != fresh.Model &&
				!hmmFreshIsMoreExpensive(pin.Model, fresh.Model, plannerTokens) {
				hysteresisTurns := s.ResolveHMMDowngradeHysteresisTurns(ctx)
				confidence, scored := hmmDecisionConfidence(fresh)
				if s.ResolveAuthoritativeDowngradeGate(ctx) && scored && confidence < s.hmmUpgradeConfidenceThreshold {
					decision := pinDecision(pin)
					res.Decision = decision
					res.StickyHit = true
					res.PinTier = string(pinTierAuthoritativeDowngradeConfidenceLow)
					log.Info("turnloop suppressed low-confidence authoritative downgrade; keeping session pin",
						"pin_model", pin.Model,
						"pin_provider", pin.Provider,
						"fresh_model", fresh.Model,
						"fresh_provider", fresh.Provider,
						"confidence", confidence,
						"threshold", s.hmmUpgradeConfidenceThreshold,
						"downgrade_votes", pin.ConsecutiveDowngradeVotes,
						"hysteresis_turns", hysteresisTurns,
					)
					s.refreshPinDowngradeVotes(ctx, installationID, res.SessionKey, pin, res.PinRole, decision, pin.ConsecutiveDowngradeVotes)
					return res, nil
				}
				votes := pin.ConsecutiveDowngradeVotes + 1
				if hysteresisTurns > 0 && votes < hysteresisTurns {
					decision := pinDecision(pin)
					res.Decision = decision
					res.StickyHit = true
					res.PinTier = string(pinTierAuthoritativeDowngradeHysteresis)
					log.Info("turnloop held authoritative downgrade below the hysteresis threshold; keeping session pin",
						"pin_model", pin.Model,
						"pin_provider", pin.Provider,
						"fresh_model", fresh.Model,
						"fresh_provider", fresh.Provider,
						"confidence", confidence,
						"threshold", s.hmmUpgradeConfidenceThreshold,
						"downgrade_votes", votes,
						"hysteresis_turns", hysteresisTurns,
					)
					s.refreshPinDowngradeVotes(ctx, installationID, res.SessionKey, pin, res.PinRole, decision, votes)
					return res, nil
				}
				shadowTurns := s.ResolveHMMDowngradeHysteresisShadowTurns(ctx)
				res.DowngradeShadow = downgradeGuardShadow{
					Served:              true,
					Votes:               votes,
					HysteresisWouldHold: shadowTurns > 0 && votes < shadowTurns,
					ConfidenceWouldHold: scored && confidence < s.hmmUpgradeConfidenceThreshold,
				}
				log.Info("turnloop served authoritative downgrade",
					"pin_model", pin.Model,
					"pin_provider", pin.Provider,
					"fresh_model", fresh.Model,
					"fresh_provider", fresh.Provider,
					"confidence", confidence,
					"threshold", s.hmmUpgradeConfidenceThreshold,
					"downgrade_votes", votes,
					"hysteresis_turns", hysteresisTurns,
					"shadow_hysteresis_turns", shadowTurns,
					"shadow_hysteresis_would_hold", res.DowngradeShadow.HysteresisWouldHold,
					"shadow_confidence_would_hold", res.DowngradeShadow.ConfidenceWouldHold,
				)
			}
			res.Decision = fresh
			res.PinTier = "authoritative_per_turn"
			if slidingServed {
				res.PinTier = string(pinTierAuthoritativeSlidingThresholdUpgrade)
			}
			s.writeNewPin(ctx, installationID, res.SessionKey, res.PinRole, fresh)
			return res, nil
		}
		if isHMMDecision(fresh) {
			activePin := sessionpin.Pin{}
			if pinFound {
				activePin = pin
			}
			hmmDecision, hmmPlannerDecision, hmmSticky, hmmStayModel := s.hmmCostGatedDecision(
				ctx, req,
				activePin,
				hmmHistory,
				fresh,
				s.plannerTokensFor(ctx, env, feats),
				prefixBroken,
			)
			res.Decision = hmmDecision
			res.PlannerDecision = hmmPlannerDecision
			if heldEffort := effortHysteresisHold(fresh, res.PriorServedModel, hmmDecision.Model, hmmDecision.Effort); heldEffort != "" {
				res.Decision.Effort = heldEffort
				res.PlannerDecision.Reason = appendEffortHysteresisReason(res.PlannerDecision.Reason)
				log.Info("HMM effort hysteresis held incumbent",
					"incumbent", res.PriorServedModel,
					"challenger_effort", hmmDecision.Effort,
					"serving_effort", heldEffort,
				)
			}
			if hmmStayModel != "" {
				res.PinModel = hmmStayModel
				if hmmPin, ok := s.hmmStayPin(req, activePin, hmmHistory); ok && hmmPin.Model == hmmStayModel {
					applyPinEvidence(&res, hmmPin, s.clock.Now())
				}
			}
			if hmmSticky {
				res.StickyHit = true
				res.PinTier = "hmm_ev_stay_" + hmmPlannerDecision.Reason
				res.StickyRole = hmmHistoryRole(res.PinRole)
			} else {
				res.PinTier = "hmm_fresh_unpinned"
				if hmmPlannerDecision.Outcome == planner.OutcomeStay && hmmPlannerDecision.Reason != "" {
					res.PinTier = "hmm_ev_same_" + hmmPlannerDecision.Reason
				} else if hmmPlannerDecision.Reason != "" && hmmPlannerDecision.Reason != planner.ReasonNoPin {
					res.PinTier = "hmm_ev_switch_" + hmmPlannerDecision.Reason
				}
			}
			return res, nil
		}

		if !pinFound && pin.Model != "" && !prefixBroken {
			pinTier := catalog.TierFor(pin.Model)
			freshTier := catalog.TierFor(fresh.Model)
			if pinTier != catalog.TierUnknown && freshTier != catalog.TierUnknown && freshTier <= pinTier {
				if _, excluded := req.ExcludedModels[pin.Model]; !excluded && !automaticallyDisabled(req, pin.Model) {
					if _, available := s.availableModels[pin.Model]; available {
						_, providerOK := req.EnabledProviders[pin.Provider]
						if req.EnabledProviders == nil || providerOK {
							if maxedOutServedModel(pin) != "" {
								log.Info("Expired session pin maxed out on previous turn; skipping re-anchor",
									"pin_model", pin.Model,
									"pin_provider", pin.Provider,
									"last_output_tokens", pin.LastOutputTokens,
								)
							} else if req.HasImages && (!catalog.AcceptsImages(pin.Model) ||
								req.ImageCount > 0 && !catalog.AcceptsImageCount(pin.Model, req.ImageCount, req.EnabledProviders)) {
								log.Info("Expired session pin is text-only for image-bearing turn; skipping re-anchor",
									"pin_model", pin.Model,
									"pin_provider", pin.Provider,
								)
							} else {
								priorDecision := pinDecision(pin)
								res.Decision = priorDecision
								res.StickyHit = true
								res.PinTier = "postgres_reanchor"
								s.writeNewPin(ctx, installationID, res.SessionKey, res.PinRole, priorDecision)
								log.Info("router re-anchored expired session pin",
									"prior_model", pin.Model,
									"prior_provider", pin.Provider,
									"fresh_model", fresh.Model,
									"fresh_provider", fresh.Provider,
									"prior_tier", pinTier.String(),
									"fresh_tier", freshTier.String(),
								)
								return res, nil
							}
						}
					}
				}
			}
		}

		if !s.ResolvePlannerEnabled(ctx) {
			res.Decision = fresh
			s.writeNewPin(ctx, installationID, res.SessionKey, res.PinRole, fresh)
			return res, nil
		}

		plannerTokens := s.plannerTokensFor(ctx, env, feats)
		prefixTokens, prefixKnown := cacheablePrefixTokens(pin, plannerTokens, prefixBroken)
		plannerIn := planner.Inputs{
			Pin:                   pin,
			Fresh:                 fresh,
			EstimatedInputTokens:  plannerTokens,
			CacheablePrefixTokens: prefixTokens,
			CachePrefixKnown:      prefixKnown,
			PriorOutputTokens:     pin.LastOutputTokens,
			AvailableModels:       s.availableModels,
			PinCacheCold:          pinFound && pinCacheCold(pin, prefixBroken, s.clock.Now()),
		}
		if !pinFound {
			plannerIn.Pin = sessionpin.Pin{}
		}
		decision := planner.Decide(plannerIn, plannerConfigFor(ctx, s.planner))
		res.PlannerDecision = decision

		if decision.Outcome == planner.OutcomeStay && pinFound {
			anchor := pinDecision(pin)
			served := s.bandSwapServed(ctx, res.TurnType, pin, fresh, req.HasImages, req.ImageCount, req.EnabledProviders, req.ExcludedModels)
			res.Decision = served
			res.StickyHit = true
			res.PinTier = "postgres_stay_" + decision.Reason
			s.refreshPin(ctx, installationID, res.SessionKey, pin, res.PinRole, anchor)
			return res, nil
		}

		if pinFound && prefixBroken {
			log.Info("Handover summarizer skipped: client history trim already bounded this switch turn",
				"pin_model", pin.Model,
				"fresh_model", fresh.Model,
			)
		}
		if pinFound && !prefixBroken {
			var (
				sumProvider       string
				sumCreds          *Credentials
				canCallSummarizer bool
			)
			if s.summarizer != nil {
				sumProvider = s.summarizer.Provider()
				sumCreds = resolveSummarizerCreds(ctx, sumProvider, reqHeaders)
				nonDepCreds := s.requestUsesNonDeploymentCreds(ctx, reqHeaders)
				canCallSummarizer = sumCreds != nil || !nonDepCreds
			}
			switch {
			case s.summarizer == nil:
				res.Handover.Invoked = true
				res.Handover.FallbackToFullHistory = true
				log.Info("Handover summarizer not wired; preserved full history instead", "pin_model", pin.Model, "fresh_model", fresh.Model)
			case !canCallSummarizer:
				res.Handover.Invoked = true
				res.Handover.FallbackToFullHistory = true
				log.Info("Handover summarizer skipped to preserve tenant boundary; preserved full history instead", "pin_model", pin.Model, "fresh_model", fresh.Model, "sum_provider", sumProvider)
			default:
				var summCtx context.Context
				if sumCreds != nil {
					summCtx = context.WithValue(ctx, CredentialsContextKey{}, sumCreds)
				} else {
					summCtx = clearCredentials(ctx)
				}
				start := s.clock.Now()
				summary, summaryUsage, sumErr := s.summarizer.Summarize(summCtx, env, req)
				res.Handover.Invoked = true
				res.Handover.LatencyMS = s.clock.Now().Sub(start).Milliseconds()
				switch {
				case sumErr != nil:
					res.Handover.FallbackToFullHistory = true
					log.Warn("Handover summarizer failed; preserved full history instead", "err", sumErr, "pin_model", pin.Model, "fresh_model", fresh.Model)
				case summary == "":
					res.Handover.FallbackToFullHistory = true
					log.Warn("Handover summarizer returned empty summary; preserved full history instead", "pin_model", pin.Model, "fresh_model", fresh.Model)
				default:
					handover.RewriteEnvelope(env, summary)
					res.ContextEstimate = env.ContextOverflowTokenEstimate()
					res.Handover.SummaryTokens = estimateSummaryTokens(summary)
					res.Handover.SummaryUsage = summaryUsage
				}
			}
		}

		res.Decision = fresh
		if pinFound {
			res.PinTier = "switch_" + decision.Reason
		}
		s.writeNewPin(ctx, installationID, res.SessionKey, res.PinRole, fresh)
		return res, nil
	}
	res, routeErr = routeRemaining()
	if escalationTurn != nil {
		if err := s.finishEscalation(ctx, escalationTurn, &res, routeErr); err != nil {
			log.Warn("Escalation state commit failed; routing without intervention", "err", err, "ordinal", escalationTurn.session.Ordinal)
			if escalationRoutingApplied(res.Decision) {
				req = baselineRequest
				res = baselineTurn
				res, routeErr = routeRemaining()
			}
		}
	}
	res.llmEscalation = llmTurn
	if llmTurn != nil && routeErr == nil {
		if err := s.applyLLMEscalation(ctx, llmTurn, res.Decision); err != nil {
			log.Warn("LLM escalation application failed", "err", err)
			if llmTurn.active && escalationRoutingApplied(res.Decision) {
				req = baselineRequest
				res = baselineTurn
				res, routeErr = routeRemaining()
				res.llmEscalation = llmTurn
			}
		}
	}
	return res, routeErr
}

func modelExcludedForRecoveryProbe(req router.Request, model string) bool {
	if _, excluded := req.ExcludedModels[model]; excluded || automaticallyDisabled(req, model) {
		return true
	}
	if len(req.GatewayProviders) > 0 {
		if _, available := gatewayProviderFor(model, req.CustomBindings, req.GatewayProviders); !available {
			return true
		}
	} else if req.EnabledProviders != nil && len(catalog.EnumerateBindingsWithCustom(model, req.EnabledProviders, req.CustomBindings)) == 0 {
		return true
	}
	return req.HasImages && (!catalog.AcceptsImages(model) ||
		req.ImageCount > 0 && !catalog.AcceptsImageCount(model, req.ImageCount, req.EnabledProviders))
}

func (s *Service) authorityCacheShadowFor(
	ctx context.Context,
	req router.Request,
	activePin sessionpin.Pin,
	hmmHistory sessionpin.Pin,
	fresh router.Decision,
	estimatedInputTokens int,
	prefixBroken bool,
) authorityCacheShadow {
	if !s.ResolveAuthorityCacheShadow(ctx) {
		return authorityCacheShadow{}
	}
	if !isHMMDecision(fresh) {
		return authorityCacheShadow{}
	}
	_, plannerDecision, sticky, stayModel := s.hmmCostGatedDecision(
		ctx, req, activePin, hmmHistory, fresh, estimatedInputTokens, prefixBroken,
	)
	stayProvider := activePin.Provider
	stayIdentity := stayModel
	stayPin, stayPinOK := s.hmmStayPin(req, activePin, hmmHistory)
	if stayPinOK && stayPin.Model == stayModel {
		stayProvider = stayPin.Provider
		stayIdentity = pinDecision(stayPin).ServedIdentity()
	}
	freshRosterArmID := ""
	if fresh.Metadata != nil {
		freshRosterArmID = fresh.Metadata.SelectedRosterArmID
	}
	shadow := authorityCacheShadow{
		Computed:   true,
		Decision:   plannerDecision,
		StayModel:  stayModel,
		Sticky:     sticky,
		StayScore:  candidateScoreForWithProvider(fresh, stayIdentity, stayProvider),
		FreshScore: candidateScoreForWithArm(fresh, fresh.ServedIdentity(), fresh.Provider, freshRosterArmID),
	}
	if stayPinOK && stayPin.Model == stayModel {
		shadow.StayProvider = stayPin.Provider
	}
	return shadow
}

func candidateScoreFor(dec router.Decision, servedIdentity string) *float64 {
	return candidateScoreForWithProvider(dec, servedIdentity, dec.Provider)
}

func candidateScoreForWithProvider(dec router.Decision, servedIdentity, provider string) *float64 {
	return candidateScoreForWithArm(dec, servedIdentity, provider, "")
}

func candidateScoreForWithArm(dec router.Decision, servedIdentity, provider, rosterArmID string) *float64 {
	if servedIdentity == "" || dec.Metadata == nil {
		return nil
	}
	if score, ok := dec.Metadata.CandidateScores[baseModelOf(servedIdentity)]; ok {
		value := float64(score)
		return &value
	}

	if rosterArmID != "" {
		if score, ok := dec.Metadata.ArmScores[rosterArmID]; ok {
			value := float64(score)
			return &value
		}
	}

	effort, model := stripEffortSuffix(servedIdentity)
	if model == "" || len(dec.Metadata.ArmScores) == 0 {
		return nil
	}
	armSuffix := ""
	if effort != "" {
		armSuffix = ":" + effort
	}
	keys := make([]string, 0, 2)
	if provider != "" {
		keys = append(keys, provider+"/"+model+armSuffix)
	}
	keys = append(keys, model+armSuffix)
	for _, key := range keys {
		if score, ok := dec.Metadata.ArmScores[key]; ok {
			value := float64(score)
			return &value
		}
	}

	var fallback *float64
	for armID, score := range dec.Metadata.ArmScores {
		armEffort, armModel := stripEffortSuffix(armID)
		if armEffort != effort || (armModel != model && !strings.HasSuffix(armModel, "/"+model)) {
			continue
		}
		if armProvider, ok := dec.Metadata.CandidateArmProviders[armID]; ok && provider != "" && armProvider == provider {
			value := float64(score)
			return &value
		}
		if fallback != nil {
			return nil
		}
		value := float64(score)
		fallback = &value
	}
	return fallback
}

func (s *Service) logAuthorityCacheShadow(ctx context.Context, res turnLoopResult) {
	if !res.AuthorityShadow.Computed {
		return
	}
	shadow := res.AuthorityShadow
	log := observability.FromContext(ctx)
	log.Info("authoritative turn cache-gate shadow",
		"turn_type", string(res.TurnType),
		"fresh_model", res.Fresh.Model,
		"shadow_outcome", plannerOutcome(shadow.Decision.Outcome),
		"shadow_reason", shadow.Decision.Reason,
		"shadow_stay_model", shadow.StayModel,
		"shadow_would_diverge", shadow.Sticky,
		"shadow_ev_ran", shadow.EVRan(),
	)
	if !shadow.EVRan() {
		return
	}
	log.Info("authoritative turn cache-gate shadow EV",
		"shadow_expected_savings_usd", shadow.Decision.ExpectedSavingsUSD,
		"shadow_eviction_cost_usd", shadow.Decision.EvictionCostUSD,
		"shadow_pin_cache_cold", shadow.Decision.PinCacheCold,
		"shadow_corrected_outcome", plannerOutcome(shadow.Decision.ShadowOutcome),
		"shadow_corrected_savings_usd", shadow.Decision.ShadowExpectedSavingsUSD,
	)
}

func maxedOutServedModel(pin sessionpin.Pin) string {
	if pin.LastOutputLimitAt.IsZero() || !pin.LastOutputLimitAt.Equal(pin.LastTurnEndedAt) {
		return ""
	}
	model := pin.LastServedModel
	if model == "" {
		model = pin.Model
	}
	return baseModelOf(model)
}

func baseModelOf(servedIdentity string) string {
	if idx := strings.LastIndex(servedIdentity, ":"); idx > 0 {
		return servedIdentity[:idx]
	}
	return servedIdentity
}

func roleForTier(t catalog.Tier) string {
	switch t {
	case catalog.TierLow:
		return sessionpin.DefaultRole + "_low"
	case catalog.TierMid:
		return sessionpin.DefaultRole + "_mid"
	case catalog.TierHigh:
		return sessionpin.DefaultRole + "_high"
	default:
		return sessionpin.DefaultRole
	}
}

func (s *Service) loadPin(ctx context.Context, sessionKey [sessionpin.SessionKeyLen]byte, role string) (sessionpin.Pin, bool) {
	pin, active, _ := s.loadPinWithStoreState(ctx, sessionKey, role)
	return pin, active
}

func (s *Service) loadPinWithStoreState(ctx context.Context, sessionKey [sessionpin.SessionKeyLen]byte, role string) (_ sessionpin.Pin, active, noStoredState bool) {
	log := observability.FromContext(ctx)
	log.Debug("loadPin called", "role", role, "session_key_hex", fmt.Sprintf("%x", sessionKey))
	pin, found, err := s.pinStore.Get(ctx, sessionKey, role)
	if err != nil {
		log.Error("session pin store unavailable; falling through to cluster scorer", "err", err)
		return sessionpin.Pin{}, false, false
	}
	if !found {
		return sessionpin.Pin{}, false, true
	}
	if !pinMatchesEffectiveStrategy(ctx, pin) {
		return sessionpin.Pin{}, false, false
	}
	if !pin.PinnedUntil.After(s.clock.Now()) {
		return pin, false, false
	}
	return pin, true, false
}

func (s *Service) loadHMMHistory(ctx context.Context, sessionKey [sessionpin.SessionKeyLen]byte, role string) sessionpin.Pin {
	log := observability.FromContext(ctx)
	pin, found, err := s.pinStore.Get(ctx, sessionKey, hmmHistoryRole(role))
	if err != nil {
		log.Error("HMM switch-history store unavailable", "err", err)
		return sessionpin.Pin{}
	}
	if !found {
		return sessionpin.Pin{}
	}
	if !pinMatchesEffectiveStrategy(ctx, pin) {
		return sessionpin.Pin{}
	}
	return pin
}

func switchHistoryFromPins(pins ...sessionpin.Pin) (string, bool) {
	latestTurn, sessionEverSwitched := latestServedTurn(pins...)
	return latestTurn.LastServedModel, sessionEverSwitched
}

func (r *turnLoopResult) applySwitchHistory(pins ...sessionpin.Pin) {
	latestTurn, sessionEverSwitched := latestServedTurn(pins...)
	r.PriorServedModel = latestTurn.LastServedModel
	r.PriorServedEndedAt = latestTurn.LastTurnEndedAt
	r.PriorPromptTokens = priorPromptTokens(latestTurn)
	r.SessionEverSwitched = sessionEverSwitched
	cals := make([]sessionpin.TokenizerCalibration, 0, len(pins))
	for _, pin := range pins {
		cals = append(cals, pin.TokenizerCalibration)
	}
	r.TokenizerCalibration = sessionpin.MergeTokenizerCalibrations(cals...)
}

func latestServedTurn(pins ...sessionpin.Pin) (sessionpin.Pin, bool) {
	var latest sessionpin.Pin
	sessionEverSwitched := false
	seenModel := ""
	for _, pin := range pins {
		sessionEverSwitched = sessionEverSwitched || pin.HasEverSwitched
		if pin.LastServedModel == "" {
			continue
		}
		if seenModel != "" && seenModel != pin.LastServedModel {
			sessionEverSwitched = true
		}
		seenModel = pin.LastServedModel
		if latest.LastServedModel == "" || pin.LastTurnEndedAt.After(latest.LastTurnEndedAt) {
			latest = pin
		}
	}
	return latest, sessionEverSwitched
}

func buildPolicyTurnContext(
	req router.Request,
	res turnLoopResult,
	activePin, hmmHistory sessionpin.Pin,
	now time.Time,
) *router.PolicyTurnContext {
	previous := activePin
	if hmmHistory.LastServedModel != "" &&
		(previous.LastServedModel == "" ||
			hmmHistory.LastTurnEndedAt.After(previous.LastTurnEndedAt)) {
		previous = hmmHistory
	}
	cacheState := router.PolicyCacheStateUnknown
	var priorOutputTokens *int
	if !previous.LastTurnEndedAt.IsZero() {
		cacheState = router.PolicyCacheStateCold
		if cacheWarm(previous, now) && !res.PrefixTrimmed && !req.HistoryTruncated {
			cacheState = router.PolicyCacheStateWarm
		}
		outputTokens := previous.LastOutputTokens
		priorOutputTokens = &outputTokens
	}
	userTurns := 0
	for _, message := range req.ConversationMessages {
		if strings.EqualFold(message.Role, "user") {
			userTurns++
		}
	}
	visibleTurnIndex := max(userTurns-1, 0)
	sessionTurnCount := max(activePin.TurnCount, hmmHistory.TurnCount)
	previousProvider := ""
	if res.PriorServedModel != "" {
		previousProvider = previous.Provider
	}
	return &router.PolicyTurnContext{
		VisibleTurnIndex:    visibleTurnIndex,
		SessionTurnCount:    sessionTurnCount,
		TurnType:            string(res.TurnType),
		PreviousServedModel: res.PriorServedModel,
		PreviousProvider:    previousProvider,
		CacheState:          cacheState,
		PriorOutputTokens:   priorOutputTokens,
		SessionEverSwitched: res.SessionEverSwitched,
		HistoryTruncated:    req.HistoryTruncated || res.PrefixTrimmed,
	}
}

func (s *Service) refreshPin(ctx context.Context, installationID uuid.UUID, sessionKey [sessionpin.SessionKeyLen]byte, existing sessionpin.Pin, role string, chosen router.Decision) {
	s.refreshPinVotes(ctx, installationID, sessionKey, existing, role, chosen, 0, 0)
}

func (s *Service) refreshPinDowngradeVotes(ctx context.Context, installationID uuid.UUID, sessionKey [sessionpin.SessionKeyLen]byte, existing sessionpin.Pin, role string, chosen router.Decision, downgradeVotes int) {
	s.refreshPinVotes(ctx, installationID, sessionKey, existing, role, chosen, downgradeVotes, 0)
}

func (s *Service) refreshPinVotes(ctx context.Context, installationID uuid.UUID, sessionKey [sessionpin.SessionKeyLen]byte, existing sessionpin.Pin, role string, chosen router.Decision, downgradeVotes, upgradeVotes int) {
	if installationID == uuid.Nil {
		return
	}
	effort := chosen.Effort
	if effort == "" && chosen.Model == existing.Model {
		effort = existing.Effort
	}
	p := sessionpin.Pin{
		SessionKey:            sessionKey,
		Role:                  role,
		InstallationID:        installationID,
		Provider:              chosen.Provider,
		Model:                 chosen.Model,
		Effort:                effort,
		PairedProvider:        existing.PairedProvider,
		PairedModel:           existing.PairedModel,
		Reason:                chosen.Reason,
		Strategy:              router.StrategyFromContext(ctx),
		PolicyGroup:           existing.PolicyGroup,
		TurnCount:             1,
		PinnedUntil:           pinExpiry(chosen.Reason, s.clock.Now()),
		LastInputTokens:       existing.LastInputTokens,
		LastCachedReadTokens:  existing.LastCachedReadTokens,
		LastCachedWriteTokens: existing.LastCachedWriteTokens,
		LastOutputTokens:      existing.LastOutputTokens,
		LastTurnEndedAt:       existing.LastTurnEndedAt,
		LastOutputLimitAt:     existing.LastOutputLimitAt,
		LastServedModel:       existing.LastServedModel,

		ConsecutiveDowngradeVotes: downgradeVotes,
		ConsecutiveUpgradeVotes:   upgradeVotes,
	}
	s.upsertPin(ctx, p)
}

func (s *Service) writeNewPin(ctx context.Context, installationID uuid.UUID, sessionKey [sessionpin.SessionKeyLen]byte, role string, chosen router.Decision) {
	log := observability.FromContext(ctx)
	var pairedProvider, pairedModel string
	if chosen.Metadata != nil {
		pairedProvider = chosen.Metadata.PairedProvider
		pairedModel = chosen.Metadata.PairedModel
	}
	log.Info("writeNewPin called", "installation_id", installationID.String(), "role", role, "model", chosen.Model, "paired_model", pairedModel, "paired_provider", pairedProvider, "session_key_hex", fmt.Sprintf("%x", sessionKey))
	if installationID == uuid.Nil {
		log.Info("writeNewPin: skipping because installationID is uuid.Nil")
		return
	}
	p := sessionpin.Pin{
		SessionKey:     sessionKey,
		Role:           role,
		InstallationID: installationID,
		Provider:       chosen.Provider,
		Model:          chosen.Model,
		Effort:         chosen.Effort,
		PairedProvider: pairedProvider,
		PairedModel:    pairedModel,
		Reason:         chosen.Reason,
		Strategy:       router.StrategyFromContext(ctx),
		PolicyGroup:    decisionPolicyGroup(chosen),
		TurnCount:      1,
		PinnedUntil:    pinExpiry(chosen.Reason, s.clock.Now()),
	}
	s.upsertPin(ctx, p)
}

func (s *Service) upsertPin(ctx context.Context, p sessionpin.Pin) {
	log := observability.FromContext(ctx)
	if p.Strategy == "" {
		p.Strategy = router.StrategyFromContext(ctx)
	}
	if err := s.pinStore.Upsert(context.WithoutCancel(ctx), p); err != nil {
		log.Error("session pin upsert failed", "err", err)
		return
	}
	log.Debug("session pin upsert ok", "installation_id", p.InstallationID.String(), "role", p.Role, "model", p.Model)
}

func estimateSummaryTokens(s string) int {
	if s == "" {
		return 0
	}
	return len(s) / 4
}

func resolveSummarizerCreds(ctx context.Context, provider string, headers http.Header) *Credentials {
	if provider == "" {
		return nil
	}
	if byok := BuildCredentialsMap(externalKeysFromContext(ctx)); byok != nil {
		if creds, ok := byok[provider]; ok {
			return creds
		}
	}
	creds := ExtractClientCredentials(provider, headers)
	if creds != nil && creds.OAuth {
		return nil
	}
	return creds
}

func sortedEnabledKeys(m map[string]struct{}) []string {
	if len(m) == 0 {
		return []string{}
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (s *Service) hardPinBlockedByContextWindow(hardPinReq HardPinRequest, req router.Request) bool {
	if len(req.ContextWindowExcludedModels) == 0 {
		return false
	}
	nonSizeExcluded := maps.Clone(req.ExcludedModels)
	maps.DeleteFunc(nonSizeExcluded, func(model string, _ struct{}) bool {
		_, sized := req.ContextWindowExcludedModels[model]
		return sized
	})
	hardPinReq.ExcludedModels = nonSizeExcluded
	_, _, ok := s.hardPinResolver(hardPinReq)
	return ok
}

func forcePinBindsNonClassifierTurn(found bool, tt turntype.TurnType) bool {
	return found && tt != turntype.Classifier
}
