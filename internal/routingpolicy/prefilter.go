package routingpolicy

import (
	"sort"

	"weave-os/router/internal/credentials"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/translate"
)

// ContextWindow returns the effective context window for a model.
// CapExtendedContext models (Opus 4.6+, Sonnet 4.6) always report 1M since the
// proxy unconditionally injects the context-1m beta for them — gating on the
// client's beta header or the token estimate instead would let a large
// request slip onto 200K and overflow on the first turn.
func ContextWindow(modelID string, provider ...string) int {
	if router.Lookup(modelID).Supports(router.CapExtendedContext) {
		return 1_000_000
	}
	if len(provider) > 0 && provider[0] != "" {
		return catalog.ContextWindowForBinding(modelID, provider[0])
	}
	return catalog.ContextWindowFor(modelID)
}

// MinContextWindow returns the smallest context window any enabled
// binding can serve. Uses MIN because the primary binding dispatches first —
// a 512K primary blocks even when a 1M fallback exists. Nil enabledProviders
// falls back to the model-level window.
func MinContextWindow(model string, enabledProviders map[string]struct{}) int {
	cw := ContextWindow(model)
	if len(enabledProviders) == 0 {
		return cw
	}
	m, ok := catalog.ByID(model)
	if !ok || len(m.Providers) == 0 {
		return cw
	}
	for _, b := range catalog.RoutableBindings(m) {
		if _, enabled := enabledProviders[b.Provider]; !enabled {
			continue
		}
		if w := ContextWindow(model, b.Provider); w < cw {
			cw = w
		}
	}
	return cw
}

// StripsAnthropicSignatures reports whether dispatching to model drops
// the Anthropic-only thought-signature blocks (every non-Anthropic-family
// target does). Lets the overflow check discount those bytes for stripping
// targets while counting them for Anthropic passthrough. Unknown models
// default to "keeps" (the conservative side).
func StripsAnthropicSignatures(model string) bool {
	m, ok := catalog.ByID(model)
	if !ok {
		return false
	}
	return providers.FamilyFor(m.PrimaryProvider()) != providers.FamilyAnthropic
}

// ExcludeContextOverflowModels returns a copy of excluded augmented with every
// model in available whose context window is too small for the request, plus
// the sorted IDs newly excluded (for logging). est is the full-body token
// estimate; sigSavings (tokens a signature-stripping target saves) is
// subtracted only for stripping targets, so Anthropic passthrough is still
// checked against the full body. Returns excluded unchanged and nil when
// nothing is added.
func ExcludeContextOverflowModels(est, sigSavings, outputReserve int, enabledProviders, excluded, available map[string]struct{}) (map[string]struct{}, []string) {
	if est <= 0 {
		return excluded, nil
	}
	var out map[string]struct{}
	var overflowed []string
	for model := range available {
		if _, alreadyExcluded := excluded[model]; alreadyExcluded {
			continue
		}
		needed := est + outputReserve
		if sigSavings > 0 && StripsAnthropicSignatures(model) {
			needed -= sigSavings
		}
		cw := MinContextWindow(model, enabledProviders)
		if needed <= cw {
			continue
		}
		if out == nil {
			out = make(map[string]struct{}, len(excluded))
			for k := range excluded {
				out[k] = struct{}{}
			}
		}
		out[model] = struct{}{}
		overflowed = append(overflowed, model)
	}
	if len(overflowed) == 0 {
		return excluded, nil
	}
	sort.Strings(overflowed)
	return out, overflowed
}

// AdmitWidestOnTotalOverflow reports the largest-window overflowed models to
// re-admit when the context pre-filter left no model in available routable.
// The estimate overcounts (÷4 over JSON bytes), so it alone never rules out
// every model: the upstream decides, and a real overflow reaches the client as
// its native prompt-too-long error so the client compacts. Only models the
// request can be served on count: one it cannot reach neither hides a total
// overflow nor sets the widest window.
func AdmitWidestOnTotalOverflow(ruledOut map[string]struct{}, overflowed []string, available, enabledProviders map[string]struct{}, bindings router.RequestBindings) []string {
	if len(overflowed) == 0 {
		return nil
	}
	for model := range available {
		if _, out := ruledOut[model]; !out && servableUnder(model, enabledProviders, bindings) {
			return nil
		}
	}
	widest := 0
	var servable []string
	for _, model := range overflowed {
		if !servableUnder(model, enabledProviders, bindings) {
			continue
		}
		servable = append(servable, model)
		widest = max(widest, MinContextWindow(model, enabledProviders))
	}
	var admitted []string
	for _, model := range servable {
		if MinContextWindow(model, enabledProviders) == widest {
			admitted = append(admitted, model)
		}
	}
	return admitted
}

