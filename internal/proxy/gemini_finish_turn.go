package proxy

import (
	"context"
	"log/slog"
	"net/http"
	"time"
	"weave-os/router/internal/routingpolicy"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/telemetry"
	"weave-os/router/internal/telemetry/turnrecord"
	"weave-os/router/internal/translate"

	"github.com/google/uuid"
)

type geminiFinishedTurn struct {
	w                 http.ResponseWriter
	body              []byte
	rateLimit         *dispatch.ThrottleLedger
	log               *slog.Logger
	requestStart      time.Time
	facts             requestcontext.RequestFacts
	inboundLastUser   translate.LastUserMessageInfo
	inboundUserPrompt bool
	responseBuffer    *responseCostBuffer
	feats             translate.RoutingFeatures
	promptText        string
	embedInput        string
	sessionKey        [16]byte
	strategy          router.Strategy
	routeRequest      router.Request
	routeMs           int64
	routeRes          routingpolicy.TurnRoute
	decision          router.Decision
	tt                turntype.TurnType
	stickyHit         bool
	pinTier           string
	pinAgeSec         int64
	reqPricing        catalog.Pricing
	actPricing        catalog.Pricing
	effortServed      dispatch.EffortResolution
	proxyStart        time.Time
	extractor         *telemetry.UsageExtractor
	contentCap        *captureWriter
	preludeBuf        *preludeBuffer
	primaryDecision   router.Decision
	proxyErr          error
	primaryFailureErr error
	proxyMs           int64
	primaryProvider   string
	finalProvider     string
}

type geminiTurnUsage struct {
	in                  int
	out                 int
	cacheCreation       int
	cacheRead           int
	usageMods           catalog.UsageModifiers
	baselineWarmPrefill int
	requestedInputCost  float64
}

func finishGeminiTurn(ctx context.Context, s *Service, t *geminiFinishedTurn) error {
	usage := geminiTurnUsageFor(ctx, s, t)
	geminiObs := recordGeminiUpstream(ctx, s, t, usage)
	settleGeminiTurn(ctx, s, t, usage, geminiSubscriberTelemetry(ctx, s, t, usage, geminiObs))
	demotionLogFields := strikeGeminiArms(ctx, s, t)
	servedCredentialSource, servedSubscriptionTier := s.servedCredentialLogValues(ctx)
	t.log.Info("ProxyGeminiGenerateContent complete", append(append([]any{"requested_model", t.feats.Model, "baseline_model", s.baselineFor(ctx, t.feats.Model), "decision_model", t.decision.Model, "decision_provider", t.decision.Provider, "decision_reason", t.decision.Reason, "embedded_tokens", len(t.promptText) / 4, "total_input_tokens", t.feats.Tokens, "has_tools", t.feats.HasTools, "embed_input", t.embedInput, "sticky_hit", t.stickyHit, "pin_tier", t.pinTier, "turn_type", string(t.tt), "route_ms", t.routeMs, "proxy_ms", t.proxyMs, "proxy_err", t.proxyErr, "error_class", classifyTurnError(t.proxyErr, "", 0), "failure_owner", turnFailureOwnerFor(ctx, t.proxyErr, classifyTurnError(t.proxyErr, "", 0)), "upstream_status", upstreamStatus(t.proxyErr), "credential_source", servedCredentialSource, "client_app", completionLogClientApp(t.facts.Client), "subscription_tier", servedSubscriptionTier}, demotionLogFields...), append(plannerLogFields(t.routeRes), t.rateLimit.CompletionLogFields()...)...)...)
	s.reportPolicyOutcome(ctx, t.routeRes, t.decision, t.effortServed, t.decision.Provider, false, t.feats.Tokens, usage.in, usage.out, usage.cacheCreation, usage.cacheRead, usage.usageMods, t.routeMs, t.proxyMs, t.proxyErr, nil)
	return t.proxyErr
}

