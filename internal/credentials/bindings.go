package credentials

import (
	"sort"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/catalog"
)

// GatewayProviders returns BYOK gateway providers for this request.
// Deployment-level gateway keys are excluded: they keep serving catalog bindings.
func GatewayProviders(keys []*auth.ExternalAPIKey) map[string]struct{} {
	var out map[string]struct{}
	for _, key := range keys {
		if len(key.Plaintext) == 0 || !providers.IsGateway(key.Provider) {
			continue
		}
		if out == nil {
			out = make(map[string]struct{}, len(keys))
		}
		out[key.Provider] = struct{}{}
	}
	return out
}

// CustomBindings derives configuration-declared provider bindings
// from BYOK keys: model_aliases names the catalog models a custom endpoint
// serves; catalog bindings rank first so a direct vendor still wins.
func CustomBindings(keys []*auth.ExternalAPIKey) map[string][]string {
	return AliasedBindings(keys, false)
}

// AliasedBindings is CustomBindings, optionally keeping retired catalog models.
func AliasedBindings(keys []*auth.ExternalAPIKey, includeRetired bool) map[string][]string {
	var out map[string][]string
	for _, key := range keys {
		// Mirrors enabledProviders: a key with no usable plaintext
		// would enroll a provider whose upstream call then 401s.
		if len(key.Plaintext) == 0 {
			continue
		}
		for model := range key.ModelAliases {
			m, known := catalog.ByID(model)
			if !known || (m.Tier == catalog.TierUnknown && !includeRetired) {
				continue
			}
			if out == nil {
				out = make(map[string][]string)
			}
			out[m.ID] = append(out[m.ID], key.Provider)
		}
	}
	// Alias maps iterate randomly; without a stable order two identical
	// installations could pick different endpoints for the same model.
	for _, provs := range out {
		sort.Strings(provs)
	}
	return out
}
