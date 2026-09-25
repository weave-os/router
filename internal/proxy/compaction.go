package proxy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/handover"
	"weave-os/router/internal/router/hmm"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/translate"
)

// ErrContextWindowExceeded is returned when safe compaction cannot fit an
// eligible model's window. Maps to HTTP 413, distinct from ErrNoEligibleProvider.
var ErrContextWindowExceeded = errors.New("proxy: request context exceeds every eligible model's window")

const (
	// DefaultCompactionTriggerPct is the fraction of the largest eligible
	// model's window at which the cascade engages. Compacting below the window
	// (not at overflow) keeps the pre-summary history small enough for a
	// summarizer to ingest.
	DefaultCompactionTriggerPct = 0.85
	// compactionSummaryOutputReserve is headroom (summary output + margin) the
	// selected summarizer model needs above the history it must ingest.
	compactionSummaryOutputReserve = DefaultCompactionMaxTokens + 8_000
)

// compactionPolicy controls the router's context handling per harness.
type compactionPolicy struct {
	// RecentTurns is how many trailing non-system messages survive a
	// summarization rewrite, so the model keeps immediate working context.
	RecentTurns int
	// ToolResultKeep is how many trailing tool results Tier-1 cleanup leaves
	// intact; older ones are replaced with a placeholder.
	ToolResultKeep int
	// DeferToClient permits deferral when a supported client budget is known
	// and the eligible pool can serve it; it never substitutes provider capacity.
	DeferToClient bool
}

var (
	defaultCompactionPolicy = compactionPolicy{RecentTurns: 12, ToolResultKeep: 5}

	compactionPolicies = map[string]compactionPolicy{
		ClientAppClaudeCode: {RecentTurns: 12, ToolResultKeep: 5, DeferToClient: true},
		ClientAppCodex:      {RecentTurns: 12, ToolResultKeep: 5},
		ClientAppGeminiCLI:  {RecentTurns: 12, ToolResultKeep: 5},
	}
)

// compactionPolicyFor returns the harness policy for a canonical client_app
// (ClientIdentity.ClientApp), or the default for unknown/absent clients.
func compactionPolicyFor(clientApp string) compactionPolicy {
	if p, ok := compactionPolicies[clientApp]; ok {
		return p
	}
	return defaultCompactionPolicy
}

func verifiedCompactionClient(clientApp string) bool {
	switch clientApp {
	case ClientAppClaudeCode, ClientAppCodex, ClientAppGeminiCLI, ClientAppOpencode:
		return true
	default:
		return false
	}
}

// CompactionSummarizer summarizes prior conversation with the structured
// compaction prompt against an explicit model. Implemented by
// *ProviderSummarizer; declared here so the Service depends on the behavior,
// not the concrete type.
type CompactionSummarizer interface {
	SummarizeForCompaction(ctx context.Context, env *translate.RequestEnvelope, target CompactionTarget, scope router.Request, maxTokens int) (string, handover.Usage, error)
	Provider() string
}

// compactionInput is the per-request context the cascade decides against.
type compactionInput struct {
	TurnType           turntype.TurnType
	OutputReserve      int
	CredentialIdentity string
	SessionKey         [sessionpin.SessionKeyLen]byte
	Endpoint           string
	// MaxWindow is the largest effective context window among eligible
	// routing models (maxEligibleContextWindow). Zero disables the cascade.
	MaxWindow    int
	ClientBudget router.ClientBudget
	// ClientApp selects the harness policy (ClientIdentity.ClientApp).
	ClientApp string
	// PreferredSummarizer resolves the session's pinned Anthropic model when
	// there is one worth reusing (compactionPreferredSummarizer). Invoked
	// only once the cascade actually needs a summarizer, so the common
	// below-threshold turn costs no extra pin-store read. Nil means none.
	PreferredSummarizer func() string
	Headers             http.Header
	// Scope is the request's eligibility the summarizer plan must honor
	// (enabled providers, excluded models, gateways, custom bindings).
	Scope router.Request
}

// compactionResult records what the cascade did, for logging and billing.
type compactionResult struct {
	Applied            bool
	ToolResultsCleared int
	Summarized         bool
	SummaryModel       string
	SummaryUsage       handover.Usage
	CheckpointReused   bool
	TrimmedToRecent    int
	FinalEstimate      int
	// DeferredToClient is true when the harness policy left compaction to the
	// client because the routable pool can serve the window it believes in.
	DeferredToClient bool
}

