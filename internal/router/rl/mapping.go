package rl

import (
	"strings"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/catalog"
)

var rosterAliases = map[string]string{
	"claude-sonnet-4-6": "anthropic/claude-sonnet-4.6",
	"claude-haiku-4-5":  "anthropic/claude-haiku-4.5",
}

func rosterIDFor(m catalog.Model) string {
	if alias, ok := rosterAliases[m.ID]; ok {
		return alias
	}
	if strings.Contains(m.ID, "/") {
		return m.ID
	}
	switch m.PrimaryProvider() {
	case providers.ProviderAnthropic, providers.ProviderAnthropicGateway:
		return "anthropic/" + m.ID
	case providers.ProviderOpenAI:
		return "openai/" + m.ID
	case providers.ProviderGoogle:
		return "google/" + m.ID
	case providers.ProviderXAI:
		return "x-ai/" + m.ID
	}
	return m.ID
}
