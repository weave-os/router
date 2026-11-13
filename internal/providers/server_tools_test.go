package providers_test

import (
	"testing"

	"weave-os/router/internal/providers"
)

func TestSupportsAnthropicServerTools(t *testing.T) {
	cases := map[string]bool{
		providers.ProviderAnthropic:        true,
		providers.ProviderAnthropicGateway: false,
		providers.ProviderOpenAI:           false,
		providers.ProviderBedrock:          false,
	}
	for provider, want := range cases {
		if got := providers.SupportsAnthropicServerTools(provider); got != want {
			t.Errorf("SupportsAnthropicServerTools(%q) = %v, want %v", provider, got, want)
		}
	}
}

func TestAnthropicGatewayShareFamilyButNotServerTools(t *testing.T) {
	if providers.FamilyFor(providers.ProviderAnthropicGateway) != providers.FamilyFor(providers.ProviderAnthropic) {
		t.Fatal("the gateway must stay Anthropic-family: it speaks the Messages wire format")
	}
	if providers.SupportsAnthropicServerTools(providers.ProviderAnthropicGateway) {
		t.Fatal("wire-format compatibility must not imply server-tool support")
	}
}