func geminiTurnUsageFor(ctx context.Context, s *Service, t *geminiFinishedTurn) geminiTurnUsage {
	in, out := t.extractor.Tokens()
	cacheCreation, cacheRead := t.extractor.CacheTokens()
	usageMods := t.extractor.UsageModifiers()
	baselineWarmPrefill := baselineWarmPrefillTokens(t.routeRes, t.requestStart, cacheCreation, cacheRead, t.decision.Model, s.baselineFor(ctx, t.feats.Model), t.routeRequest.HistoryTruncated)
	requestedInputCost := catalog.CounterfactualInputCost(in, cacheCreation, cacheRead, baselineWarmPrefill, t.reqPricing, t.decision.Provider, usageMods)
	if t.responseBuffer != nil && t.proxyErr == nil {
		setRouterCostHeaders(t.w.Header(), routerResponseCostFromPricing(t.actPricing, t.decision.Provider, in, out, cacheCreation, cacheRead, usageMods))
	}
	return geminiTurnUsage{in: in, out: out, cacheCreation: cacheCreation, cacheRead: cacheRead, usageMods: usageMods, baselineWarmPrefill: baselineWarmPrefill, requestedInputCost: requestedInputCost}
}

func recordGeminiUpstream(ctx context.Context, s *Service, t *geminiFinishedTurn, usage geminiTurnUsage) observationContext {
	geminiUpstreamBuilder, geminiObs := recordUpstreamSpan(ctx, s.clock, upstreamSpan{
		facts: t.facts, feats: t.feats, decision: t.decision, provider: t.decision.Provider, routeRes: &t.routeRes, sessionKey: t.sessionKey,
		extractor: t.extractor, inputTokens: usage.in, outputTokens: usage.out, cacheCreation: usage.cacheCreation, cacheRead: usage.cacheRead, usageMods: usage.usageMods,
		requestedInputCost: usage.requestedInputCost, baselineWarmPrefill: usage.baselineWarmPrefill, reqPricing: t.reqPricing, actPricing: t.actPricing,
		subscriptionServed: s.costNeutralSubscriptionServed(ctx), proxyMs: t.proxyMs, requestStart: t.requestStart, proxyStart: t.proxyStart, proxyErr: t.proxyErr,
		effortServed: t.effortServed, captureMode: s.effectiveCaptureMode(ctx), strategyPorts: s.strategyPorts, strategy: t.strategy,
		subscriptionAttrs: func(b *telemetry.AttrBuilder) {
			s.applySubscriptionSpanTelemetry(ctx, b, t.feats.Model, t.decision.Model)
		},
	})
	respBody, respTrunc := capturedResponse(t.contentCap)
	s.recordCallLog(ctx, geminiUpstreamBuilder.Build(), t.routeMs, t.proxyErr, t.body, respBody, respTrunc)
	telemetry.Flush(ctx)

	s.recordTurnUsage(ctx, t.routeRes, t.finalProvider, t.decision.ServedIdentity(), usage.in, usage.out, usage.cacheCreation, usage.cacheRead, t.extractor.OutputLimitReached())
	clearRecoveredCooldown(ctx, s, t.routeRes, t.decision.Model, t.proxyErr)
	return geminiObs
}

