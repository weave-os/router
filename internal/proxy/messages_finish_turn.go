package proxy

import (
	"context"
	"log/slog"
	"net/http"
	"time"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/routingpolicy"
	"weave-os/router/internal/telemetry/respobserve"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/conversation"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/telemetry"
	"weave-os/router/internal/telemetry/capture"
	"weave-os/router/internal/telemetry/iolog"
	"weave-os/router/internal/telemetry/turnrecord"
	"weave-os/router/internal/translate"

	"github.com/google/uuid"
)

type messagesFinishedTurn struct {
	w                      http.ResponseWriter
	body                   []byte
	facts                  requestcontext.RequestFacts
	requestStart           time.Time
	rateLimit              *dispatch.ThrottleLedger
	log                    *slog.Logger
	env                    *translate.RequestEnvelope
	responseBuffer         *responseCostBuffer
	agentShadowMode        bool
	sessionKey             [16]byte
	feats                  translate.RoutingFeatures
	promptText             string
	embedInput             string
	strategy               router.Strategy
	taskArm                taskArmTurn
	inboundSpiralSignals   conversation.SpiralSignals
	inboundSpiralReasons   []conversation.SpiralReason
	inboundSpiralComputed  bool
	inboundLastUser        translate.LastUserMessageInfo
	inboundUserPrompt      bool
	inboundLatestToolCalls []byte
	overflowEstimate       int
	req                    router.Request
	routeRes               routingpolicy.TurnRoute
	decision               router.Decision
	tt                     turntype.TurnType
	stickyHit              bool
	pinAgeSec              int64
	routeMs                int64
	reqPricing             catalog.Pricing
	actPricing             catalog.Pricing
	opts                   translate.EmitOptions
	effortServed           dispatch.EffortResolution
	contentCap             *capture.Writer
	policyOutcomeCap       *capture.Writer
	preludeBuf             *preludeBuffer
	refusalObs             *respobserve.RefusalObserver
	toolUseObs             *respobserve.ToolUseObserver
	streamCut              *dispatch.StreamCutObserver
	proxyStart             time.Time
	proxyErr               error
	crossFormat            bool
	extractor              *telemetry.UsageExtractor
	respSummary            translate.ResponseSummary
	reqStats               providers.RequestMutationStats
	marker                 string
	fastServed             bool
	rescue                 *dispatch.MessagesRescue
	winnerIdx              int
	finalProvider          string
	proxyMs                int64
}

type messagesTurnUsage struct {
	nativeRespSummary    bool
	terminalFinishReason string
	terminalToolUse      respobserve.LastToolUse
	endedOnToolUse       bool
	toolErrorTally       map[string]respobserve.ToolCallTally
	in                   int
	out                  int
	cacheCreation        int
	cacheRead            int
	usageMods            catalog.UsageModifiers
	baselineWarmPrefill  int
	requestedInputCost   float64
}

func finishMessagesTurn(ctx context.Context, s *Service, t *messagesFinishedTurn) error {
	usage := messagesTurnUsageFor(ctx, s, t)
	obs := recordMessagesUpstream(ctx, s, t, usage)
	settleMessagesTurn(ctx, s, t, usage, messagesSubscriberTelemetry(ctx, s, t, usage, obs))
	completeMessagesTurn(ctx, s, t, usage, strikeMessagesArms(ctx, s, t))
	policyRespBody, policyRespTrunc := capturedResponse(t.policyOutcomeCap)
	var policyResp *policyOutcomeResponse
	if t.policyOutcomeCap != nil {
		policyResp = &policyOutcomeResponse{Body: policyRespBody, Truncated: policyRespTrunc}
	}
	if !t.agentShadowMode {
		s.reportPolicyOutcome(ctx, t.routeRes, t.decision, t.effortServed, t.finalProvider, t.fastServed, t.feats.Tokens, usage.in, usage.out, usage.cacheCreation, usage.cacheRead, usage.usageMods, t.routeMs, t.proxyMs, t.proxyErr, policyResp)
	}
	return t.proxyErr
}

