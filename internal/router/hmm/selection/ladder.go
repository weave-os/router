package selection

import (
	"slices"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/hmm/rosterdata"
)

type LadderChoice int

const (
	LadderNotApplied LadderChoice = iota
	LadderNoSubscription
	LadderSubscription
)

type Ladder struct {
	Choice   LadderChoice
	Provider string
}

var harnessSubscriptionProvider = map[rosterdata.Harness]string{
	rosterdata.HarnessClaudeCode: providers.ProviderAnthropic,
	rosterdata.HarnessCodex:      providers.ProviderOpenAI,
}

func SubscriptionLadder(harness string, subscriptionProviders []string) Ladder {
	if len(subscriptionProviders) == 0 {
		return Ladder{Choice: LadderNoSubscription}
	}
	if native, ok := harnessSubscriptionProvider[rosterdata.SelectionHarness(harness)]; ok {
		if slices.Contains(subscriptionProviders, native) {
			return Ladder{Choice: LadderSubscription, Provider: native}
		}
		return Ladder{}
	}
	if len(subscriptionProviders) == 1 {
		return Ladder{Choice: LadderSubscription, Provider: subscriptionProviders[0]}
	}
	return Ladder{}
}

func (l Ladder) LeadArmEvenOutsideGroup(roster *rosterdata.Roster, group rosterdata.Group) string {
	switch l.Choice {
	case LadderNoSubscription:
		return roster.SubscriptionLadders[rosterdata.NoSubscriptionLadderKey][group]
	case LadderSubscription:
		return roster.SubscriptionLadders[l.Provider][group]
	case LadderNotApplied:
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
