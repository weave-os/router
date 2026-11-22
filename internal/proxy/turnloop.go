package proxy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"weave-os/router/internal/conversation"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/breaker"
	"weave-os/router/internal/router/candidates"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/continuity"
	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/escalation/floor"
	"weave-os/router/internal/router/planner"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/telemetry/turnrecord"

	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/routingpolicy"
	"weave-os/router/internal/routingpolicy/overrides"
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

func applyPinEvidence(ports continuity.StrategyPorts, res *turnLoopResult, pin sessionpin.Pin, now time.Time) {
	res.PinModel = pin.Model
	res.PinProvider = pin.Provider
	res.PinPolicyGroup = ports.Facts.PinStateOf(pin).PolicyGroup
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
	if !plannerConfigFor(ctx, s.continuityConfig.Planner).CorrectedEconomics {
		return feats.Tokens
	}
	return plannerInputTokens(env, feats)
}

const policyPinTier = "policy_pin"

type pinTier string

const (
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
	escalation             *floor.Turn

	Decision                    router.Decision
	SessionKey                  [sessionpin.SessionKeyLen]byte
	InstallationID              uuid.UUID
	Strategy                    router.Strategy
	TurnType                    turntype.TurnType
	StickyHit                   bool
	HardPinned                  bool
	UserForced                  bool
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
	Handover                    dispatch.HandoverOutcome
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

func isHMMDecision(ports continuity.StrategyPorts, dec router.Decision) bool {
	return ports.Ownership.IsDecision(dec)
}

const policyDeadlineFallbackReason = "policy_deadline_last_known_good"

const unscorableHMMStickyReason = "hmm_no_user_boundary_sticky"

func isPolicyDeadlineErr(err error) bool {
	if err == nil {
		return false
	}
	if !errors.Is(err, router.ErrHMMUnavailable) {
		return false
	}
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
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
	return dispatch.RoutedOrigin(decision, r.HardPinned, r.StickyHit)
}

func (r turnLoopResult) rescueOrigin() policy.OverrideSource {
	if len(r.Purpose) > 0 {
		return policy.OverrideSourceDeployment
	}
	return ""
}

func (s *Service) isHardPinnedTurn(ctx context.Context, strategy router.Strategy, tt turntype.TurnType) bool {
	switch tt {
	case turntype.Probe:
		return true
	case turntype.TitleGen:
		return s.explicitUtilityHardPin
	case turntype.SubAgentDispatch:
		if s.hasSubAgentOverride() {
			return true
		}
		return flags.BoolOr(ctx, flags.KeyHardPinExplore, s.hardPinExplore) && !s.strategyPorts.Ownership.Owns(strategy)
	default:
		return false
	}
}

func isUnpinnedScoredTurn(tt turntype.TurnType) bool {
	return tt == turntype.Classifier || tt == turntype.Recap || tt == turntype.TitleGen
}

func (s *Service) routeWithoutPin(
	ctx context.Context, strategy router.Strategy,
	req router.Request,
	res turnLoopResult,
	reqHeaders http.Header,
	forceModelFound bool,
	forceModelPin sessionpin.Pin,
) (turnLoopResult, error) {
	if forceModelFound && res.TurnType != turntype.Classifier && candidates.ForcedPinEligible(forceModelPin, req) {
		res.serveForceModelPin(forceModelPin)
		return res, nil
	}
	req.Session.PolicyTurnContext = buildPolicyTurnContext(req, res, sessionpin.Pin{}, sessionpin.Pin{}, s.clock.Now())
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
	decision, err := s.routeFor(ctx, strategy, req)
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
	if err != nil || !s.engine.Clients().Has(dec.Provider) {
		return router.Decision{}, false
	}
	dec.Reason = reasonClassifierRequestedModel
	return dec, true
}

func authoritativePolicyTurn(tt turntype.TurnType) bool {
	return tt == turntype.MainLoop || tt == turntype.ToolResult
}

func withAutomaticExclusions(req HardPinRequest, automaticExcluded map[string]struct{}) HardPinRequest {
	req.ExcludedModels = candidates.MergeExcludedModels(req.ExcludedModels, automaticExcluded)
	return req
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
	turnReq turnRequest,
	subAgentHint string,
	reqHeaders http.Header,
	req router.Request,
) (res turnLoopResult, routeErr error) {
	strategy, apiKeyID, installationID, subAgentHint := turnReq.Strategy, turnReq.APIKeyID, turnReq.InstallationID, subAgentHintForRequest(ctx, subAgentHint)
	shadowStartedAt, sessionPins := s.clock.Now(), routingpolicy.NewSessionPins(s.pinStore, s.clock.Now, strategy)
	defer func() {
		if routeErr == nil {
			routeErr = policyPinServed(ctx, res)
			if routeErr == nil {
				logAuthoritativeUpgrade(ctx, res)
			}
		}
		if routeErr != nil && agreementShadowEnabled(ctx) {
			key := deriveSessionKeyForRequest(ctx, env, apiKeyID)
			params := turnrecord.InsertTelemetryParams{InstallationID: installationID.String(), RequestID: observability.RequestIDFromContext(ctx),
				Timestamp: shadowStartedAt, SessionKey: key[:], Role: res.PinRole,
				TurnType: string(turntype.Detect(env, feats, subAgentHint, openCodeCaller(ClientIdentityFrom(ctx)))), ErrorClass: turnrecord.TurnErrorRoutingRefused}
			applyAgreementShadowTelemetry(s.strategyPorts, ctx, &params, res)
			if params.AgreementShadow != nil {
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
	req.Candidates.AutomaticExcludedModels = s.globalAutomaticExcludedModels(ctx)
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
	req.OrganizationID, _ = ctx.Value(requestcontext.ExternalIDContextKey{}).(string)
	if installationID != uuid.Nil {
		req.InstallationID = installationID.String()
	}
	res = turnLoopResult{
		ContextEstimate:             env.ContextOverflowTokenEstimate(),
		TranslationEnabledProviders: req.EnabledProviders,
		InstallationID:              installationID,
		Strategy:                    strategy,
		TurnType:                    turntype.Detect(env, feats, subAgentHint, openCodeCaller(ClientIdentityFrom(ctx))),
		PinTier:                     "miss",
		RequestedTier:               catalog.TierFor(feats.Model),
		StripThinkingBlocks:         stripsThinkingHistory(ctx),
	}
	res.AuthoritativePerTurn = authoritativePolicyTurn(res.TurnType) &&
		s.authoritativePerTurnSelection(ctx, strategy)
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
		return s.routeWithoutPin(ctx, strategy, req, res, reqHeaders, false, sessionpin.Pin{})
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
		forceModelPin, forceModelFound, forceModelCleared = routingpolicy.NewForceModelPins(s.pinStore, s.clock.Now).Load(ctx, forceModelSessionKey, sessionKeyHex(threadSessionKey), turntype.IsSubAgent(env, subAgentHint, openCodeCaller(ClientIdentityFrom(ctx))))
	}
	if forcePinBindsNonClassifierTurn(forceModelFound, res.TurnType) {
		binding, refusal := s.forcedModelBinding(ctx, forceModelPin.Model, forceModelPin.Provider)
		if refusal != nil {
			return res, refusal
		}
		forceModelPin.Provider = binding
		req.Candidates.ExcludedModels = s.readmitForcedModel(ctx, req, env, feats, forceModelPin)
	}
	if !forceModelFound && authoritativePolicyTurn(res.TurnType) {
		if probe, release, claimed := s.claimOverloadProbe(ctx, req); claimed {
			ctx = withOverloadProbe(ctx, probe)
			res.OverloadProbe = &probe
			res.CooldownProbeReleases = append(res.CooldownProbeReleases, release)
			req.Candidates.AutomaticExcludedModels = s.globalAutomaticExcludedModels(ctx)
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
	if router.ClassifiesThreads(res.Strategy) {
		return s.runClassifierTurn(ctx, strategy, req, res, threadSessionKey)
	}
	log.Info("turnloop classified",
		"turn_type", string(res.TurnType),
		"requested_tier", res.RequestedTier.String(),
		"pin_role", res.PinRole,
		"sub_agent_hint", subAgentHint,
	)

	automaticProbe := res.TurnType == turntype.Probe && !s.explicitUtilityHardPin &&
		(req.RequestedModel == "" || req.RequestedModel == automaticProbeModel || router.IsRoutingModel(req.RequestedModel))
	if _, pinned := router.HonouredPolicyPin(ctx); pinned && (!s.isHardPinnedTurn(ctx, strategy, res.TurnType) || automaticProbe) {
		if s.pinStore != nil && !isUnpinnedScoredTurn(res.TurnType) && !automaticProbe {
			res.SessionKey = threadSessionKey
			_, _, res.SessionFirstTurn = sessionPins.LoadWithStoreState(ctx, res.SessionKey, res.PinRole)
		}
		req.Session.PolicyTurnContext = buildPolicyTurnContext(req, res, sessionpin.Pin{}, sessionpin.Pin{}, s.clock.Now())
		decision, err := s.routeFor(ctx, strategy, req)
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

	hardPinnedTurn := s.isHardPinnedTurn(ctx, strategy, res.TurnType)
	if s.pinStore != nil && hardPinnedTurn && !forceModelFound && !forceModelCleared {
		legacyPin, found := sessionPins.Load(ctx, threadSessionKey, res.PinRole)
		if found && isUserForcedReason(legacyPin.Reason) {
			binding, refusal := s.forcedModelBinding(ctx, legacyPin.Model, legacyPin.Provider)
			if refusal != nil {
				return res, refusal
			}
			legacyPin.Provider = binding
			forceModelPin, forceModelFound = legacyPin, true
			req.Candidates.ExcludedModels = s.readmitForcedModel(ctx, req, env, feats, forceModelPin)
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
	if hardPinnedTurn {
		spec, _ := s.InferencePolicyRegistry().Spec(utilityPurposes[res.TurnType])
		_, err := overrides.Resolve(spec.OverridePrecedence, map[policy.OverrideSource][]overrides.Resolver{
			policy.OverrideSourceRequest: {
				func() (bool, error) {
					if !forceModelFound {
						return false, nil
					}
					if candidates.ForcedPinEligible(forceModelPin, req) {
						res.Purpose = utilityPurposes[res.TurnType]
						threadPin, hmmHistory, forceHistory := sessionpin.Pin{}, sessionpin.Pin{}, sessionpin.Pin{}
						if s.pinStore != nil {
							threadPin, _ = sessionPins.Load(ctx, threadSessionKey, res.PinRole)
							hmmHistory = sessionPins.LoadHMMHistory(ctx, threadSessionKey, res.PinRole)
							forceHistory = s.loadForceModelHistory(ctx, threadSessionKey, res.PinRole)
						}
						res.SessionKey = threadSessionKey
						res.PinModel = forceModelPin.Model
						res.PinAgeSec = pinAge(forceModelPin, s.clock.Now())
						res.applySwitchHistory(threadPin, hmmHistory, forceHistory)
						res.EscalateEffort = !forceHistory.LastTurnEndedAt.IsZero() &&
							(forceHistory.LastOutputTokens == 0 || forceHistory.ConsecutiveUpstreamErrors > 0)
						res.serveForceModelPin(forceModelPin)
						sessionPins.AnchorForceModelHistory(ctx, installationID, threadSessionKey, res.PinRole, forceModelPin)
						return true, nil
					}
					res.SessionKey = threadSessionKey
					res.ForcedPinDropped = true
					res.ForcedPinModel = forceModelPin.Model
					res.ForcedPinDropReason = candidates.ForcedPinIneligibilityReason(forceModelPin, req)
					log.Info("Forced session pin dropped for hard-pinned turn",
						"pin_model", forceModelPin.Model,
						"pin_provider", forceModelPin.Provider,
						"drop_reason", res.ForcedPinDropReason,
						"enabled_providers", sortedEnabledKeys(req.EnabledProviders),
						"role", res.PinRole,
					)
					return false, nil
				},
				func() (bool, error) {
					if res.TurnType != turntype.Probe || s.explicitUtilityHardPin {
						return false, nil
					}
					res.SessionKey = [sessionpin.SessionKeyLen]byte{}
					if automaticProbe {
						var err error
						res, err = s.routeWithoutPin(ctx, strategy, req, res, reqHeaders, forceModelFound, forceModelPin)
						return true, err
					}
					decision, err := s.callerModelPassthroughDecision(ctx, req)
					if err != nil {
						return true, err
					}
					res.Decision = decision
					res.CallerModelPassthrough = true
					res.Origin = policy.OverrideSourceRequest
					return true, nil
				},
			},
			policy.OverrideSourceDeployment: {
				func() (bool, error) {
					resolver, provider, model := s.hardPinResolver, s.hardPinProvider, s.hardPinModel
					if res.TurnType == turntype.SubAgentDispatch && s.hasSubAgentOverride() {
						resolver, provider, model = nil, s.subAgentProvider, s.subAgentModel
					}
					provider, model, err := utilityHardPinTarget(ctx, req, res.TurnType, resolver, provider, model)
					if err != nil {
						return true, err
					}
					res.Decision = router.Decision{
						Provider: provider,
						Model:    model,
						Reason:   string(res.TurnType) + "_hard_pin",
					}
					res.StickyHit = true
					res.HardPinned = true
					res.Origin = policy.OverrideSourceDeployment
					res.PinTier = string(res.TurnType) + "_hard_pin"
					return true, nil
				},
			},
		})
		return res, err
	}

	if !forceModelFound && env.IsNativeWebSearchSubTurn() {
		if decision, ok := s.usageBypassDecision(ctx, reqHeaders, req, nil, res.TurnType); ok {
			res.SessionKey = threadSessionKey
			res.Decision = decision
			res.UsageBypass = true
			return res, nil
		}

		if decision, ok := candidates.NativeWebSearchPassthrough(req, s.baselineFor(ctx, req.RequestedModel)); ok {
			res.SessionKey = threadSessionKey
			res.Decision = decision
			res.HardPinned = true
			res.PinTier = candidates.NativeWebSearchPassthroughReason
			log.Info("Native web-search sub-turn passed through to requested model", "model", decision.Model, "provider", decision.Provider)
			return res, nil
		}
	}

	if isUnpinnedScoredTurn(res.TurnType) {
		return s.routeWithoutPin(ctx, strategy, req, res, reqHeaders, forceModelFound, forceModelPin)
	}
	if res.TurnType == turntype.Compaction {
		return s.routeCompaction(ctx, req, res, reqHeaders, sessionTarget{strategy: strategy, installationID: installationID, sessionKey: threadSessionKey}, forceModelFound, forceModelPin)
	}

	sessionKey := threadSessionKey

	res.PrefixTrimmed = s.lifecycle.Compaction().CheckAndRecord(
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

	if routesWithoutSessionPin(s.pinStore, req) {
		return s.routeWithoutPin(ctx, strategy, req, res, reqHeaders, forceModelFound, forceModelPin)
	}

	res.SessionKey = sessionKey

	pin, pinFound, noStoredState := sessionPins.LoadWithStoreState(ctx, res.SessionKey, res.PinRole)
	res.SessionFirstTurn = noStoredState
	clearedPinReason := routingpolicy.ClearedPinReason(pin, pinFound)
	hmmHistory := sessionPins.LoadHMMHistory(ctx, res.SessionKey, res.PinRole)
	forceHistory := sessionpin.Pin{}
	if forceModelFound || forceModelCleared {
		forceHistory = s.loadForceModelHistory(ctx, res.SessionKey, res.PinRole)
	}
	if !forceModelFound && !forceModelCleared && pinFound && isUserForcedReason(pin.Reason) {
		binding, refusal := s.forcedModelBinding(ctx, pin.Model, pin.Provider)
		if refusal != nil {
			return res, refusal
		}
		pin.Provider = binding
		forceModelPin, forceModelFound = pin, true
		req.Candidates.ExcludedModels = s.readmitForcedModel(ctx, req, env, feats, forceModelPin)
	}
	if forceModelCleared && pinFound && isUserForcedReason(pin.Reason) {
		pinFound = false
		pin = sessionpin.Pin{}
	}
	commandContinuation := sessionpin.Pin{}
	commandContinuationFound := false
	if res.TurnType == turntype.MainLoop {
		commandContinuation, commandContinuationFound = postCommandContinuation(s, strategy).Consume(ctx, res.SessionKey, res.PinRole)
	}
	if pinFound && isUserForcedReason(pin.Reason) {
		binding, refusal := s.forcedModelBinding(ctx, pin.Model, pin.Provider)
		if refusal != nil {
			log.Warn("turnloop: forced pin refers to an excluded model",
				"pin_model", pin.Model,
				"pin_provider", pin.Provider,
				"reason", refusal.Reason,
			)
			return res, refusal
		}
		pin.Provider = binding
	}
	forcedProvider := ""
	if forceModelFound {
		forcedProvider = forceModelPin.Provider
	} else if pinFound && isUserForcedReason(pin.Reason) {
		forcedProvider = pin.Provider
	}
	if disabledProviders := candidates.SessionDisabledProviders(pin.DisabledProviders, hmmHistory.DisabledProviders, forcedProvider); len(disabledProviders) > 0 {
		res.SessionDisabledProviders = disabledProviders
		req.EnabledProviders = candidates.WithoutProviders(req.EnabledProviders, disabledProviders)
	}
	demoted := sessionDemotedModels(ctx, s, env, &res, pin, hmmHistory)
	res.SessionStrikeReadmitModels = harnessSafeModels(imageSafeModels(demoted, req.ImageCount), req.HasTools)
	if s.ResolveTransientRateLimit(ctx) && (!forceModelFound || !candidates.ForcedPinEligible(forceModelPin, req)) {
		ctx, demoted = applyDemotionCooldowns(ctx, s, &req, &res, demoted, mergeDemotionCooldowns(pin.DemotionCooldowns, hmmHistory.DemotionCooldowns))
	}
	if len(demoted) > 0 {
		res.SessionDemotedModels = demoted
		for _, model := range demoted {
			req.Candidates.AutomaticExcludedModels = addToSet(req.Candidates.AutomaticExcludedModels, model)
		}
	}
	res.applySwitchHistory(pin, hmmHistory, forceHistory)
	req, res.ObservedOverflowModels = s.excludeObservedOverflowModels(ctx, req, res, max(contextWindowOutputReserve, feats.MaxTokens))
	req.Session.PolicyTurnContext = buildPolicyTurnContext(req, res, pin, hmmHistory, s.clock.Now())
	res.EscalateEffort = pinFound && !pin.LastTurnEndedAt.IsZero() &&
		(pin.LastOutputTokens == 0 || pin.ConsecutiveUpstreamErrors > 0)
	if pinFound {
		res.PinTurnCount = pin.TurnCount + 1
		res.PinFirstPinnedAt = pin.FirstPinnedAt
		applyPinEvidence(s.strategyPorts, &res, pin, s.clock.Now())
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
		served, tierFloor := serveForceModelPin(ctx, &req, &res, forceModelPin, forceHistory, sessionPins, s.clock.Now())
		if served {
			return res, nil
		}
		forcedTierFloor = tierFloor
		if !sessionForceControlFound || isUserForcedReason(pin.Reason) {
			pinFound = false
			pin = sessionpin.Pin{}
		}
	}
	if pinFound && isUserForcedReason(pin.Reason) {
		served, tierFloor := serveUserForcedPin(ctx, &req, &res, pin, sessionPins)
		if served {
			return res, nil
		}
		forcedTierFloor = tierFloor
		pinFound = false
		pin = sessionpin.Pin{}
	}

	reusable := turnPin{pin: pin, found: pinFound}
	dropPinRuledOutLastTurn(ctx, s, env, &req, &res, &reusable, hmmHistory)
	screenPinContextWindow(ctx, s, env, feats, &res, &req, &reusable)
	screenPinForRequest(ctx, req, &reusable)
	pin, pinFound = reusable.pin, reusable.found
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
	if commandContinuationFound && candidates.AutomaticPinEligible(commandContinuation, req) {
		decision := commandContinuation.Decision()
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
		sessionPins.Refresh(ctx, installationID, res.SessionKey, commandContinuation, res.PinRole, decision)
		return res, nil
	}

	if dec, ok := s.usageBypassDecision(ctx, reqHeaders, req, res.SessionDemotedModels, res.TurnType); ok {
		res.Decision = dec
		res.UsageBypass = true
		return res, nil
	}

	if s.strategyPorts.Ownership.Owns(strategy) && pinFound && res.AuthoritativePerTurn && req.ForceCluster == "" &&
		escalation.Rank(escalation.Group(s.strategyPorts.Facts.PinStateOf(pin).PolicyGroup)) >= 0 && s.ResolveAuthoritativeUpgradeSlidingThreshold(ctx) {
		req.Session.SlidingThresholdPinGroup = escalation.Group(s.strategyPorts.Facts.PinStateOf(pin).PolicyGroup)
	}
	baselineRequest := req
	baselineTurn := res
	escalationTurn := s.escalationHelper().Begin(ctx, s.escalationFacts(ctx, env, res, apiKeyID), req)
	escalationTurn.Apply(&req)
	res.escalation = escalationTurn
	if pinFound {
		req.Session.PreviousPolicyGroup = escalation.Group(s.strategyPorts.Facts.PinStateOf(pin).PolicyGroup)
	}

	routeRemaining := func() (turnLoopResult, error) {
		req.Session.Pin = router.SessionPin{Found: pinFound, Provider: pin.Provider, Model: pin.Model, LastTurnEndedAt: pin.LastTurnEndedAt, LastOutputTokens: pin.LastOutputTokens}
		holdToolResult := res.AuthoritativePerTurn && req.Escalation == nil &&
			res.TurnType == turntype.ToolResult && s.ResolveHMMToolResultHold(ctx, strategy)
		if holdToolResult {
			res.AuthoritativePerTurn = false
		}
		if req.Escalation == nil && pinFound && candidates.AutomaticPinEligible(pin, req) && req.ConversationMessages != nil &&
			s.strategyPorts.Ownership.Owns(strategy) &&
			!hasTextUserBoundary(req.ConversationMessages) {
			if _, honoured := router.HonouredPolicyPin(ctx); !honoured {
				decision := pin.Decision()
				decision.Reason = unscorableHMMStickyReason
				res.Decision = decision
				res.StickyHit = true
				res.PinTier = unscorableHMMStickyReason
				log.Info("HMM turn has no user text; preserving eligible session pin",
					"pin_model", pin.Model, "pin_provider", pin.Provider)
				sessionPins.Refresh(ctx, installationID, res.SessionKey, pin, res.PinRole, pin.Decision())
				return res, nil
			}
		}
		turn := continuity.Turn{IsToolResult: res.TurnType == turntype.ToolResult, Escalated: req.Escalation != nil, AuthoritativePerTurn: res.AuthoritativePerTurn}
		continuityCfg := continuity.Config{PlannerEnabled: s.ResolvePlannerEnabled(ctx), ScoreToolResultTurns: s.ResolveScoreToolResultTurns(ctx), HoldToolResult: holdToolResult, Planner: s.continuityConfig.Planner}
		if continuity.ToolResultSticky(req.Session, turn, continuityCfg) {
			decision := pin.Decision()
			res.Decision = decision
			res.StickyHit = true
			res.PinTier = "postgres_tool_result_sc"
			sessionPins.Refresh(ctx, installationID, res.SessionKey, pin, res.PinRole, decision)
			return res, nil
		}

		if continuity.CanReusePin(req.Session, turn, continuityCfg) {
			decision := pin.Decision()
			res.Decision = decision
			res.StickyHit = true
			res.PinTier = "postgres"
			sessionPins.Refresh(ctx, installationID, res.SessionKey, pin, res.PinRole, decision)
			return res, nil
		}

		var fresh router.Decision
		routed := false
		if forcedTierFloor != catalog.TierUnknown {
			if constrained, ok := s.restrictToTier(req.Candidates.ExcludedModels, forcedTierFloor); ok {
				tierReq := req
				tierReq.Candidates.ExcludedModels = constrained
				if dec, derr := s.routeFor(ctx, strategy, tierReq); derr == nil {
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
			dec, err := s.routeFor(ctx, strategy, req)
			if err != nil {
				if s.policyDeadlineFallback && isPolicyDeadlineErr(err) && servePolicyDeadlineFallback(ctx, s, &res, err, req, sessionPins, pinIfFound(pin, pinFound)) {
					return res, nil
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
		if floor.RoutingApplied(fresh) {
			res.Decision = fresh
			res.PinTier = escalationTurn.PinTier()
			return res, nil
		}
		if res.AuthoritativePerTurn {
			return serveAuthoritativeTurn(ctx, s, &res, authoritativeTurn{env: env, feats: feats, strategy: strategy, req: req, pin: pin, pinFound: pinFound, hmmHistory: hmmHistory, fresh: fresh, prefixBroken: prefixBroken, sessionPins: sessionPins, installationID: installationID})
		}
		if isHMMDecision(s.strategyPorts, fresh) {
			activePin := sessionpin.Pin{}
			if pinFound {
				activePin = pin
			}
			hmmGateInput := continuity.CostGateInput{
				Request: req, ActivePin: activePin, HMMHistory: hmmHistory, Fresh: fresh,
				EstimatedInputTokens: s.plannerTokensFor(ctx, env, feats), PrefixBroken: prefixBroken,
			}
			hmmDecision, hmmPlannerDecision, hmmSticky, hmmStayModel := hmmCostGatedDecision(ctx, s, hmmGateInput)
			res.Decision = hmmDecision
			res.PlannerDecision = hmmPlannerDecision
			if heldEffort := s.strategyPorts.Upgrade.EffortHysteresisHold(fresh, res.PriorServedModel, hmmDecision.Model, hmmDecision.Effort); heldEffort != "" {
				res.Decision.Effort = heldEffort
				res.PlannerDecision.Reason = s.strategyPorts.Upgrade.AppendEffortHysteresisReason(res.PlannerDecision.Reason)
				log.Info("HMM effort hysteresis held incumbent",
					"incumbent", res.PriorServedModel,
					"challenger_effort", hmmDecision.Effort,
					"serving_effort", heldEffort,
				)
			}
			if hmmStayModel != "" {
				res.PinModel = hmmStayModel
				if hmmPin, ok := hmmStayPin(s, req, activePin, hmmHistory); ok && hmmPin.Model == hmmStayModel {
					applyPinEvidence(s.strategyPorts, &res, hmmPin, s.clock.Now())
				}
			}
			if hmmSticky {
				res.StickyHit = true
				res.PinTier = "hmm_ev_stay_" + hmmPlannerDecision.Reason
				res.StickyRole = hmmHistoryRole(res.PinRole)
			} else {
				res.PinTier = "hmm_fresh_unpinned"
				if hmmPlannerDecision.Outcome == planner.OutcomeStay && hmmPlannerDecision.Evaluated() {
					res.PinTier = "hmm_ev_same_" + hmmPlannerDecision.Reason
				} else if hmmPlannerDecision.EvaluatedAgainstPin() {
					res.PinTier = "hmm_ev_switch_" + hmmPlannerDecision.Reason
				}
			}
			hmmShadowStayPin, _ := hmmStayPin(s, req, activePin, hmmHistory)
			logSwitchShadow(ctx, hmmShadowStayPin, hmmGateInput, s.clock.Now(), res)
			return res, nil
		}

		if !pinFound && pin.Model != "" && !prefixBroken {
			switch continuity.JudgeReanchor(pin, fresh, req, s.availableModels) {
			case continuity.ReanchorNone:
			case continuity.ReanchorSkipMaxedOut:
				log.Info("Expired session pin maxed out on previous turn; skipping re-anchor", "pin_model", pin.Model, "pin_provider", pin.Provider, "last_output_tokens", pin.LastOutputTokens)
			case continuity.ReanchorSkipTextOnly:
				log.Info("Expired session pin is text-only for image-bearing turn; skipping re-anchor", "pin_model", pin.Model, "pin_provider", pin.Provider)
			case continuity.ReanchorServe:
				priorDecision := pin.Decision()
				res.Decision = priorDecision
				res.StickyHit = true
				res.PinTier = "postgres_reanchor"
				sessionPins.WriteNew(ctx, installationID, res.SessionKey, res.PinRole, priorDecision)
				log.Info("router re-anchored expired session pin", "prior_model", pin.Model, "prior_provider", pin.Provider, "fresh_model", fresh.Model, "fresh_provider", fresh.Provider, "prior_tier", catalog.TierFor(pin.Model).String(), "fresh_tier", catalog.TierFor(fresh.Model).String())
				return res, nil
			}
		}

		if !continuityCfg.PlannerEnabled {
			res.Decision = fresh
			sessionPins.WriteNew(ctx, installationID, res.SessionKey, res.PinRole, fresh)
			return res, nil
		}

		plannerTokens := s.plannerTokensFor(ctx, env, feats)
		prefixTokens, prefixKnown := cacheablePrefixTokens(pin, plannerTokens, prefixBroken)
		decision := continuity.Plan(req.Session, fresh, continuity.PlanInputs{
			EstimatedInputTokens:  plannerTokens,
			CacheablePrefixTokens: prefixTokens,
			CachePrefixKnown:      prefixKnown,
			PinCacheCold:          pinCacheCold(pin, prefixBroken, s.clock.Now()),
			AvailableModels:       s.availableModels,
		}, plannerConfigFor(ctx, s.continuityConfig.Planner))
		res.PlannerDecision = decision

		outcome := continuity.Settle(decision, pinFound, prefixBroken)
		if outcome.Stay {
			anchor := pin.Decision()
			served := s.bandSwapServed(ctx, res.TurnType, pin, fresh, req.HasImages, req.ImageCount, req.EnabledProviders, req.Candidates.ExcludedModels)
			res.Decision = served
			res.StickyHit = true
			res.PinTier = outcome.PinTier
			sessionPins.Refresh(ctx, installationID, res.SessionKey, pin, res.PinRole, anchor)
			return res, nil
		}

		if outcome.Handover == continuity.HandoverSkippedTrimmedHistory {
			log.Info("Handover summarizer skipped: client history trim already bounded this switch turn", "pin_model", pin.Model, "fresh_model", fresh.Model)
		}
		if outcome.Handover == continuity.HandoverSummarize {
			var rewritten bool
			res.Handover, rewritten = conversation.ModelSwitchSummary{
				Summarizer: s.summarizer,
				Access:     summarizerAccess{s: s, headers: reqHeaders},
				Now:        s.clock.Now,
			}.Run(ctx, env, req, pin.Model, fresh.Model)
			if rewritten {
				res.ContextEstimate = env.ContextOverflowTokenEstimate()
			}
		}

		res.Decision = fresh
		if outcome.PinTier != "" {
			res.PinTier = outcome.PinTier
		}
		sessionPins.WriteNew(ctx, installationID, res.SessionKey, res.PinRole, fresh)
		return res, nil
	}
	res, routeErr = routeRemaining()
	if escalationTurn != nil {
		committed, rerunBaseline, err := s.escalationHelper().Settle(ctx, escalationTurn, res.Decision, decisionPolicyGroup(res.Decision), routeErr)
		if rerunBaseline {
			req, res = baselineRequest, baselineTurn
			res, routeErr = routeRemaining()
		} else if err == nil {
			res.EscalationShadowMarked, res.EscalationScope, res.EscalationOrdinal = committed.ShadowMarked, committed.Scope, committed.Ordinal
		}
	}
	res.escalation = escalationTurn
	if s.escalationHelper().SettleLLM(ctx, escalationTurn, res.Decision, routeErr) {
		req, res = baselineRequest, baselineTurn
		res, routeErr = routeRemaining()
		res.escalation = escalationTurn
	}
	return res, routeErr
}

func modelExcludedForRecoveryProbe(req router.Request, model string) bool {
	if _, excluded := req.Candidates.ExcludedModels[model]; excluded || automaticallyDisabled(req, model) {
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
	if !isHMMDecision(s.strategyPorts, fresh) {
		return authorityCacheShadow{}
	}
	_, plannerDecision, sticky, stayModel := hmmCostGatedDecision(ctx, s, continuity.CostGateInput{
		Request: req, ActivePin: activePin, HMMHistory: hmmHistory, Fresh: fresh,
		EstimatedInputTokens: estimatedInputTokens, PrefixBroken: prefixBroken,
	})
	stayProvider := activePin.Provider
	stayIdentity := stayModel
	stayPin, stayPinOK := hmmStayPin(s, req, activePin, hmmHistory)
	if stayPinOK && stayPin.Model == stayModel {
		stayProvider = stayPin.Provider
		stayIdentity = stayPin.Decision().ServedIdentity()
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
		StayScore:  candidateScoreForWithProvider(s.strategyPorts, fresh, stayIdentity, stayProvider),
		FreshScore: candidateScoreForWithArm(s.strategyPorts, fresh, fresh.ServedIdentity(), fresh.Provider, freshRosterArmID),
	}
	if stayPinOK && stayPin.Model == stayModel {
		shadow.StayProvider = stayPin.Provider
	}
	return shadow
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

func forcePinBindsNonClassifierTurn(found bool, tt turntype.TurnType) bool {
	return found && tt != turntype.Classifier
}