func messagesTurnUsageFor(ctx context.Context, s *Service, t *messagesFinishedTurn) messagesTurnUsage {
	if t.refusalObs.Refused && t.respSummary.StopReason == "" {
		t.respSummary.StopReason = respobserve.AnthropicRefusalStopReason
	}

	nativeRespSummary := false
	if t.respSummary.StopReason == "" && s.turnTelemetry.flags.resolveNativeAnthropicResponseSignals(ctx) {
		if stopReason, toolUseBlocks, observed := t.extractor.AnthropicResponse(); observed {
			t.respSummary.UpstreamFinishReason = stopReason
			t.respSummary.StopReason = stopReason
			t.respSummary.ToolUseBlocks = toolUseBlocks
			nativeRespSummary = true
		}
	}
	terminalFinishReason := t.respSummary.UpstreamFinishReason
	if nativeRespSummary {
		terminalFinishReason = ""
	}
	terminalToolUse, endedOnToolUse := t.toolUseObs.Terminal()
	toolErrorTally := respobserve.ToolErrorCounts(t.env.AssistantToolCallOutcomes())

	in, out := t.extractor.Tokens()
	cacheCreation, cacheRead := t.extractor.CacheTokens()
	usageMods := t.extractor.UsageModifiers()
	baselineWarmPrefill := baselineWarmPrefillTokens(t.routeRes, t.requestStart, cacheCreation, cacheRead, t.decision.Model, s.baselineFor(ctx, t.feats.Model), t.req.HistoryTruncated)
	requestedInputCost := catalog.CounterfactualInputCost(in, cacheCreation, cacheRead, baselineWarmPrefill, t.reqPricing, t.decision.Provider, usageMods)
	if t.responseBuffer != nil && t.proxyErr == nil {
		setRouterCostHeaders(t.w.Header(), routerResponseCostFromPricing(t.actPricing, t.decision.Provider, in, out, cacheCreation, cacheRead, usageMods))
	}
	return messagesTurnUsage{nativeRespSummary: nativeRespSummary, terminalFinishReason: terminalFinishReason, terminalToolUse: terminalToolUse, endedOnToolUse: endedOnToolUse, toolErrorTally: toolErrorTally, in: in, out: out, cacheCreation: cacheCreation, cacheRead: cacheRead, usageMods: usageMods, baselineWarmPrefill: baselineWarmPrefill, requestedInputCost: requestedInputCost}
}

