package proxy

import (
	"context"
	"log/slog"
	"net/http"
	"time"

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
	"weave-os/router/internal/telemetry/turnrecord"
	"weave-os/router/internal/translate"

	"github.com/google/uuid"
)

type messagesFinishedTurn struct {
	w                         http.ResponseWriter
	body                      []byte
	facts                     requestcontext.RequestFacts
	requestStart              time.Time
	rateLimit                 *dispatch.ThrottleLedger
	log                       *slog.Logger
	env                       *translate.RequestEnvelope
	responseBuffer            *responseCostBuffer
	agentShadowMode           bool
	sessionKey                [16]byte
	feats                     translate.RoutingFeatures
	promptText                string
	embedInput                string
	strategy                  router.Strategy
	taskArm                   taskArmTurn
	inboundSpiralSignals      conversation.SpiralSignals
	inboundSpiralReasons      []conversation.SpiralReason
	inboundSpiralComputed     bool
	inboundLastUser           translate.LastUserMessageInfo
	inboundUserPrompt         bool
	inboundLatestToolCalls    []byte
	overflowEstimate          int
	req                       router.Request
	routeRes                  turnLoopResult
	decision                  router.Decision
	tt                        turntype.TurnType
	stickyHit                 bool
	pinAgeSec                 int64
	routeMs                   int64
	compactionHandoverOutcome dispatch.HandoverOutcome
	reqPricing                catalog.Pricing
	actPricing                catalog.Pricing
	opts                      translate.EmitOptions
	effortServed              dispatch.EffortResolution
	contentCap                *captureWriter
	policyOutcomeCap          *captureWriter
	preludeBuf                *preludeBuffer
	refusalObs                *refusalObserver
	toolUseObs                *toolUseObserver
	streamCut                 *streamCutObserver
	proxyStart                time.Time
	proxyErr                  error
	crossFormat               bool
	extractor                 *telemetry.UsageExtractor
	respSummary               translate.ResponseSummary
	reqStats                  providers.RequestMutationStats
	marker                    string
	fastServed                bool
	rescue                    *messagesRescue
	winnerIdx                 int
	finalProvider             string
	proxyMs                   int64
}

type messagesTurnUsage struct {
	nativeRespSummary    bool
	terminalFinishReason string
	terminalToolUse      lastToolUse
	endedOnToolUse       bool
	toolErrorTally       map[string]toolCallTally
	in                   int
	out                  int
	cacheCreation        int
	cacheRead            int
	usageMods            catalog.UsageModifiers
	baselineWarmPrefill  int
	requestedInputCost   float64
}

func finishMessagesTurn(ctx context.Context, s *Service, t *messagesFinishedTurn) error {
	u := messagesTurnUsageFor(ctx, s, t)
	obs := recordMessagesUpstream(ctx, s, t, u)
	settleMessagesTurn(ctx, s, t, u, messagesSubscriberTelemetry(ctx, s, t, u, obs))
	completeMessagesTurn(ctx, s, t, u, strikeMessagesArms(ctx, s, t))
	policyRespBody, policyRespTrunc := capturedResponse(t.policyOutcomeCap)
	var policyResp *policyOutcomeResponse
	if t.policyOutcomeCap != nil {
		policyResp = &policyOutcomeResponse{Body: policyRespBody, Truncated: policyRespTrunc}
	}
	if !t.agentShadowMode {
		s.reportPolicyOutcome(ctx, t.routeRes, t.decision, t.effortServed, t.finalProvider, t.fastServed, t.feats.Tokens, u.in, u.out, u.cacheCreation, u.cacheRead, u.usageMods, t.routeMs, t.proxyMs, t.proxyErr, policyResp)
	}
	return t.proxyErr
}