// servableUnder reports whether an enabled provider serves model through a
// catalog or configuration-declared binding; a gateway-exclusive request is
// served only through its gateways' aliases. No enabled set, or a model
// without catalog bindings, counts as servable.
func servableUnder(model string, enabledProviders map[string]struct{}, bindings router.RequestBindings) bool {
	if len(bindings.Gateways) > 0 {
		for _, provider := range bindings.Custom[model] {
			if _, gateway := bindings.Gateways[provider]; gateway {
				return true
			}
		}
		return false
	}
	if len(enabledProviders) == 0 {
		return true
	}
	for _, provider := range bindings.Custom[model] {
		if _, enabled := enabledProviders[provider]; enabled {
			return true
		}
	}
	m, ok := catalog.ByID(model)
	if !ok || len(m.Providers) == 0 {
		return true
	}
	for _, b := range catalog.RoutableBindings(m) {
		if _, enabled := enabledProviders[b.Provider]; enabled {
			return true
		}
	}
	return false
}

// WithoutModels returns set minus models, copying only when it must change.
func WithoutModels(set map[string]struct{}, models []string) map[string]struct{} {
	if len(set) == 0 || len(models) == 0 {
		return set
	}
	out := make(map[string]struct{}, len(set))
	for model := range set {
		out[model] = struct{}{}
	}
	for _, model := range models {
		delete(out, model)
	}
	return out
}

// WithoutModelsKeep is WithoutModels but leaves keep IDs in the set.
func WithoutModelsKeep(set map[string]struct{}, models, keep []string) map[string]struct{} {
	if len(keep) == 0 {
		return WithoutModels(set, models)
	}
	kept := make(map[string]struct{}, len(keep))
	for _, model := range keep {
		kept[model] = struct{}{}
	}
	filtered := make([]string, 0, len(models))
	for _, model := range models {
		if _, ok := kept[model]; !ok {
			filtered = append(filtered, model)
		}
	}
	return WithoutModels(set, filtered)
}

// ExcludeGemini3xOnUnsignedHistory augments excluded with every Gemini 3.x
// model when the request history carries an assistant tool call lacking a
// Gemini thoughtSignature (guaranteed 400 on foreign/cross-model history).
// Native Gemini continuations round-trip their own signature and are
// unaffected. Returns excluded unchanged (and nil) when nothing is added.
func ExcludeGemini3xOnUnsignedHistory(env *translate.RequestEnvelope, excluded, available map[string]struct{}) (map[string]struct{}, []string) {
	if env == nil || !env.HasUnsignedToolCallHistory() {
		return excluded, nil
	}
	var out map[string]struct{}
	var added []string
	for model := range available {
		if !catalog.RequiresSignedToolHistory(model) {
			continue
		}
		if _, already := excluded[model]; already {
			continue
		}
		if out == nil {
			out = make(map[string]struct{}, len(excluded))
			for k := range excluded {
				out[k] = struct{}{}
			}
		}
		out[model] = struct{}{}
		added = append(added, model)
	}
	if len(added) == 0 {
		return excluded, nil
	}
	sort.Strings(added)
	return out, added
}

// SafetyExcludedModels returns the hard request-time safety exclusion set
// (context-overflow + image-limit + gemini-unsigned-history + advisor pairing). It re-runs the filters
// against an EMPTY base — the routing-path filters skip models already in
// excluded_models, so a policy-excluded overflow model would be absent from
// those lists yet must still block bypass (it would 400 on the subscription).
// Returns nil when neither filter fires.
func SafetyExcludedModels(env *translate.RequestEnvelope, outputReserve int, enabledProviders map[string]struct{}, customBindings map[string][]string, available map[string]struct{}, advisorOutranking []string) map[string]struct{} {
	_, overflowed := ExcludeContextOverflowModels(env.ContextOverflowTokenEstimate(), env.SignatureTokenSavings(), outputReserve, enabledProviders, nil, available)
	_, imageLimited := ExcludeImageLimitModels(env.ImageCount(), enabledProviders, customBindings, nil, available)
	_, geminiUnsigned := ExcludeGemini3xOnUnsignedHistory(env, nil, available)
	if len(overflowed) == 0 && len(imageLimited) == 0 && len(geminiUnsigned) == 0 && len(advisorOutranking) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(overflowed)+len(imageLimited)+len(geminiUnsigned)+len(advisorOutranking))
	for _, models := range [][]string{overflowed, imageLimited, geminiUnsigned, advisorOutranking} {
		for _, m := range models {
			out[m] = struct{}{}
		}
	}
	return out
}

