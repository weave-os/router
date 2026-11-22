package proxy

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/ingress"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/candidates"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/subscriptions/entitlement"
	"weave-os/router/internal/telemetry"
	"weave-os/router/internal/translate"
)

var ErrGeminiCrossFormatUnsupported = ingress.ErrGeminiCrossFormatUnsupported

func (s *Service) ProxyGeminiGenerateContent(ctx context.Context, body []byte, w http.ResponseWriter, r *http.Request) (returnErr error) {
	ctx = withWeaveCreditsFunding(ctx, r.Header)
	w, finishBlindExperimentWriter := withBlindExperimentWriter(ctx, w, body)
	defer finishBlindExperimentWriter()
	ctx = withAttemptTrace(ctx, s.ingress.Gemini.TraceBody(body), r.Header)
	ctx = s.withSubscriptionFundingAuthorization(ctx, r.Header)
	ctx, returnErr = s.withClassifierInput(ctx, body, router.EndpointGeminiGenerate)
	if returnErr != nil {
		return returnErr
	}
	ctx = requestcontext.WithContentLogging(ctx, s.effectiveCaptureMode(ctx) != CaptureOff)
	ctx, err := s.checkUserMonthlySpendLimit(ctx, r.Header, r.URL.Path)
	if err != nil {
		return err
	}

	if subscriptionStateModelsEnabled(ctx) {
		ctx = context.WithValue(ctx, subscriptionStateAllowedModelsKey{}, modelSet(installationSubscriptionModelsWhenInactiveFromContext(ctx)))
		ctx = context.WithValue(ctx, subscriptionAPIOnlyKey{}, true)
	}
	ctx, rateLimit := s.withRateLimitTurn(ctx)
	log := observability.FromContext(ctx)
	requestStart := s.clock.Now()
	facts := requestFactsFrom(ctx, r.Header)
	ctx = telemetry.WithRecorder(ctx, s.newTelemetryRecorder())

	env, body, parseErr := s.ingress.Gemini.Parse(ctx, body)
	if parseErr != nil {
		log.Error("Failed to parse Gemini request", "err", parseErr)
		return fmt.Errorf("parse request: %w", parseErr)
	}
	inboundLastUser := env.LastUserMessage()
	inboundUserPrompt := routingEndsWithUserPrompt(ctx, env)
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
	feats := env.RoutingFeatures()
	ctx = withRequestImageCount(ctx, feats.ImageCount)
	promptText := feats.PromptText
	embedInput := "concatenated_stream"
	if feats.OnlyUserMessageText != "" {
		promptText = feats.OnlyUserMessageText
		embedInput = "only_user_message"
	}

	var sessionKey [sessionpin.SessionKeyLen]byte
	ctx, log, sessionKey = bindRequestLogger(ctx, env, facts.APIKeyID, facts.RequestID, "gemini_generate_content")
	log.Info("ProxyGeminiGenerateContent start",
		"requested_model", feats.Model,
		"stream", env.Stream(),
		"message_count", feats.MessageCount,
		"has_tools", feats.HasTools,
		"total_input_tokens", feats.Tokens,
	)

	if s.effectiveCaptureMode(ctx) != CaptureOff {
		logInboundRequestDiagnostics(log, env)
	}

	subAgentHint, strategy := r.Header.Get("x-weave-subagent-type"), router.StrategyFromContext(ctx)

	forceCluster, forceErr := applyForceClusterHeader(s.strategyPorts, ctx, strategy, r)
	if forceErr != nil {
		return forceErr
	}

	eligibility, eligibilityErr := requestEligibility(ctx, s, providers.ProviderGoogle, r.Header)
	if eligibilityErr != nil {
		return eligibilityErr
	}
	enabledProviders := eligibility.EnabledProviders
	excluded := s.excludedModelsForRequest(ctx)
	outputReserve := contextWindowOutputReserve
	if feats.MaxTokens > outputReserve {
		outputReserve = feats.MaxTokens
	}
	prefiltered := candidates.Prefilter(candidates.PrefilterRequest{
		Envelope: env, ImageCount: feats.ImageCount, OutputReserve: outputReserve, Eligibility: eligibility,
		Excluded: excluded, Available: s.availableModels,
		AdvisorExclusions: advisorExclusions(env, s.routableUniverse()),
	})
	logPrefilter(log, env, feats.ImageCount, outputReserve, prefiltered)
	excluded, overflowEstimate := prefiltered.Excluded, prefiltered.OverflowEstimate

	routingPromptText, routingMessages := routingInputForRequest(ctx, env, promptText)
	routeRequest := router.Request{
		RequestedModel:               feats.Model,
		ForceCluster:                 forceCluster,
		EstimatedInputTokens:         feats.Tokens,
		HasTools:                     feats.HasTools,
		HasImages:                    feats.HasImages,
		ImageCount:                   feats.ImageCount,
		TranslationRequirements:      env.TranslationRequirements(router.EndpointGeminiGenerate),
		ReasoningConfigurationSHA256: env.ReasoningConfigurationSHA256(),
		ToolConfigurationSHA256:      env.ToolConfigurationSHA256(),
		PromptText:                   routingPromptText,
		ConversationMessages:         routingMessages,
		AvailableTools:               availableToolsForRouting(env),
		Tools:                        toolsForRouting(env),
		ClientSessionID:              clientSessionIDForRequest(ctx, env),
		EnabledProviders:             enabledProviders,
		CustomBindings:               eligibility.CustomBindings,
		GatewayProviders:             s.gatewayProvidersForRequest(ctx),
		Candidates:                   prefilteredCandidates(ctx, excluded, prefiltered),
		PreferredModels:              s.preferredModelsForRequest(ctx),
		RoutingKnobs:                 router.RoutingKnobsFromContext(ctx),
		ClusterArmOverrides:          clusterArmOverridesForRequest(ctx),
		ProductEligibility:           entitlement.ModelBoundaryFromContext(ctx),
	}
	routeRes, routeMs, err := routeTurn(ctx, s, s.clock, log, routeTurnRequest{
		Request:        routeRequest,
		Started:        s.clock.Now(),
		Failure:        routeFailureRequest{RequestID: facts.RequestID, RequestStart: requestStart, RequestedModel: feats.Model, Strategy: strategy},
		FailureMessage: "Routing failed for Gemini request",
		IsUserPrompt:   inboundUserPrompt,
	}, func(routeCtx context.Context) (turnLoopResult, error) {
		return s.runTurnLoop(routeCtx, env, feats, turnRequest{Strategy: strategy, APIKeyID: facts.APIKeyID, InstallationID: facts.InstallationID}, subAgentHint, r.Header, routeRequest)
	})
	defer routeRes.releaseCooldownProbes()
	var escalationCapture *captureWriter
	defer func() {
		s.completeEscalation(ctx, routeRes, returnErr, escalationCapture, translate.EscalationResponseGemini)
	}()
	if err != nil {
		return err
	}
	ctx = requestcontext.WithCallerModelPassthrough(ctx, routeRes.CallerModelPassthrough)
	ctx = withRouteSessionState(ctx, routeRes)
	routeRes.SuggestionMode = r.Header.Get("x-weave-suggestion-mode") == "true"
	decision := routeRes.Decision
	s.firePolicyShadowForServingDecision(ctx, strategy, decision, routeRequest)
	tt := routeRes.TurnType
	stickyHit := routeRes.StickyHit
	pinTier := routeRes.PinTier
	pinAgeSec := routeRes.PinAgeSec
	s.logPlannerOutcome(ctx, routeRes)

	w.Header().Set(requestcontext.HeaderRouterDecision, decision.Reason)
	w.Header().Set(requestcontext.HeaderRouterProvider, decision.Provider)
	w.Header().Set(requestcontext.HeaderRouterModel, decision.Model)
	w.Header().Set(requestcontext.HeaderRouterContextWindow, strconv.Itoa(contextWindowForRequest(decision.Model, decision.Provider)))
	setContextEstimateHeaders(w.Header(), overflowEstimate, outputReserve)
	s.setFeedbackLinkHeader(ctx, w, facts.InstallationID, facts.ExternalID, facts.RequestID, "")

	reqPricing, _ := catalog.PrimaryPriceFor(s.baselineFor(ctx, feats.Model))
	actPricing, _ := catalog.PrimaryPriceFor(decision.Model)
	recordDecisionSpan(ctx, s.clock, decisionSpan{
		surface: decisionSpanBlindExperimentOnly, facts: facts, feats: feats, decision: decision, routeRes: &routeRes,
		sessionKey: sessionKey, embedInput: embedInput, routeMs: routeMs,
		reqPricing: reqPricing, actPricing: actPricing, start: requestStart,
	})

	if err := s.ingress.Gemini.CheckTarget(decision.Provider); err != nil {
		return err
	}

	opts := translate.EmitOptions{
		TargetModel:        decision.Model,
		TargetProvider:     decision.Provider,
		Capabilities:       router.Lookup(decision.Model),
		IncludeStreamUsage: s.usageRequired(),
	}
	effortServed := s.resolveEffort(ctx, decision, opts.Capabilities, routeRes.EscalateEffort)
	effortServed.Apply(&opts)
	ctx = resolveAndInjectCredentials(ctx, decision.Provider, decision.Model, r.Header)

	prep, emitErr := env.PrepareGemini(r.Header, geminiOptionsForTurn(opts, routeRes.TurnType))
	if emitErr != nil {
		log.Error("Failed to emit Gemini body", "err", emitErr)
		return fmt.Errorf("emit body: %w", emitErr)
	}

	proxyStart := s.clock.Now()
	inferenceParentCtx := ctx
	ctx, inferenceSpan := startInferenceSpan(ctx, decision, clientSessionIDForRequest(ctx, env))
	defer inferenceSpan.End()
	var extractor *telemetry.UsageExtractor
	clientSink := w
	if env.Stream() {
		if footer := s.feedbackFooter(ctx, ClientIdentityFrom(ctx).ClientApp, routeRes.TurnType, false); footer != "" {
			clientSink = translate.NewGeminiRoutingFooterWriter(w, footer)
		}
	}
	clientSink, escalationCapture = s.captureEscalationResponse(clientSink, routeRes)
	contentSink, contentCap := s.maybeCaptureResponse(ctx, clientSink)
	preludeBuf := newPreludeBuffer(contentSink)
	marker := suppressMarkerIfRequested(ctx, r.Header, modelSelectionMarkerForRequest(s.strategyPorts, ctx, routeRes, routingMarkerFor(routeRes), decision.Model, ""))
	muteSlowStartFor(ctx, routeRes)
	bindings := s.resolveBindingsForDispatch(ctx, decision)
	primaryDecision := decision
	notices := &requestNoticeStream{now: s.clock.Now}
	attempt := func(actx context.Context, d router.Decision, p dispatch.Upstream) error {
		attemptSink, noticeErr := notices.prepare(actx, preludeBuf, preludeBuf, translate.FormatGemini, env.Stream(), translate.NewRouterNotice(translate.NoticeModelSelection, marker, d.Model, ""))
		if noticeErr != nil {
			return fmt.Errorf("emit Gemini router notice: %w", noticeErr)
		}
		proxySink := attemptSink
		if s.usageRequired() {
			extractor, proxySink = s.turnTelemetry.newUsageExtractor(attemptSink, d.Provider)
		}
		preludeBuf.Seal()
		err := p.Proxy(actx, d, prep, proxySink, r)
		if err == nil && env.Stream() && !preludeBuf.Committed() {
			return translate.ErrStreamEmpty
		}
		if err != nil && env.Stream() && preludeBuf.Committed() {
			s.ingress.Gemini.WriteStreamError(contentSink)
		}
		return err
	}
	winnerIdx, proxyErr := s.dispatchWithFallback(ctx, dispatch.FailoverInputs{
		Writer:          contentSink,
		Prelude:         preludeBuf,
		InitialDecision: decision,
		Bindings:        bindings,
		Attempt:         attempt,
		FlushErr: func(w http.ResponseWriter, err error) {
			if env.Stream() && preludeBuf.PreludeSent() {
				s.ingress.Gemini.WriteStreamError(w)
				return
			}
			flushBufferedIfPresent(w, err)
		},
		Purpose: routeRes.dispatchPurpose(inference.PurposeGeminiGenerateContent),
		Origin:  routeRes.dispatchOrigin(decision),
	})
	primaryFailureErr := proxyErr
	proxyMs := s.clock.Now().Sub(proxyStart).Milliseconds()
	primaryProvider := decision.Provider
	finalProvider := primaryProvider
	if winnerIdx >= 0 && winnerIdx < len(bindings) {
		finalProvider = bindings[winnerIdx].Provider
	}
	finishInferenceSpan(inferenceSpan, decision, finalProvider, winnerIdx, proxyErr)
	ctx = restoreParentSpan(ctx, inferenceParentCtx)
	decision.Provider = finalProvider
	if actBindingPricing, ok := servedPricing(finalProvider, decision.Model, false); ok {
		actPricing = actBindingPricing
	}

	return finishGeminiTurn(ctx, s, &geminiFinishedTurn{
		w: w, body: body, rateLimit: rateLimit, log: log, requestStart: requestStart, facts: facts, inboundLastUser: inboundLastUser,
		inboundUserPrompt: inboundUserPrompt, responseBuffer: responseBuffer, feats: feats, promptText: promptText, embedInput: embedInput,
		sessionKey: sessionKey, strategy: strategy, routeRequest: routeRequest, routeMs: routeMs, routeRes: routeRes, decision: decision, tt: tt,
		stickyHit: stickyHit, pinTier: pinTier, pinAgeSec: pinAgeSec, reqPricing: reqPricing, actPricing: actPricing, effortServed: effortServed,
		proxyStart: proxyStart, extractor: extractor, contentCap: contentCap, preludeBuf: preludeBuf, primaryDecision: primaryDecision,
		proxyErr: proxyErr, primaryFailureErr: primaryFailureErr, proxyMs: proxyMs, primaryProvider: primaryProvider, finalProvider: finalProvider,
	})
}
