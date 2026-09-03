package proxy

import (
	"context"
	"net/http"

	"workweave/router/internal/observability"
	"workweave/router/internal/providers"
	"workweave/router/internal/proxy/usage"
	"workweave/router/internal/router/catalog"
	"workweave/router/internal/subscriptions"
)

// Inference route paths (gin FullPath templates) whose upstream cost a caller
// subscription can cover: the Anthropic Messages API is served by a Claude
// subscription, the OpenAI chat/responses APIs by a Codex subscription. Other
// gated routes (Gemini /v1beta, /v1/route) have no consumer subscription.
const (
	routePathMessages        = "/v1/messages"
	routePathChatCompletions = "/v1/chat/completions"
	routePathResponses       = "/v1/responses"
)

// codexCoveredModels is the fail-closed set of models the Codex CLI may serve
// through the caller's ChatGPT OAuth credential. Deliberately a curated
// allowlist, not "every OpenAI model": infrastructure-served OpenAI models
// share ProviderOpenAI with the native Codex family, but must use BYOK or the
// router deployment credential instead of chatgpt.com/backend-api/codex.
var codexCoveredModels = []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"}

// CodexSubscriptionCoversModel reports whether model may receive the caller's
// ChatGPT OAuth credential. Exact canonical IDs only; aliases are resolved
// before routing, and unknown/future models fail closed.
func CodexSubscriptionCoversModel(model string) bool {
	for _, covered := range codexCoveredModels {
		if model == covered {
			return true
		}
	}
	return false
}

func codexSubscriptionCoversModel(model string) bool {
	return CodexSubscriptionCoversModel(model)
}

// claudeCoveredModels returns the catalog models a Claude (Pro/Max) subscription
// covers — every Anthropic-primary model. Derived from the catalog so it tracks
// model additions without a second source of truth.
func claudeCoveredModels() []string {
	out := make([]string, 0, 8)
	for _, m := range catalog.Models {
		if m.PrimaryProvider() == providers.ProviderAnthropic {
			out = append(out, m.ID)
		}
	}
	return out
}

// WithSubscriptionAwareRouting turns on subscription-aware cost discounting.
// The composition root calls it (from ROUTER_SUBSCRIPTION_AWARE_ROUTING) with a
// constructed usage.Observer and the epsilon/gamma knobs. Left unset, the
// feature is fully off: no header observer is installed and no cost factors are
// computed, so routing is byte-for-byte today's behavior.
func (s *Service) WithSubscriptionAwareRouting(obs *usage.Observer, epsilon, gamma float64) *Service {
	s.usageObserver = obs
	s.subsidyEnabled = true
	s.subsidyEpsilon = epsilon
	s.subsidyGamma = gamma
	return s
}

// WithUsageObserver wires the subscription rate-limit observer WITHOUT enabling
// the cost discount. The composition root calls this so the per-installation
// usage-bypass gate works even when ROUTER_SUBSCRIPTION_AWARE_ROUTING is off;
// WithSubscriptionAwareRouting additionally turns on the discount.
func (s *Service) WithUsageObserver(obs *usage.Observer) *Service {
	s.usageObserver = obs
	return s
}

// presentSubscriptionTokens returns the caller's Codex and Claude subscription
// tokens, "" when absent. It sources each from BOTH the dedicated
// X-Weave-*-Subscription headers (the opencode dual-sub path) AND the inbound
// Authorization bearer (Claude Code's sk-ant-oat… / Codex CLI's JWT+account-id
// on their native harnesses) — mirroring resolveAndInjectCredentials so the
// subsidy works for all three harnesses, not just opencode. The token doubles as
// the usage-observer key, and equals the eventually-resolved credential, so
// record and read agree regardless of source.
//
// A free function (not a *Service method) because it reads only ctx + headers:
// the prepaid balance gate (server/middleware) has no Service handle but must
// agree with this path on what counts as "a subscription is present".
func presentSubscriptionTokens(ctx context.Context, headers http.Header) (codex, anthropic string) {
	// Subscription routing disabled: treat as absent so subsidy, usage-bypass, and
	// balance gate all agree the turn is prepaid.
	if subscriptionRoutingDisabledForRequest(ctx) {
		return "", ""
	}
	if codexSubscriptionFromContext(ctx) != nil {
		codex = openaiSubscriptionFromContext(ctx)
	} else if c := ExtractClientCredentials(providers.ProviderOpenAI, headers); c != nil && c.OAuth {
		codex = string(c.APIKey)
	}
	// Validate the dedicated Anthropic header via subscriptionCredsFromHeaderValue
	// (requires sk-ant-oat), NOT a bare non-empty check: a junk header is never
	// injected as a subscription, so treating it as present would let the balance
	// gate exempt a turn that then routes to a paid model.
	if creds := subscriptionCredsFromHeaderValue(anthropicSubscriptionFromContext(ctx)); creds != nil {
		anthropic = string(creds.APIKey)
	} else if c := ExtractClientCredentials(providers.ProviderAnthropic, headers); c != nil && c.OAuth {
		anthropic = string(c.APIKey)
	}
	return codex, anthropic
}

