package proxy

import (
	"context"
	"log/slog"
	"net/http"
	"time"

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
)

type chatFinishedTurn struct {
	w                       http.ResponseWriter
	body                    []byte
	requestStart            time.Time
	facts                   requestcontext.RequestFacts
	rateLimit               *dispatch.ThrottleLedger
	log                     *slog.Logger
	env                     *translate.RequestEnvelope
	sessionKey              [16]byte
	feats                   translate.RoutingFeatures
	promptText              string
	embedInput              string
	strategy                router.Strategy
	taskArm                 taskArmTurn
	inboundLastUser         translate.LastUserMessageInfo
	inboundUserPromptOpenAI bool
	routeRequest            router.Request
	routeRes                turnLoopResult
	routeMs                 int64
	decision                router.Decision
	tt                      turntype.TurnType
	stickyHit               bool
	pinTier                 string
	pinAgeSec               int64
	reqPricing              catalog.Pricing
	actPricing              catalog.Pricing
	effortServed            dispatch.EffortResolution
	fastServed              bool
	contentCap              *captureWriter
	marker                  string
	responsesPreludeBuf     *preludeBuffer
	preludeBuf              *preludeBuffer
	proxyStart              time.Time
	proxyErr                error
	crossFormat             bool
	extractor               *telemetry.UsageExtractor
	respSummary             translate.ResponseSummary
	nativeRespSummary       bool
	cyberRefusalSeen        bool
	rescue                  *chatRescue
	winnerIdx               int
	finalProvider           string
	proxyMs                 int64
}

type chatTurnUsage struct {
	in                  int
	out                 int
	cacheCreation       int
	cacheRead           int
	usageMods           catalog.UsageModifiers
	baselineWarmPrefill int
	requestedInputCost  float64
}

func finishChatTurn(ctx context.Context, s *Service, t *chatFinishedTurn) error {
	u := chatTurnUsageFor(ctx, s, t)
	openaiObs := recordChatUpstream(ctx, s, t, u)
	subscriberSettlement := billChatTurn(ctx, s, t, u)
	armStrikeFields := strikeChatArms(ctx, s, t)
	fireChatTelemetry(ctx, s, t, u, openaiObs, subscriberSettlement)
	completeChatTurn(ctx, s, t, armStrikeFields)
	s.reportPolicyOutcome(ctx, t.routeRes, t.decision, t.effortServed, t.finalProvider, t.fastServed, t.feats.Tokens, u.in, u.out, u.cacheCreation, u.cacheRead, u.usageMods, t.routeMs, t.proxyMs, t.proxyErr, nil)
	return t.proxyErr
}

func chatTurnUsageFor(ctx context.Context, s *Service, t *chatFinishedTurn) chatTurnUsage {
	in, out := t.extractor.Tokens()
	cacheCreation, cacheRead := t.extractor.CacheTokens()
	usageMods := t.extractor.UsageModifiers()
	baselineWarmPrefill := t.routeRes.baselineWarmPrefillTokens(t.requestStart, cacheCreation, cacheRead, t.decision.Model, s.baselineFor(ctx, t.feats.Model), t.routeRequest.HistoryTruncated)
	requestedInputCost := catalog.CounterfactualInputCost(in, cacheCreation, cacheRead, baselineWarmPrefill, t.reqPricing, t.decision.Provider, usageMods)
	if !t.env.Stream() && t.proxyErr == nil {
		setRouterCostHeaders(t.w.Header(), routerResponseCostFromPricing(t.actPricing, t.decision.Provider, in, out, cacheCreation, cacheRead, usageMods))
	}

	if t.respSummary.StopReason == "" && s.ResolveNativeOpenAIResponseSignals(ctx) {
		if finishReason, toolCalls, observed := t.extractor.OpenAIChatResponse(); observed {
			t.respSummary.UpstreamFinishReason = finishReason
			t.respSummary.StopReason = finishReason
			t.respSummary.ToolUseBlocks = toolCalls
			t.nativeRespSummary = true
		}
	}
	return chatTurnUsage{in: in, out: out, cacheCreation: cacheCreation, cacheRead: cacheRead, usageMods: usageMods, baselineWarmPrefill: baselineWarmPrefill, requestedInputCost: requestedInputCost}
}

