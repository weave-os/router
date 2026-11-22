package credentials

import (
	"context"
	"net/http"

	"github.com/google/uuid"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
)

func ByokServedForProvider(ctx context.Context, provider string) bool {
	if provider == "" {
		return false
	}
	for _, key := range ExternalKeysFromContext(ctx) {
		if key.Provider == provider && len(key.Plaintext) > 0 {
			return true
		}
	}
	return false
}

func (b *Brokerage) AnthropicFallbackKeyAvailable(ctx context.Context) bool {
	if byok := BuildCredentialsMap(ExternalKeysFromContext(ctx)); byok != nil {
		if _, ok := byok[providers.ProviderAnthropic]; ok {
			return true
		}
	}
	if b.KeyedProviders != nil {
		if _, ok := b.KeyedProviders[providers.ProviderAnthropic]; ok {
			return true
		}
	}
	return false
}

func (b *Brokerage) AnthropicCredentialReachable(ctx context.Context, headers http.Header, deploymentClient func(provider string) bool) bool {
	if b.AnthropicFallbackKeyAvailable(ctx) {
		return true
	}
	if b.KeyedProviders == nil && !b.BYOKOnly {
		if deploymentClient(providers.ProviderAnthropic) {
			return true
		}
	}
	return requestcontext.ExtractClientCredentials(providers.ProviderAnthropic, headers) != nil
}

func (b *Brokerage) HasOpenAIInfrastructureCredential(ctx context.Context, headers http.Header, deploymentClient func(provider string) bool) bool {
	if ByokServedForProvider(ctx, providers.ProviderOpenAI) {
		return true
	}
	if !b.BYOKOnly {
		if b.KeyedProviders == nil {
			if deploymentClient(providers.ProviderOpenAI) {
				return true
			}
		} else if _, keyed := b.KeyedProviders[providers.ProviderOpenAI]; keyed {
			return true
		}
	}
	if requestcontext.InstallationIDFromContext(ctx) == uuid.Nil {
		if client := requestcontext.ExtractClientCredentials(providers.ProviderOpenAI, headers); client != nil && !client.OAuth {
			return true
		}
	}
	return false
}
