package proxy

import (
	"context"
	"encoding/hex"
	"fmt"
	"net/http"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/conversation"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/routingpolicy"
	"weave-os/router/internal/telemetry/capture"
	"weave-os/router/internal/translate"
)

func (s *Service) ProxyOpenAIChatCompletion(ctx context.Context, body []byte, w http.ResponseWriter, r *http.Request) (returnErr error) {
	ctx = withWeaveCreditsFunding(ctx, r.Header)
	w, finishBlindExperimentWriter := withBlindExperimentWriter(ctx, w, body)
	defer finishBlindExperimentWriter()
	ctx = s.withSubscriptionFundingAuthorization(ctx, r.Header)
	ctx, strategy, returnErr := withTurnInput(ctx, s, body, r.Header, router.EndpointOpenAIChat)
	if returnErr != nil {
		return returnErr
	}
	ctx, turn, err := admitTurn(ctx, s, s.clock, r)
	if err != nil {
		return err
	}
	rateLimit, facts, requestStart := turn.rateLimit, turn.facts, turn.start
	log := observability.FromContext(ctx)

	ctx, body, replayedModel, footerEchoed, err := stripInboundEchoes(ctx, log, body, "OpenAI")
	if err != nil {
		return err
	}
	if replayedModel == "" {
		replayedModel, _ = ctx.Value(responsesReplayedModelContextKey{}).(string)
	}
	env, body, parseErr := s.ingress.OpenAIChat.Parse(ctx, body)
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
	if ctx, err = conversation.StripClientHistory(ctx, log, env, "OpenAI"); err != nil {
		return err
	}
	codexTitleGen, _ := ctx.Value(codexTitleGenerationContextKey{}).(bool)
	feats := env.RoutingFeatures()
	ctx = requestcontext.WithRequestImageCount(ctx, feats.ImageCount)
	if codexTitleGen {
		feats.TitleGenHint = true
	}
	promptText, embedInput := conversation.TurnPromptText(feats)
	conversation.LogTurnStart(log, "ProxyOpenAIChatCompletion start", env.Stream(), feats, s.zdrLogField(ctx, observability.Preview(promptText, 200)))

	betaSessionKey := deriveBetaSessionKeyForRequest(ctx, env, facts.APIKeyID, sessionKey)
	if cmd, hasCmd := env.ExtractBetaCommand(); hasCmd {
		log.Info("ProxyOpenAIChatCompletion beta command")
		return (betaCommands{s}).handle(ctx, w, env, cmd, facts.InstallationID, sessionKey, betaSessionKey, feats.Tokens)
	}
	ctx, strategy, err = s.applySessionStrategy(ctx, strategy, facts.InstallationID, betaSessionKey)
	if err != nil {
		return err
	}
	*r = *r.WithContext(ctx)
	forceModelSessionKey := deriveForceModelSessionKeyForRequest(ctx, env, facts.APIKeyID, sessionKey)

	outcome, err := conversation.CommandDispatch{
		Source:                  chatCommandSource{RequestEnvelope: env, routing: routingEnvelopeForRequest(ctx, env)},
		Handlers:                turnCommands{s: s, w: w, env: env, target: sessionTarget{strategy: strategy, installationID: facts.InstallationID, sessionKey: sessionKey}, forceModelSessionKey: forceModelSessionKey, inputTokens: feats.Tokens, requestedModel: feats.Model},
		Protocol:                "ProxyOpenAIChatCompletion",
		ForceModelEnabled:       s.pinStore != nil,
		ForceModelContinuesTurn: func(cmd translate.ForceModelResult) bool { return conversation.AgentForceModelContinuesTurn(ctx, cmd) },
		SyntheticFeedback: func(cmd translate.RouterFeedbackResult) bool {
			return !cmd.FromToolResult || conversation.CodexSkillTurnFrom(ctx) == translate.CodexSkillTurnFeedback
		},
	}.Run(ctx)
	if err != nil || outcome.Handled {
		return err
	}
	agentForceModel, requestBodyChanged := outcome.AgentForceModel, outcome.BodyChanged

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
		ctx = requestcontext.WithRequestImageCount(ctx, feats.ImageCount)
		if codexTitleGen {
			feats.TitleGenHint = true
		}
		promptText, embedInput = conversation.TurnPromptText(feats)
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

	if s.effectiveCaptureMode(ctx) != capture.Off {
		logInboundRequestDiagnostics(log, env)
	}

	subAgentHint := r.Header.Get("x-weave-subagent-type")

	eligibility, eligibilityErr := requestEligibility(ctx, s, providers.ProviderOpenAI, r.Header)
	if eligibilityErr != nil {
		return eligibilityErr
	}

	responsesBody, _ := ctx.Value(codexResponsesBodyContextKey{}).([]byte)
	reasoningConfigurationHash := env.ReasoningConfigurationSHA256()
	if nativeResponsesHash, ok := ctx.Value(nativeResponsesReasoningHashContextKey{}).(string); ok {
		reasoningConfigurationHash = nativeResponsesHash
	}
	toolConfigurationHash := env.ToolConfigurationSHA256()
	if nativeResponsesHash, ok := ctx.Value(nativeResponsesToolHashContextKey{}).(string); ok {
		toolConfigurationHash = nativeResponsesHash
	}

	outputReserveOAI, prefiltered := prefilterTurn(ctx, s, turnPrefilterInput{
		env: env, feats: feats, eligibility: eligibility, header: r.Header, log: log,
	})
	inboundLastUser := env.LastUserMessage()
	inboundUserPromptOpenAI := routingEndsWithUserPrompt(ctx, env)
	overflowEstimateOAI := prefiltered.OverflowEstimate
	feats, routingPromptText, routingMessages := routingTurnInputForRequest(ctx, env, feats, promptText)
	routeRequest := preRouteRequest(ctx, s, preRouteRequestInput{
		env: env, feats: feats, forceModel: forceModel, forceCluster: forceCluster,
		endpoint: router.EndpointOpenAIChat, reasoningConfigHash: reasoningConfigurationHash, toolConfigHash: toolConfigurationHash,
		promptText: routingPromptText, messages: routingMessages, feedbackKey: hex.EncodeToString(sessionKey[:]),
		eligibility: eligibility, gatewayProviders: s.gatewayProvidersForRequest(ctx), header: r.Header, prefiltered: prefiltered,
	})
	routeRes, routeMs, err := routeTurn(ctx, s, s.clock, log, routeTurnRequest{
		Request:        routeRequest,
		Started:        s.clock.Now(),
		Failure:        routeFailureRequest{RequestID: facts.RequestID, RequestStart: requestStart, RequestedModel: feats.Model, Strategy: strategy},
		FailureMessage: "Routing failed for OpenAI request",
		IsUserPrompt:   inboundUserPromptOpenAI,
	}, func(routeCtx context.Context) (routingpolicy.TurnRoute, error) {
		return taskArm.route(routeCtx, s.runTurnLoop)(env, feats, turnRequest{Strategy: strategy, APIKeyID: facts.APIKeyID, InstallationID: facts.InstallationID}, subAgentHint, r.Header, routeRequest)
	})
	defer routeRes.ReleaseCooldownProbes()
	var escalationCapture *capture.Writer
	defer func() {
		s.completeEscalation(ctx, routeRes, returnErr, escalationCapture, translate.EscalationResponseChat)
	}()
	if err != nil {
		return err
	}
	ctx = requestcontext.WithCallerModelPassthrough(ctx, routeRes.CallerModelPassthrough)
	ctx = withPickedModelFunding(ctx, routeRes.CallerModelPassthrough, routeRes.Decision.Provider)
	ctx = withCodexPickFunding(ctx, routeRes.Decision.Model, routeRes.Decision.Provider)
	ctx = s.withTurnOverloadProbe(ctx, routeRes)
	ctx = withRouteSessionState(ctx, routeRes)
	routeRes.SuggestionMode = r.Header.Get("x-weave-suggestion-mode") == "true"
	decision := routeRes.Decision
	s.firePolicyShadowForServingDecision(ctx, strategy, decision, routeRequest)
	tt := routeRes.TurnType
	stickyHit := routeRes.StickyHit
	pinTier := routeRes.PinTier
	pinAgeSec := routeRes.PinAgeSec
	logPlannerOutcome(ctx, s.strategyPorts, routeRes)

	if facts.Client.ClientApp == requestcontext.ClientAppOpencode && !routeRes.CallerModelPassthrough && !routeRes.AuthoritativePerTurn && s.ResolveTextRepetitionBreakEnabled(ctx) && (tt == turntype.MainLoop || tt == turntype.ToolResult) {
		if looped, count, sampleHash := conversation.DetectTextRepetition(env); looped {
			role := roleForTier(catalog.TierFor(feats.Model))
			return writeLoopStop(ctx, w, env, newLoopBreak(s, strategy, facts.InstallationID, sessionKey, role, decision).TextRepetition(ctx, env, count, sampleHash), feats.Tokens)
		}
	}

	credentialCtx := resolveAndInjectCredentials(ctx, decision.Provider, decision.Model, r.Header)

	if _, err := s.provider(decision.Provider); err != nil {
		return err
	}

	setDecisionHeaders(w.Header(), decision)
	setContextEstimateHeaders(w.Header(), env.ContextOverflowTokenEstimate(), outputReserveOAI)
	s.setFeedbackLinkHeader(ctx, w, facts.InstallationID, facts.ExternalID, facts.RequestID, auth.UserIDFrom(ctx))

	reqPricing, _ := catalog.PrimaryPriceFor(s.baselineFor(ctx, feats.Model))
	actPricing, _ := catalog.PrimaryPriceFor(decision.Model)
	recordDecisionSpan(ctx, s.clock, decisionSpan{
		surface: decisionSpanWithTaskArmAndSubscriber, facts: facts, feats: feats, decision: decision, routeRes: &routeRes, taskArm: taskArm,
		sessionKey: sessionKey, embedInput: embedInput, routeMs: routeMs,
		reqPricing: reqPricing, actPricing: actPricing, start: requestStart,
	})

	opts := translate.EmitOptions{
		TargetModel:              decision.Model,
		TargetProvider:           decision.Provider,
		Capabilities:             router.Lookup(decision.Model),
		IncludeStreamUsage:       s.usageRequired(),
		SessionAffinity:          sessionAffinityHint(routeRes.SessionKey),
		EnableServerSideFallback: s.rescueConfig.AnthropicServerSideFallbackEnabled(ctx),
		ModelSwitched:            routeRes.ModelSwitched(),
		EnableExtendedContext:    shouldEnableExtendedContext(env.FullTokenEstimate(), outputReserveOAI),
	}
	effortServed := s.resolveEffort(ctx, decision, opts.Capabilities, routeRes.EscalateEffort)
	effortServed.Apply(&opts)

	if s.codexSubscriptionExhausted(ctx, r.Header) {
		nudge := codexSubscriptionReconnectNudge(ctx, r.Header, s.credentials.UsageObserver)
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
		}) && !s.engine.GatewayCaps().LacksResponses(responsesEndpointKey)
		if !env.RequiresChatCompletionsParams(opts.Capabilities) && !s.engine.GatewayCaps().LacksResponses(responsesEndpointKey) && s.supportsSubscriptionTransport(decision.Provider) &&
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
	ctx, err = dispatchChatTurn(ctx, credentialCtx, &chatTurnDispatch{
		s: s, w: w, r: r, body: body, requestStart: requestStart, facts: facts, rateLimit: rateLimit, log: log, env: env, sessionKey: sessionKey,
		feats: feats, promptText: promptText, embedInput: embedInput, strategy: strategy, taskArm: taskArm, inboundLastUser: inboundLastUser,
		inboundUserPromptOpenAI: inboundUserPromptOpenAI, routeRequest: routeRequest, routeRes: routeRes, routeMs: routeMs, decision: decision, tt: tt,
		stickyHit: stickyHit, pinTier: pinTier, pinAgeSec: pinAgeSec, reqPricing: reqPricing, actPricing: actPricing, effortServed: effortServed,
		opts: opts, replayedModel: replayedModel, footerEchoed: footerEchoed, responsesBody: responsesBody,
		openAIResponsesEndpoint: openAIResponsesEndpoint, isResponsesWriter: isResponsesWriter, overflowEstimateOAI: overflowEstimateOAI,
		outputReserveOAI: outputReserveOAI, responsesEndpointKey: responsesEndpointKey, escalationCapture: &escalationCapture,
	})
	return err
}