func recordMessagesUpstream(ctx context.Context, s *Service, t *messagesFinishedTurn, usage messagesTurnUsage) observationContext {
	upstreamBuilder, obs := recordUpstreamSpan(ctx, s.clock, upstreamSpan{
		extendedAttrs: true, taskArm: t.taskArm, respSummary: t.respSummary, refusal: t.refusalObs, fastServed: t.fastServed, crossFormat: t.crossFormat,
		facts: t.facts, feats: t.feats, decision: t.decision, provider: t.finalProvider, routeRes: &t.routeRes, sessionKey: t.sessionKey,
		extractor: t.extractor, inputTokens: usage.in, outputTokens: usage.out, cacheCreation: usage.cacheCreation, cacheRead: usage.cacheRead, usageMods: usage.usageMods,
		requestedInputCost: usage.requestedInputCost, baselineWarmPrefill: usage.baselineWarmPrefill, reqPricing: t.reqPricing, actPricing: t.actPricing,
		subscriptionServed: s.costNeutralSubscriptionServed(ctx), proxyMs: t.proxyMs, requestStart: t.requestStart, proxyStart: t.proxyStart, proxyErr: t.proxyErr,
		effortServed: t.effortServed, captureMode: s.effectiveCaptureMode(ctx), strategyPorts: s.strategyPorts, strategy: t.strategy,
		subscriptionAttrs: func(b *telemetry.AttrBuilder) {
			s.applySubscriptionSpanTelemetry(ctx, b, t.feats.Model, t.decision.Model)
		},
		dispatch: &upstreamSpanDispatch{
			primaryProvider: t.rescue.PrimaryProvider, primaryModel: t.rescue.PrimaryModel, fallbackAttempts: t.winnerIdx,
			failoverUsed: t.rescue.FailoverUsed, failoverAttempted: t.rescue.FailoverAttempted,
			surfaceAttrs: func(b *telemetry.AttrBuilder) {
				b.Bool("dispatch.baseline_failover", t.rescue.BaselineFailoverUsed).
					Bool("dispatch.subscription_failover", t.rescue.SubscriptionFailoverUsed).
					Bool("dispatch.sibling_failover", t.rescue.SiblingFailoverUsed).
					Bool("dispatch.early_stop_redo", t.rescue.EarlyStopRedone)
			},
		},
	})
	respBody, respTrunc := capturedResponse(t.contentCap)
	if !t.agentShadowMode {
		s.recordCallLog(ctx, upstreamBuilder.Build(), t.routeMs, t.proxyErr, t.body, respBody, respTrunc)
	}
	telemetry.Flush(ctx)

	if !t.agentShadowMode {
		s.recordTurnUsage(ctx, withServedCacheWriteTTL(t.routeRes, t.extractor), t.finalProvider, t.decision.ServedIdentity(), usage.in, usage.out, usage.cacheCreation, usage.cacheRead, t.extractor.OutputLimitReached())
		clearRecoveredCooldown(ctx, s, t.routeRes, t.decision.Model, t.proxyErr)
	}
	return obs
}

func messagesSubscriberTelemetry(ctx context.Context, s *Service, t *messagesFinishedTurn, usage messagesTurnUsage, obs observationContext) *turnrecord.InsertTelemetryParams {
	var subscriberTelemetry *turnrecord.InsertTelemetryParams
	if !t.agentShadowMode && t.facts.InstallationID != uuid.Nil {
		credentialKeyPrefix, credentialKeySuffix, credSource := s.credentialKeyParts(ctx)
		s.markServedExternalKeyUsed(ctx, t.proxyErr)
		degenerateShadow := t.proxyErr == nil && !usage.nativeRespSummary && isDegenerateResponse(usage.out, t.respSummary.ToolUseBlocks, t.respSummary.StopReason, t.respSummary.StopReasonDemoted)
		if degenerateShadow {
			t.log.Info("router.degenerate_shadow",
				"model", t.decision.Model,
				"provider", t.finalProvider,
				"output_tokens", usage.out,
				"tool_use_blocks", t.respSummary.ToolUseBlocks,
				"stop_reason", t.respSummary.StopReason,
				"upstream_finish_reason", t.respSummary.UpstreamFinishReason,
				"would_failover", true,
			)
			s.evictPinAfterDegenerateResponse(ctx, t.strategy, t.stickyHit, t.decision.Reason, t.facts.InstallationID, t.routeRes.SessionKey, stickyStateRole(t.routeRes))
		}
		tel := messagesTelemetryRow(ctx, s, t, usage, obs)
		tel.DegenerateShadow = boolPtrOrNil(degenerateShadow)
		tel.CredentialKeyPrefix, tel.CredentialKeySuffix, tel.CredentialSource = credentialKeyPrefix, credentialKeySuffix, credSource
		applyServedTurnTelemetry(ctx, s.strategyPorts, &tel, t.routeRes, t.decision, t.effortServed)
		signalTurn := t.tt == turntype.MainLoop || t.tt == turntype.ToolResult
		applyTurnSignalTelemetry(&tel, t.inboundSpiralSignals, t.inboundSpiralReasons,
			t.inboundSpiralComputed && signalTurn,
			obs.TrainingAllowed,
			s.effectiveCaptureMode(ctx))
		applyClientGitContextTelemetry(ctx, &tel, t.routeRes.SessionFirstTurn, t.env.SystemBlocks())
		subscriberTelemetry = &tel
	}
	return subscriberTelemetry
}

