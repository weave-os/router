package routingpolicy

import (
	"sort"

	"weave-os/router/internal/credentials"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/translate"
)

func ContextWindow(modelID string, provider ...string) int {
	if router.Lookup(modelID).Supports(router.CapExtendedContext) {
		return 1_000_000
	}
	if len(provider) > 0 && provider[0] != "" {
		return catalog.ContextWindowForBinding(modelID, provider[0])
	}
	return catalog.ContextWindowFor(modelID)
}

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

func StripsAnthropicSignatures(model string) bool {
	m, ok := catalog.ByID(model)
	if !ok {
		return false
	}
	return providers.FamilyFor(m.PrimaryProvider()) != providers.FamilyAnthropic
}

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

func ContextWindowOnlyExclusions(overflowed, admitted, hardExcluded []string) map[string]struct{} {
	return WithoutModels(WithoutModels(ModelSet(overflowed), admitted), hardExcluded)
}

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

type PrefilterRequest struct {
	Envelope                 *translate.RequestEnvelope
	ImageCount               int
	OutputReserve            int
	Eligibility              credentials.Eligibility
	Excluded                 map[string]struct{}
	Available                map[string]struct{}
	AdvisorExclusions        func(excluded map[string]struct{}) (map[string]struct{}, []string)
	ExcludeAdvisorOutranking bool
}

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

func Prefilter(req PrefilterRequest) Prefiltered {
	env, available := req.Envelope, req.Available
	enabled, customBindings := req.Eligibility.EnabledProviders, req.Eligibility.CustomBindings
	var out Prefiltered
	excluded, unsigned := ExcludeGemini3xOnUnsignedHistory(env, req.Excluded, available)
	out.UnsignedHistory = unsigned
	if req.ExcludeAdvisorOutranking {
		excluded, out.AdvisorOutranking = req.AdvisorExclusions(excluded)
	}
	excluded, out.ImageLimited = ExcludeImageLimitModels(req.ImageCount, enabled, customBindings, excluded, available)
	out.OverflowEstimate = env.ContextOverflowTokenEstimate()
	excluded, out.ContextOverflowed = ExcludeContextOverflowModels(out.OverflowEstimate, env.SignatureTokenSavings(), req.OutputReserve, enabled, excluded, available)
	out.OverflowAdmitted = AdmitWidestOnTotalOverflow(excluded, out.ContextOverflowed, available, enabled, router.RequestBindings{Custom: customBindings, Gateways: req.Eligibility.GatewayProviders})
	out.Excluded = WithoutModels(excluded, out.OverflowAdmitted)
	hardExcluded := append(append(append([]string(nil), out.UnsignedHistory...), out.AdvisorOutranking...), out.ImageLimited...)
	_, safetyAdvisor := req.AdvisorExclusions(nil)
	out.Safety = WithoutModelsKeep(SafetyExcludedModels(env, req.OutputReserve, enabled, customBindings, available, safetyAdvisor), out.OverflowAdmitted, hardExcluded)
	out.ContextWindowOnly = ContextWindowOnlyExclusions(out.ContextOverflowed, out.OverflowAdmitted, hardExcluded)
	return out
}
