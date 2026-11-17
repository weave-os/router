package proxy

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/ingress"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/routingpolicy"
	"weave-os/router/internal/subscriptions/entitlement"
	"weave-os/router/internal/telemetry"
	"weave-os/router/internal/translate"

	"github.com/google/uuid"
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
	requestID := requestIDFor(ctx)
	ctx = telemetry.WithRecorder(ctx, s.newTelemetryRecorder())

	apiKeyID, _ := ctx.Value(APIKeyIDContextKey{}).(string)
	externalID, _ := ctx.Value(ExternalIDContextKey{}).(string)
	installationID := installationIDFromContext(ctx)
	clientID := ClientIdentityFrom(ctx)

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
	ctx, log, sessionKey = bindRequestLogger(ctx, env, apiKeyID, requestID, "gemini_generate_content")
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

	subAgentHint := r.Header.Get("x-weave-subagent-type")

	forceCluster, forceErr := applyForceClusterHeader(ctx, r)
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
	prefiltered := routingpolicy.Prefilter(routingpolicy.PrefilterRequest{
		Envelope: env, ImageCount: feats.ImageCount, OutputReserve: outputReserve, Eligibility: eligibility,
		Excluded: excluded, Available: s.availableModels,
		AdvisorExclusions: advisorExclusions(env, s.routableUniverse()),
	})
	logPrefilter(log, env, feats.ImageCount, outputReserve, prefiltered)
	excluded, overflowEstimate := prefiltered.Excluded, prefiltered.OverflowEstimate

	routingPromptText, routingMessages := routingInputForRequest(ctx, env, promptText)
	routeRequest := router.Request{
		RequestedModel:                feats.Model,
		ForceCluster:                  forceCluster,
		EstimatedInputTokens:          feats.Tokens,
		HasTools:                      feats.HasTools,
		HasImages:                     feats.HasImages,
		ImageCount:                    feats.ImageCount,
		TranslationRequirements:       env.TranslationRequirements(router.EndpointGeminiGenerate),
		ReasoningConfigurationSHA256:  env.ReasoningConfigurationSHA256(),
		ToolConfigurationSHA256:       env.ToolConfigurationSHA256(),
		PromptText:                    routingPromptText,
		ConversationMessages:          routingMessages,
		AvailableTools:                availableToolsForRouting(env),
		Tools:                         toolsForRouting(env),
		ClientSessionID:               clientSessionIDForRequest(ctx, env),
		EnabledProviders:              enabledProviders,
		CustomBindings:                eligibility.CustomBindings,
		GatewayProviders:              s.gatewayProvidersForRequest(ctx),
		ExcludedModels:                excluded,
		AllowedModels:                 allowedModelsForRequest(ctx),
		SafetyExcludedModels:          prefiltered.Safety,
		ContextWindowExcludedModels:   prefiltered.ContextWindowOnly,
		ImageLimitExcludedModels:      modelSet(prefiltered.ImageLimited),
		UnsignedHistoryExcludedModels: modelSet(prefiltered.UnsignedHistory),
		OverflowAdmittedModels:        modelSet(prefiltered.OverflowAdmitted),
		PreferredModels:               s.preferredModelsForRequest(ctx),
		RoutingKnobs:                  router.RoutingKnobsFromContext(ctx),
		ClusterArmOverrides:           clusterArmOverridesForRequest(ctx),
		ProductEligibility:            entitlement.ModelBoundaryFromContext(ctx),
	}
	routeStart := s.clock.Now()
	routeCtx, routeSpan := startRoutingSpan(ctx, routeRequest)
	routeRes, err := s.runTurnLoop(routeCtx, env, feats, apiKeyID, installationID, subAgentHint, r.Header, routeRequest)
	defer routeRes.releaseCooldownProbes()
	var escalationCapture *captureWriter
	defer func() {
		s.completeEscalation(ctx, routeRes, returnErr, escalationCapture, translate.EscalationResponseGemini)
	}()
	finishRoutingSpan(routeSpan, routeRes.Decision, err)
	routeMs := s.clock.Now().Sub(routeStart).Milliseconds()
	if err != nil {
		log.Error("Routing failed for Gemini request", "err", err, "route_ms", routeMs, "requested_model", feats.Model, "total_input_tokens", feats.Tokens)
		s.recordPolicyPinRouteFailure(ctx, requestID, requestStart, feats.Model, routeRes.TurnType, inboundUserPrompt, err)
		return err
	}
	ctx = requestcontext.WithCallerModelPassthrough(ctx, routeRes.CallerModelPassthrough)
	if len(routeRes.SessionDisabledProviders) > 0 {
		ctx = context.WithValue(ctx, SessionDisabledProvidersContextKey{}, routeRes.SessionDisabledProviders)
	}
	if len(routeRes.ObservedOverflowModels) > 0 {
		ctx = context.WithValue(ctx, ObservedOverflowModelsContextKey{}, routeRes.ObservedOverflowModels)
	}
	if len(routeRes.SessionDemotedModels) > 0 {
		ctx = context.WithValue(ctx, SessionDemotedModelsContextKey{}, routeRes.SessionDemotedModels)
		if len(routeRes.SessionCooldownModels) > 0 {
			ctx = context.WithValue(ctx, SessionCooldownModelsContextKey{}, routeRes.SessionCooldownModels)
		}
		if len(routeRes.SessionStrikeReadmitModels) > 0 {
			ctx = context.WithValue(ctx, SessionStrikeReadmitModelsContextKey{}, routeRes.SessionStrikeReadmitModels)
		}
	}
	if len(routeRes.CooldownProbeDeniedModels) > 0 {
		ctx = context.WithValue(ctx, SessionCooldownProbeDeniedModelsContextKey{}, routeRes.CooldownProbeDeniedModels)
	}
	routeRes.SuggestionMode = r.Header.Get("x-weave-suggestion-mode") == "true"
	decision := routeRes.Decision
	s.firePolicyShadowForServingDecision(ctx, decision, routeRequest)
	tt := routeRes.TurnType
	stickyHit := routeRes.StickyHit
	pinTier := routeRes.PinTier
	pinAgeSec := routeRes.PinAgeSec
	s.logPlannerOutcome(ctx, routeRes)

	w.Header().Set(HeaderRouterDecision, decision.Reason)
	w.Header().Set(HeaderRouterProvider, decision.Provider)
	w.Header().Set(HeaderRouterModel, decision.Model)
	w.Header().Set(HeaderRouterContextWindow, strconv.Itoa(contextWindowForRequest(decision.Model, decision.Provider)))
	setContextEstimateHeaders(w.Header(), overflowEstimate, outputReserve)
	s.setFeedbackLinkHeader(ctx, w, installationID, externalID, requestID, "")

	reqPricing, _ := catalog.PrimaryPriceFor(s.baselineFor(ctx, feats.Model))
	actPricing, _ := catalog.PrimaryPriceFor(decision.Model)
	reqDecisionPricing := reqPricing.ForInputTokens(feats.Tokens)
	actDecisionPricing := actPricing.ForInputTokens(feats.Tokens)
	geminiDecisionBuilder := telemetry.NewAttrBuilder(46).
		String("request_id", requestID).
		String("external_id", externalID).
		String("client.device_id", clientID.DeviceID).
		String("client.account_id", clientID.AccountID).
		String("client.session_id", clientID.SessionID).
		String("client.user_agent", clientID.UserAgent).
		String("client.app", clientID.TelemetryClientApp()).
		String("rollout_id", policyRolloutIDFromContext(ctx)).
		String("requested.model", feats.Model).
		String("decision.model", decision.Model).
		String("decision.provider", decision.Provider).
		String("decision.reason", decision.Reason).
		Bool("routing.policy_fallback", routeRes.PolicyFallback).
		Bool("routing.sticky_hit", stickyHit).
		Bool("routing.session_pin_hit", pinTier == "in_proc" || pinTier == "postgres").
		String("routing.session_pin_tier", pinTier).
		Int64("routing.session_pin_age_s", pinAgeSec).
		String("routing.turn_type", string(tt)).
		String("routing.embed_input", embedInput).
		Int64("routing.estimated_input_tokens", int64(feats.Tokens)).
		Float64("catalog.requested_input_per_1m", reqDecisionPricing.InputUSDPer1M).
		Float64("catalog.requested_output_per_1m", reqDecisionPricing.OutputUSDPer1M).
		Float64("catalog.actual_input_per_1m", actDecisionPricing.InputUSDPer1M).
		Float64("catalog.actual_output_per_1m", actDecisionPricing.OutputUSDPer1M).
		Int64("latency.route_ms", routeMs)
	applyServingSpanAttrs(ctx, geminiDecisionBuilder)
	applySidecarAttrs(geminiDecisionBuilder, routeRes)
	applyPlannerAttrs(geminiDecisionBuilder, routeRes)
	applyRoutingStateAttrs(geminiDecisionBuilder, routeRes, decision.ServedIdentity(), sessionKey)
	applyCallerRoutingAttrs(ctx, geminiDecisionBuilder)
	applyBlindExperimentOutcomeAttrs(ctx, geminiDecisionBuilder, &routeRes, feats.Model, routeRes.Decision.Model)
	telemetry.Record(ctx, telemetry.Span{
		Name:  "router.decision",
		Start: requestStart,
		End:   s.clock.Now(),
		Attrs: geminiDecisionBuilder.Build(),
	})
	telemetry.Flush(ctx)

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
	marker := suppressMarkerIfRequested(ctx, r.Header, modelSelectionMarkerForRequest(ctx, routeRes, routingMarkerFor(routeRes), decision.Model, ""))
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
			extractor, proxySink = s.newUsageExtractor(attemptSink, d.Provider)
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

	in, out := extractor.Tokens()
	cacheCreation, cacheRead := extractor.CacheTokens()
	usageMods := extractor.UsageModifiers()
	baselineWarmPrefill := routeRes.baselineWarmPrefillTokens(requestStart, cacheCreation, cacheRead, decision.Model, s.baselineFor(ctx, feats.Model), routeRequest.HistoryTruncated)
	requestedInputCost := catalog.CounterfactualInputCost(in, cacheCreation, cacheRead, baselineWarmPrefill, reqPricing, decision.Provider, usageMods)
	if responseBuffer != nil && proxyErr == nil {
		setRouterCostHeaders(w.Header(), routerResponseCostFromPricing(actPricing, decision.Provider, in, out, cacheCreation, cacheRead, usageMods))
	}
	geminiUpstreamBuilder := telemetry.NewAttrBuilder(41).
		String("request_id", requestID).
		String("external_id", externalID).
		String("client.device_id", clientID.DeviceID).
		String("client.account_id", clientID.AccountID).
		String("client.session_id", clientID.SessionID).
		String("client.user_agent", clientID.UserAgent).
		String("client.app", clientID.TelemetryClientApp()).
		String("rollout_id", policyRolloutIDFromContext(ctx)).
		String("requested.model", feats.Model).
		String("decision.model", decision.Model).
		String("decision.provider", decision.Provider).
		String("decision.reason", decision.Reason).
		String("routing.turn_type", string(tt)).
		Int64("usage.input_tokens", int64(in)).
		Int64("usage.output_tokens", int64(out)).
		Int64("usage.cache_creation_input_tokens", int64(cacheCreation)).
		Int64("usage.cache_read_input_tokens", int64(cacheRead)).
		Float64("cost.requested_input_usd", requestedInputCost).
		Int64("cost.baseline_warm_prefill_tokens", int64(baselineWarmPrefill)).
		Float64("cost.requested_output_usd", catalog.EffectiveOutputCost(catalog.PromptTokens(in, cacheCreation, cacheRead, decision.Provider), out, reqPricing, usageMods)).
		Float64("cost.actual_input_usd", catalog.EffectiveInputCost(in, cacheCreation, cacheRead, actPricing, decision.Provider, usageMods)).
		Float64("cost.actual_output_usd", catalog.EffectiveOutputCost(catalog.PromptTokens(in, cacheCreation, cacheRead, decision.Provider), out, actPricing, usageMods)).
		Bool("cost.subscription_served", s.costNeutralSubscriptionServed(ctx)).
		Int64("latency.upstream_ms", proxyMs).
		Int64("latency.total_ms", s.clock.Now().Sub(requestStart).Milliseconds()).
		Int64("upstream.status_code", int64(upstreamStatus(proxyErr))).
		Bool("routing.cross_format", false)
	if s.effectiveCaptureMode(ctx) == CaptureOff {
		geminiUpstreamBuilder.Int64("request.message_count", int64(feats.MessageCount)).
			Bool("request.has_tools", feats.HasTools)
	}
	applyPlannerAttrs(geminiUpstreamBuilder, routeRes)
	applyRoutingStateAttrs(geminiUpstreamBuilder, routeRes, decision.ServedIdentity(), sessionKey)
	applyCallerRoutingAttrs(ctx, geminiUpstreamBuilder)
	applyBlindExperimentOutcomeAttrs(ctx, geminiUpstreamBuilder, &routeRes, feats.Model, decision.Model)
	applyEffortAttrs(geminiUpstreamBuilder, effortServed)
	s.applySubscriptionSpanTelemetry(ctx, geminiUpstreamBuilder, feats.Model, decision.Model)
	addTimingAttrs(ctx, geminiUpstreamBuilder)
	geminiObs := buildObservationContext(ctx, decision, routeRes.Fresh, s.effectiveCaptureMode(ctx))
	geminiObs.applySpanAttrs(geminiUpstreamBuilder)
	telemetry.Record(ctx, telemetry.Span{
		Name:  "router.upstream",
		Start: proxyStart,
		End:   s.clock.Now(),
		Attrs: geminiUpstreamBuilder.Build(),
	})
	respBody, respTrunc := capturedResponse(contentCap)
	s.recordCallLog(ctx, geminiUpstreamBuilder.Build(), routeMs, proxyErr, body, respBody, respTrunc)
	telemetry.Flush(ctx)

	s.recordTurnUsage(ctx, routeRes, finalProvider, decision.ServedIdentity(), in, out, cacheCreation, cacheRead, extractor.OutputLimitReached())
	s.clearRecoveredCooldown(ctx, routeRes, decision.Model, proxyErr)

	var subscriberTelemetry *InsertTelemetryParams
	if installationID != uuid.Nil {
		credentialKeyPrefix, credentialKeySuffix, credentialSource := s.credentialKeyParts(ctx)
		s.markServedExternalKeyUsed(ctx, proxyErr)
		errorClass := classifyTurnError(proxyErr, "", 0)
		if errorClass == "" && extractor.OutputLimitReached() {
			errorClass = TurnErrorMaxTokens
		}
		telemetryParams := InsertTelemetryParams{
			InstallationID:         installationID.String(),
			APIKeyID:               apiKeyIDFromContext(ctx),
			RequestID:              requestID,
			SpanType:               "router.upstream",
			TraceID:                requestID,
			Timestamp:              requestStart,
			RequestedModel:         feats.Model,
			DecisionModel:          decision.Model,
			DecisionProvider:       finalProvider,
			DecisionReason:         telemetryDecisionReason(ctx, decision.Reason),
			RequestedAllowedModels: requestedAllowedModelsForTelemetry(ctx),
			EstimatedInputTokens:   int32(feats.Tokens),
			ContextSnapshot:        contextSnapshotJSON(w.Header(), requestID, feats.Model, requestStart, s.clock.Now()),
			StickyHit:              stickyHit,
			PinTier:                routeRes.PinTier,
			EmbedInput:             embedInput,
			InputTokens:            int32(in),
			OutputTokens:           int32(out),
			RequestedInputCostUSD:  requestedInputCost,
			RequestedOutputCostUSD: catalog.EffectiveOutputCost(catalog.PromptTokens(in, cacheCreation, cacheRead, finalProvider), out, reqPricing, usageMods),
			ActualInputCostUSD:     catalog.EffectiveInputCost(in, cacheCreation, cacheRead, actPricing, finalProvider, usageMods),
			ActualOutputCostUSD:    catalog.EffectiveOutputCost(catalog.PromptTokens(in, cacheCreation, cacheRead, finalProvider), out, actPricing, usageMods),
			RouteLatencyMs:         routeMs,
			UpstreamLatencyMs:      proxyMs,
			TotalLatencyMs:         s.clock.Now().Sub(requestStart).Milliseconds(),
			CrossFormat:            false,
			UpstreamStatusCode:     int32(upstreamStatus(proxyErr)),
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
			CacheCreationTokens:    cacheTokenPtr(cacheCreation),
			CacheReadTokens:        cacheTokenPtr(cacheRead),
			ReasoningTokens:        cacheTokenPtr(extractor.ReasoningTokens()),
			DeviceID:               clientID.DeviceID,
			SessionID:              clientID.SessionID,
			RouterUserID:           auth.UserIDFrom(ctx),
			ClientApp:              clientID.TelemetryClientApp(),
			TurnType:               string(routeRes.TurnType),
			RolloutID:              geminiObs.RolloutID,
			FailoverUsed:           boolPtrTrue(finalProvider != primaryProvider),
			SessionKey:             sessionKey[:],
			Role:                   routeRes.PinRole,
			FreshDecisionModel:     geminiObs.FreshDecisionModel,
			FreshCandidateScores:   geminiObs.FreshCandidateScores,
			PinAgeSec:              int64PtrIf(stickyHit && pinAgeSec > 0, pinAgeSec),
			ToolResultBytes:        toolResultBytesPtr(inboundLastUser, tt),
			ErrorClass:             errorClass,
			FailureOwner:           turnFailureOwnerFor(ctx, proxyErr, errorClass),
			UserPrompt:             &inboundUserPrompt,
			CredentialKeyPrefix:    credentialKeyPrefix,
			CredentialKeySuffix:    credentialKeySuffix,
			CredentialSource:       credentialSource,
		}
		applyServedGroupTelemetry(ctx, &telemetryParams, routeRes, decision)
		applyPlannerTelemetry(&telemetryParams, routeRes)
		applyEffortTelemetry(&telemetryParams, effortServed)
		applyAuthorityShadowTelemetry(&telemetryParams, routeRes)
		applyBlindExperimentTelemetry(ctx, &telemetryParams, &routeRes)
		applyPolicyPinTelemetry(ctx, &telemetryParams, decision.Metadata)
		applySubscriberTelemetry(ctx, &telemetryParams)
		applyAgreementShadowTelemetry(ctx, &telemetryParams, routeRes)
		subscriberTelemetry = &telemetryParams
	}

	var subscriberSettlement subscriberSettlementState
	if turnUsageBillable(ctx, proxyErr, in, out, cacheCreation, cacheRead) {
		subscriberSettlement = s.emitBilling(ctx, requestID, externalID, feats.Model, decision, actPricing, routeRes, in, out, cacheCreation, cacheRead, usageMods)
	}
	if subscriberTelemetry != nil {
		if turnUsageBillable(ctx, proxyErr, in, out, cacheCreation, cacheRead) {
			applySubscriberSettlementTelemetry(subscriberSettlement, subscriberTelemetry)
		} else {
			markSubscriberTelemetryUnsettled(subscriberTelemetry)
		}
		s.fireTelemetry(ctx, *subscriberTelemetry)
	}

	armDemoted := ""
	var armDemotionReason sessionpin.DemotionReason
	primaryFailureDemoted := ""
	var primaryFailureDemotionReason sessionpin.DemotionReason
	if !routeRes.CallerModelPassthrough {
		s.maybeDisableProviderAfterOverload(ctx, stickyHit, proxyErr, finalProvider, decision.Reason, installationID, routeRes.SessionKey, stickyStateRole(routeRes), routeRes.PinRole)
		armDemoted = s.maybeDemoteArmAfterCommittedStreamFailure(ctx, committed(preludeBuf), routeRes.HardPinned, proxyErr, decision.Model, decision.Reason, installationID, routeRes.SessionKey, stickyStateRole(routeRes), routeRes.PinRole)
		if armDemoted != "" {
			armDemotionReason = sessionpin.DemotionReasonCommittedStreamFailure
		}
		if providers.IsResponseHeaderTimeout(primaryFailureErr) && (proxyErr != nil || decision.Model != primaryDecision.Model) {
			primaryFailureDemoted, primaryFailureDemotionReason = s.maybeStrikeArmAfterRescuedFailure(ctx, false, routeRes.HardPinned, primaryFailureErr, primaryDecision, installationID, routeRes.SessionKey, stickyStateRole(routeRes), routeRes.PinRole)
		}
		if proxyErr != nil {
			unrescuedStallDemoted := s.maybeDemoteArmAfterUnrescuedStall(ctx, false, committed(preludeBuf), routeRes.HardPinned, primaryFailureErr, primaryDecision, installationID, routeRes.SessionKey, stickyStateRole(routeRes), routeRes.PinRole)
			if unrescuedStallDemoted != "" {
				armDemoted = unrescuedStallDemoted
				armDemotionReason = sessionpin.DemotionReasonUnrescuedStall
			}
		}
	}

	demotionLogFields := armStrikeLogFieldsWithPrimaryReason(armDemoted, armDemotionReason, primaryFailureDemoted, primaryFailureDemotionReason)
	servedCredentialSource, servedSubscriptionTier := s.servedCredentialLogValues(ctx)
	log.Info("ProxyGeminiGenerateContent complete", append(append([]any{"requested_model", feats.Model, "baseline_model", s.baselineFor(ctx, feats.Model), "decision_model", decision.Model, "decision_provider", decision.Provider, "decision_reason", decision.Reason, "embedded_tokens", len(promptText) / 4, "total_input_tokens", feats.Tokens, "has_tools", feats.HasTools, "embed_input", embedInput, "sticky_hit", stickyHit, "pin_tier", pinTier, "turn_type", string(tt), "route_ms", routeMs, "proxy_ms", proxyMs, "proxy_err", proxyErr, "error_class", classifyTurnError(proxyErr, "", 0), "failure_owner", turnFailureOwnerFor(ctx, proxyErr, classifyTurnError(proxyErr, "", 0)), "upstream_status", upstreamStatus(proxyErr), "credential_source", servedCredentialSource, "client_app", completionLogClientApp(clientID), "subscription_tier", servedSubscriptionTier}, demotionLogFields...), append(plannerLogFields(routeRes), rateLimit.CompletionLogFields()...)...)...)
	s.reportPolicyOutcome(ctx, routeRes, decision, effortServed, decision.Provider, false, feats.Tokens, in, out, cacheCreation, cacheRead, usageMods, routeMs, proxyMs, proxyErr, nil)
	return proxyErr
}