func messagesTelemetryRow(ctx context.Context, s *Service, t *messagesFinishedTurn, usage messagesTurnUsage, obs observationContext) turnrecord.InsertTelemetryParams {
	return turnrecord.InsertTelemetryParams{
		InstallationID:           t.facts.InstallationID.String(),
		APIKeyID:                 requestcontext.APIKeyIDFromContext(ctx),
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
		InputTokens:              int32(usage.in),
		OutputTokens:             int32(usage.out),
		RequestedInputCostUSD:    usage.requestedInputCost,
		RequestedOutputCostUSD:   catalog.EffectiveOutputCost(catalog.PromptTokens(usage.in, usage.cacheCreation, usage.cacheRead, t.decision.Provider), usage.out, t.reqPricing, usage.usageMods),
		ActualInputCostUSD:       catalog.EffectiveInputCost(usage.in, usage.cacheCreation, usage.cacheRead, t.actPricing, t.decision.Provider, usage.usageMods),
		ActualOutputCostUSD:      catalog.EffectiveOutputCost(catalog.PromptTokens(usage.in, usage.cacheCreation, usage.cacheRead, t.decision.Provider), usage.out, t.actPricing, usage.usageMods),
		RouteLatencyMs:           t.routeMs,
		UpstreamLatencyMs:        t.proxyMs,
		TotalLatencyMs:           s.clock.Now().Sub(t.requestStart).Milliseconds(),
		CrossFormat:              t.crossFormat,
		UpstreamStatusCode:       int32(upstreamStatus(t.proxyErr)),
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
		CacheCreationTokens:      cacheTokenPtr(usage.cacheCreation),
		CacheCreation1hTokens:    cacheCreation1hPtr(usage.cacheCreation, usage.usageMods.CacheCreation1h),
		CacheReadTokens:          cacheTokenPtr(usage.cacheRead),
		Speed:                    string(t.extractor.Speed()),
		InferenceGeo:             string(t.extractor.InferenceGeo()),
		ReasoningTokens:          cacheTokenPtr(t.extractor.ReasoningTokens()),
		DeviceID:                 t.facts.Client.DeviceID,
		SessionID:                t.facts.Client.SessionID,
		RouterUserID:             auth.UserIDFrom(ctx),
		ClientApp:                t.facts.Client.TelemetryClientApp(),
		TurnType:                 string(t.routeRes.TurnType),
		RolloutID:                obs.RolloutID,
		UpstreamFinishReason:     stringPtrOrEmpty(t.respSummary.UpstreamFinishReason),
		StopReason:               stringPtrOrEmpty(t.respSummary.StopReason),
		ToolUseBlocks:            int32PtrIfKnown(int32(t.respSummary.ToolUseBlocks), t.respSummary.StopReason != ""),
		InvalidToolArgsBlocks:    int32PtrIfKnown(int32(t.respSummary.InvalidToolArgsBlocks), t.respSummary.StopReason != "" && !usage.nativeRespSummary),
		LastToolUseName:          usage.terminalToolUse.Name,
		LastToolUseInputBytes:    int32PtrIfKnown(int32(usage.terminalToolUse.InputBytes), usage.endedOnToolUse),
		ToolErrorCounts:          respobserve.ToolErrorCountsJSON(usage.toolErrorTally),
		AutonomyAppendFired:      boolPtrOrNil(autonomyAppendFired(t.opts, t.finalProvider)),
		WorkspaceAppendFired:     boolPtrOrNil(workspaceAppendFired(t.opts, t.finalProvider)),
		FailoverUsed:             boolPtrTrue(t.rescue.FailoverUsed),
		FailoverAttempted:        boolPtrTrue(t.rescue.FailoverAttempted),
		SessionKey:               t.sessionKey[:],
		Role:                     t.routeRes.PinRole,
		FreshDecisionModel:       obs.FreshDecisionModel,
		FreshCandidateScores:     obs.FreshCandidateScores,
		PinAgeSec:                int64PtrIf(t.stickyHit && t.pinAgeSec > 0, t.pinAgeSec),
		ToolResultBytes:          toolResultBytesPtr(t.inboundLastUser, t.tt),
		UserPrompt:               &t.inboundUserPrompt,
		ErrorClass:               classifyTurnError(t.proxyErr, t.respSummary.StopReason, t.respSummary.InvalidToolArgsBlocks),
		FailureOwner:             turnFailureOwnerAfterFinish(ctx, t.proxyErr, classifyTurnError(t.proxyErr, t.respSummary.StopReason, t.respSummary.InvalidToolArgsBlocks), usage.terminalFinishReason),
		LatestToolCallCounts:     t.inboundLatestToolCalls,
		UnifiedLimitHeaders:      credentials.UnifiedLimitHeadersJSON(ctx),
		CodexLimitHeaders:        credentials.CodexLimitHeadersJSON(ctx),
	}
}