func geminiSubscriberTelemetry(ctx context.Context, s *Service, t *geminiFinishedTurn, usage geminiTurnUsage, geminiObs observationContext) *turnrecord.InsertTelemetryParams {
	var subscriberTelemetry *turnrecord.InsertTelemetryParams
	if t.facts.InstallationID != uuid.Nil {
		credentialKeyPrefix, credentialKeySuffix, credentialSource := s.credentialKeyParts(ctx)
		s.markServedExternalKeyUsed(ctx, t.proxyErr)
		errorClass := classifyTurnError(t.proxyErr, "", 0)
		if errorClass == "" && t.extractor.OutputLimitReached() {
			errorClass = turnrecord.TurnErrorMaxTokens
		}
		telemetryParams := turnrecord.InsertTelemetryParams{
			InstallationID:         t.facts.InstallationID.String(),
			APIKeyID:               apiKeyIDFromContext(ctx),
			RequestID:              t.facts.RequestID,
			SpanType:               "router.upstream",
			TraceID:                t.facts.RequestID,
			Timestamp:              t.requestStart,
			RequestedModel:         t.feats.Model,
			DecisionModel:          t.decision.Model,
			DecisionProvider:       t.finalProvider,
			DecisionReason:         telemetryDecisionReason(ctx, t.decision.Reason),
			RequestedAllowedModels: requestedAllowedModelsForTelemetry(ctx),
			EstimatedInputTokens:   int32(t.feats.Tokens),
			ContextSnapshot:        contextSnapshotJSON(t.w.Header(), t.facts.RequestID, t.feats.Model, t.requestStart, s.clock.Now()),
			StickyHit:              t.stickyHit,
			PinTier:                t.routeRes.PinTier,
			EmbedInput:             t.embedInput,
			InputTokens:            int32(usage.in),
			OutputTokens:           int32(usage.out),
			RequestedInputCostUSD:  usage.requestedInputCost,
			RequestedOutputCostUSD: catalog.EffectiveOutputCost(catalog.PromptTokens(usage.in, usage.cacheCreation, usage.cacheRead, t.finalProvider), usage.out, t.reqPricing, usage.usageMods),
			ActualInputCostUSD:     catalog.EffectiveInputCost(usage.in, usage.cacheCreation, usage.cacheRead, t.actPricing, t.finalProvider, usage.usageMods),
			ActualOutputCostUSD:    catalog.EffectiveOutputCost(catalog.PromptTokens(usage.in, usage.cacheCreation, usage.cacheRead, t.finalProvider), usage.out, t.actPricing, usage.usageMods),
			RouteLatencyMs:         t.routeMs,
			UpstreamLatencyMs:      t.proxyMs,
			TotalLatencyMs:         s.clock.Now().Sub(t.requestStart).Milliseconds(),
			CrossFormat:            false,
			UpstreamStatusCode:     int32(upstreamStatus(t.proxyErr)),
			CandidateModels:        geminiObs.CandidateModels,
			ChosenScore:            geminiObs.ChosenScore,
			CandidateScores:        geminiObs.CandidateScores,
			Propensity:             geminiObs.Propensity,
			Strategy:               geminiObs.Strategy,
			RouteID:                geminiObs.RouteID,
			PolicyRouteKey:         geminiObs.PolicyRouteKey,
			PolicyArtifactID:       geminiObs.PolicyArtifactID,
			PolicyArtifactSHA256:   geminiObs.PolicyArtifactSHA256,
			RosterVersion:          geminiObs.RosterVersion,
			SidecarSchemaVersion:   geminiObs.SidecarSchemaVersion,
			TrainingAllowed:        geminiObs.TrainingAllowed,
			CaptureMode:            geminiObs.CaptureMode,
			DebugRef:               geminiObs.DebugRef,
			TTFTMs:                 geminiObs.TTFTMs,
			CacheCreationTokens:    cacheTokenPtr(usage.cacheCreation),
			CacheReadTokens:        cacheTokenPtr(usage.cacheRead),
			ReasoningTokens:        cacheTokenPtr(t.extractor.ReasoningTokens()),
			DeviceID:               t.facts.Client.DeviceID,
			SessionID:              t.facts.Client.SessionID,
			RouterUserID:           auth.UserIDFrom(ctx),
			ClientApp:              t.facts.Client.TelemetryClientApp(),
			TurnType:               string(t.routeRes.TurnType),
			RolloutID:              geminiObs.RolloutID,
			FailoverUsed:           boolPtrTrue(t.finalProvider != t.primaryProvider),
			SessionKey:             t.sessionKey[:],
			Role:                   t.routeRes.PinRole,
			FreshDecisionModel:     geminiObs.FreshDecisionModel,
			FreshCandidateScores:   geminiObs.FreshCandidateScores,
			PinAgeSec:              int64PtrIf(t.stickyHit && t.pinAgeSec > 0, t.pinAgeSec),
			ToolResultBytes:        toolResultBytesPtr(t.inboundLastUser, t.tt),
			ErrorClass:             errorClass,
			FailureOwner:           turnFailureOwnerFor(ctx, t.proxyErr, errorClass),
			UserPrompt:             &t.inboundUserPrompt,
			CredentialKeyPrefix:    credentialKeyPrefix,
			CredentialKeySuffix:    credentialKeySuffix,
			CredentialSource:       credentialSource,
		}
		applyServedTurnTelemetry(ctx, s.strategyPorts, &telemetryParams, t.routeRes, t.decision, t.effortServed)
		subscriberTelemetry = &telemetryParams
	}
	return subscriberTelemetry
}

