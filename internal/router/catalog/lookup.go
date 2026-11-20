package catalog

import (
	"fmt"
	"sort"
	"strings"

	"weave-os/router/internal/router"
)

var byID map[string]Model

func init() {
	byID = make(map[string]Model, len(Models))
	for _, m := range Models {
		byID[m.ID] = m
	}
}

func ByID(id string) (Model, bool) {
	if m, ok := byID[id]; ok {
		return m, true
	}
	if base := router.StripDateSuffix(id); base != id {
		if m, ok := byID[base]; ok {
			return m, true
		}
	}
	return Model{}, false
}

func CodexSubscriptionModels() []string {
	models := make([]string, 0, 8)
	for _, model := range Models {
		if model.CodexSubscription {
			models = append(models, model.ID)
		}
	}
	return models
}

func CodexSubscriptionCoversModel(id string) bool {
	model, ok := byID[id]
	return ok && model.CodexSubscription
}

func ResolveBinding(id string, available map[string]struct{}) (ProviderBinding, bool) {
	m, ok := ByID(id)
	if !ok {
		return ProviderBinding{}, false
	}
	for _, b := range RoutableBindings(m) {
		if _, ok := available[b.Provider]; ok {
			return b, true
		}
	}
	return ProviderBinding{}, false
}

func RoutableBindings(m Model) []ProviderBinding {
	out := make([]ProviderBinding, 0, len(m.Providers))
	for _, b := range m.Providers {
		if !b.SubscriptionOverflowOnly {
			out = append(out, b)
		}
	}
	return out
}

func (b ProviderBinding) AcceptsImageCount(n int) bool {
	return b.MaxImagesPerRequest == 0 || n <= b.MaxImagesPerRequest
}

func AcceptsImageCount(id string, n int, enabledProviders map[string]struct{}) bool {
	if n == 0 {
		return true
	}
	m, ok := ByID(id)
	if !ok {
		return true
	}
	if m.ImageInput == ImageInputUnsupported {
		return false
	}
	foundEnabledBinding := false
	for _, binding := range RoutableBindings(m) {
		if len(enabledProviders) > 0 {
			if _, enabled := enabledProviders[binding.Provider]; !enabled {
				continue
			}
		}
		foundEnabledBinding = true
		if binding.AcceptsImageCount(n) {
			return true
		}
	}
	return !foundEnabledBinding
}

func BindingFor(id, provider string) (ProviderBinding, bool) {
	m, ok := ByID(id)
	if !ok {
		return ProviderBinding{}, false
	}
	for _, b := range m.Providers {
		if b.Provider == provider {
			return b, true
		}
	}
	return ProviderBinding{}, false
}

func ResolveBindingWithCustom(id string, available map[string]struct{}, custom map[string][]string) (ProviderBinding, bool) {
	if b, ok := ResolveBinding(id, available); ok {
		return b, true
	}
	m, ok := ByID(id)
	if !ok {
		return ProviderBinding{}, false
	}
	provider := CustomProviderFor(id, available, custom)
	if provider == "" {
		return ProviderBinding{}, false
	}
	binding := ProviderBinding{Provider: provider}
	if len(m.Providers) > 0 {
		binding.Price = m.Providers[0].Price.WithoutInferenceGeoPremium()
	}
	return binding, true
}

func CustomProviderFor(id string, available map[string]struct{}, custom map[string][]string) string {
	if ps := customProvidersFor(id, available, custom); len(ps) > 0 {
		return ps[0]
	}
	return ""
}

func customProvidersFor(id string, available map[string]struct{}, custom map[string][]string) []string {
	if len(custom) == 0 {
		return nil
	}
	m, known := ByID(id)
	if !known {
		return nil
	}
	bound := make(map[string]struct{}, len(m.Providers))
	for _, b := range RoutableBindings(m) {
		bound[b.Provider] = struct{}{}
	}
	var out []string
	for _, provider := range custom[m.ID] {
		if _, ok := available[provider]; !ok {
			continue
		}
		if _, dup := bound[provider]; dup {
			continue
		}
		out = append(out, provider)
	}
	return out
}

func EnumerateBindingsWithCustom(id string, available map[string]struct{}, custom map[string][]string) []IndexedBinding {
	out := EnumerateBindings(id, available)
	customProviders := customProvidersFor(id, available, custom)
	if len(customProviders) == 0 {
		return out
	}
	m, _ := ByID(id)
	next := len(m.Providers)
	for _, provider := range customProviders {
		binding := ProviderBinding{Provider: provider}
		if len(m.Providers) > 0 {
			binding.Price = m.Providers[0].Price.WithoutInferenceGeoPremium()
		}
		out = append(out, IndexedBinding{Index: next, ProviderBinding: binding})
		next++
	}
	return out
}

func AvailableBindings(id string, available map[string]struct{}) []ProviderBinding {
	indexed := EnumerateBindings(id, available)
	out := make([]ProviderBinding, 0, len(indexed))
	for _, binding := range indexed {
		out = append(out, binding.ProviderBinding)
	}
	return out
}

type IndexedBinding struct {
	Index int
	ProviderBinding
}