func messagesTurnUsageFor(ctx context.Context, s *Service, t *messagesFinishedTurn) messagesTurnUsage {
	if t.refusalObs.refused && t.respSummary.StopReason == "" {
		t.respSummary.StopReason = anthropicRefusalStopReason
	}

	nativeRespSummary := false
	if t.respSummary.StopReason == "" && s.ResolveNativeAnthropicResponseSignals(ctx) {
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
	terminalToolUse, endedOnToolUse := t.toolUseObs.terminal()
	toolErrorTally := toolErrorCounts(t.env.AssistantToolCallOutcomes())

	in, out := t.extractor.Tokens()
	cacheCreation, cacheRead := t.extractor.CacheTokens()
	usageMods := t.extractor.UsageModifiers()
	baselineWarmPrefill := t.routeRes.baselineWarmPrefillTokens(t.requestStart, cacheCreation, cacheRead, t.decision.Model, s.baselineFor(ctx, t.feats.Model), t.req.HistoryTruncated)
	requestedInputCost := catalog.CounterfactualInputCost(in, cacheCreation, cacheRead, baselineWarmPrefill, t.reqPricing, t.decision.Provider, usageMods)
	if t.responseBuffer != nil && t.proxyErr == nil {
		setRouterCostHeaders(t.w.Header(), routerResponseCostFromPricing(t.actPricing, t.decision.Provider, in, out, cacheCreation, cacheRead, usageMods))
	}
	return messagesTurnUsage{nativeRespSummary: nativeRespSummary, terminalFinishReason: terminalFinishReason, terminalToolUse: terminalToolUse, endedOnToolUse: endedOnToolUse, toolErrorTally: toolErrorTally, in: in, out: out, cacheCreation: cacheCreation, cacheRead: cacheRead, usageMods: usageMods, baselineWarmPrefill: baselineWarmPrefill, requestedInputCost: requestedInputCost}
}

func recordMessagesUpstream(ctx context.Context, s *Service, t *messagesFinishedTurn, u messagesTurnUsage) observationContext {
	upstreamBuilder, obs := recordUpstreamSpan(ctx, s.clock, upstreamSpan{
		extendedAttrs: true, taskArm: t.taskArm, respSummary: t.respSummary, refusal: t.refusalObs, fastServed: t.fastServed, crossFormat: t.crossFormat,
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
				b.Bool("dispatch.baseline_failover", t.rescue.baselineFailoverUsed).
					Bool("dispatch.subscription_failover", t.rescue.subscriptionFailoverUsed).
					Bool("dispatch.sibling_failover", t.rescue.siblingFailoverUsed).
					Bool("dispatch.early_stop_redo", t.rescue.earlyStopRedone)
			},
		},
	})
	respBody, respTrunc := capturedResponse(t.contentCap)
	if !t.agentShadowMode {
		s.recordCallLog(ctx, upstreamBuilder.Build(), t.routeMs, t.proxyErr, t.body, respBody, respTrunc)
	}
	telemetry.Flush(ctx)

	if !t.agentShadowMode {
		s.recordTurnUsage(ctx, t.routeRes, t.finalProvider, t.decision.ServedIdentity(), u.in, u.out, u.cacheCreation, u.cacheRead, t.extractor.OutputLimitReached())
		s.clearRecoveredCooldown(ctx, t.routeRes, t.decision.Model, t.proxyErr)
	}
	return obs
}