func settleGeminiTurn(ctx context.Context, s *Service, t *geminiFinishedTurn, usage geminiTurnUsage, subscriberTelemetry *turnrecord.InsertTelemetryParams) {
	var subscriberSettlement subscriberSettlementState
	if turnUsageBillable(ctx, t.proxyErr, usage.in, usage.out, usage.cacheCreation, usage.cacheRead) {
		subscriberSettlement = s.emitBilling(ctx, t.facts.RequestID, t.facts.ExternalID, t.feats.Model, t.decision, t.actPricing, t.routeRes, usage.in, usage.out, usage.cacheCreation, usage.cacheRead, usage.usageMods)
	}
	if subscriberTelemetry != nil {
		applyTelemetrySettlement(subscriberTelemetry, turnUsageBillable(ctx, t.proxyErr, usage.in, usage.out, usage.cacheCreation, usage.cacheRead), subscriberSettlement)
		s.fireTelemetry(ctx, *subscriberTelemetry)
	}
}

func strikeGeminiArms(ctx context.Context, s *Service, t *geminiFinishedTurn) []any {
	armDemoted := ""
	var armDemotionReason sessionpin.DemotionReason
	primaryFailureDemoted := ""
	var primaryFailureDemotionReason sessionpin.DemotionReason
	if !t.routeRes.CallerModelPassthrough {
		s.maybeDisableProviderAfterOverload(ctx, t.stickyHit, t.proxyErr, t.finalProvider, t.decision.Reason, sessionTarget{strategy: t.strategy, installationID: t.facts.InstallationID, sessionKey: t.routeRes.SessionKey}, stickyStateRole(t.routeRes), t.routeRes.PinRole)
		armDemoted = s.maybeDemoteArmAfterCommittedStreamFailure(ctx, committed(t.preludeBuf), t.routeRes.HardPinned, t.proxyErr, t.decision.Model, t.decision.Reason, sessionTarget{strategy: t.strategy, installationID: t.facts.InstallationID, sessionKey: t.routeRes.SessionKey}, stickyStateRole(t.routeRes), t.routeRes.PinRole)
		if armDemoted != "" {
			armDemotionReason = sessionpin.DemotionReasonCommittedStreamFailure
		}
		if providers.IsResponseHeaderTimeout(t.primaryFailureErr) && (t.proxyErr != nil || t.decision.Model != t.primaryDecision.Model) {
			primaryFailureDemoted, primaryFailureDemotionReason = s.maybeStrikeArmAfterRescuedFailure(ctx, false, t.routeRes.HardPinned, t.primaryFailureErr, t.primaryDecision, sessionTarget{strategy: t.strategy, installationID: t.facts.InstallationID, sessionKey: t.routeRes.SessionKey}, stickyStateRole(t.routeRes), t.routeRes.PinRole)
		}
		if t.proxyErr != nil {
			unrescuedStallDemoted := s.maybeDemoteArmAfterUnrescuedStall(ctx, false, committed(t.preludeBuf), t.routeRes.HardPinned, t.primaryFailureErr, t.primaryDecision, sessionTarget{strategy: t.strategy, installationID: t.facts.InstallationID, sessionKey: t.routeRes.SessionKey}, stickyStateRole(t.routeRes), t.routeRes.PinRole)
			if unrescuedStallDemoted != "" {
				armDemoted = unrescuedStallDemoted
				armDemotionReason = sessionpin.DemotionReasonUnrescuedStall
			}
		}
	}

	return armStrikeLogFieldsWithPrimaryReason(armDemoted, armDemotionReason, primaryFailureDemoted, primaryFailureDemotionReason)
}
