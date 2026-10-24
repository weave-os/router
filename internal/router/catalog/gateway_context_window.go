package catalog

import "weave-os/router/internal/providers"

// gatewayContextWindows are the input windows a customer AI gateway enforces,
// measured against the gateway itself. A gateway can cap a model below the
// vendor's window (Snowflake Cortex rejects gpt-6-luna past ~920k input), and
// gateway aliases are tenant data rather than catalog bindings, so the
// override lives here keyed by gateway provider.
var gatewayContextWindows = map[string]map[string]int{
	providers.ProviderAnthropicGateway: {
		"claude-opus-5-5":   1_000_000,
		"claude-sonnet-5-5": 1_000_000,
		"claude-haiku-5-5":  1_000_000,
	},
	providers.ProviderOpenAIGateway: {
		ModelGPT6Luna: 910_000,
	},
}