// maxEligibleContextWindow returns the largest effective context window among
// available routing models that are not policy-excluded. It uses the smallest
// enabled binding window for each model, matching the overflow pre-filter's
// conservative dispatch check: compaction must not stop because a fallback
// binding has more capacity than the binding that can actually serve first.
// A signature-stripping (non-Anthropic) target gets sigSavings added to its
// window, mirroring excludeContextOverflowModels. Zero when none are known
// (availableModels unset), which disables compaction.
func (s *Service) maxEligibleContextWindow(policyExcluded, enabledProviders map[string]struct{}, sigSavings int) int {
	maxWindow := 0
	for model := range s.availableModels {
		if _, excluded := policyExcluded[model]; excluded {
			continue
		}
		w := minContextWindowForModel(model, enabledProviders)
		if sigSavings > 0 && modelStripsAnthropicSignatures(model) {
			w += sigSavings
		}
		if w > maxWindow {
			maxWindow = w
		}
	}
	return maxWindow
}

// summarizerScope is the eligibility a router-initiated summary call inherits
// from the request it serves: the same provider, model, gateway, and custom
// binding limits the request's own routing candidates were filtered by.
func (s *Service) summarizerScope(ctx context.Context, enabledProviders, excludedModels map[string]struct{}) router.Request {
	return router.Request{
		EnabledProviders: enabledProviders,
		ExcludedModels:   excludedModels,
		CustomBindings:   s.customBindingsForRequest(ctx),
		GatewayProviders: s.gatewayProvidersForRequest(ctx),
	}
}

// compactionModelOrDefault returns the configured Sonnet-class summarizer.
func (s *Service) compactionModelOrDefault() string {
	if s.compactionModel != "" {
		return s.compactionModel
	}
	return policy.PrecompactionDefaultModel
}

// anthropicSummarizerEligible reports whether model is a reviewed member of
// the precompaction-summary policy: an Anthropic-served, non-low-tier catalog
// model the cascade may reuse as a warm-pin summarizer.
func anthropicSummarizerEligible(model string) bool {
	spec, ok := policy.DefaultRegistry().Spec(policy.PurposePrecompactionSummary)
	if !ok || !slices.Contains(spec.FixedCatalogModels, model) {
		return false
	}
	m, ok := catalog.ByID(model)
	if !ok || m.Tier == catalog.TierLow {
		return false
	}
	for _, b := range m.Providers {
		if b.Provider == providers.ProviderAnthropic {
			return true
		}
	}
	return false
}

// compactionTargetFor types the cascade's chosen summarizer model by where the
// choice came from, so the policy resolver can validate it as a session or
// deployment override rather than an untyped string.
func (s *Service) compactionTargetFor(model, preferred string) CompactionTarget {
	selected := func(candidate string) bool { return candidate == model }
	switch {
	case model != "" && catalog.LatestInFamily(preferred, selected) == model:
		return CompactionTarget{CatalogID: model, Source: policy.OverrideSourceSession}
	case model != "" && catalog.LatestInFamily(s.compactionModelOrDefault(), selected) == model:
		return CompactionTarget{CatalogID: model, Source: policy.OverrideSourceDeployment}
	default:
		return CompactionTarget{CatalogID: model}
	}
}

// compactionSummarizerCandidates prefers the session's Anthropic family,
// followed by the configured default and large-window family.
func (s *Service) compactionSummarizerCandidates(preferred string) []string {
	out := make([]string, 0, 3)
	seen := map[string]struct{}{}
	add := func(m string) {
		if m == "" {
			return
		}
		if _, dup := seen[m]; dup {
			return
		}
		seen[m] = struct{}{}
		out = append(out, m)
	}
	add(preferred)
	add(s.compactionModelOrDefault())
	add(policy.PrecompactionLargeWindowModel)
	return out
}

// selectCompactionSummarizer returns the first candidate summarizer model
// whose context window can ingest the prepared request plus summary headroom.
func (s *Service) selectCompactionSummarizer(env *translate.RequestEnvelope, preferred string, excluded map[string]struct{}) string {
	eligible := func(model string) bool {
		if _, blocked := excluded[model]; blocked {
			return false
		}
		if !anthropicSummarizerEligible(model) {
			return false
		}
		estimate, err := compactionSummaryEstimate(env, model)
		return err == nil && catalog.ContextWindowFor(model) >= estimate+compactionSummaryOutputReserve
	}
	for _, m := range s.compactionSummarizerCandidates(preferred) {
		if latest := catalog.LatestInFamily(m, eligible); latest != "" {
			return latest
		}
	}
	return ""
}