func EnumerateBindings(id string, available map[string]struct{}) []IndexedBinding {
	m, ok := ByID(id)
	if !ok {
		return nil
	}
	out := make([]IndexedBinding, 0, len(m.Providers))
	for index, b := range m.Providers {
		if _, ok := available[b.Provider]; ok && !b.SubscriptionOverflowOnly {
			out = append(out, IndexedBinding{Index: index, ProviderBinding: b})
		}
	}
	return out
}

func RoutingTargetSet(availableProviders map[string]struct{}) map[string]struct{} {
	out := make(map[string]struct{}, len(Models))
	for _, model := range Models {
		if model.Tier == TierUnknown {
			continue
		}
		if len(EnumerateBindings(model.ID, availableProviders)) == 0 {
			continue
		}
		out[model.ID] = struct{}{}
	}
	return out
}

func HMMRoutingTargetSet(availableProviders map[string]struct{}) map[string]struct{} {
	out := RoutingTargetSet(availableProviders)
	for _, model := range Models {
		if !model.HMMTarget || len(EnumerateBindings(model.ID, availableProviders)) == 0 {
			continue
		}
		out[model.ID] = struct{}{}
	}
	return out
}

func UpstreamIDFor(catalogID, bindingID string) string {
	if bindingID != "" {
		return bindingID
	}
	return catalogID
}

func PriceFor(provider, id string) (Pricing, bool) {
	m, ok := ByID(id)
	if !ok {
		return Pricing{}, false
	}
	for _, b := range m.Providers {
		if b.Provider == provider {
			return b.Price, true
		}
	}
	return Pricing{}, false
}

func PrimaryPriceFor(id string) (Pricing, bool) {
	m, ok := ByID(id)
	if !ok {
		return Pricing{}, false
	}
	if len(m.Providers) == 0 {
		return Pricing{}, false
	}
	return m.Providers[0].Price, true
}

func TierFor(id string) Tier {
	m, ok := ByID(id)
	if !ok {
		return TierUnknown
	}
	return m.Tier
}

func AdvisorRankFor(id string) (rank int, ok bool) {
	m, found := ByID(id)
	return m.AdvisorRank, found && m.AdvisorRank > 0
}

func ThinkTagReasoningFor(id string) bool {
	m, ok := ByID(id)
	if !ok {
		return false
	}
	return m.ThinkTagReasoning
}

const DefaultContextWindow = 128_000

func ContextWindowFor(id string) int {
	m, ok := ByID(id)
	if !ok || m.ContextWindow <= 0 {
		return DefaultContextWindow
	}
	return m.ContextWindow
}

func ContextWindowForBinding(modelID, provider string) int {
	m, ok := ByID(modelID)
	if !ok {
		return DefaultContextWindow
	}
	cw := m.ContextWindow
	if cw <= 0 {
		cw = DefaultContextWindow
	}
	for _, b := range m.Providers {
		if b.Provider == provider && b.ContextWindow > 0 {
			return b.ContextWindow
		}
	}
	return cw
}

func ToolUseLowSet() map[string]struct{} {
	out := make(map[string]struct{}, len(Models))
	for _, m := range Models {
		if m.ToolUseQuality == ToolUseLow {
			out[m.ID] = struct{}{}
		}
	}
	return out
}

func AgenticLowSet() map[string]struct{} {
	out := make(map[string]struct{}, len(Models))
	for _, m := range Models {
		if m.AgenticUse == AgenticLow {
			out[m.ID] = struct{}{}
		}
	}
	return out
}

func CustomToolUnsupportedSet() map[string]struct{} {
	out := make(map[string]struct{})
	for _, m := range Models {
		if m.CustomToolInput == CustomToolInputUnsupported {
			out[m.ID] = struct{}{}
		}
	}
	return out
}

func ImageUnsupportedSet() map[string]struct{} {
	out := make(map[string]struct{}, len(Models))
	for _, m := range Models {
		if m.ImageInput == ImageInputUnsupported {
			out[m.ID] = struct{}{}
		}
	}
	return out
}

func AcceptsImages(id string) bool {
	m, ok := ByID(id)
	if !ok {
		return true
	}
	return m.ImageInput != ImageInputUnsupported
}

func AcceptsCustomTools(id string) bool {
	m, ok := ByID(id)
	return !ok || m.CustomToolInput != CustomToolInputUnsupported
}

func AllPrimaryPricing() map[string]Pricing {
	out := make(map[string]Pricing, len(Models))
	for _, m := range Models {
		if len(m.Providers) == 0 {
			continue
		}
		out[m.ID] = m.Providers[0].Price
	}
	return out
}

func ValidateDeployed(deployed []string) error {
	var missing []string
	for _, id := range deployed {
		m, ok := ByID(id)
		if !ok {
			missing = append(missing, id+" (not in catalog)")
			continue
		}
		if m.Tier == TierUnknown {
			missing = append(missing, id+" (no tier set)")
		}
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)
	return fmt.Errorf("catalog: deployed models missing or unconfigured — add or fix them in internal/router/catalog/catalog.go: %s", strings.Join(missing, ", "))
}
