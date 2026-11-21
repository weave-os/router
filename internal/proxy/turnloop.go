package proxy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/breaker"
	"weave-os/router/internal/router/candidates"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/continuity"
	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/escalation/floor"
	"weave-os/router/internal/router/handover"
	"weave-os/router/internal/router/planner"
	"weave-os/router/internal/router/policy"

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

const (
	nativeWebSearchPassthroughReason = "native_web_search_passthrough"
)

const policyDeadlineFallbackReason = "policy_deadline_last_known_good"

const policyDeadlineDefaultReason = "policy_deadline_default_model"

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
	return routedOrigin(decision, r.HardPinned, r.StickyHit)
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
	if err != nil || !s.clients.Has(dec.Provider) {
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
			params := InsertTelemetryParams{InstallationID: installationID.String(), RequestID: observability.RequestIDFromContext(ctx),
				Timestamp: shadowStartedAt, SessionKey: key[:], Role: res.PinRole,
				TurnType: string(turntype.Detect(env, feats, subAgentHint, openCodeCaller(ClientIdentityFrom(ctx)))), ErrorClass: TurnErrorRoutingRefused}
			applyAgreementShadowTelemetry(s.strategyPorts, ctx, &params, res)
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
	req.OrganizationID, _ = ctx.Value(ExternalIDContextKey{}).(string)
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
		forceModelPin, forceModelFound, forceModelCleared = s.loadForceModelSessionPin(ctx, forceModelSessionKey, threadSessionKey, turntype.IsSubAgent(env, subAgentHint, openCodeCaller(ClientIdentityFrom(ctx))))
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
	if res.Strategy == router.StrategyLLMClassifier {
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

		passthroughModel := s.baselineFor(ctx, req.RequestedModel)
		_, excluded := req.Candidates.ExcludedModels[passthroughModel]
		_, safetyExcluded := req.Candidates.SafetyExcludedModels[passthroughModel]
		_, allowlisted := req.Candidates.AllowedModels[passthroughModel]
		allowed := req.Candidates.AllowedModels == nil || allowlisted
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
		return s.routeWithoutPin(ctx, strategy, req, res, reqHeaders, forceModelFound, forceModelPin)
	}
	if res.TurnType == turntype.Compaction {
		return s.routeCompaction(ctx, req, res, reqHeaders, sessionTarget{strategy: strategy, installationID: installationID, sessionKey: threadSessionKey}, forceModelFound, forceModelPin)
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
		commandContinuation, commandContinuationFound = s.consumePostCommandContinuation(ctx, strategy, res.SessionKey, res.PinRole)
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
	disabledProviders := candidates.MergeSessionStrikes(pin.DisabledProviders, hmmHistory.DisabledProviders)
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
	demoted := candidates.MergeSessionStrikes(pin.DemotedModels, hmmHistory.DemotedModels)
	if turnStrikes := candidates.MergeSessionStrikes(pin.TurnStrikes, hmmHistory.TurnStrikes); len(turnStrikes) > 0 {
		if routingEndsWithUserPrompt(ctx, env) {
			s.clearTurnStrikes(ctx, res.SessionKey, res.PinRole)
		} else {
			demoted = candidates.MergeSessionStrikes(demoted, turnStrikes)
		}
	}
	res.SessionStrikeReadmitModels = harnessSafeModels(imageSafeModels(demoted, req.ImageCount), req.HasTools)
	if s.ResolveTransientRateLimit(ctx) && (!forceModelFound || !candidates.ForcedPinEligible(forceModelPin, req)) {
		cooldowns := mergeDemotionCooldowns(pin.DemotionCooldowns, hmmHistory.DemotionCooldowns)
		now := s.clock.Now()
		cooling := activeDemotionCooldowns(cooldowns, now)
		if len(cooling) > 0 {
			res.SessionCooldownModels = readmittableCooldowns(cooling, demoted, req.ImageCount)
			demoted = candidates.MergeSessionStrikes(demoted, cooldownsByExpiry(cooling))
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
			demoted = candidates.MergeSessionStrikes(demoted, []string{model})
			res.CooldownProbeDeniedModels = append(res.CooldownProbeDeniedModels, model)
			req.Candidates.ExcludedModels = candidates.ExcludingModel(req.Candidates.ExcludedModels, model)
			ctx = context.WithValue(ctx, SessionCooldownProbeDeniedModelsContextKey{}, res.CooldownProbeDeniedModels)
		}
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
		_, excluded := req.Candidates.ExcludedModels[forceModelPin.Model]
		_, providerEnabled := req.EnabledProviders[forceModelPin.Provider]
		providerEligible := req.EnabledProviders == nil || providerEnabled
		imageCapable := candidates.ForcedPinServesImages(forceModelPin, req)
		if _, estimateOnly := req.Candidates.ContextWindowExcludedModels[forceModelPin.Model]; excluded && estimateOnly {
			log.Info("Forced session pin dispatched past the context-window estimate; the provider decides",
				"pin_model", forceModelPin.Model,
				"pin_provider", forceModelPin.Provider,
				"role", res.PinRole,
			)
			excluded = false
		}
		if _, imageLimitExcluded := req.Candidates.ImageLimitExcludedModels[forceModelPin.Model]; imageLimitExcluded {
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
			res.serveForceModelPin(forceModelPin)
			sessionPins.AnchorForceModelHistory(ctx, installationID, res.SessionKey, res.PinRole, forceModelPin)
			return res, nil
		}
		res.ForcedPinDropped = true
		res.ForcedPinDropReason = candidates.ForcedPinIneligibilityReason(forceModelPin, req)
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
			req.Candidates.ExcludedModels = candidates.ExcludingModel(req.Candidates.ExcludedModels, forceModelPin.Model)
		}
		if !sessionForceControlFound || isUserForcedReason(pin.Reason) {
			pinFound = false
			pin = sessionpin.Pin{}
		}
	}
	if pinFound && isUserForcedReason(pin.Reason) {
		_, excluded := req.Candidates.ExcludedModels[pin.Model]
		_, providerEnabled := req.EnabledProviders[pin.Provider]
		providerEligible := req.EnabledProviders == nil || providerEnabled
		imageCapable := candidates.PinServesImages(pin, req)
		if !excluded && providerEligible && imageCapable {
			decision := pinDecision(pin)
			decision.Reason = pin.Reason
			res.PinTier = pin.Reason
			res.Decision = decision
			res.UserForced = decision.UserForcedModel()
			res.StickyHit = true
			sessionPins.Refresh(ctx, installationID, res.SessionKey, pin, res.PinRole, pinDecision(pin))
			return res, nil
		}
		dropReason := "excluded"
		switch {
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
		res.ForcedPinDropped = true
		res.ForcedPinDropReason = dropReason
		res.ForcedPinModel = pin.Model
		if excluded || !imageCapable {
			forcedTierFloor = catalog.TierFor(pin.Model)
		}
		if !imageCapable {
			req.Candidates.ExcludedModels = candidates.ExcludingModel(req.Candidates.ExcludedModels, pin.Model)
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
		excluded := make(map[string]struct{}, len(req.Candidates.ExcludedModels)+1)
		for k := range req.Candidates.ExcludedModels {
			excluded[k] = struct{}{}
		}
		excluded[maxedModel] = struct{}{}
		req.Candidates.ExcludedModels = excluded
		req.Candidates.SafetyExcludedModels = addToSet(req.Candidates.SafetyExcludedModels, maxedModel)
		pinFound = false
		pin = sessionpin.Pin{}
	}
	if maxedModel := maxedOutServedModel(hmmHistory); maxedModel != "" {
		log.Info("HMM history maxed out on previous turn; excluding for this turn",
			"history_provider", hmmHistory.Provider,
			"maxed_model", maxedModel,
			"last_output_tokens", hmmHistory.LastOutputTokens,
		)
		excluded := make(map[string]struct{}, len(req.Candidates.ExcludedModels)+1)
		for k := range req.Candidates.ExcludedModels {
			excluded[k] = struct{}{}
		}
		excluded[maxedModel] = struct{}{}
		req.Candidates.ExcludedModels = excluded
		req.Candidates.SafetyExcludedModels = addToSet(req.Candidates.SafetyExcludedModels, maxedModel)
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
		if _, overCapacity := req.Candidates.ExcludedModels[pin.Model]; overCapacity {
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
					if len(req.Candidates.ExcludedModels) > 0 {
						pruned := make(map[string]struct{}, len(req.Candidates.ExcludedModels)-1)
						for k := range req.Candidates.ExcludedModels {
							if k != pin.Model {
								pruned[k] = struct{}{}
							}
						}
						req.Candidates.ExcludedModels = pruned
					}
					delete(req.Candidates.SafetyExcludedModels, pin.Model)
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
	if commandContinuationFound && candidates.AutomaticPinEligible(commandContinuation, req) {
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
				decision := pinDecision(pin)
				decision.Reason = unscorableHMMStickyReason
				res.Decision = decision
				res.StickyHit = true
				res.PinTier = unscorableHMMStickyReason
				log.Info("HMM turn has no user text; preserving eligible session pin",
					"pin_model", pin.Model, "pin_provider", pin.Provider)
				sessionPins.Refresh(ctx, installationID, res.SessionKey, pin, res.PinRole, pinDecision(pin))
				return res, nil
			}
		}
		turn := continuity.Turn{IsToolResult: res.TurnType == turntype.ToolResult, Escalated: req.Escalation != nil, AuthoritativePerTurn: res.AuthoritativePerTurn}
		continuityCfg := continuity.Config{PlannerEnabled: s.ResolvePlannerEnabled(ctx), ScoreToolResultTurns: s.ResolveScoreToolResultTurns(ctx), HoldToolResult: holdToolResult, Planner: s.continuityConfig.Planner}
		if continuity.ToolResultSticky(req.Session, turn, continuityCfg) {
			decision := pinDecision(pin)
			res.Decision = decision
			res.StickyHit = true
			res.PinTier = "postgres_tool_result_sc"
			sessionPins.Refresh(ctx, installationID, res.SessionKey, pin, res.PinRole, decision)
			return res, nil
		}

		if continuity.CanReusePin(req.Session, turn, continuityCfg) {
			decision := pinDecision(pin)
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
							"pin_policy_group", s.strategyPorts.Facts.PinStateOf(pin).PolicyGroup,
							"requested_model", req.RequestedModel,
						)
						refreshed := decision
						refreshed.Reason = pin.Reason
						sessionPins.Refresh(ctx, installationID, res.SessionKey, pin, res.PinRole, refreshed)
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
						sessionPins.WriteNew(ctx, installationID, res.SessionKey, res.PinRole, decision)
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
		if floor.RoutingApplied(fresh) {
			res.Decision = fresh
			res.PinTier = escalationTurn.PinTier()
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
			if res.UpgradeShadow != nil && res.UpgradeShadow.Verdict.DemotedModel() {
				if pinFound && candidates.AutomaticPinEligible(pin, req) {
					decision := pinDecision(pin)
					res.Decision = decision
					res.StickyHit = true
					res.PinTier = string(pinTierAuthoritativeExcludedPin)
					log.Warn("authoritative policy returned an excluded model; keeping session pin",
						"pin_model", pin.Model,
						"fresh_model", fresh.Model,
						"reason", res.UpgradeShadow.Verdict.Reason,
					)
					sessionPins.Refresh(ctx, installationID, res.SessionKey, pin, res.PinRole, decision)
					return res, nil
				}

				rerouteReq := req
				rerouted, rerouteErr := s.routeFor(ctx, strategy, rerouteReq)
				if rerouteErr != nil {
					return res, rerouteErr
				}
				reroutedPin := sessionpin.Pin{Model: rerouted.Model, Provider: rerouted.Provider}
				available := s.availableModels == nil
				if s.availableModels != nil {
					_, available = s.availableModels[rerouted.Model]
				}
				if !available || !candidates.PinEligible(reroutedPin, rerouteReq) {
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
				sessionPins.WriteNew(ctx, installationID, res.SessionKey, res.PinRole, rerouted)
				return res, nil
			}
			sliding := s.strategyPorts.Upgrade.SlidingThresholdServes(req, pin, fresh)
			if sliding.Served {
				log.Info("authoritative sliding threshold serving fresh model",
					"pin_model", pin.Model,
					"pin_group", req.Session.SlidingThresholdPinGroup,
					"fresh_model", fresh.Model,
					"fresh_group", sliding.FreshGroup,
					"classified_group", sliding.ClassifiedGroup,
					"probability", sliding.Probability,
				)
			}
			verdict := s.strategyPorts.Upgrade.JudgeAuthoritative(authoritativeGuardsFor(ctx, s, sliding.Served), pin, pinFound, fresh, plannerTokens)
			if holdAuthoritativePin(ctx, sessionPins, s.strategyPorts.Ownership, &res, pin, fresh, verdict) {
				return res, nil
			}
			res.Decision = fresh
			res.PinTier = "authoritative_per_turn"
			if sliding.Served {
				res.PinTier = string(pinTierAuthoritativeSlidingThresholdUpgrade)
			}
			sessionPins.WriteNew(ctx, installationID, res.SessionKey, res.PinRole, fresh)
			return res, nil
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
			pinTier := catalog.TierFor(pin.Model)
			freshTier := catalog.TierFor(fresh.Model)
			if pinTier != catalog.TierUnknown && freshTier != catalog.TierUnknown && freshTier <= pinTier {
				if _, excluded := req.Candidates.ExcludedModels[pin.Model]; !excluded && !automaticallyDisabled(req, pin.Model) {
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
								sessionPins.WriteNew(ctx, installationID, res.SessionKey, res.PinRole, priorDecision)
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

		if decision.Outcome == planner.OutcomeStay && pinFound {
			anchor := pinDecision(pin)
			served := s.bandSwapServed(ctx, res.TurnType, pin, fresh, req.HasImages, req.ImageCount, req.EnabledProviders, req.Candidates.ExcludedModels)
			res.Decision = served
			res.StickyHit = true
			res.PinTier = "postgres_stay_" + decision.Reason
			sessionPins.Refresh(ctx, installationID, res.SessionKey, pin, res.PinRole, anchor)
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
		sessionPins.WriteNew(ctx, installationID, res.SessionKey, res.PinRole, fresh)
		return res, nil
	}
	res, routeErr = routeRemaining()
	if escalationTurn != nil {
		committed, err := s.escalationHelper().Finish(ctx, escalationTurn, res.Decision, decisionPolicyGroup(res.Decision), routeErr)
		if err != nil {
			log.Warn("Escalation state commit failed; routing without intervention", "err", err, "ordinal", escalationTurn.Ordinal())
			if floor.RoutingApplied(res.Decision) {
				req = baselineRequest
				res = baselineTurn
				res, routeErr = routeRemaining()
			}
		} else {
			res.EscalationShadowMarked, res.EscalationScope, res.EscalationOrdinal = committed.ShadowMarked, committed.Scope, committed.Ordinal
		}
	}
	res.escalation = escalationTurn
	if escalationTurn.HasLLM() && routeErr == nil {
		if err := s.escalationHelper().ApplyLLM(ctx, escalationTurn, res.Decision); err != nil {
			log.Warn("LLM escalation application failed", "err", err)
			if escalationTurn.LLMActive() && floor.RoutingApplied(res.Decision) {
				req = baselineRequest
				res = baselineTurn
				res, routeErr = routeRemaining()
				res.escalation = escalationTurn
			}
		}
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
		StayScore:  candidateScoreForWithProvider(s.strategyPorts, fresh, stayIdentity, stayProvider),
		FreshScore: candidateScoreForWithArm(s.strategyPorts, fresh, fresh.ServedIdentity(), fresh.Provider, freshRosterArmID),
	}
	if stayPinOK && stayPin.Model == stayModel {
		shadow.StayProvider = stayPin.Provider
	}
	return shadow
}

func candidateScoreFor(ports continuity.StrategyPorts, dec router.Decision, servedIdentity string) *float64 {
	return candidateScoreForWithProvider(ports, dec, servedIdentity, dec.Provider)
}

func candidateScoreForWithProvider(ports continuity.StrategyPorts, dec router.Decision, servedIdentity, provider string) *float64 {
	return candidateScoreForWithArm(ports, dec, servedIdentity, provider, "")
}

func candidateScoreForWithArm(ports continuity.StrategyPorts, dec router.Decision, servedIdentity, provider, rosterArmID string) *float64 {
	return ports.Facts.CandidateScore(dec, servedIdentity, provider, rosterArmID)
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

func estimateSummaryTokens(s string) int {
	if s == "" {
		return 0
	}
	return len(s) / 4
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