func recordChatUpstream(ctx context.Context, s *Service, t *chatFinishedTurn, u chatTurnUsage) observationContext {
	openaiUpstreamBuilder, openaiObs := recordUpstreamSpan(ctx, s.clock, upstreamSpan{
		extendedAttrs: true, taskArm: t.taskArm, respSummary: t.respSummary, fastServed: t.fastServed, crossFormat: t.crossFormat,
		facts: t.facts, feats: t.feats, decision: t.decision, provider: t.finalProvider, routeRes: &t.routeRes, sessionKey: t.sessionKey,
		extractor: t.extractor, inputTokens: u.in, outputTokens: u.out, cacheCreation: u.cacheCreation, cacheRead: u.cacheRead, usageMods: u.usageMods,
		requestedInputCost: u.requestedInputCost, baselineWarmPrefill: u.baselineWarmPrefill, reqPricing: t.reqPricing, actPricing: t.actPricing,
		subscriptionServed: s.costNeutralSubscriptionServed(ctx), proxyMs: t.proxyMs, requestStart: t.requestStart, proxyStart: t.proxyStart, proxyErr: t.proxyErr,
		effortServed: t.effortServed, captureMode: s.effectiveCaptureMode(ctx), strategyPorts: s.strategyPorts, strategy: t.strategy,
		subscriptionAttrs: func(b *telemetry.AttrBuilder) {
			s.applySubscriptionSpanTelemetry(ctx, b, t.feats.Model, t.decision.Model)
		},
		dispatch: &upstreamSpanDispatch{
			primaryProvider: t.rescue.primaryProvider, primaryModel: t.rescue.primaryModel, fallbackAttempts: t.winnerIdx,
			failoverUsed: t.rescue.failoverUsed, failoverAttempted: t.rescue.failoverAttempted,
			surfaceAttrs: func(b *telemetry.AttrBuilder) {
				b.Bool("dispatch.subscription_failover", t.rescue.codexFailoverUsed || t.rescue.claudeFailoverUsed).
					Bool("dispatch.cyber_refusal_retry", t.rescue.cyberRetryRan).
					Bool("dispatch.sibling_failover", t.rescue.siblingFailoverUsed)
				if responsesSurface, _ := ctx.Value(responsesSurfaceContextKey{}).(bool); responsesSurface {
					b.String("request.api_surface", string(requestAPISurfaceResponses))
				}
			},
		},
	})
	callLogBase := openaiUpstreamBuilder.Build()
	emitCallLog := func() {
		reqBody := t.body
		if h := deferredCallLogFrom(ctx); h != nil && h.requestBody != nil {
			reqBody = h.requestBody
		}
		respBody, respTrunc := capturedResponse(t.contentCap)
		s.recordCallLog(ctx, callLogBase, t.routeMs, t.proxyErr, reqBody, respBody, respTrunc)
		telemetry.Flush(ctx)
	}
	if h := deferredCallLogFrom(ctx); h != nil {
		h.fn = emitCallLog
	} else {
		emitCallLog()
	}

	s.recordTurnUsage(ctx, t.routeRes, t.finalProvider, t.decision.ServedIdentity(), u.in, u.out, u.cacheCreation, u.cacheRead, t.extractor.OutputLimitReached())
	s.clearRecoveredCooldown(ctx, t.routeRes, t.decision.Model, t.proxyErr)
	return openaiObs
}

func billChatTurn(ctx context.Context, s *Service, t *chatFinishedTurn, u chatTurnUsage) subscriberSettlementState {
	var subscriberSettlement subscriberSettlementState
	if turnUsageBillable(ctx, t.proxyErr, u.in, u.out, u.cacheCreation, u.cacheRead) {
		subscriberSettlement = s.emitBilling(ctx, t.facts.RequestID, t.facts.ExternalID, t.feats.Model, t.decision, t.actPricing, t.routeRes, u.in, u.out, u.cacheCreation, u.cacheRead, u.usageMods)
	}
	return subscriberSettlement
}

