package catalog

import "weave-os/router/internal/providers"

const anthropicMaxRequestBodyBytes = 32 * 1024 * 1024

var providerMaxRequestBodyBytes = map[string]int{
	providers.ProviderAnthropic: anthropicMaxRequestBodyBytes,
}

func MaxRequestBodyBytesForProvider(provider string) int {
	return providerMaxRequestBodyBytes[provider]
}

func (b ProviderBinding) AcceptsRequestBodyBytes(n int) bool {
	limit := MaxRequestBodyBytesForProvider(b.Provider)
	return limit == 0 || n <= limit
}