// subscriptionServableProviders returns the provider lanes the caller's own
// subscription can enter: OpenAI for a Codex (ChatGPT) sub, Anthropic for a
// Claude sub. Codex model coverage is narrower than its provider lane and is
// applied by excludeCodexOAuthOnlyModels before routing.
func subscriptionServableProviders(ctx context.Context, headers http.Header) map[string]struct{} {
	codex, anthropic := presentSubscriptionTokens(ctx, headers)
	out := make(map[string]struct{}, 2)
	if codex != "" || managedSubscriptionEnrolled(ctx, subscriptions.ProviderCodex) {
		out[providers.ProviderOpenAI] = struct{}{}
	}
	if anthropic != "" || managedSubscriptionEnrolled(ctx, subscriptions.ProviderClaude) {
		out[providers.ProviderAnthropic] = struct{}{}
	}
	return out
}

// restrictToSubscriptionProviders intersects enabled with the providers the
// caller's subscription can serve, so subscription-only mode routes only to the
// caller's own plan and never a paid model.
// Returns enabled unchanged when the caller presents no usable subscription
// (defensive: the balance gate only flags subscription-only when a covering sub
// is present, so this keeps a misconfiguration from emptying the eligible set).
func restrictToSubscriptionProviders(ctx context.Context, headers http.Header, enabled map[string]struct{}) map[string]struct{} {
	sub := subscriptionServableProviders(ctx, headers)
	if len(sub) == 0 {
		return enabled
	}
	out := make(map[string]struct{}, len(sub))
	for p := range enabled {
		if _, ok := sub[p]; ok {
			out[p] = struct{}{}
		}
	}
	return out
}

// RequestPresentsCoveringSubscription reports whether the request carries a
// validated subscription credential that can serve at least one model on
// routePath: a Claude (sk-ant-oat…) sub for /v1/messages, a Codex sub for
// /v1/chat/completions and /v1/responses. Codex's exact model coverage is
// applied downstream before selection. Any other route returns false.
//
// Scoped to the covering family (not "any subscription present") so a Codex
// bearer on /v1/messages — which can't serve that route — doesn't exempt a
// turn that would debit the prepaid balance.
func RequestPresentsCoveringSubscription(ctx context.Context, headers http.Header, routePath string) bool {
	codex, anthropic := presentSubscriptionTokens(ctx, headers)
	switch routePath {
	case routePathMessages:
		return anthropic != "" || managedSubscriptionEnrolled(ctx, subscriptions.ProviderClaude)
	case routePathChatCompletions, routePathResponses:
		return codex != "" || managedSubscriptionEnrolled(ctx, subscriptions.ProviderCodex)
	default:
		return false
	}
}

// withUsageObserver installs a context header observer that records the present
// subscription's rate-limit headroom and captures raw unified-limit headers for
// Phase 0 telemetry. Both concerns share one observer slot
// (providers.WithUpstreamHeaderObserver holds only one per ctx); the capture is
// wrapped in recover so a Phase 0 bug can never take down the subsidy observer.
// No-op when usageObserver is nil AND no subscription token is present.
func (s *Service) withUsageObserver(ctx context.Context, headers http.Header, routePaths ...string) context.Context {
	routePath := ""
	if len(routePaths) > 0 {
		routePath = routePaths[0]
	}
	ctx = s.withPlanAwareSubscriptionModels(ctx, headers)
	ctx = s.withSubscriptionConditionalModels(ctx, headers, routePath)
	codexTok, anthroTok := presentSubscriptionTokens(ctx, headers)
	if s.usageObserver == nil && anthroTok == "" {
		return ctx
	}
	ctx = withUnifiedLimitCapture(ctx)
	obs := func(callCtx context.Context, h http.Header) {
		func() {
			defer func() {
				if r := recover(); r != nil {
					observability.FromContext(callCtx).Error("weave-capture Phase 0: recovered from panic capturing unified limit headers", "recover", r)
				}
			}()
			captureUnifiedLimitHeaders(callCtx, h)
		}()

		if s.usageObserver == nil || (codexTok == "" && anthroTok == "") {
			return
		}
		// Record only when the call's RESOLVED credential is one of THIS request's
		// detected subscription tokens — not any incidental OAuth credential — and
		// key by that token so subsidyFactors reads the same key. Gating on the
		// resolved credential also skips internal calls that don't use the sub,
		// e.g. the handover summarizer's deployment-key Anthropic call after
		// clearCredentials. The parser follows the matched token's family, so this
		// works whether the sub arrived via a dedicated header or the inbound bearer.
		creds := CredentialsFromContext(callCtx)
		if creds == nil || !creds.OAuth {
			return
		}
		switch string(creds.APIKey) {
		case codexTok:
			if snap, ok := usage.ParseCodexHeaders(h); ok {
				s.usageObserver.Record(s.usageObserver.Key([]byte(codexTok)), snap)
			}
		case anthroTok:
			if snap, ok := usage.ParseAnthropicUnifiedHeaders(h); ok {
				s.usageObserver.Record(s.usageObserver.Key([]byte(anthroTok)), snap)
			}
		}
	}
	return providers.WithUpstreamHeaderObserver(ctx, obs)
}