func (s *Service) hasEligibleCompactionSummarizer(preferred string, excluded map[string]struct{}) bool {
	for _, candidate := range s.compactionSummarizerCandidates(preferred) {
		if catalog.LatestInFamily(candidate, func(model string) bool {
			_, blocked := excluded[model]
			return !blocked && anthropicSummarizerEligible(model)
		}) != "" {
			return true
		}
	}
	return false
}

// compactionPreferredSummarizer returns the session's active pinned model
// when it is served by Anthropic directly — the same model that has been
// running the conversation, so its prompt cache is warm for the summary call.
// Empty when there is no pin store, no active pin, or the pin is elsewhere.
func (s *Service) compactionPreferredSummarizer(ctx context.Context, sessionKey [sessionpin.SessionKeyLen]byte, role string) string {
	if s.pinStore == nil {
		return ""
	}
	pin, active := s.loadPin(ctx, sessionKey, role)
	if !active || pin.Provider != providers.ProviderAnthropic {
		return ""
	}
	return pin.Model
}

// clientWouldCompact defers only when the pool can serve the harness default.
// A private lower override can compact sooner; unknown harnesses never borrow a provider window.
func clientWouldCompact(pol compactionPolicy, budget router.ClientBudget, maxWindow int) bool {
	return pol.DeferToClient && budget.Evidence == router.ClientBudgetHarnessDefault &&
		budget.DefaultCompactThreshold > 0 && budget.DefaultCompactThreshold <= maxWindow
}

// maybeCompact runs the compaction cascade when needed ≥ compactionTriggerPct
// of in.MaxWindow: (1) clear old tool results, (2) summarize with
// capacity-bounded calls, (3) retain a complete recent tool batch alongside
// the summary. Mutates env in place — caller MUST recompute estimates when
// res.Applied is true. Returns ErrContextWindowExceeded with the original
// request intact if no safe path fits;
// no-ops when pct is zero/unset, below threshold, the turn is hard-pinned
// (Claude Code's own compaction turn must not be rewritten, and
// probe/title-gen turns bypass the scorer), the turn is a classifier grading
// a transcript it carries as payload, or the harness policy defers to the
// client's own compaction.
func (s *Service) maybeCompact(ctx context.Context, env *translate.RequestEnvelope, in compactionInput) (compactionResult, error) {
	// The classifier requires the exact causal prefix. Compaction must never
	// silently replace the context from which its counters and digests derive.
	if router.StrategyFromContext(ctx) == router.StrategyLLMClassifier {
		return compactionResult{}, nil
	}
	log := observability.FromContext(ctx)
	var res compactionResult
	if s.compactionTriggerPct <= 0 || in.MaxWindow <= 0 || env == nil || s.isHardPinnedTurn(ctx, in.TurnType) || isUnpinnedScoredTurn(in.TurnType) {
		return res, nil
	}
	pol := compactionPolicyFor(in.ClientApp)

	needed := func() int { return env.ContextOverflowTokenEstimate() + in.OutputReserve }
	fits := func() bool { return needed() <= in.MaxWindow }
	trigger := int(float64(in.MaxWindow) * s.compactionTriggerPct)
	if needed() < trigger {
		return res, nil
	}
	original := env.Clone()
	if fits() && clientWouldCompact(pol, in.ClientBudget, in.MaxWindow) {
		res.DeferredToClient = true
		log.Info("Compaction deferred to client harness",
			"client_app", in.ClientApp,
			"needed", needed(),
			"max_window", in.MaxWindow,
			"client_default_window", in.ClientBudget.DefaultWindow,
			"client_budget_evidence", in.ClientBudget.Evidence,
		)
		return res, nil
	}
	log.Info("Compaction cascade engaged",
		"client_app", in.ClientApp,
		"needed", needed(),
		"trigger", trigger,
		"max_window", in.MaxWindow,
	)

	if env.SupportsHistoryCompaction() && s.reuseCompactionCheckpoint(ctx, env, original, in, pol, &res, trigger) {
		return res, nil
	}

	// Tier 1: clear stale tool results (cheap, local, no model call).
	if n := env.ClearOldToolResults(pol.ToolResultKeep); n > 0 {
		res.Applied = true
		res.ToolResultsCleared = n
		log.Info("Compaction Tier-1: cleared old tool results", "cleared", n, "needed_after", needed())
	}
	// Tier-1 alone is enough only if it brought the request back under the
	// trigger; merely fitting the window is not — the point of triggering
	// below the window is to summarize while a summarizer can still ingest
	// the history.
	if needed() < trigger {
		res.FinalEstimate = needed()
		return res, nil
	}

	if s.compactionSummarizer != nil && verifiedCompactionClient(in.ClientApp) && env.SupportsHistoryCompaction() {
		preferred := ""
		if in.PreferredSummarizer != nil {
			preferred = in.PreferredSummarizer()
		}
		summary, usage, model, ok := s.runCompactionSummary(ctx, env, preferred, in.Scope, in.Headers)
		res.SummaryUsage = usage
		if ok {
			fitBefore, before := fits(), env.Clone()
			res.SummaryModel = model
			for _, n := range []int{pol.RecentTurns, 6, 3, 1} {
				*env = *before.Clone()
				env.RewriteForCompaction(summary, n)
				if fits() {
					res.Applied = true
					res.Summarized = true
					if n != pol.RecentTurns {
						res.TrimmedToRecent = n
					}
					log.Info("Compaction Tier-3: history summarized", "summary_model", model, "needed_after", needed())
					s.saveCompactionCheckpoint(ctx, original, in, pol, summary, model, n)
					break
				}
			}
			if !res.Summarized {
				*env = *before
				log.Warn("Compaction Tier-3: summary rewrite would overflow; reverted", "summary_model", model)
				if fitBefore {
					res.FinalEstimate = needed()
					return res, nil
				}
			}
		}
	}
	if fits() {
		res.FinalEstimate = needed()
		return res, nil
	}

	res.FinalEstimate = needed()
	*env = *original
	return res, fmt.Errorf("context estimate %d tokens exceeds largest window %d: %w", res.FinalEstimate, in.MaxWindow, ErrContextWindowExceeded)
}