func strikeChatArms(ctx context.Context, s *Service, t *chatFinishedTurn) []any {
	armDemotedOAI := ""
	var armDemotionReasonOAI sessionpin.DemotionReason
	rescuedArmDemotedOAI := ""
	var rescuedArmDemotionReasonOAI sessionpin.DemotionReason
	if !t.routeRes.CallerModelPassthrough {
		s.maybeEvictPinAfterUpstreamErr(ctx, t.stickyHit, t.proxyErr, t.decision.Reason, sessionTarget{strategy: t.strategy, installationID: installationIDFromContext(ctx), sessionKey: t.routeRes.SessionKey}, stickyStateRole(t.routeRes))
		armDemotedOAI = s.maybeDemoteArmAfterCommittedStreamFailure(ctx, committed(t.preludeBuf) || committed(t.responsesPreludeBuf), t.routeRes.HardPinned, t.proxyErr, t.decision.Model, t.decision.Reason, sessionTarget{strategy: t.strategy, installationID: installationIDFromContext(ctx), sessionKey: t.routeRes.SessionKey}, stickyStateRole(t.routeRes), t.routeRes.PinRole)
		if armDemotedOAI != "" {
			armDemotionReasonOAI = sessionpin.DemotionReasonCommittedStreamFailure
		}
		primaryModelRescueRan := t.rescue.siblingRescueRan || t.decision.Model != t.rescue.primaryModel
		if providers.IsResponseHeaderTimeout(t.rescue.primaryFailureErr) && (primaryModelRescueRan || t.proxyErr != nil) {
			rescuedArmDemotedOAI, rescuedArmDemotionReasonOAI = s.maybeStrikeArmAfterRescuedFailure(ctx, primaryModelRescueRan, t.routeRes.HardPinned, t.rescue.primaryFailureErr, t.rescue.primaryDecision, sessionTarget{strategy: t.strategy, installationID: installationIDFromContext(ctx), sessionKey: t.routeRes.SessionKey}, stickyStateRole(t.routeRes), t.routeRes.PinRole)
		} else {
			rescuedArmDemotedOAI, rescuedArmDemotionReasonOAI = s.maybeStrikeArmAfterRescuedFailure(ctx, t.rescue.siblingRescueRan, t.routeRes.HardPinned, t.rescue.rescuedPrimaryErr, t.rescue.rescuedPrimary, sessionTarget{strategy: t.strategy, installationID: installationIDFromContext(ctx), sessionKey: t.routeRes.SessionKey}, stickyStateRole(t.routeRes), t.routeRes.PinRole)
		}
		s.strikeRescuedModelsForTurn(ctx, t.proxyErr, t.decision, t.routeRes.HardPinned, t.routeRes.SessionKey, stickyStateRole(t.routeRes), t.routeRes.PinRole)
		if t.proxyErr != nil {
			unrescuedStallDemoted := s.maybeDemoteArmAfterUnrescuedStall(ctx, t.rescue.siblingRescueRan, committed(t.preludeBuf) || committed(t.responsesPreludeBuf), t.routeRes.HardPinned, t.rescue.primarySubscriptionArmFailure, t.rescue.primaryDecision, sessionTarget{strategy: t.strategy, installationID: installationIDFromContext(ctx), sessionKey: t.routeRes.SessionKey}, stickyStateRole(t.routeRes), t.routeRes.PinRole)
			if armDemotedOAI == "" && unrescuedStallDemoted != "" {
				armDemotedOAI = unrescuedStallDemoted
				armDemotionReasonOAI = sessionpin.DemotionReasonUnrescuedStall
			}
		}
		s.maybeExpireSubscriptionArmPin(ctx, t.strategy, t.rescue.primarySubscriptionArmFailure, t.decision.Reason, installationIDFromContext(ctx), t.routeRes.SessionKey, stickyStateRole(t.routeRes))
		s.maybeDisableProviderAfterOverload(ctx, t.stickyHit, t.proxyErr, t.finalProvider, t.decision.Reason, sessionTarget{strategy: t.strategy, installationID: installationIDFromContext(ctx), sessionKey: t.routeRes.SessionKey}, stickyStateRole(t.routeRes), t.routeRes.PinRole)
	}
	return armStrikeLogFieldsWithPrimaryReason(armDemotedOAI, armDemotionReasonOAI, rescuedArmDemotedOAI, rescuedArmDemotionReasonOAI)
}

func fireChatTelemetry(ctx context.Context, s *Service, t *chatFinishedTurn, u chatTurnUsage, openaiObs observationContext, subscriberSettlement subscriberSettlementState) {
	installationIDOAI, _ := ctx.Value(requestcontext.InstallationIDContextKey{}).(string)
	if installationIDOAI != "" {
		credentialKeyPrefix, credentialKeySuffix, credSource := s.credentialKeyParts(ctx)
		s.markServedExternalKeyUsed(ctx, t.proxyErr)
		telOAI := chatTelemetryRow(ctx, s, t, u, openaiObs, installationIDOAI)
		telOAI.CredentialKeyPrefix, telOAI.CredentialKeySuffix, telOAI.CredentialSource = credentialKeyPrefix, credentialKeySuffix, credSource
		applyServedTurnTelemetry(ctx, s.strategyPorts, &telOAI, t.routeRes, t.decision, t.effortServed)
		applyTelemetrySettlement(&telOAI, turnUsageBillable(ctx, t.proxyErr, u.in, u.out, u.cacheCreation, u.cacheRead), subscriberSettlement)
		s.fireTelemetry(ctx, telOAI)
	}
}

