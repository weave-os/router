package candidates

import "maps"

func SessionDisabledProviders(pinStrikes, historyStrikes []string, forcedProvider string) []string {
	disabled := MergeSessionStrikes(pinStrikes, historyStrikes)
	if forcedProvider == "" {
		return disabled
	}
	kept := make([]string, 0, len(disabled))
	for _, provider := range disabled {
		if provider != forcedProvider {
			kept = append(kept, provider)
		}
	}
	return kept
}

func WithoutProviders(enabled map[string]struct{}, providers []string) map[string]struct{} {
	kept := maps.Clone(enabled)
	for _, provider := range providers {
		delete(kept, provider)
	}
	return kept
}