// withSubscriptionConditionalModels selects the per-request conditional model allowlist based on subscription state.
// An unobserved credential is treated as active (cold-start priming); an empty
// selected list is kept in context so configured empty state lists fail closed.
func (s *Service) withSubscriptionConditionalModels(ctx context.Context, headers http.Header, routePaths ...string) context.Context {
	activeModels := installationSubscriptionModelsWhenActiveFromContext(ctx)
	inactiveModels := installationSubscriptionModelsWhenInactiveFromContext(ctx)
	if s.usageObserver == nil || (len(activeModels) == 0 && len(inactiveModels) == 0) {
		return ctx
	}
	codexTok, anthroTok := presentSubscriptionTokens(ctx, headers)
	routePath := ""
	if len(routePaths) > 0 {
		routePath = routePaths[0]
	}
	// Only the credential family covering this endpoint determines state; an
	// unrelated active credential must not mask an exhausted covering one.
	switch routePath {
	case routePathMessages:
		codexTok = ""
	case routePathChatCompletions, routePathResponses:
		anthroTok = ""
	}
	if codexTok == "" && anthroTok == "" {
		return ctx
	}

	active := false
	observed := false
	for _, token := range []string{codexTok, anthroTok} {
		if token == "" {
			continue
		}
		snapshot, ok := s.usageObserver.Snapshot(s.usageObserver.Key([]byte(token)))
		if !ok {
			active = true
			continue
		}
		observed = true
		if !snapshot.Exhausted() {
			active = true
		}
	}
	if !active && observed {
		return context.WithValue(ctx, InstallationSubscriptionConditionalModelsContextKey{}, inactiveModels)
	}
	return context.WithValue(ctx, InstallationSubscriptionConditionalModelsContextKey{}, activeModels)
}

// subsidyFactors computes the per-covered-model cost multiplier for this
// request from the present subscription(s). When headroom has been observed it
// uses the real factor; when a subscription is present but NO headroom has been
// observed yet, it OPTIMISTICALLY assumes slack (the epsilon floor) so the
// covered models are favored from the very first turn. This bootstraps the
// feature: otherwise the subscription would never serve a turn, so its headroom
// would never be observed, so the discount would never engage — a chicken-and-
// egg that pins routing to whatever wins at full price. The optimistic factor
// self-corrects to the real headroom once the first subscription-served response
// records it (including a 429's near-cap reading). Returns nil when the feature
// is off, no subscription is present, or the installation has disabled
// subscription-aware routing. Keyed identically to withUsageObserver so record
// and read agree across all three harnesses.
func (s *Service) subsidyFactors(ctx context.Context, headers http.Header) map[string]float64 {
	if s.usageObserver == nil || !s.subsidyEnabled {
		return nil
	}
	// Per-installation opt-out: when the org has disabled subscription-aware
	// routing, add no subscription bonus so the scorer decides on merits and
	// non-Claude models (GLM, Kimi, DeepSeek, ...) compete fairly. The
	// subscription credential is still forwarded downstream for turns that route
	// to Claude on their own merits, so this removes only the routing bias, not
	// the prepaid billing path. Keeping the usage observer installed (it is wired
	// separately in withUsageObserver) preserves the usage-bypass gate.
	if subscriptionRoutingDisabledForRequest(ctx) {
		return nil
	}
	codexTok, anthroTok := presentSubscriptionTokens(ctx, headers)
	factors := make(map[string]float64)
	if codexTok != "" {
		f := s.observedOrOptimisticFactor(codexTok)
		for _, m := range codexCoveredModels {
			factors[m] = f
		}
	}
	if anthroTok != "" {
		f := s.observedOrOptimisticFactor(anthroTok)
		for _, m := range claudeCoveredModels() {
			factors[m] = f
		}
	}
	if len(factors) == 0 {
		return nil
	}
	return factors
}

// observedOrOptimisticFactor returns the cost factor for a present subscription:
// the real factor from observed headroom, or the optimistic slack floor
// (subsidyEpsilon) when none has been observed yet. The optimistic default is
// what lets the covered models win the FIRST turn and thereby get a chance to
// serve and record real headroom; without it the feature never bootstraps.
//
// The optimistic floor is safe here precisely because the observer keeps a
// reading alive for the life of its binding quota window (usage.Observer.freshFor),
// not a short TTL: a Snapshot miss therefore means the credential was genuinely
// never observed, or its window has since reset — both states where assuming
// slack is correct. A credential observed near its cap keeps returning that
// near-1.0 factor (no optimistic re-subsidy) until its window actually resets.
func (s *Service) observedOrOptimisticFactor(token string) float64 {
	if snap, ok := s.usageObserver.Snapshot(s.usageObserver.Key([]byte(token))); ok {
		return snap.CostFactor(s.subsidyEpsilon, s.subsidyGamma)
	}
	return s.subsidyEpsilon
}