func settleMessagesTurn(ctx context.Context, s *Service, t *messagesFinishedTurn, usage messagesTurnUsage, subscriberTelemetry *turnrecord.InsertTelemetryParams) {
	var subscriberSettlement subscriberSettlementState
	if turnUsageBillable(ctx, t.proxyErr, usage.in, usage.out, usage.cacheCreation, usage.cacheRead) && !t.agentShadowMode {
		subscriberSettlement = s.emitBilling(ctx, t.facts.RequestID, t.facts.ExternalID, t.feats.Model, t.decision, t.actPricing, t.routeRes, usage.in, usage.out, usage.cacheCreation, usage.cacheRead, usage.usageMods)
	}
	if subscriberTelemetry != nil {
		applyTelemetrySettlement(subscriberTelemetry, turnUsageBillable(ctx, t.proxyErr, usage.in, usage.out, usage.cacheCreation, usage.cacheRead), subscriberSettlement)
		s.fireTelemetry(ctx, *subscriberTelemetry)
	}
}

func strikeMessagesArms(ctx context.Context, s *Service, t *messagesFinishedTurn) []any {
	armDemoted := ""
	var armDemotionReasonValue sessionpin.DemotionReason
	rescuedArmDemoted := ""
	var rescuedArmDemotionReason sessionpin.DemotionReason
	if !t.agentShadowMode && !t.routeRes.CallerModelPassthrough {
		s.maybeEvictPinAfterUpstreamErr(ctx, t.stickyHit, t.proxyErr, t.decision.Reason, routingpolicy.SessionTarget{Strategy: t.strategy, InstallationID: t.facts.InstallationID, SessionKey: t.routeRes.SessionKey}, stickyStateRole(t.routeRes))

		armDemoted = s.maybeDemoteArmAfterCommittedStreamFailure(ctx, t.preludeBuf.Committed(), t.routeRes.HardPinned, t.proxyErr, t.decision.Model, t.decision.Reason, routingpolicy.SessionTarget{Strategy: t.strategy, InstallationID: t.facts.InstallationID, SessionKey: t.routeRes.SessionKey}, stickyStateRole(t.routeRes), t.routeRes.PinRole)
		if armDemoted != "" {
			armDemotionReasonValue = sessionpin.DemotionReasonCommittedStreamFailure
		}

		primaryModelRescueRan := t.rescue.SiblingRescueRan || t.decision.Model != t.rescue.PrimaryModel
		if providers.IsResponseHeaderTimeout(t.rescue.PrimaryFailureErr) && (primaryModelRescueRan || t.proxyErr != nil) {
			rescuedArmDemoted, rescuedArmDemotionReason = s.maybeStrikeArmAfterRescuedFailure(ctx, primaryModelRescueRan, t.routeRes.HardPinned, t.rescue.PrimaryFailureErr, t.rescue.PrimaryDecision, routingpolicy.SessionTarget{Strategy: t.strategy, InstallationID: t.facts.InstallationID, SessionKey: t.routeRes.SessionKey}, stickyStateRole(t.routeRes), t.routeRes.PinRole)
		} else {
			rescuedArmDemoted, rescuedArmDemotionReason = s.maybeStrikeArmAfterRescuedFailure(ctx, t.rescue.SiblingRescueRan, t.routeRes.HardPinned, t.rescue.RescuedPrimaryErr, t.rescue.RescuedPrimary, routingpolicy.SessionTarget{Strategy: t.strategy, InstallationID: t.facts.InstallationID, SessionKey: t.routeRes.SessionKey}, stickyStateRole(t.routeRes), t.routeRes.PinRole)
		}
		s.strikeRescuedModelsForTurn(ctx, t.proxyErr, t.decision, t.routeRes.HardPinned, t.routeRes.SessionKey, stickyStateRole(t.routeRes), t.routeRes.PinRole)
		if t.proxyErr != nil {
			unrescuedStallDemoted := s.maybeDemoteArmAfterUnrescuedStall(ctx, t.rescue.SiblingRescueRan, t.preludeBuf.Committed(), t.routeRes.HardPinned, t.rescue.PrimarySubscriptionArmFailure, t.rescue.PrimaryDecision, routingpolicy.SessionTarget{Strategy: t.strategy, InstallationID: t.facts.InstallationID, SessionKey: t.routeRes.SessionKey}, stickyStateRole(t.routeRes), t.routeRes.PinRole)
			if armDemoted == "" && unrescuedStallDemoted != "" {
				armDemoted = unrescuedStallDemoted
				armDemotionReasonValue = sessionpin.DemotionReasonUnrescuedStall
			}
		}

		s.maybeExpireDeadArmPin(ctx, t.strategy, t.rescue.DeadArmRejected, t.decision.Reason, t.facts.InstallationID, t.routeRes.SessionKey, stickyStateRole(t.routeRes))
		s.maybeExpireSubscriptionArmPin(ctx, t.strategy, t.rescue.PrimarySubscriptionArmFailure, t.decision.Reason, t.facts.InstallationID, t.routeRes.SessionKey, stickyStateRole(t.routeRes))

		if !t.rescue.BaselineAttempted {
			s.maybeDisableProviderAfterOverload(ctx, t.stickyHit, t.proxyErr, t.finalProvider, t.decision.Reason, routingpolicy.SessionTarget{Strategy: t.strategy, InstallationID: t.facts.InstallationID, SessionKey: t.routeRes.SessionKey}, stickyStateRole(t.routeRes), t.routeRes.PinRole)
		}

		s.maybeRepinOnRefusal(ctx, t.strategy, t.refusalObs, t.routeRes.SessionKey, stickyStateRole(t.routeRes), t.decision)
	}
	return armStrikeLogFieldsWithPrimaryReason(armDemoted, armDemotionReasonValue, rescuedArmDemoted, rescuedArmDemotionReason)
}