func chatTelemetryRow(ctx context.Context, s *Service, t *chatFinishedTurn, u chatTurnUsage, openaiObs observationContext, installationIDOAI string) turnrecord.InsertTelemetryParams {
	return turnrecord.InsertTelemetryParams{
		InstallationID:           installationIDOAI,
		APIKeyID:                 apiKeyIDFromContext(ctx),
		RequestID:                t.facts.RequestID,
		SpanType:                 "router.upstream",
		TraceID:                  t.facts.RequestID,
		Timestamp:                t.requestStart,
		RequestedModel:           t.feats.Model,
		DecisionModel:            t.decision.Model,
		DecisionProvider:         t.decision.Provider,
		DecisionReason:           telemetryDecisionReason(ctx, t.decision.Reason),
		RequestedAllowedModels:   requestedAllowedModelsForTelemetry(ctx),
		EstimatedInputTokens:     int32(t.feats.Tokens),
		ContextSnapshot:          contextSnapshotJSONForDecision(t.w.Header(), t.facts.RequestID, t.feats.Model, t.decision.Model, t.decision.Provider, t.requestStart, s.clock.Now()),
		StickyHit:                t.stickyHit,
		PinTier:                  t.routeRes.PinTier,
		EmbedInput:               t.embedInput,
		InputTokens:              int32(u.in),
		OutputTokens:             int32(u.out),
		RequestedInputCostUSD:    u.requestedInputCost,
		RequestedOutputCostUSD:   catalog.EffectiveOutputCost(catalog.PromptTokens(u.in, u.cacheCreation, u.cacheRead, t.decision.Provider), u.out, t.reqPricing, u.usageMods),
		ActualInputCostUSD:       catalog.EffectiveInputCost(u.in, u.cacheCreation, u.cacheRead, t.actPricing, t.decision.Provider, u.usageMods),
		ActualOutputCostUSD:      catalog.EffectiveOutputCost(catalog.PromptTokens(u.in, u.cacheCreation, u.cacheRead, t.decision.Provider), u.out, t.actPricing, u.usageMods),
		RouteLatencyMs:           t.routeMs,
		UpstreamLatencyMs:        t.proxyMs,
		TotalLatencyMs:           s.clock.Now().Sub(t.requestStart).Milliseconds(),
		CrossFormat:              t.crossFormat,
		UpstreamStatusCode:       int32(upstreamStatus(t.proxyErr)),
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
		CacheCreationTokens:      cacheTokenPtr(u.cacheCreation),
		CacheCreation1hTokens:    cacheCreation1hPtr(u.cacheCreation, u.usageMods.CacheCreation1h),
		CacheReadTokens:          cacheTokenPtr(u.cacheRead),
		Speed:                    string(t.extractor.Speed()),
		InferenceGeo:             string(t.extractor.InferenceGeo()),
		ReasoningTokens:          cacheTokenPtr(t.extractor.ReasoningTokens()),
		DeviceID:                 t.facts.Client.DeviceID,
		SessionID:                t.facts.Client.SessionID,
		RouterUserID:             auth.UserIDFrom(ctx),
		ClientApp:                t.facts.Client.TelemetryClientApp(),
		TurnType:                 string(t.routeRes.TurnType),
		RolloutID:                openaiObs.RolloutID,
		UpstreamFinishReason:     stringPtrOrEmpty(t.respSummary.UpstreamFinishReason),
		StopReason:               stringPtrOrEmpty(t.respSummary.StopReason),
		ToolUseBlocks:            int32PtrIfKnown(int32(t.respSummary.ToolUseBlocks), t.respSummary.StopReason != ""),
		InvalidToolArgsBlocks:    int32PtrIfKnown(int32(t.respSummary.InvalidToolArgsBlocks), t.respSummary.StopReason != "" && !t.nativeRespSummary),
		FailoverUsed:             boolPtrTrue(t.rescue.failoverUsed),
		FailoverAttempted:        boolPtrTrue(t.rescue.failoverAttempted),
		SessionKey:               t.sessionKey[:],
		Role:                     t.routeRes.PinRole,
		FreshDecisionModel:       openaiObs.FreshDecisionModel,
		FreshCandidateScores:     openaiObs.FreshCandidateScores,
		PinAgeSec:                int64PtrIf(t.stickyHit && t.pinAgeSec > 0, t.pinAgeSec),
		ToolResultBytes:          toolResultBytesPtr(t.inboundLastUser, t.tt),
		UserPrompt:               &t.inboundUserPromptOpenAI,
		ErrorClass:               classifyTurnError(t.proxyErr, t.respSummary.StopReason, t.respSummary.InvalidToolArgsBlocks),
		FailureOwner:             turnFailureOwnerAfterFinish(ctx, t.proxyErr, classifyTurnError(t.proxyErr, t.respSummary.StopReason, t.respSummary.InvalidToolArgsBlocks), t.respSummary.UpstreamFinishReason),
		UnifiedLimitHeaders:      unifiedLimitHeadersJSON(ctx),
		CodexLimitHeaders:        codexLimitHeadersJSON(ctx),
	}
}

