package catalog

import "weave-os/router/internal/providers"

var cortexGatewayContextWindows = map[string]map[string]int{
	providers.ProviderAnthropicGateway: {
		"claude-opus-5-5":   1_000_000,
		"claude-sonnet-5-5": 1_000_000,
		"claude-haiku-5-5":  1_000_000,
	},
	providers.ProviderOpenAIGateway: {
		ModelGPT6Luna: 910_000,
	},
}

func CortexGatewayContextWindow(modelID, provider string) (int, bool) {
	w, ok := cortexGatewayContextWindows[provider][modelID]
	return w, ok
}
