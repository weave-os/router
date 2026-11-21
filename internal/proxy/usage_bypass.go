package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/candidates"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/routingpolicy/overrides"
	"weave-os/router/internal/subscriptions"
	"weave-os/router/internal/subscriptions/entitlement"
	"weave-os/router/internal/telemetry"
	"weave-os/router/internal/telemetry/turnrecord"
	"weave-os/router/internal/translate"

	"github.com/google/uuid"
)

const (
	reasonUsageBypass              = "usage_bypass"
	reasonClassifierPassthrough    = "classifier_subscription_passthrough"
	reasonClassifierRequestedModel = "classifier_requested_model"
)

func (s *Service) usageBypassDecision(ctx context.Context, headers http.Header, req router.Request, sessionDemotedModels []string, turnType turntype.TurnType) (router.Decision, bool) {
	if subscriptionStateModelsEnabled(ctx) || !candidates.RequestedModelServable(req, sessionDemotedModels) {
		return router.Decision{}, false
	}
	provider, reason, engaged := s.subscriptionPassthroughEngaged(ctx, headers, req, turnType)
	if !engaged {
		return router.Decision{}, false
	}
	return candidates.RequestedModelDecision(req, provider, reason), true
}

func (s *Service) subscriptionPassthroughEngaged(ctx context.Context, headers http.Header, req router.Request, turnType turntype.TurnType) (provider, reason string, engaged bool) {
	if provider, ok := s.classifierPassthroughEngaged(ctx, headers, req, turnType); ok {
		return provider, reasonClassifierPassthrough, true
	}
	if provider, ok := s.usageBypassEngaged(ctx, headers, req); ok {
		return provider, reasonUsageBypass, true
	}
	return "", "", false
}

func (s *Service) usageBypassEngaged(ctx context.Context, headers http.Header, req router.Request) (string, bool) {
	if !s.supportsSubscriptionTransport(providers.ProviderAnthropic) && !s.supportsSubscriptionTransport(providers.ProviderOpenAI) {
		return "", false
	}
	cfg, ok := usageBypassFromContext(ctx)
	if !ok || s.usageObserver == nil {
		return "", false
	}
	provider, token, covered := subscriptionCoveredTarget(ctx, headers, req)
	if !covered || !s.supportsSubscriptionTransport(provider) {
		return "", false
	}
	if provider == providers.ProviderAnthropic && s.subscriptionModels.denied([]byte(token), req.RequestedModel, s.clock.Now()) {
		return "", false
	}
	snap, observed := s.usageObserver.Snapshot(s.usageObserver.Key([]byte(token)))
	if !observed {
		return provider, true
	}
	if provider == providers.ProviderAnthropic && claudeSnapshotUnservable(ctx, snap) ||
		provider != providers.ProviderAnthropic && snap.BillableOrExhausted() {
		return "", false
	}
	if billing.SubscriptionOnlyFromContext(ctx) {
		return provider, true
	}
	return provider, overrides.UsageBelowThreshold(cfg.Threshold, max(snap.Primary.UsedPercent, snap.Secondary.UsedPercent))
}

func subscriptionCoveredTarget(ctx context.Context, headers http.Header, req router.Request) (provider, token string, covered bool) {
	codexToken, anthropicToken := presentSubscriptionTokens(ctx, headers)
	return overrides.SubscriptionCoveredTarget(req, codexToken, anthropicToken)
}

func (s *Service) anthropicFallbackKeyAvailable(ctx context.Context) bool {
	if byok := BuildCredentialsMap(externalKeysFromContext(ctx)); byok != nil {
		if _, ok := byok[providers.ProviderAnthropic]; ok {
			return true
		}
	}
	if s.credentials.KeyedProviders != nil {
		if _, ok := s.credentials.KeyedProviders[providers.ProviderAnthropic]; ok {
			return true
		}
	}
	return false
}