func ExcludeImageLimitModels(
	imageCount int,
	enabledProviders map[string]struct{},
	customBindings map[string][]string,
	excluded, available map[string]struct{},
) (map[string]struct{}, []string) {
	if imageCount <= 0 || len(available) == 0 {
		return excluded, nil
	}

	var out map[string]struct{}
	var removed []string
	for model := range available {
		if _, alreadyExcluded := excluded[model]; alreadyExcluded {
			continue
		}
		if len(customBindings[model]) > 0 {
			continue
		}
		entry, ok := catalog.ByID(model)
		if !ok || entry.ImageInput == catalog.ImageInputUnsupported ||
			catalog.AcceptsImageCount(model, imageCount, enabledProviders) {
			continue
		}
		if out == nil {
			out = make(map[string]struct{}, len(excluded)+1)
			for id := range excluded {
				out[id] = struct{}{}
			}
		}
		out[model] = struct{}{}
		removed = append(removed, model)
	}
	if len(removed) == 0 {
		return excluded, nil
	}
	sort.Strings(removed)
	return out, removed
}

// ContextWindowOnlyExclusions returns the models the context-window pre-filter
// excluded and nothing else did. overflowed already omits models excluded
// before the pre-filter ran; admitted models were re-admitted for the upstream
// to decide, and hardExcluded models (gemini-unsigned history, advisor
// pairing) stay out for a reason of their own.
func ContextWindowOnlyExclusions(overflowed, admitted, hardExcluded []string) map[string]struct{} {
	return WithoutModels(WithoutModels(ModelSet(overflowed), admitted), hardExcluded)
}

// ModelSet renders a model ID list as an exclusion set, nil when empty so
// callers keep their existing map untouched.
func ModelSet(ids []string) map[string]struct{} {
	if len(ids) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	return set
}

// PrefilterRequest is the input to Prefilter.
type PrefilterRequest struct {
	Envelope      *translate.RequestEnvelope
	ImageCount    int
	OutputReserve int
	Eligibility   credentials.Eligibility
	// Excluded is the policy exclusion set the request-time filters extend.
	Excluded  map[string]struct{}
	Available map[string]struct{}
	// AdvisorExclusions adds the models the request's advisor tool cannot
	// advise; ExcludeAdvisorOutranking applies it to routing as well as safety.
	AdvisorExclusions        func(excluded map[string]struct{}) (map[string]struct{}, []string)
	ExcludeAdvisorOutranking bool
}

// Prefiltered is the request-time exclusion state handed to the router.
type Prefiltered struct {
	Excluded          map[string]struct{}
	Safety            map[string]struct{}
	ContextWindowOnly map[string]struct{}
	UnsignedHistory   []string
	AdvisorOutranking []string
	ImageLimited      []string
	ContextOverflowed []string
	OverflowAdmitted  []string
	OverflowEstimate  int
}

// Prefilter drops the models this request can never use, then the models
// whose context window it overflows. Hard exclusions run first so a model the
// request can never use does not count as fitting and suppress the
// total-overflow admission.
func Prefilter(req PrefilterRequest) Prefiltered {
	env, available := req.Envelope, req.Available
	enabled, custom := req.Eligibility.EnabledProviders, req.Eligibility.CustomBindings
	var out Prefiltered
	excluded, unsigned := ExcludeGemini3xOnUnsignedHistory(env, req.Excluded, available)
	out.UnsignedHistory = unsigned
	if req.ExcludeAdvisorOutranking {
		excluded, out.AdvisorOutranking = req.AdvisorExclusions(excluded)
	}
	excluded, out.ImageLimited = ExcludeImageLimitModels(req.ImageCount, enabled, custom, excluded, available)
	out.OverflowEstimate = env.ContextOverflowTokenEstimate()
	excluded, out.ContextOverflowed = ExcludeContextOverflowModels(out.OverflowEstimate, env.SignatureTokenSavings(), req.OutputReserve, enabled, excluded, available)
	bindings := router.RequestBindings{Custom: custom, Gateways: req.Eligibility.GatewayProviders}
	out.OverflowAdmitted = AdmitWidestOnTotalOverflow(excluded, out.ContextOverflowed, available, enabled, bindings)
	out.Excluded = WithoutModels(excluded, out.OverflowAdmitted)
	hard := append(append(append([]string(nil), out.UnsignedHistory...), out.AdvisorOutranking...), out.ImageLimited...)
	_, safetyAdvisor := req.AdvisorExclusions(nil)
	out.Safety = WithoutModelsKeep(SafetyExcludedModels(env, req.OutputReserve, enabled, custom, available, safetyAdvisor), out.OverflowAdmitted, hard)
	out.ContextWindowOnly = ContextWindowOnlyExclusions(out.ContextOverflowed, out.OverflowAdmitted, hard)
	return out
}
