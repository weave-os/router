package selection

import (
	"slices"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/hmm/rosterdata"
)

var harnessSubscriptionProvider = map[rosterdata.Harness]string{
	rosterdata.HarnessClaudeCode: providers.ProviderAnthropic,
	rosterdata.HarnessCodex:      providers.ProviderOpenAI,
}

func SubscriptionLadder(harness string, subscriptionProviders []string) string {
	if len(subscriptionProviders) == 0 {
		return rosterdata.NoSubscriptionLadderKey
	}
	if native, ok := harnessSubscriptionProvider[rosterdata.SelectionHarness(harness)]; ok {
		if slices.Contains(subscriptionProviders, native) {
			return native
		}
		return ""
	}
	if len(subscriptionProviders) == 1 {
		return subscriptionProviders[0]
	}
	return ""
}

func leadWith(order []string, arm string) []string {
	if arm == "" {
		return order
	}
	led := make([]string, 0, len(order)+1)
	led = append(led, arm)
	for _, member := range order {
		if member != arm {
			led = append(led, member)
		}
	}
	return led
}
