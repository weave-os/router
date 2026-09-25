package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/google/uuid"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/observability/otel"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/subscriptions/entitlement"
	"weave-os/router/internal/translate"
)

// Decision reasons for the two strict pass-through lanes. Both dispatch through
// bypassToAnthropic and record a router.usage_bypass span; the reason tells
// them apart in telemetry.
const (
	// reasonUsageBypass: the installation opted in (usage_bypass_enabled) and
	// the caller's subscription has headroom for the requested model.
	reasonUsageBypass = "usage_bypass"
	// reasonClassifierPassthrough: a Claude Code classifier turn arrived with
	// the caller's own Claude subscription credential. No opt-in needed — see
	// classifierPassthroughEngaged.
	reasonClassifierPassthrough = "classifier_subscription_passthrough"
)

// usageBypassDecision returns the strict pass-through decision when either
// subscription lane should engage (classifier passthrough first, then the
// opt-in usage bypass), false otherwise.
//
// sessionDemotedModels are the models this session struck out after a
// committed or rescued stream failure (turnResult.SessionDemotedModels). They
// travel to the scorer inside req.AutomaticExcludedModels, which the bypass
// deliberately ignores because that field is otherwise the deployment-wide
// soft exclusion an explicit subscription request may override. A session
// strike is different: the arm just failed this very user mid-turn, so serving
// it straight through on their plan would replay the failure the strike exists
// to avoid. The set is passed separately so the two stay distinguishable.
func (s *Service) usageBypassDecision(ctx context.Context, headers http.Header, req router.Request, sessionDemotedModels []string, turnType turntype.TurnType) (router.Decision, bool) {
	if slices.Contains(sessionDemotedModels, req.RequestedModel) {
		return router.Decision{}, false
	}
	// The lane serves the requested model verbatim, so a model the product
	// does not sell disqualifies it: the turn falls through to routed
	// dispatch, which picks from the eligible pool instead.
	if !catalog.PermittedBy(req.ProductEligibility, req.RequestedModel) {
		return router.Decision{}, false
	}
	provider, reason, engaged := s.subscriptionPassthroughEngaged(ctx, headers, req, turnType)
	if !engaged {
		return router.Decision{}, false
	}
	return router.Decision{
		Provider: provider,
		Model:    req.RequestedModel,
		Reason:   reason,
	}, true
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

// classifierPassthroughEngaged reports whether a Claude Code classifier turn
// should be served straight through on the caller's own Claude subscription.
// Anthropic bills the Auto-mode security classifier to the plan (free on
// Pro/Max/Team), so for a subscription caller the requested Claude model costs
// them nothing extra, while any model the scorer substitutes is API spend the
// router adds. Engages when the turn is a Classifier, the requested model is
// Anthropic-served and admissible for this request (subscriptionCoveredTarget),
// the request presents a Claude subscription credential, and that credential
// is not observed-exhausted. Unlike usageBypassEngaged it needs neither the
// installation opt-in nor a utilization threshold: the classifier is a
// by-product of the conversation's own turns, so conserving quota by
// re-routing it buys nothing. An exhausted subscription falls through to the
// scorer, which already handles the paid-key fallback and subscription-only
// refusal for that state.
func (s *Service) classifierPassthroughEngaged(ctx context.Context, headers http.Header, req router.Request, turnType turntype.TurnType) (string, bool) {
	if turnType != turntype.Classifier {
		return "", false
	}
	provider, token, covered := subscriptionCoveredTarget(ctx, headers, req)
	if !covered || provider != providers.ProviderAnthropic || s.subscriptionModels.denied([]byte(token), req.RequestedModel, s.clockNow()) {
		return "", false
	}
	if s.usageObserver == nil {
		return provider, true
	}
	snap, observed := s.usageObserver.Snapshot(s.usageObserver.Key([]byte(token)))
	if observed && snap.Exhausted() {
		return "", false
	}
	return provider, true
}

// usageBypassEngaged reports whether the requested model should be served
// straight through to the matching caller subscription instead of routed. It
// engages only when:
//
//   - the installation has turned the gate on (usageBypassFromContext),
//   - the subscription usage observer is wired (it drives the threshold read),
//   - the requested model is covered by the matching Claude or Codex
//     subscription and is neither provider- nor model-excluded for this request
//     (denylist or context-overflow filter),
//   - the request presents that subscription credential (the turn is paid for
//     by the customer's own plan — nothing for the router to save, nothing for
//     us to bill), and
//   - observed utilization is still below the threshold, OR nothing has been
//     observed yet (cold start: serve the first turn on the subscription so its
//     response primes the observer, mirroring the subsidy bootstrap).
//
// Once observed utilization crosses the threshold the gate disengages and the
// normal routing path (scorer + subscription-aware cost discounting) takes over,
// so the caller starts conserving their remaining quota.
func (s *Service) usageBypassEngaged(ctx context.Context, headers http.Header, req router.Request) (string, bool) {
	cfg, ok := usageBypassFromContext(ctx)
	if !ok || s.usageObserver == nil {
		return "", false
	}
	provider, token, covered := subscriptionCoveredTarget(ctx, headers, req)
	if !covered {
		return "", false
	}
	if provider == providers.ProviderAnthropic && s.subscriptionModels.denied([]byte(token), req.RequestedModel, s.clockNow()) {
		return "", false
	}
	threshold := defaultUsageBypassThreshold
	if cfg.Threshold != nil {
		threshold = min(1, max(0, *cfg.Threshold))
	}
	snap, observed := s.usageObserver.Snapshot(s.usageObserver.Key([]byte(token)))
	if !observed {
		return provider, true
	}
	// Never bypass a spent subscription: the upstream will reject the token
	// even if the configured threshold sits above exhaustedFraction.
	if snap.Exhausted() {
		return "", false
	}
	// Subscription-only mode: paid failover is disabled, so the threshold's
	// purpose — disengage the bypass to conserve remaining quota by routing to a
	// cheaper paid model — no longer applies. Serve on the subscription right up
	// to exhaustion instead of gating early.
	if billing.SubscriptionOnlyFromContext(ctx) {
		return provider, true
	}
	util := max(snap.Primary.UsedPercent, snap.Secondary.UsedPercent)
	return provider, util < threshold
}

// subscriptionCoveredTarget resolves the requested model to the provider lane a
// caller subscription could serve it on and the credential that would pay for
// it: Anthropic for a Claude subscription, OpenAI for a Codex subscription
// covering the model. It reports false when the model is unknown, on another
// provider, provider-disabled for this request, safety-excluded, outside the
// allowlist, or when no matching subscription credential is present. Quota
// state is the caller's concern; this is the admissibility check both
// pass-through lanes share.
func subscriptionCoveredTarget(ctx context.Context, headers http.Header, req router.Request) (provider, token string, covered bool) {
	model := req.RequestedModel
	m, found := catalog.ByID(model)
	if !found {
		return "", "", false
	}
	provider = m.PrimaryProvider()
	codexTok, anthroTok := presentSubscriptionTokens(ctx, headers)
	switch provider {
	case providers.ProviderAnthropic:
		token = anthroTok
	case providers.ProviderOpenAI:
		if !codexSubscriptionCoversModel(model) {
			return "", "", false
		}
		token = codexTok
	default:
		return "", "", false
	}
	if req.EnabledProviders != nil {
		if _, enabled := req.EnabledProviders[provider]; !enabled {
			return "", "", false
		}
	}
	// Use SafetyExcludedModels (hard constraints: context-overflow, gemini-unsigned),
	// not ExcludedModels — the installation's excluded_models is a routing preference
	// bypass may override; a model that can't accept the request on any credential cannot.
	if _, excluded := req.SafetyExcludedModels[model]; excluded {
		return "", "", false
	}
	// Allowlist is a compliance boundary, not a routing preference —
	// unlike excluded_models the bypass must honor it. Checked explicitly
	// because SafetyExcludedModels has different readers and semantics.
	if req.AllowedModels != nil {
		if _, allowed := req.AllowedModels[model]; !allowed {
			return "", "", false
		}
	}
	if token == "" {
		return "", "", false
	}
	return provider, token, true
}

// claudeSubscriptionExhausted reports whether the caller's present Claude
// subscription has bound its plan window — the upstream will 429 any further
// turn until it resets. True only when: the usage observer is wired, a Claude
// subscription token is present on this request, its most-recent observed
// snapshot is exhausted, AND a non-subscription Anthropic key exists to serve the
// turn instead. The token key is derived identically to withUsageObserver /
// usageBypassEngaged so this read agrees with what the observer recorded. When
// true the caller suppresses the subscription credential (withSuppressedSubscription)
// so the turn serves on the Weave / BYOK key rather than the spent subscription.
func (s *Service) claudeSubscriptionExhausted(ctx context.Context, headers http.Header) bool {
	return s.anthropicFallbackKeyAvailable(ctx) && s.anthropicSubscriptionObservedExhausted(ctx, headers)
}

// anthropicSubscriptionObservedExhausted reports whether the caller's present
// Claude subscription has bound its plan window per the usage observer,
// independent of whether a fallback key exists. claudeSubscriptionExhausted
// layers the fallback-key requirement on top for its suppress-and-serve-on-Weave
// -key path; subscription-only refusal uses this bare signal because paid
// fallback is disabled there — an exhausted sub can only 429, so the turn is
// refused with the controlled 402 rather than sent on a doomed round-trip.
func (s *Service) anthropicSubscriptionObservedExhausted(ctx context.Context, headers http.Header) bool {
	if s.usageObserver == nil {
		return false
	}
	_, anthroTok := presentSubscriptionTokens(ctx, headers)
	if anthroTok == "" {
		return false
	}
	snap, ok := s.usageObserver.Snapshot(s.usageObserver.Key([]byte(anthroTok)))
	return ok && snap.Exhausted()
}

// anthropicFallbackKeyAvailable reports whether a non-subscription Anthropic
// credential is configured to serve a Claude turn when the caller's subscription
// is spent: a per-request BYOK Anthropic key, or the deployment's own
// ANTHROPIC_API_KEY (tracked in deploymentKeyedProviders). Without one, dropping
// the subscription token would leave the turn with no Anthropic credential and
// 400 — strictly worse than the 429 — so the caller keeps using the subscription.
func (s *Service) anthropicFallbackKeyAvailable(ctx context.Context) bool {
	if byok := BuildCredentialsMap(externalKeysFromContext(ctx)); byok != nil {
		if _, ok := byok[providers.ProviderAnthropic]; ok {
			return true
		}
	}
	if s.deploymentKeyedProviders != nil {
		if _, ok := s.deploymentKeyedProviders[providers.ProviderAnthropic]; ok {
			return true
		}
	}
	return false
}

// anthropicOAuthCredentialRejected reports whether err is a buffered Anthropic
// 401 authentication_error or 403 permission_error — a rejected subscription
// OAuth token that gates the failover onto the BYOK/deployment key. Narrow by
// design so an unrelated 403 (e.g. content policy) stays terminal.
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

// topUpURL is the customer-facing page where org admins buy router credits.
// Duplicated from middleware.TopUpURL (proxy can't import the middleware
// adapter without an import cycle) so subscription-only warnings and the
// credits-exhausted 402 can surface the CTA.
const topUpURL = "https://app.workweave.ai/organization/settings/weave-router"

// subscriptionOnlyWarningMarker is prepended to a subscription-only bypass
// response so the customer sees why they're being served on their own plan and
// how to restore full routing. The marker is emitted only when terminal
// surfaces are enabled for the installation and request.
const subscriptionOnlyWarningMarker = routingMarkerPrefix +
	"your Weave router credits are depleted, so this turn is running on your own Anthropic subscription and paid model fallback is disabled. Add credits to restore full routing: " +
	topUpURL + "\n\n"

// subscriptionOnlyWarningMarkerCodex is the Codex/OpenAI-surface counterpart to
// subscriptionOnlyWarningMarker, prepended to a subscription-only turn served on
// the caller's own ChatGPT (Codex) subscription.
const subscriptionOnlyWarningMarkerCodex = routingMarkerPrefix +
	"your Weave router credits are depleted, so this turn is running on your own ChatGPT (Codex) subscription and paid model fallback is disabled. Add credits to restore full routing: " +
	topUpURL + "\n\n"

// subscriptionOnlyWarnsDepleted reports whether a subscription-only turn is
// standing in for capacity the organization could not fund, and so must carry
// the depleted-credits warning and its top-up CTA. A linked-first turn is the
// ordinary funded path — the caller's own plan paying first by design — and
// keeps its routing marker, so it no longer claims credits are gone.
func subscriptionOnlyWarnsDepleted(ctx context.Context) bool {
	reason, ok := billing.SubscriptionOnlyReasonFromContext(ctx)
	return ok && reason == billing.SubscriptionOnlyCreditsDepleted
}

// subscriptionOnlyWarningMarkerForRequest returns the depletion warning only
// when the turn is subscription-only because credits are depleted and the
// caller has not opted out of terminal routing surfaces.
func subscriptionOnlyWarningMarkerForRequest(ctx context.Context, headers http.Header, marker string) string {
	if !subscriptionOnlyWarnsDepleted(ctx) {
		return ""
	}
	return suppressMarkerIfRequested(ctx, headers, marker)
}

// ErrCreditsExhaustedSubscriptionUnavailable is returned by ProxyMessages and
// ProxyOpenAIChatCompletion when the org is in subscription-only mode but the
// turn cannot be served on the caller's own
// subscription (Claude or Codex) at all — routing resolved to a paid model, or
// (Anthropic bypass) the subscription is already rate-limit exhausted. Paid
// failover is disabled in this mode, so the turn is refused (HTTP 402) rather
// than billed against an already-negative balance. A runtime failure on a turn
// that DID resolve onto the subscription surfaces the raw upstream error
// instead — it's the caller's own plan failing, with nowhere to fail over to.
var ErrCreditsExhaustedSubscriptionUnavailable = errors.New("credits exhausted and subscription unavailable for this turn")

// errBypassRetryable is returned by bypassToAnthropic when the bypass attempt
// hit a retryable upstream error (e.g., Anthropic 429 weekly-limit) BEFORE
// writing any response bytes. The caller should fall through to the normal
// routed dispatch path so the turn can still be served by a different provider.
var errBypassRetryable = errors.New("usage bypass: retryable error, fall back to routed dispatch")

// bypassToAnthropic proxies an inbound Anthropic-Messages request straight to
// the Anthropic provider with the caller-requested model. It deliberately skips
// the cluster scorer, planner, session pin, semantic cache, AND billing: the
// turn runs on the customer's own subscription quota, so there is no model
// substitution to make and no usage to charge. The Anthropic adapter still
// observes the response's rate-limit headers (via the ctx observer installed by
// withUsageObserver), keeping the gate primed for the next request.
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
	// This lane skips routing entirely, so it needs its own product gate: a
	// plan that does not sell this model must not serve it even on the
	// caller's own subscription quota.
	if err := catalog.CheckEligibility(entitlement.ModelBoundaryFromContext(ctx), decision.Model); err != nil {
		return err
	}
	w.Header().Set(HeaderRouterDecision, decision.Reason)
	w.Header().Set(HeaderRouterProvider, decision.Provider)
	w.Header().Set(HeaderRouterModel, decision.Model)
	w.Header().Set(HeaderRouterContextWindow, strconv.Itoa(contextWindowForRequest(decision.Model, decision.Provider)))

	p, provErr := s.provider(providers.ProviderAnthropic)
	if provErr != nil {
		return provErr
	}

	// Resolve credentials onto ctx so the Anthropic adapter's setAuth picks up
	// the subscription (or BYOK / client) credential exactly as a routed turn
	// would, and so servedOnSubscription / the usage observer key off the same
	// token the upstream call sends.
	ctx = s.resolveCredentials(ctx, decision.Provider, decision.Model, r.Header)

	outputReserve := contextWindowOutputReserve
	if feats.MaxTokens > outputReserve {
		outputReserve = feats.MaxTokens
	}
	opts := translate.EmitOptions{
		TargetModel:              decision.Model,
		TargetProvider:           decision.Provider,
		Capabilities:             router.Lookup(decision.Model),
		IncludeStreamUsage:       s.usageRequired(),
		EnableExtendedContext:    shouldEnableExtendedContext(env.FullTokenEstimate(), outputReserve),
		EnableServerSideFallback: s.ResolveAnthropicServerSideFallback(ctx),
		// When the session previously served a different model, strip thinking
		// blocks whose signatures the requested model would reject (else
		// Anthropic 400s on the stale signature).
		ModelSwitched: modelSwitched,
		FastMode:      fastModeForAttempt(ctx, decision.Model, decision.Provider),
	}
	prep, emitErr := env.PrepareAnthropic(r.Header, opts)
	if emitErr != nil {
		log.Error("Failed to emit Anthropic body on usage-bypass path", "err", emitErr)
		return fmt.Errorf("emit bypass body: %w", emitErr)
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

	// Subscription-only mode: prepend a warning
	// text block so the customer sees they're on their own subscription with
	// paid failover disabled, and how to restore full routing. The marker writer
	// injects only on a streaming response and is transparent otherwise.
	respW := http.ResponseWriter(w)
	var streamCost *streamCostWriter
	if env.Stream() {
		streamCost = newStreamCostWriter(respW)
		streamCost.SetCostCalculator(routerCostCalculatorFor(decision.Model, decision.Provider, opts.FastMode), false)
		respW = streamCost
	}
	if warning := subscriptionOnlyWarningMarkerForRequest(ctx, r.Header, subscriptionOnlyWarningMarker); warning != "" {
		respW = translate.NewAnthropicRoutingMarkerWriter(respW, decision.Model, warning)
	}

	// Tap the response stream so the bypass span carries token usage for
	// Weave's router cost-savings metric — subscription turns are otherwise invisible.
	var extractor *otel.UsageExtractor
	if s.usageRequired() {
		extractor = otel.NewUsageExtractor(respW, decision.Provider)
		respW = extractor
	}

	proxyStart := time.Now()
	inferenceParentCtx := ctx
	ctx, inferenceSpan := startInferenceSpan(ctx, decision, clientSessionIDForRequest(ctx, env))
	proxyErr := p.Proxy(ctx, decision, prep, respW, r)
	finishInferenceSpan(inferenceSpan, decision, decision.Provider, 0, proxyErr)
	ctx = restoreParentSpan(ctx, inferenceParentCtx)
	// The Anthropic adapter returns a buffered *UpstreamErrorResponse on 4xx/5xx
	// without writing to w (the routed path flushes it via dispatchWithFallback).
	// When the proxy error is retryable (429 weekly-limit, or a raw transport
	// error like a connection reset / TLS timeout) AND no bytes have been
	// committed to w, return errBypassRetryable so the caller falls through to
	// the normal routed dispatch. Non-retryable *UpstreamErrorResponse values
	// (400/401/403) still flush — those won't be fixed by a different upstream.
	// Local prep errors (provider-not-configured, emit-body) are returned
	// directly so the client sees the real failure instead of a silent reroute.
	var upstreamErr *providers.UpstreamErrorResponse
	s.recordSubscriptionModelRejection(ctx, decision.Provider, decision.Model, proxyErr)
	if providers.IsRetryable(proxyErr) {
		return errBypassRetryable
	}
	if anthropicSubscriptionModelRejected(proxyErr) && s.anthropicFallbackKeyAvailable(ctx) && !billing.SubscriptionOnlyFromContext(ctx) {
		return errBypassRetryable
	}
	if errors.As(proxyErr, &upstreamErr) {
		flushUpstreamErrorAsAnthropic(w, proxyErr)
		proxyErr = nil
	}
	// Bypass never substitutes the model, so requested == actual; Weave credits
	// actual to $0 downstream when cost.subscription_served is set.
	in, out := extractor.Tokens()
	cacheCreation, cacheRead := extractor.CacheTokens()
	pricing, _ := servedPricing(decision.Provider, decision.Model, opts.FastMode)
	if !env.Stream() && proxyErr == nil {
		setRouterCostHeaders(w.Header(), routerResponseCostFromPricing(pricing, decision.Provider, in, out, cacheCreation, cacheRead))
	}
	inputCost := catalog.EffectiveInputCost(in, cacheCreation, cacheRead, pricing, decision.Provider)
	outputCost := catalog.EffectiveOutputCost(in, out, pricing)

	// Same identity block as the routed upstream span so Weave groups bypass turns by user/session.
	clientID := ClientIdentityFrom(ctx)
	otel.Record(ctx, otel.Span{
		Name:  "router.usage_bypass",
		Start: requestStart,
		End:   time.Now(),
		Attrs: otel.NewAttrBuilder(18).
			String("request_id", requestID).
			String("external_id", externalID).
			String("router_user_id", auth.UserIDFrom(ctx)).
			String("client.app", clientID.TelemetryClientApp()).
			String("client.session_id", clientID.SessionID).
			// Bypass never substitutes, so requested model IS the served model.
			String("requested.model", decision.Model).
			String("decision.model", decision.Model).
			String("decision.provider", decision.Provider).
			String("decision.reason", decision.Reason).
			Bool("cost.subscription_served", servedOnSubscription(ctx)).
			Int64("usage.input_tokens", int64(in)).
			Int64("usage.output_tokens", int64(out)).
			Int64("usage.cache_creation_input_tokens", int64(cacheCreation)).
			Int64("usage.cache_read_input_tokens", int64(cacheRead)).
			Float64("cost.requested_input_usd", inputCost).
			Float64("cost.requested_output_usd", outputCost).
			Float64("cost.actual_input_usd", inputCost).
			Float64("cost.actual_output_usd", outputCost).
			Build(),
	})
	otel.Flush(ctx)

	// Persist a router.upstream telemetry row so bypass turns appear in the
	// telemetry table. Routing-brain fields stay NULL; decision_reason
	// (usage_bypass / classifier_subscription_passthrough) marks the lane. Required for Phase 0 unified_limit_headers capture.
	if installationID := installationIDFromContext(ctx); installationID != uuid.Nil {
		credentialKeyPrefix, credentialKeySuffix, credSource := s.credentialKeyParts(ctx)
		telemetryParams := InsertTelemetryParams{
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
			InputTokens:            int32(in),
			OutputTokens:           int32(out),
			RequestedInputCostUSD:  inputCost,
			RequestedOutputCostUSD: outputCost,
			ActualInputCostUSD:     inputCost,
			ActualOutputCostUSD:    outputCost,
			UpstreamLatencyMs:      time.Since(proxyStart).Milliseconds(),
			TotalLatencyMs:         time.Since(requestStart).Milliseconds(),
			UpstreamStatusCode:     int32(upstreamStatus(proxyErr)),
			CaptureMode:            s.effectiveCaptureMode(ctx).String(),
			TurnType:               string(turnType),
			CacheCreationTokens:    cacheTokenPtr(cacheCreation),
			CacheReadTokens:        cacheTokenPtr(cacheRead),
			DeviceID:               clientID.DeviceID,
			SessionID:              clientID.SessionID,
			RouterUserID:           auth.UserIDFrom(ctx),
			ClientApp:              clientID.TelemetryClientApp(),
			CredentialKeyPrefix:    credentialKeyPrefix,
			CredentialKeySuffix:    credentialKeySuffix,
			CredentialSource:       credSource,
			UnifiedLimitHeaders:    unifiedLimitHeadersJSON(ctx),
		}
		applyBlindExperimentTelemetry(ctx, &telemetryParams, &turnLoopResult{Decision: decision, UsageBypass: true})
		applyPolicyPinTelemetry(ctx, &telemetryParams, nil)
		s.fireTelemetry(telemetryParams)
	}

	log.Info("ProxyMessages usage-bypass complete",
		"request_id", requestID,
		"external_id", externalID,
		"requested_model", feats.Model,
		"decision_model", decision.Model,
		"proxy_ms", time.Since(proxyStart).Milliseconds(),
		"total_ms", time.Since(requestStart).Milliseconds(),
		"proxy_err", proxyErr,
	)
	return proxyErr
}