func completeMessagesTurn(ctx context.Context, s *Service, t *messagesFinishedTurn, usage messagesTurnUsage, armStrikeFields []any) {
	iolog.LogToolCallIssues(t.log, t.respSummary.ToolCallIssues, t.decision.Model, t.finalProvider, conversation.ShortSessionKey(t.routeRes.SessionKey))

	if t.preludeBuf.Committed() {
		t.streamCut.NoteCut(t.proxyErr)
	}
	servedCredentialSource, servedSubscriptionTier := s.servedCredentialLogValues(ctx)
	t.log.Info("ProxyMessages complete", append(append([]any{"requested_model", t.feats.Model, "baseline_model", s.baselineFor(ctx, t.feats.Model), "decision_model", t.decision.Model, "decision_provider", t.decision.Provider, "primary_provider", t.rescue.PrimaryProvider, "primary_model", t.rescue.PrimaryModel, "fallback_attempts", t.winnerIdx, "failover_used", t.rescue.FailoverUsed, "failover_attempted", t.rescue.FailoverAttempted, "subscription_failover", t.rescue.SubscriptionFailoverUsed, "decision_reason", t.decision.Reason, "requested_tier", t.routeRes.RequestedTier.String(), "decision_tier", catalog.TierFor(t.decision.Model).String(), "embedded_tokens", len(t.promptText) / 4, "total_input_tokens", t.feats.Tokens, "context_estimate_tokens", t.overflowEstimate, "has_tools", t.feats.HasTools, "message_count", t.feats.MessageCount, "last_kind", t.feats.LastKind, "last_preview", s.zdrLogField(ctx, t.feats.LastPreview), "embed_input", t.embedInput, "cross_format", t.crossFormat, "sticky_hit", t.stickyHit, "route_ms", t.routeMs, "proxy_ms", t.proxyMs, "proxy_err", t.proxyErr, "error_class", classifyTurnError(t.proxyErr, t.respSummary.StopReason, t.respSummary.InvalidToolArgsBlocks), "failure_owner", turnFailureOwnerAfterFinish(ctx, t.proxyErr, classifyTurnError(t.proxyErr, t.respSummary.StopReason, t.respSummary.InvalidToolArgsBlocks), usage.terminalFinishReason), "upstream_err_body", s.zdrLogField(ctx, providers.UpstreamErrorBodyMessage(t.proxyErr)), "upstream_status", upstreamStatus(t.proxyErr), "upstream_finish_reason", t.respSummary.UpstreamFinishReason, "resp_stop_reason", t.respSummary.StopReason, "resp_refusal", t.refusalObs.Refused, "resp_refusal_category", t.refusalObs.Category, "stop_reason_promoted", t.respSummary.StopReasonPromoted, "tool_use_blocks", t.respSummary.ToolUseBlocks, "invalid_tool_args_blocks", t.respSummary.InvalidToolArgsBlocks, "last_tool_use_name", usage.terminalToolUse.Name, "last_tool_use_input_bytes", usage.terminalToolUse.InputBytes, "ended_on_tool_use", usage.endedOnToolUse, "tool_error_counts", usage.toolErrorTally, "stop_reason_demoted", t.respSummary.StopReasonDemoted, "suppressed_tool_calls", t.respSummary.SuppressedToolCalls, "tool_call_invalid_blocks", len(t.respSummary.ToolCallIssues), "cc_only_tools_stripped", t.reqStats.CCOnlyToolsStripped, "cc_task_reminders_stripped", t.reqStats.CCTaskRemindersStripped, "gemini_reminder_injected", t.reqStats.GeminiReminderInjected, "gemini_validated_tool_mode", t.reqStats.GeminiValidatedToolMode, "resp_output_tokens", t.respSummary.OutputTokens, "resp_reasoning_tokens", t.extractor.ReasoningTokens(), "prelude_committed", t.preludeBuf.Committed(), "routing_marker", t.marker, "prior_served_model", t.routeRes.PriorServedModel, "hard_pinned", t.routeRes.HardPinned, "credential_source", servedCredentialSource, "client_app", requestcontext.CompletionLogClientApp(t.facts.Client), "subscription_tier", servedSubscriptionTier}, append(append(append(armStrikeFields, plannerLogFields(t.routeRes)...), t.streamCut.CompletionLogFields()...), t.rateLimit.CompletionLogFields()...)...), downgradeShadowLogFields(t.routeRes)...)...)
}

func (s *Service) maybeRepinOnRefusal(ctx context.Context, strategy router.Strategy, obs *respobserve.RefusalObserver, sessionKey [sessionpin.SessionKeyLen]byte, role string, served router.Decision) {
	if obs == nil || !obs.Refused {
		return
	}
	s.repinOffRefusingModel(ctx, strategy, sessionKey, role, served, obs.Category, "")
}