// billCompactionSummary debits the compaction summary call as its own ledger
// row and records its session-tagged telemetry row (mirrors the
// switch-handover summary billing). No-ops when billing is unwired or the
// usage carries no tokens.
func (s *Service) billCompactionSummary(ctx context.Context, requestID, externalID string, usage handover.Usage) {
	s.billAuxiliaryInference(ctx, requestID, auxSuffixPrecompactionSummary, externalID, usage)
}

// runCompactionSummary picks a window-aware summarizer model and dispatches the
// structured summary call, honoring the tenant-boundary credential rules used
// by the switch-handover path. Returns ok=false (and logs) when no summarizer
// fits the history, the tenant boundary forbids the call, or the call fails.
func (s *Service) runCompactionSummary(ctx context.Context, env *translate.RequestEnvelope, preferred string, scope router.Request, reqHeaders http.Header) (string, handover.Usage, string, bool) {
	log := observability.FromContext(ctx)

	if scope.EnabledProviders != nil {
		if _, enabled := scope.EnabledProviders[s.compactionSummarizer.Provider()]; !enabled {
			log.Info("Compaction skipped: summary provider disabled", "reason", "tenant_restriction")
			return "", handover.Usage{}, "", false
		}
	}
	excluded := mergeExcludedModels(s.excludedModelsForRequest(ctx), s.globalAutomaticExcludedModels(ctx))
	// Auxiliary models need not belong to the routing pool whose allowlist was desugared.
	if allowed := allowedModelsForRequest(ctx); allowed != nil && s.excludedModelsOverride == nil {
		if excluded == nil {
			excluded = make(map[string]struct{})
		}
		for _, candidate := range catalog.Models {
			if _, permitted := allowed[candidate.ID]; !permitted {
				excluded[candidate.ID] = struct{}{}
			}
		}
	}
	model := s.selectCompactionSummarizer(env, preferred, excluded)
	if model == "" {
		if !s.hasEligibleCompactionSummarizer(preferred, excluded) {
			log.Info("Compaction skipped: no eligible summary model", "reason", "no_eligible_model")
			return "", handover.Usage{}, "", false
		}
		log.Info("Compaction Tier-3: no single summary request fits or is authorized",
			"reason", "no_single_model", "history", env.ContextOverflowTokenEstimate())
		return s.runChunkedCompactionSummary(ctx, env, preferred, scope, reqHeaders, excluded)
	}

	sumProvider := s.compactionSummarizer.Provider()
	sumCreds := resolveSummarizerCreds(ctx, sumProvider, reqHeaders)
	if sumCreds == nil && s.requestUsesNonDeploymentCreds(ctx, reqHeaders) {
		log.Info("Compaction Tier-3 skipped: would cross tenant boundary", "reason", "tenant_boundary", "sum_provider", sumProvider)
		return "", handover.Usage{}, "", false
	}
	summCtx := ctx
	if sumCreds != nil {
		summCtx = context.WithValue(ctx, CredentialsContextKey{}, sumCreds)
	} else {
		summCtx = clearCredentials(ctx)
	}

	summary, usage, err := s.compactionSummarizer.SummarizeForCompaction(summCtx, env, s.compactionTargetFor(model, preferred), scope, DefaultCompactionMaxTokens)
	if err != nil {
		reason := "provider_failure"
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			reason = "timeout"
		case errors.Is(err, ErrSummaryRefusal):
			reason = "refusal"
		case errors.Is(err, ErrInvalidSummary):
			reason = "invalid_output"
		case errors.Is(err, ErrEmptySummary):
			reason = "empty_output"
		}
		log.Warn("Compaction summarizer failed", "err", err, "model", model, "reason", reason)
		return "", usage, "", false
	}
	if strings.TrimSpace(summary) == "" {
		log.Warn("Compaction summarizer returned empty", "model", model, "reason", "empty_output")
		return "", usage, "", false
	}
	return summary, usage, model, true
}

