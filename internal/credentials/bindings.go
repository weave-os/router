package credentials

import (
	"sort"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/catalog"
)

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

func CustomBindings(keys []*auth.ExternalAPIKey) map[string][]string {
	return AliasedBindings(keys, false)
}

func AliasedBindings(keys []*auth.ExternalAPIKey, includeRetired bool) map[string][]string {
	var out map[string][]string
	for _, key := range keys {
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
	for _, provs := range out {
		sort.Strings(provs)
	}
	return out
}