func anthropicOAuthCredentialRejected(err error) bool {
	var buffered *providers.UpstreamErrorResponse
	if !errors.As(err, &buffered) {
		return false
	}
	if buffered.Status != http.StatusUnauthorized && buffered.Status != http.StatusForbidden {
		return false
	}
	var env struct {
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	if jsonErr := json.Unmarshal(buffered.Body, &env); jsonErr != nil {
		return false
	}
	return env.Error.Type == "authentication_error" || env.Error.Type == "permission_error"
}

const topUpURL = "https://app.weaveos.com/organization/settings/weave-router/credits"

func routerNoticeForRequest(ctx context.Context, headers http.Header, source subscriptions.Provider, model, marker string) translate.RouterNotice {
	kind := translate.NoticeModelSelection
	reason, _ := billing.SubscriptionOnlyReasonFromContext(ctx)
	warning := subscriptionFundingWarning{reason: reason, source: source}
	if fundedByCustomerKey(ctx, model) {
		warning = subscriptionFundingWarning{reason: reason, customerKey: true}
	}
	echoed, _ := ctx.Value(subscriptionOnlyWarningsEchoedContextKey{}).(map[subscriptionFundingWarning]bool)
	if text := warning.text(); text != "" && !echoed[warning] {
		kind = translate.NoticeFundingWarning
		marker = text
	} else if nudge, _ := ctx.Value(subscriptionReconnectNudgeContextKey{}).(string); nudge != "" && !reconnectNudgeEchoed(ctx, nudge) {
		kind = translate.NoticeSubscriptionReconnect
		marker = nudge
	}
	return translate.NewRouterNotice(kind, suppressMarkerIfRequested(ctx, headers, marker), model, "")
}

type subscriptionOnlyWarningsEchoedContextKey struct{}

const SubscriptionsDashboardURL = "https://app.weaveos.com/organization/settings/weave-router/subscriptions"

const ownCodexPlanSpentNudge = routingMarkerPrefix +
	"your own ChatGPT (Codex) plan is out of usage for now. Connect or reconnect a Codex seat to keep this on a subscription: " +
	SubscriptionsDashboardURL + "?reconnect=codex\n\n"

const ownClaudePlanSpentNudge = routingMarkerPrefix +
	"your own Claude plan is out of usage for now. Connect or reconnect a Claude seat to keep this on a subscription: " +
	SubscriptionsDashboardURL + "?reconnect=claude\n\n"

var subscriptionReconnectNudges = []string{ownCodexPlanSpentNudge, ownCodexCredentialRejectedNudge, ownClaudePlanSpentNudge, ownClaudeCredentialRejectedNudge}

type subscriptionReconnectNudgeContextKey struct{}

type subscriptionReconnectNudgesEchoedContextKey struct{}

func withSubscriptionReconnectNudge(ctx context.Context, text string) context.Context {
	return context.WithValue(ctx, subscriptionReconnectNudgeContextKey{}, text)
}

type deferredReconnectNudgeContextKey struct{}

func withDeferredReconnectNudge(ctx context.Context, text string) context.Context {
	return context.WithValue(ctx, deferredReconnectNudgeContextKey{}, text)
}

func reconnectNoticeForAttempt(ctx context.Context, headers http.Header, notice translate.RouterNotice) translate.RouterNotice {
	nudge, _ := ctx.Value(deferredReconnectNudgeContextKey{}).(string)
	if nudge == "" || notice.Kind != translate.NoticeModelSelection || reconnectNudgeEchoed(ctx, nudge) {
		return notice
	}
	if creds := CredentialsFromContext(ctx); creds != nil && creds.OAuth {
		return notice
	}
	return translate.NewRouterNotice(translate.NoticeSubscriptionReconnect, suppressMarkerIfRequested(ctx, headers, nudge), notice.Model, notice.AttemptID)
}

func reconnectNudgeEchoed(ctx context.Context, text string) bool {
	echoed, _ := ctx.Value(subscriptionReconnectNudgesEchoedContextKey{}).(map[string]bool)
	return echoed[text]
}

func withSubscriptionOnlyWarningEcho(ctx context.Context, body []byte) context.Context {
	texts := make([]string, 0, len(subscriptionFundingWarnings))
	for _, warning := range subscriptionFundingWarnings {
		texts = append(texts, warning.text())
	}
	seen := translate.EchoedInAssistantText(body, texts)
	echoed := make(map[subscriptionFundingWarning]bool, len(subscriptionFundingWarnings))
	prior, _ := ctx.Value(subscriptionOnlyWarningsEchoedContextKey{}).(map[subscriptionFundingWarning]bool)
	for _, warning := range subscriptionFundingWarnings {
		echoed[warning] = seen[warning.text()] || prior[warning]
	}
	ctx = context.WithValue(ctx, subscriptionOnlyWarningsEchoedContextKey{}, echoed)
	nudgesSeen := translate.EchoedInAssistantText(body, subscriptionReconnectNudges)
	priorNudges, _ := ctx.Value(subscriptionReconnectNudgesEchoedContextKey{}).(map[string]bool)
	nudgesEchoed := make(map[string]bool, len(subscriptionReconnectNudges))
	for _, nudge := range subscriptionReconnectNudges {
		nudgesEchoed[nudge] = nudgesSeen[nudge] || priorNudges[nudge]
	}
	return context.WithValue(ctx, subscriptionReconnectNudgesEchoedContextKey{}, nudgesEchoed)
}

var ErrCreditsExhaustedSubscriptionUnavailable = errors.New("credits exhausted and subscription unavailable for this turn")

var ErrSpendCapSubscriptionUnavailable = fmt.Errorf("spend cap reached: %w", ErrCreditsExhaustedSubscriptionUnavailable)

type IncludedAllowanceSpentError struct {
	Window  entitlement.SpentWindow
	orgGate error
}

func (e *IncludedAllowanceSpentError) Error() string {
	return fmt.Sprintf("%s %s allowance spent: %v", e.Window.Plan, e.Window.Kind, e.orgGate)
}

func (e *IncludedAllowanceSpentError) Unwrap() error { return e.orgGate }

var errBypassRetryable = errors.New("usage bypass: retryable error, fall back to routed dispatch")

func (s *Service) bypassToAnthropic(
	ctx context.Context,
	env *translate.RequestEnvelope,
	feats translate.RoutingFeatures,
	modelSwitched bool,
	requestStart time.Time,
	requestID, externalID string,
	turnType turntype.TurnType,
	reason string,
	r *http.Request,
	w http.ResponseWriter,
) error {
	log := observability.FromContext(ctx)
	decision := router.Decision{
		Provider: providers.ProviderAnthropic,
		Model:    feats.Model,
		Reason:   reason,
	}
	if err := productEligibilityRefusal(ctx, entitlement.ModelBoundaryFromContext(ctx), decision.Model); err != nil {
		return err
	}
	w.Header().Set(requestcontext.HeaderRouterDecision, decision.Reason)
	w.Header().Set(requestcontext.HeaderRouterProvider, decision.Provider)
	w.Header().Set(requestcontext.HeaderRouterModel, decision.Model)
	w.Header().Set(requestcontext.HeaderRouterContextWindow, strconv.Itoa(contextWindowForRequest(decision.Model, decision.Provider)))
	setContextEstimateHeaders(w.Header(), env.ContextOverflowTokenEstimate(), max(contextWindowOutputReserve, feats.MaxTokens))

	p, provErr := s.clients.Upstream(providers.ProviderAnthropic)
	if provErr != nil {
		return provErr
	}

	ctx = s.resolveCredentials(ctx, decision.Provider, decision.Model, r.Header)
	if claudeEnterpriseOnly(ctx) && !servedOnSubscription(ctx) {
		return claudeEnterpriseRefusal(ctx)
	}

	outputReserve := max(contextWindowOutputReserve, feats.MaxTokens)
	opts := translate.EmitOptions{
		TargetModel:              decision.Model,
		TargetProvider:           decision.Provider,
		Capabilities:             router.Lookup(decision.Model),
		IncludeStreamUsage:       s.usageRequired(),
		EnableExtendedContext:    shouldEnableExtendedContext(env.FullTokenEstimate(), outputReserve),
		EnableServerSideFallback: s.ResolveAnthropicServerSideFallback(ctx),
		ModelSwitched:            modelSwitched,
		FastMode:                 fastModeForAttempt(ctx, decision.Model, decision.Provider),
	}
	prep, emitErr := prepareAnthropicForTurn(env, r.Header, opts, turnType)
	if emitErr != nil {
		log.Error("Failed to emit Anthropic body on usage-bypass path", "err", emitErr)
		return fmt.Errorf("emit bypass body: %w", emitErr)
	}
	w, flushBuffered := bufferNonStreamBypassResponse(w, env.Stream(), log)
	defer flushBuffered()

	respW := http.ResponseWriter(w)
	var streamCost *streamCostWriter
	if env.Stream() {
		streamCost = newStreamCostWriter(respW)
		streamCost.SetCostCalculator(routerCostCalculatorFor(decision.Model, decision.Provider, opts.FastMode), false, func() bool { return translate.AnthropicRequestCacheTTL1h(prep.Body) })
		respW = streamCost
	}
	preludeBuf := newPreludeBuffer(respW)
	notices := &requestNoticeStream{now: s.clock.Now}
	notice := routerNoticeForRequest(ctx, r.Header, subscriptions.ProviderClaude, decision.Model, "")
	respW, noticeErr := notices.prepare(ctx, preludeBuf, preludeBuf, translate.FormatAnthropic, env.Stream(), notice)
	if noticeErr != nil {
		return fmt.Errorf("emit bypass funding notice: %w", noticeErr)
	}

	var extractor *telemetry.UsageExtractor
	if s.usageRequired() || streamCost != nil {
		extractor, respW = s.newUsageExtractor(respW, decision.Provider)
		extractor.SetRequestCacheTTL1h(func() bool { return translate.AnthropicRequestCacheTTL1h(prep.Body) })
	}

	if streamCost != nil {
		streamCost.source = extractor.UsageFacts
	}

	var terminal *streamTerminalObserver
	if env.Stream() {
		terminal = newStreamTerminalObserver(respW, nativeStreamAnthropic)
		respW = terminal
	}
	preludeBuf.Seal()

	proxyStart := s.clock.Now()
	inferenceParentCtx := ctx
	ctx, inferenceSpan := startInferenceSpan(ctx, decision, clientSessionIDForRequest(ctx, env))
	passiveRefusal := newPassiveRefusalObserver(respW)
	toolNames := translate.NewAnthropicToolNameWriter(passiveRefusal, prep.ResponseToolNames)
	decision, proxyErr := proxyClassifierWithRefusalRescue(ctx, func(d router.Decision, prep providers.PreparedRequest, out http.ResponseWriter) error {
		return p.Proxy(ctx, d, prep, out, r)
	}, decision, prep, newClassifierRefusalRescue(ctx, newClassifierRescuePolicy(ctx, s), turnType, decision, env, r.Header, opts), toolNames)
	proxyErr = finalizeAfterProxy(proxyErr, toolNames.Finalize)
	if proxyErr == nil && terminal != nil {
		proxyErr = terminal.streamErr()
	}
	finishInferenceSpan(inferenceSpan, decision, decision.Provider, 0, proxyErr)
	ctx = restoreParentSpan(ctx, inferenceParentCtx)
	var upstreamErr *providers.UpstreamErrorResponse
	recordSubscriptionModelOutcome(s, ctx, decision.Provider, decision.Model, proxyErr)
	if proxyErr != nil {
		preludeBuf.Discard()
	}
	if env.Stream() && proxyErr != nil && preludeBuf.Committed() {
		proxyErr = emitAnthropicSSEErrorEvent(w, proxyErr)
	} else if !preludeBuf.Committed() && providers.IsRetryable(proxyErr) && bypassMayReroute(ctx, proxyErr) {
		return errBypassRetryable
	}
	if !claudeEnterpriseOnly(ctx) && !preludeBuf.Committed() && (anthropicSubscriptionModelRejected(proxyErr) || anthropicSubscriptionThirdPartyRefused(proxyErr)) && s.anthropicFallbackKeyAvailable(ctx) &&
		(!billing.SubscriptionOnlyFromContext(ctx) || linkedFirst(ctx)) {
		return errBypassRetryable
	}
	if !preludeBuf.Committed() && errors.As(proxyErr, &upstreamErr) {
		flushUpstreamErrorAsAnthropic(w, proxyErr)
		proxyErr = nil
	}
	in, out := extractor.Tokens()
	cacheCreation, cacheRead := extractor.CacheTokens()
	usageMods := extractor.UsageModifiers()
	pricing, _ := servedPricing(decision.Provider, decision.Model, opts.FastMode || extractor.Speed() == catalog.SpeedFast)
	if !env.Stream() && proxyErr == nil {
		setRouterCostHeaders(w.Header(), routerResponseCostFromPricing(pricing, decision.Provider, in, out, cacheCreation, cacheRead, usageMods))
	}
	inputCost := catalog.EffectiveInputCost(in, cacheCreation, cacheRead, pricing, decision.Provider, usageMods)
	outputCost := catalog.EffectiveOutputCost(catalog.PromptTokens(in, cacheCreation, cacheRead, decision.Provider), out, pricing, usageMods)

	clientID := ClientIdentityFrom(ctx)
	stopReason := passiveRefusal.stopReason(extractor)
	outcomeErr := proxyErr
	if upstreamErr != nil {
		outcomeErr = upstreamErr
	}
	bypassBuilder := telemetry.NewAttrBuilder(18).
		String("request_id", requestID).
		String("external_id", externalID).
		String("router_user_id", auth.UserIDFrom(ctx)).
		String("client.app", clientID.TelemetryClientApp()).
		String("client.session_id", clientID.SessionID).
		String("requested.model", feats.Model).
		String("decision.model", decision.Model).
		String("decision.provider", decision.Provider).
		String("decision.reason", decision.Reason).
		String("upstream.stop_reason", stopReason).Bool("upstream.refusal", passiveRefusal.scan.refused).String("upstream.refusal_category", passiveRefusal.scan.category).
		Bool("cost.subscription_served", proxyErr == nil && upstreamErr == nil && s.costNeutralSubscriptionServed(ctx)).
		Int64("usage.input_tokens", int64(in)).
		Int64("usage.output_tokens", int64(out)).
		Int64("usage.cache_creation_input_tokens", int64(cacheCreation)).
		Int64("usage.cache_read_input_tokens", int64(cacheRead)).
		Int64("usage.cache_creation_1h_input_tokens", int64(usageMods.CacheCreation1h)).
		String("usage.speed", string(extractor.Speed())).
		String("usage.inference_geo", string(extractor.InferenceGeo())).
		Float64("cost.requested_input_usd", inputCost).
		Float64("cost.requested_output_usd", outputCost).
		Float64("cost.actual_input_usd", inputCost).
		Float64("cost.actual_output_usd", outputCost).Int64("upstream.status_code", int64(upstreamStatus(outcomeErr)))
	s.applySubscriptionSpanTelemetry(ctx, bypassBuilder, feats.Model, decision.Model)
	telemetry.Record(ctx, telemetry.Span{
		Name:  "router.usage_bypass",
		Start: requestStart,
		End:   s.clock.Now(),
		Attrs: bypassBuilder.Build(),
	})
	telemetry.Flush(ctx)

	if installationID := installationIDFromContext(ctx); installationID != uuid.Nil {
		userPrompt := env.EndsWithUserPrompt()
		credentialKeyPrefix, credentialKeySuffix, credSource := s.credentialKeyParts(ctx)
		s.markServedExternalKeyUsed(ctx, outcomeErr)
		sessionKey := deriveSessionKeyForRequest(ctx, env, apiKeyIDFromContext(ctx))
		telemetryParams := turnrecord.InsertTelemetryParams{
			InstallationID:         installationID.String(),
			APIKeyID:               apiKeyIDFromContext(ctx),
			RequestID:              requestID,
			SpanType:               "router.upstream",
			TraceID:                requestID,
			Timestamp:              requestStart,
			RequestedModel:         feats.Model,
			DecisionModel:          decision.Model,
			DecisionProvider:       decision.Provider,
			DecisionReason:         telemetryDecisionReason(ctx, decision.Reason),
			RequestedAllowedModels: requestedAllowedModelsForTelemetry(ctx),
			EstimatedInputTokens:   int32(feats.Tokens),
			ContextSnapshot:        contextSnapshotJSON(w.Header(), requestID, feats.Model, requestStart, s.clock.Now()),
			InputTokens:            int32(in),
			OutputTokens:           int32(out),
			RequestedInputCostUSD:  inputCost,
			RequestedOutputCostUSD: outputCost,
			ActualInputCostUSD:     inputCost,
			ActualOutputCostUSD:    outputCost,
			UpstreamLatencyMs:      s.clock.Now().Sub(proxyStart).Milliseconds(),
			TotalLatencyMs:         s.clock.Now().Sub(requestStart).Milliseconds(),
			UpstreamStatusCode:     int32(upstreamStatus(outcomeErr)),
			UpstreamFinishReason:   stringPtrOrEmpty(stopReason),
			StopReason:             stringPtrOrEmpty(stopReason),
			ErrorClass:             classifyTurnError(outcomeErr, stopReason, 0),
			FailureOwner:           turnFailureOwnerFor(ctx, outcomeErr, classifyTurnError(outcomeErr, stopReason, 0)),
			UserPrompt:             &userPrompt,
			LatestToolCallCounts:   latestToolCallCountsJSON(env),
			SessionKey:             sessionKey[:],
			CaptureMode:            s.effectiveCaptureMode(ctx).String(),
			TurnType:               string(turnType),
			CacheCreationTokens:    cacheTokenPtr(cacheCreation),
			CacheCreation1hTokens:  cacheCreation1hPtr(cacheCreation, usageMods.CacheCreation1h),
			CacheReadTokens:        cacheTokenPtr(cacheRead),
			Speed:                  string(extractor.Speed()),
			InferenceGeo:           string(extractor.InferenceGeo()),
			ReasoningTokens:        cacheTokenPtr(extractor.ReasoningTokens()),
			DeviceID:               clientID.DeviceID,
			SessionID:              clientID.SessionID,
			RouterUserID:           auth.UserIDFrom(ctx),
			ClientApp:              clientID.TelemetryClientApp(),
			CredentialKeyPrefix:    credentialKeyPrefix,
			CredentialKeySuffix:    credentialKeySuffix,
			CredentialSource:       credSource,
			UnifiedLimitHeaders:    unifiedLimitHeadersJSON(ctx),
			CodexLimitHeaders:      codexLimitHeadersJSON(ctx),
		}
		applyBlindExperimentTelemetry(ctx, &telemetryParams, &turnLoopResult{Decision: decision, UsageBypass: true})
		applyPolicyPinTelemetry(ctx, &telemetryParams, nil)
		applySubscriberTelemetry(ctx, &telemetryParams)
		applyAgreementShadowTelemetry(s.strategyPorts, ctx, &telemetryParams, turnLoopResult{UsageBypass: true})
		s.fireTelemetry(ctx, telemetryParams)
	}

	servedCredentialSource, servedSubscriptionTier := s.servedCredentialLogValues(ctx)
	log.Info("ProxyMessages usage-bypass complete",
		"request_id", requestID,
		"external_id", externalID,
		"requested_model", feats.Model,
		"decision_model", decision.Model,
		"proxy_ms", s.clock.Now().Sub(proxyStart).Milliseconds(),
		"total_ms", s.clock.Now().Sub(requestStart).Milliseconds(),
		"proxy_err", proxyErr,
		"resp_stop_reason", stopReason, "resp_refusal", passiveRefusal.scan.refused, "resp_refusal_category", passiveRefusal.scan.category,
		"credential_source", servedCredentialSource,
		"subscription_tier", servedSubscriptionTier,
	)
	return proxyErr
}
