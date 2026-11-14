package main

import (
	"weave-os/router/internal/providers"
	"weave-os/router/internal/subscriptions"
)

// codexResetClient returns the OpenAI adapter's earned-reset client, or nil
// when the configured OpenAI client cannot manage Codex resets.
func codexResetClient(providerMap map[string]providers.Client) subscriptions.CodexResetClient {
	client, _ := providerMap[providers.ProviderOpenAI].(subscriptions.CodexResetClient)
	return client
}