func messagesSubscriberTelemetry(ctx context.Context, s *Service, t *messagesFinishedTurn, u messagesTurnUsage, obs observationContext) *turnrecord.InsertTelemetryParams {
	var subscriberTelemetry *turnrecord.InsertTelemetryParams
	if !t.agentShadowMode && t.facts.InstallationID != uuid.Nil {
		credentialKeyPrefix, credentialKeySuffix, credSource := s.credentialKeyParts(ctx)
		s.markServedExternalKeyUsed(ctx, t.proxyErr)
		degenerateShadow := t.proxyErr == nil && !u.nativeRespSummary && isDegenerateResponse(u.out, t.respSummary.ToolUseBlocks, t.respSummary.StopReason, t.respSummary.StopReasonDemoted)
		if degenerateShadow && !t.agentShadowMode {
			t.log.Info("router.degenerate_shadow",
				"model", t.decision.Model,
				"provider", t.finalProvider,
				"output_tokens", u.out,
				"tool_use_blocks", t.respSummary.ToolUseBlocks,
				"stop_reason", t.respSummary.StopReason,
				"upstream_finish_reason", t.respSummary.UpstreamFinishReason,
				"would_failover", true,
			)
			s.evictPinAfterDegenerateResponse(ctx, t.strategy, t.stickyHit, t.decision.Reason, t.facts.InstallationID, t.routeRes.SessionKey, stickyStateRole(t.routeRes))
		}
		tel := messagesTelemetryRow(ctx, s, t, u, obs)
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

func messagesTelemetryRow(ctx context.Context, s *Service, t *messagesFinishedTurn, u messagesTurnUsage, obs observationContext) turnrecord.InsertTelemetryParams {
	return turnrecord.InsertTelemetryParams{
		InstallationID:           t.facts.InstallationID.String(),
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
		RolloutID:                obs.RolloutID,
		UpstreamFinishReason:     stringPtrOrEmpty(t.respSummary.UpstreamFinishReason),
		StopReason:               stringPtrOrEmpty(t.respSummary.StopReason),
		ToolUseBlocks:            int32PtrIfKnown(int32(t.respSummary.ToolUseBlocks), t.respSummary.StopReason != ""),
		InvalidToolArgsBlocks:    int32PtrIfKnown(int32(t.respSummary.InvalidToolArgsBlocks), t.respSummary.StopReason != "" && !u.nativeRespSummary),
		LastToolUseName:          u.terminalToolUse.Name,
		LastToolUseInputBytes:    int32PtrIfKnown(int32(u.terminalToolUse.InputBytes), u.endedOnToolUse),
		ToolErrorCounts:          toolErrorCountsJSON(u.toolErrorTally),
		AutonomyAppendFired:      boolPtrOrNil(autonomyAppendFired(t.opts, t.finalProvider)),
		WorkspaceAppendFired:     boolPtrOrNil(workspaceAppendFired(t.opts, t.finalProvider)),
		FailoverUsed:             boolPtrTrue(t.rescue.failoverUsed),
		FailoverAttempted:        boolPtrTrue(t.rescue.failoverAttempted),
		SessionKey:               t.sessionKey[:],
		Role:                     t.routeRes.PinRole,
		FreshDecisionModel:       obs.FreshDecisionModel,
		FreshCandidateScores:     obs.FreshCandidateScores,
		PinAgeSec:                int64PtrIf(t.stickyHit && t.pinAgeSec > 0, t.pinAgeSec),
		ToolResultBytes:          toolResultBytesPtr(t.inboundLastUser, t.tt),
		UserPrompt:               &t.inboundUserPrompt,
		ErrorClass:               classifyTurnError(t.proxyErr, t.respSummary.StopReason, t.respSummary.InvalidToolArgsBlocks),
		FailureOwner:             turnFailureOwnerAfterFinish(ctx, t.proxyErr, classifyTurnError(t.proxyErr, t.respSummary.StopReason, t.respSummary.InvalidToolArgsBlocks), u.terminalFinishReason),
		LatestToolCallCounts:     t.inboundLatestToolCalls,
		UnifiedLimitHeaders:      unifiedLimitHeadersJSON(ctx),
		CodexLimitHeaders:        codexLimitHeadersJSON(ctx),
	}
}

func settleMessagesTurn(ctx context.Context, s *Service, t *messagesFinishedTurn, u messagesTurnUsage, subscriberTelemetry *turnrecord.InsertTelemetryParams) {
	var subscriberSettlement subscriberSettlementState
	if turnUsageBillable(ctx, t.proxyErr, u.in, u.out, u.cacheCreation, u.cacheRead) && !t.agentShadowMode {
		subscriberSettlement = s.emitBilling(ctx, t.facts.RequestID, t.facts.ExternalID, t.feats.Model, t.decision, t.actPricing, t.routeRes, u.in, u.out, u.cacheCreation, u.cacheRead, u.usageMods)
		if t.compactionHandoverOutcome.Invoked && !t.compactionHandoverOutcome.FallbackToFullHistory {
			s.billAuxiliaryInference(ctx, t.facts.RequestID, auxSuffixCompactionHandoverSummry, t.facts.ExternalID, t.compactionHandoverOutcome.SummaryUsage)
		}
	}
	if subscriberTelemetry != nil {
		applyTelemetrySettlement(subscriberTelemetry, turnUsageBillable(ctx, t.proxyErr, u.in, u.out, u.cacheCreation, u.cacheRead), subscriberSettlement)
		s.fireTelemetry(ctx, *subscriberTelemetry)
	}
}

func strikeMessagesArms(ctx context.Context, s *Service, t *messagesFinishedTurn) []any {
	armDemoted := ""
	var armDemotionReasonValue sessionpin.DemotionReason
	rescuedArmDemoted := ""
	var rescuedArmDemotionReason sessionpin.DemotionReason
	if !t.agentShadowMode && !t.routeRes.CallerModelPassthrough {
		s.maybeEvictPinAfterUpstreamErr(ctx, t.stickyHit, t.proxyErr, t.decision.Reason, sessionTarget{strategy: t.strategy, installationID: t.facts.InstallationID, sessionKey: t.routeRes.SessionKey}, stickyStateRole(t.routeRes))

		armDemoted = s.maybeDemoteArmAfterCommittedStreamFailure(ctx, t.preludeBuf.Committed(), t.routeRes.HardPinned, t.proxyErr, t.decision.Model, t.decision.Reason, sessionTarget{strategy: t.strategy, installationID: t.facts.InstallationID, sessionKey: t.routeRes.SessionKey}, stickyStateRole(t.routeRes), t.routeRes.PinRole)
		if armDemoted != "" {
			armDemotionReasonValue = sessionpin.DemotionReasonCommittedStreamFailure
		}

		primaryModelRescueRan := t.rescue.siblingRescueRan || t.decision.Model != t.rescue.primaryModel
		if providers.IsResponseHeaderTimeout(t.rescue.primaryFailureErr) && (primaryModelRescueRan || t.proxyErr != nil) {
			rescuedArmDemoted, rescuedArmDemotionReason = s.maybeStrikeArmAfterRescuedFailure(ctx, primaryModelRescueRan, t.routeRes.HardPinned, t.rescue.primaryFailureErr, t.rescue.primaryDecision, sessionTarget{strategy: t.strategy, installationID: t.facts.InstallationID, sessionKey: t.routeRes.SessionKey}, stickyStateRole(t.routeRes), t.routeRes.PinRole)
		} else {
			rescuedArmDemoted, rescuedArmDemotionReason = s.maybeStrikeArmAfterRescuedFailure(ctx, t.rescue.siblingRescueRan, t.routeRes.HardPinned, t.rescue.rescuedPrimaryErr, t.rescue.rescuedPrimary, sessionTarget{strategy: t.strategy, installationID: t.facts.InstallationID, sessionKey: t.routeRes.SessionKey}, stickyStateRole(t.routeRes), t.routeRes.PinRole)
		}
		s.strikeRescuedModelsForTurn(ctx, t.proxyErr, t.decision, t.routeRes.HardPinned, t.routeRes.SessionKey, stickyStateRole(t.routeRes), t.routeRes.PinRole)
		if t.proxyErr != nil {
			unrescuedStallDemoted := s.maybeDemoteArmAfterUnrescuedStall(ctx, t.rescue.siblingRescueRan, t.preludeBuf.Committed(), t.routeRes.HardPinned, t.rescue.primarySubscriptionArmFailure, t.rescue.primaryDecision, sessionTarget{strategy: t.strategy, installationID: t.facts.InstallationID, sessionKey: t.routeRes.SessionKey}, stickyStateRole(t.routeRes), t.routeRes.PinRole)
			if armDemoted == "" && unrescuedStallDemoted != "" {
				armDemoted = unrescuedStallDemoted
				armDemotionReasonValue = sessionpin.DemotionReasonUnrescuedStall
			}
		}

		s.maybeExpireDeadArmPin(ctx, t.strategy, t.rescue.deadArmRejected, t.decision.Reason, t.facts.InstallationID, t.routeRes.SessionKey, stickyStateRole(t.routeRes))
		s.maybeExpireSubscriptionArmPin(ctx, t.strategy, t.rescue.primarySubscriptionArmFailure, t.decision.Reason, t.facts.InstallationID, t.routeRes.SessionKey, stickyStateRole(t.routeRes))

		if !t.rescue.baselineAttempted {
			s.maybeDisableProviderAfterOverload(ctx, t.stickyHit, t.proxyErr, t.finalProvider, t.decision.Reason, sessionTarget{strategy: t.strategy, installationID: t.facts.InstallationID, sessionKey: t.routeRes.SessionKey}, stickyStateRole(t.routeRes), t.routeRes.PinRole)
		}

		s.maybeRepinOnRefusal(ctx, t.strategy, t.refusalObs, t.routeRes.SessionKey, stickyStateRole(t.routeRes), t.decision)
	}
	return armStrikeLogFieldsWithPrimaryReason(armDemoted, armDemotionReasonValue, rescuedArmDemoted, rescuedArmDemotionReason)
}

func completeMessagesTurn(ctx context.Context, s *Service, t *messagesFinishedTurn, u messagesTurnUsage, armStrikeFields []any) {
	logToolCallIssues(t.log, t.respSummary.ToolCallIssues, t.decision.Model, t.finalProvider, t.routeRes.SessionKey)

	if t.preludeBuf.Committed() {
		t.streamCut.noteCut(t.proxyErr)
	}
	servedCredentialSource, servedSubscriptionTier := s.servedCredentialLogValues(ctx)
	t.log.Info("ProxyMessages complete", append(append([]any{"requested_model", t.feats.Model, "baseline_model", s.baselineFor(ctx, t.feats.Model), "decision_model", t.decision.Model, "decision_provider", t.decision.Provider, "primary_provider", t.rescue.primaryProvider, "primary_model", t.rescue.primaryModel, "fallback_attempts", t.winnerIdx, "failover_used", t.rescue.failoverUsed, "failover_attempted", t.rescue.failoverAttempted, "subscription_failover", t.rescue.subscriptionFailoverUsed, "decision_reason", t.decision.Reason, "requested_tier", t.routeRes.RequestedTier.String(), "decision_tier", catalog.TierFor(t.decision.Model).String(), "embedded_tokens", len(t.promptText) / 4, "total_input_tokens", t.feats.Tokens, "context_estimate_tokens", t.overflowEstimate, "has_tools", t.feats.HasTools, "message_count", t.feats.MessageCount, "last_kind", t.feats.LastKind, "last_preview", s.zdrLogField(ctx, t.feats.LastPreview), "embed_input", t.embedInput, "cross_format", t.crossFormat, "sticky_hit", t.stickyHit, "route_ms", t.routeMs, "proxy_ms", t.proxyMs, "proxy_err", t.proxyErr, "error_class", classifyTurnError(t.proxyErr, t.respSummary.StopReason, t.respSummary.InvalidToolArgsBlocks), "failure_owner", turnFailureOwnerAfterFinish(ctx, t.proxyErr, classifyTurnError(t.proxyErr, t.respSummary.StopReason, t.respSummary.InvalidToolArgsBlocks), u.terminalFinishReason), "upstream_err_body", s.zdrLogField(ctx, providers.UpstreamErrorBodyMessage(t.proxyErr)), "upstream_status", upstreamStatus(t.proxyErr), "upstream_finish_reason", t.respSummary.UpstreamFinishReason, "resp_stop_reason", t.respSummary.StopReason, "resp_refusal", t.refusalObs.refused, "resp_refusal_category", t.refusalObs.category, "stop_reason_promoted", t.respSummary.StopReasonPromoted, "tool_use_blocks", t.respSummary.ToolUseBlocks, "invalid_tool_args_blocks", t.respSummary.InvalidToolArgsBlocks, "last_tool_use_name", u.terminalToolUse.Name, "last_tool_use_input_bytes", u.terminalToolUse.InputBytes, "ended_on_tool_use", u.endedOnToolUse, "tool_error_counts", u.toolErrorTally, "text_only_turn_nudged", t.respSummary.TextOnlyTurnNudged, "stop_reason_demoted", t.respSummary.StopReasonDemoted, "suppressed_tool_calls", t.respSummary.SuppressedToolCalls, "tool_call_invalid_blocks", len(t.respSummary.ToolCallIssues), "cc_only_tools_stripped", t.reqStats.CCOnlyToolsStripped, "cc_task_reminders_stripped", t.reqStats.CCTaskRemindersStripped, "gemini_reminder_injected", t.reqStats.GeminiReminderInjected, "gemini_validated_tool_mode", t.reqStats.GeminiValidatedToolMode, "resp_output_tokens", t.respSummary.OutputTokens, "resp_reasoning_tokens", t.extractor.ReasoningTokens(), "prelude_committed", t.preludeBuf.Committed(), "routing_marker", t.marker, "prior_served_model", t.routeRes.PriorServedModel, "hard_pinned", t.routeRes.HardPinned, "credential_source", servedCredentialSource, "client_app", completionLogClientApp(t.facts.Client), "subscription_tier", servedSubscriptionTier}, append(append(append(armStrikeFields, plannerLogFields(t.routeRes)...), t.streamCut.completionLogFields()...), t.rateLimit.CompletionLogFields()...)...), downgradeShadowLogFields(t.routeRes)...)...)
}
