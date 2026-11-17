package main

import (
	"weave-os/router/internal/providers"
	"weave-os/router/internal/subscriptions"
)

func codexResetClient(providerMap map[string]providers.Client) subscriptions.CodexResetClient {
	client, _ := providerMap[providers.ProviderOpenAI].(subscriptions.CodexResetClient)
	return client
}