func (s *Service) runChunkedCompactionSummary(ctx context.Context, env *translate.RequestEnvelope, preferred string, scope router.Request, headers http.Header, excluded map[string]struct{}) (string, handover.Usage, string, bool) {
	boundaries := env.CompactionBoundaries()
	if len(boundaries) < 3 {
		return "", handover.Usage{}, "", false
	}
	var summary, model string
	var total handover.Usage
	for index := 0; index < len(boundaries)-1; {
		lo, hi, best := index+1, len(boundaries)-1, 0
		for lo <= hi {
			mid := lo + (hi-lo)/2
			chunk, err := env.CompactionChunk(boundaries[index], boundaries[mid], summary)
			if err != nil {
				return "", total, "", false
			}
			if s.selectCompactionSummarizer(chunk, preferred, excluded) != "" {
				best = mid
				lo = mid + 1
			} else {
				hi = mid - 1
			}
		}
		if best == 0 {
			observability.FromContext(ctx).Warn("Compaction chunk cannot fit summarizer", "start", boundaries[index])
			return "", total, "", false
		}
		chunk, err := env.CompactionChunk(boundaries[index], boundaries[best], summary)
		if err != nil {
			return "", total, "", false
		}
		next, usage, selected, ok := s.runCompactionSummary(ctx, chunk, preferred, scope, headers)
		total.InputTokens += usage.InputTokens
		total.OutputTokens += usage.OutputTokens
		total.CacheCreation += usage.CacheCreation
		total.CacheRead += usage.CacheRead
		total.Provider, total.Model = usage.Provider, usage.Model
		if !ok {
			return "", total, "", false
		}
		summary, model = next, selected
		index = best
	}
	return summary, total, model, true
}

