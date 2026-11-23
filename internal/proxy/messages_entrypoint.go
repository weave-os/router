package proxy

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/conversation"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/ingress"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/handover"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/routingpolicy"
	"weave-os/router/internal/telemetry/capture"
	"weave-os/router/internal/translate"
	"weave-os/router/internal/websearch"
)

func (s *Service) ProxyMessages(ctx context.Context, body []byte, w http.ResponseWriter, r *http.Request) (returnErr error) {
	ctx = credentials.WithWeaveCreditsFunding(ctx, r.Header)
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
	ctx, body, err := s.lifecycle.PiHandoff(s.clock.Now).Parse(ctx, body, requestcontext.InstallationIDFromContext(ctx))
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
	if ctx, err = conversation.StripClientHistory(ctx, log, env, "Anthropic"); err != nil {
		return err
	}

	feats := env.RoutingFeatures()
	ctx = requestcontext.WithRequestImageCount(ctx, feats.ImageCount)
	if err := validateHandoffEnvelope(ctx, env, r.Header, body); err != nil {
		return err
	}
	if conversation.PreparingHandoff(ctx) && !s.canPrepareHandoff(ctx, env, feats, r.Header, body) {
		return writePreparedHandoff(w, preparedHandoff{Bypass: true})
	}
	promptText, embedInput := conversation.TurnPromptText(feats)
	conversation.LogTurnStart(log, "ProxyMessages start", env.Stream(), feats, s.zdrLogField(ctx, observability.Preview(promptText, 200)))

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

	ctx, preRoute, handled, err := assembleMessagesPreRoute(ctx, s, messagesPreRouteInput{
		w: w, r: r, body: body, log: log, env: env, facts: facts, strategy: strategy, clientBudget: clientBudget,
		agentShadowMode: agentShadowMode, agentForceModel: agentForceModel, requestBodyChanged: requestBodyChanged,
		sessionKey: sessionKey, forceModelSessionKey: forceModelSessionKey, feats: feats, promptText: promptText, embedInput: embedInput,
	})
	if handled || err != nil {
		return err
	}
	feats, promptText, embedInput = preRoute.feats, preRoute.promptText, preRoute.embedInput
	taskArm, req, routeStart, outputReserve, overflowEstimate := preRoute.taskArm, preRoute.req, preRoute.routeStart, preRoute.outputReserve, preRoute.overflowEstimate
	inboundSpiralSignals, inboundSpiralReasons, inboundSpiralComputed := preRoute.inboundSpiralSignals, preRoute.inboundSpiralReasons, preRoute.inboundSpiralComputed
	inboundToolCallCount, inboundLastUser, inboundUserPrompt, inboundLatestToolCalls := preRoute.inboundToolCallCount, preRoute.inboundLastUser, preRoute.inboundUserPrompt, preRoute.inboundLatestToolCalls
	routeRes, _, routeErr := routeTurn(ctx, s, s.clock, log, routeTurnRequest{
		Request:        req,
		Started:        routeStart,
		Failure:        routeFailureRequest{RequestID: facts.RequestID, RequestStart: requestStart, RequestedModel: feats.Model, Strategy: strategy},
		FailureMessage: "Routing failed",
		IsUserPrompt:   inboundUserPrompt,
	}, func(routeCtx context.Context) (routingpolicy.TurnRoute, error) {
		switch {
		case conversation.HandoffFromContext(ctx) != nil:
			return s.resumeHandoff(routeCtx, strategy, env, req, turntype.IsSubAgent(env, subAgentHintForRequest(routeCtx, r.Header.Get("x-weave-subagent-type")), openCodeCaller(requestcontext.ClientIdentityFrom(routeCtx))))
		case agentShadowMode:
			return s.runAgentShadowEvaluationRoute(routeCtx, env, feats, facts.InstallationID, req, agentShadowEval)
		}
		return taskArm.route(routeCtx, s.runTurnLoop)(env, feats, turnRequest{Strategy: strategy, APIKeyID: facts.APIKeyID, InstallationID: facts.InstallationID}, "", r.Header, req)
	})
	defer routeRes.ReleaseCooldownProbes()
	var escalationCapture *capture.Writer
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
		ctx, linkedSubscriptionRetry, err = s.credentials.RetryAfterSubscriptionOnlyBypass(ctx, log, facts.RequestID, facts.ExternalID, routeRes.Decision, credentials.ManagedSubscriptionCanServe)
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

	postOutcome, err := conversation.PostRouteChecks{
		AgentShadowMode: agentShadowMode, Route: routeRes, Env: env, Features: feats, PromptText: promptText, ToolCallCount: inboundToolCallCount,
		ToolBearing: inboundToolCallCount > 0 || inboundLastUser.HasToolResult, ClientBudget: clientBudget, Scope: req,
		LoopBreak:  newLoopBreak(s, strategy, facts.InstallationID, routeRes.SessionKey, roleForTier(catalog.TierFor(feats.Model)), decision),
		NoProgress: s.lifecycle.NoProgress(), Now: s.clock.Now, TextRepetitionEnabled: s.lifecycle.TextRepetitionBreakEnabled(ctx),
		RecoverClientCompaction: !callerModelPassthroughActive(ctx), SummaryMaxTokens: DefaultCompactionMaxTokens, ClientDefaultWindow: claudeCodeDefaultWindow,
		HandoverEligible: !subscriptionStateModelsEnabled(ctx) && decision.Provider != providers.ProviderAnthropic,
		RunCompactionHandover: func(ctx context.Context) handover.Outcome {
			return s.runCompactionHandover(ctx, env, r.Header, decision.Model, req)
		},
	}.Run(ctx)
	if err != nil {
		return err
	}
	if postOutcome.LoopStopped {
		return writeLoopStop(ctx, w, env, postOutcome.LoopStopMessage, feats.Tokens)
	}
	compactionHandoverOutcome := postOutcome.CompactionHandover

	credentialCtx := credentials.ResolveAndInjectCredentials(ctx, decision.Provider, decision.Model, r.Header)

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
		KeepCrossVendorOrchestrationTools: flags.BoolOr(ctx, flags.KeyCCOrchToolsCrossVendor, s.ingress.CCOrchToolsCrossVendor),
		KeepCrossVendorTaskTools:          s.ingress.TaskToolsCrossVendor(ctx),
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

	ctx, err = dispatchMessagesTurn(ctx, credentialCtx, &messagesTurnDispatch{
		s: s, w: w, r: r, body: body, requestStart: requestStart, facts: facts, rateLimit: rateLimit, log: log, env: env, sessionKey: sessionKey,
		feats: feats, promptText: promptText, embedInput: embedInput, strategy: strategy, taskArm: taskArm, inboundLastUser: inboundLastUser,
		inboundUserPrompt: inboundUserPrompt, inboundLatestToolCalls: inboundLatestToolCalls, inboundSpiralSignals: inboundSpiralSignals,
		inboundSpiralReasons: inboundSpiralReasons, inboundSpiralComputed: inboundSpiralComputed, req: req, routeRes: routeRes, routeMs: routeMs,
		decision: decision, tt: tt, stickyHit: stickyHit, pinAgeSec: pinAgeSec, reqPricing: reqPricing, actPricing: actPricing, effortServed: effortServed,
		opts: opts, replayedModel: replayedModel, footerEchoed: footerEchoed, agentShadowMode: agentShadowMode, responseBuffer: responseBuffer,
		overflowEstimate: overflowEstimate, outputReserve: outputReserve, compactionHandoverOutcome: compactionHandoverOutcome,
		escalationCapture: &escalationCapture,
	})
	return err
}