func completeChatTurn(ctx context.Context, s *Service, t *chatFinishedTurn, armStrikeFields []any) {
	if t.cyberRefusalSeen && !t.routeRes.CallerModelPassthrough {
		s.repinOffRefusingModel(ctx, t.strategy, t.routeRes.SessionKey, stickyStateRole(t.routeRes), t.rescue.primaryDecision, providers.CyberPolicyErrorCode, t.rescue.primaryDecision.Provider)
	}

	logToolCallIssues(t.log, t.respSummary.ToolCallIssues, t.decision.Model, t.finalProvider, t.routeRes.SessionKey)

	servedCredentialSource, servedSubscriptionTier := s.servedCredentialLogValues(ctx)
	t.log.Info("ProxyOpenAIChatCompletion complete", append(append([]any{"requested_model", t.feats.Model, "baseline_model", s.baselineFor(ctx, t.feats.Model), "decision_model", t.decision.Model, "decision_provider", t.decision.Provider, "primary_provider", t.rescue.primaryProvider, "primary_model", t.rescue.primaryModel, "fallback_attempts", t.winnerIdx, "failover_used", t.rescue.failoverUsed, "failover_attempted", t.rescue.failoverAttempted, "subscription_failover", t.rescue.codexFailoverUsed || t.rescue.claudeFailoverUsed, "decision_reason", t.decision.Reason, "requested_tier", t.routeRes.RequestedTier.String(), "decision_tier", catalog.TierFor(t.decision.Model).String(), "embedded_tokens", len(t.promptText) / 4, "total_input_tokens", t.feats.Tokens, "has_tools", t.feats.HasTools, "embed_input", t.embedInput, "cross_format", t.crossFormat, "sticky_hit", t.stickyHit, "pin_tier", t.pinTier, "turn_type", string(t.tt), "route_ms", t.routeMs, "proxy_ms", t.proxyMs, "proxy_err", t.proxyErr, "error_class", classifyTurnError(t.proxyErr, t.respSummary.StopReason, t.respSummary.InvalidToolArgsBlocks), "failure_owner", turnFailureOwnerAfterFinish(ctx, t.proxyErr, classifyTurnError(t.proxyErr, t.respSummary.StopReason, t.respSummary.InvalidToolArgsBlocks), t.respSummary.UpstreamFinishReason), "upstream_err_body", s.zdrLogField(ctx, providers.UpstreamErrorBodyMessage(t.proxyErr)), "upstream_status", upstreamStatus(t.proxyErr), "upstream_finish_reason", t.respSummary.UpstreamFinishReason, "resp_stop_reason", t.respSummary.StopReason, "stop_reason_promoted", t.respSummary.StopReasonPromoted, "tool_use_blocks", t.respSummary.ToolUseBlocks, "invalid_tool_args_blocks", t.respSummary.InvalidToolArgsBlocks, "tool_call_invalid_blocks", len(t.respSummary.ToolCallIssues), "routing_marker", t.marker, "prior_served_model", t.routeRes.PriorServedModel, "hard_pinned", t.routeRes.HardPinned, "credential_source", servedCredentialSource, "client_app", completionLogClientApp(t.facts.Client), "subscription_tier", servedSubscriptionTier}, append(append(armStrikeFields, plannerLogFields(t.routeRes)...), t.rateLimit.CompletionLogFields()...)...), downgradeShadowLogFields(t.routeRes)...)...)
}
