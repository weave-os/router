package subscriptionlane_test

import (
	"slices"
	"testing"

	"weave-os/router/internal/refusal"
	"weave-os/router/internal/subscriptionlane"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRulesEnumerateEveryPairOnceWithAVerdict(t *testing.T) {
	seen := map[[2]int]bool{}
	for _, rule := range subscriptionlane.Rules() {
		key := [2]int{int(rule.From), int(rule.On)}
		assert.False(t, seen[key], "%s on %s listed twice", rule.From, rule.On)
		seen[key] = true
		assert.Contains(t, []subscriptionlane.Verdict{subscriptionlane.Allowed, subscriptionlane.Rejected},
			rule.Transition.Verdict, "%s on %s", rule.From, rule.On)
		if rule.Transition.Verdict == subscriptionlane.Allowed {
			assert.Contains(t, subscriptionlane.States(), rule.Transition.To)
		}
	}
	assert.Len(t, seen, len(subscriptionlane.States())*len(subscriptionlane.Events()))
}

func TestTerminalStatesRejectEveryEvent(t *testing.T) {
	for _, from := range subscriptionlane.States() {
		if !from.Terminal() {
			continue
		}
		for _, on := range subscriptionlane.Events() {
			next, ok := subscriptionlane.Step(from, on)
			assert.False(t, ok, "%s on %s", from, on)
			assert.Equal(t, from, next)
		}
	}
}

func TestEveryStateIsReachableFromFunded(t *testing.T) {
	reached := map[subscriptionlane.State]bool{subscriptionlane.Funded: true}
	frontier := []subscriptionlane.State{subscriptionlane.Funded}
	for len(frontier) > 0 {
		from := frontier[0]
		frontier = frontier[1:]
		for _, on := range subscriptionlane.Events() {
			if next, ok := subscriptionlane.Step(from, on); ok && !reached[next] {
				reached[next] = true
				frontier = append(frontier, next)
			}
		}
	}
	for _, s := range subscriptionlane.States() {
		assert.True(t, reached[s], "%s is unreachable", s)
	}
}

func TestEveryPendingStateCanTerminate(t *testing.T) {
	for _, from := range subscriptionlane.States() {
		if from.Terminal() {
			continue
		}
		assert.True(t, slices.ContainsFunc(subscriptionlane.Events(), func(on subscriptionlane.Event) bool {
			next, ok := subscriptionlane.Step(from, on)
			return ok && next.Terminal()
		}), from.String())
	}
}

func TestTerminalStatesMapToSuccessOrARefusalKind(t *testing.T) {
	refusedKinds := map[refusal.Kind]subscriptionlane.State{}
	for _, s := range subscriptionlane.States() {
		kind, refused := s.Refusal()
		switch s.Outcome() {
		case subscriptionlane.Pending:
			assert.False(t, refused, s.String())
		case subscriptionlane.Success:
			assert.False(t, refused, s.String())
		case subscriptionlane.Refused:
			require.True(t, refused, s.String())
			assert.Contains(t, refusal.Kinds(), kind, s.String())
			other, dup := refusedKinds[kind]
			assert.False(t, dup, "%s and %s share a refusal kind", s, other)
			refusedKinds[kind] = s
		}
	}
}

func TestStateAndEventNamesAreUniqueIdentifiers(t *testing.T) {
	names := map[string]bool{}
	for _, s := range subscriptionlane.States() {
		assert.Regexp(t, `^[A-Z][A-Za-z]+$`, s.String())
		assert.False(t, names[s.String()], s.String())
		names[s.String()] = true
	}
	for _, e := range subscriptionlane.Events() {
		assert.Regexp(t, `^[A-Z][A-Za-z]+$`, e.String())
		assert.False(t, names[e.String()], e.String())
		names[e.String()] = true
	}
}

var unfundedStates = []subscriptionlane.State{subscriptionlane.SpendCapReached, subscriptionlane.CreditsDepleted}

var paidReachableStates = []subscriptionlane.State{
	subscriptionlane.Funded,
	subscriptionlane.LinkedFirst,
	subscriptionlane.ServedOnOrganizationCredits,
}

func forbiddenTransition(from, to subscriptionlane.State, on subscriptionlane.Event) string {
	switch {
	case from.Terminal():
		return "left a terminal state"
	case slices.Contains(unfundedStates, from) && slices.Contains(paidReachableStates, to):
		return "an unfunded turn regained organization-paid capacity"
	case from == subscriptionlane.CreditsDepleted && to == subscriptionlane.SpendCapReached:
		return "a depleted mark was downgraded to a spend cap"
	case from == subscriptionlane.LinkedFirst && to == subscriptionlane.Funded && on != subscriptionlane.LinkedPlanReleased:
		return "linked-first was dropped without a release"
	case from == subscriptionlane.Funded && to == subscriptionlane.LinkedFirst && on != subscriptionlane.LinkedPlanPreferred:
		return "linked-first was set without a preference"
	}
	return ""
}

func TestRandomEventSequencesNeverTakeAForbiddenTransition(t *testing.T) {
	events := subscriptionlane.Events()
	for seed := range uint64(2000) {
		rng := splitMix64(seed)
		state := subscriptionlane.Funded
		walk := []string{state.String()}
		for range 1 + rng.next(16) {
			on := events[rng.next(len(events))]
			next, ok := subscriptionlane.Step(state, on)
			walk = append(walk, on.String())
			if !ok {
				require.Equal(t, state, next, "rejected step moved the lane: %v", walk)
				continue
			}
			require.Empty(t, forbiddenTransition(state, next, on), "%v -> %s", walk, next)
			state = next
		}
		_, refused := state.Refusal()
		assert.Equal(t, state.Outcome() == subscriptionlane.Refused, refused, "%v", walk)
	}
}

type splitMix64 uint64

func (s *splitMix64) next(n int) int {
	*s += 0x9e3779b97f4a7c15
	z := uint64(*s)
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	z ^= z >> 31
	return int(z % uint64(n))
}