// compactionHardPin picks the model for a harness's own compaction turn: the
// model that ran the conversation when it is Sonnet-class or better (prompt
// cache warm — what Claude Code and Codex do against their vendor directly),
// else the configured compaction model on Anthropic. A Codex thread arrives
// in Responses format, so when a non-Anthropic model has been serving it the
// turn stays there rather than being summarized cross-format by Sonnet — the
// summary replaces the thread's history for every turn that follows. source
// records whether the session's own model or the deployment's configured one
// won, so the plan is authorized under the matching override. ok=false when
// nothing is eligible for this request, so the caller falls back to the
// generic hard-pin tier.
func (s *Service) compactionHardPin(ctx context.Context, sessionKey [sessionpin.SessionKeyLen]byte, role string, req router.Request) (provider, model string, source policy.OverrideSource, ok bool) {
	// Gateway-exclusive tenants drop vendor bindings; leave them to the resolver.
	if len(req.GatewayProviders) > 0 {
		return "", "", "", false
	}
	if req.ClientApp == ClientAppCodex {
		if p, m, served := s.compactionSessionModel(ctx, sessionKey, role, req); served {
			return p, m, policy.OverrideSourceSession, true
		}
	}
	if req.EnabledProviders != nil {
		if _, enabled := req.EnabledProviders[providers.ProviderAnthropic]; !enabled {
			return "", "", "", false
		}
	}
	eligible := func(m string) bool {
		if !anthropicSummarizerEligible(m) {
			return false
		}
		if s.availableModels != nil {
			if _, available := s.availableModels[m]; !available {
				return false
			}
		}
		if _, excluded := req.ExcludedModels[m]; excluded {
			return false
		}
		return !automaticallyDisabled(req, m)
	}
	preferred := s.compactionPreferredSummarizer(ctx, sessionKey, role)
	if latest := catalog.LatestInFamily(preferred, eligible); latest != "" {
		return providers.ProviderAnthropic, latest, policy.OverrideSourceSession, true
	}
	if m := catalog.LatestInFamily(s.compactionModelOrDefault(), eligible); m != "" {
		return providers.ProviderAnthropic, m, policy.OverrideSourceDeployment, true
	}
	return "", "", "", false
}

// compactionSessionModel upgrades the last served non-Anthropic session
// family to its newest eligible catalog version. The active thread pin and
// HMM history compete by completion time; Anthropic uses its separate path.
func (s *Service) compactionSessionModel(ctx context.Context, sessionKey [sessionpin.SessionKeyLen]byte, role string, req router.Request) (provider, model string, ok bool) {
	if s.pinStore == nil {
		return "", "", false
	}
	threadPin, active := s.loadPin(ctx, sessionKey, role)
	if !active {
		threadPin = sessionpin.Pin{}
	}
	served := latestServedPin(threadPin, s.loadHMMHistory(ctx, sessionKey, role))
	model = served.LastServedModel
	if model == "" {
		model = served.Model
	}
	model, _ = hmm.SplitEffort(model)
	model = catalog.LatestInFamily(model, func(candidate string) bool {
		m, known := catalog.ByID(candidate)
		if !known || m.Tier == catalog.TierLow {
			return false
		}
		binding, bound := s.servedBinding(candidate, served.Provider, req)
		if !bound || binding.Provider == providers.ProviderAnthropic {
			return false
		}
		if s.availableModels != nil {
			if _, available := s.availableModels[candidate]; !available {
				return false
			}
		}
		if _, excluded := req.ExcludedModels[candidate]; excluded {
			return false
		}
		return !automaticallyDisabled(req, candidate)
	})
	if model == "" {
		return "", "", false
	}
	binding, bound := s.servedBinding(model, served.Provider, req)
	if !bound {
		return "", "", false
	}
	return binding.Provider, model, true
}

// latestServedPin picks the pin whose last turn ended most recently among
// those that record a served model.
func latestServedPin(pins ...sessionpin.Pin) sessionpin.Pin {
	var latest sessionpin.Pin
	found := false
	for _, pin := range pins {
		if pin.LastServedModel == "" && pin.Model == "" {
			continue
		}
		if !found || pin.LastTurnEndedAt.After(latest.LastTurnEndedAt) {
			latest = pin
			found = true
		}
	}
	return latest
}

// servedBinding resolves the provider binding for a session's served model:
// the provider that served it when that provider is still enabled and binds
// the model, else the first enabled binding (a failed turn may leave the
// stored provider stale).
func (s *Service) servedBinding(model, servedProvider string, req router.Request) (catalog.ProviderBinding, bool) {
	providerSet := req.EnabledProviders
	if providerSet == nil {
		providerSet = s.clients.NameSet()
	}
	if _, enabled := providerSet[servedProvider]; enabled {
		pinned := map[string]struct{}{servedProvider: {}}
		if binding, valid := catalog.ResolveBindingWithCustom(model, pinned, req.CustomBindings); valid {
			return binding, true
		}
	}
	return catalog.ResolveBindingWithCustom(model, providerSet, req.CustomBindings)
}
