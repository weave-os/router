package catalog

import "weave-os/router/internal/providers"

// snowflakeGatewayContextWindows are the input windows Snowflake Cortex's AI
// Gateway enforces, measured against the gateway itself (2026-10-24). Cortex
// caps gpt-6-luna below the vendor's window (it rejects past ~920k input).
// Other tenants' gateways of the same provider type keep the catalog window.
var snowflakeGatewayContextWindows = map[string]map[string]int{
	providers.ProviderAnthropicGateway: {
		"claude-opus-5-5":   1_000_000,
		"claude-sonnet-5-5": 1_000_000,
		"claude-haiku-5-5":  1_000_000,
	},
	providers.ProviderOpenAIGateway: {
		ModelGPT6Luna: 910_000,
	},
}

// SnowflakeGatewayContextWindow returns the measured window for modelID on a
// Snowflake Cortex gateway of the given provider type, if one was measured.
func SnowflakeGatewayContextWindow(modelID, provider string) (int, bool) {
	w, ok := snowflakeGatewayContextWindows[provider][modelID]
	return w, ok
}
