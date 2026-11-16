package subscriptionlane_test

import (
	"slices"
	"testing"

	"weave-os/router/internal/subscriptionlane"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var servingEvents = []subscriptionlane.Event{
	subscriptionlane.SubscriptionServed,
	subscriptionlane.CustomerKeyServed,
	subscriptionlane.OrganizationCreditsServed,
}

var paidServingEvents = []subscriptionlane.Event{subscriptionlane.CustomerKeyServed, subscriptionlane.OrganizationCreditsServed}

var planBoundBindings = []subscriptionlane.Binding{
	subscriptionlane.IncludedLane,
	subscriptionlane.SubscriptionAttempt,
	subscriptionlane.OwnPlanPick,
	subscriptionlane.EnterpriseSeat,
}

func pendingStates() []subscriptionlane.State {
	return slices.DeleteFunc(subscriptionlane.States(), subscriptionlane.State.Terminal)
}

func TestBindingRulesEnumerateEveryPairOnceWithAVerdict(t *testing.T) {
	seen := map[[2]int]bool{}
	for _, rule := range subscriptionlane.BindingRules() {
		key := [2]int{int(rule.From), int(rule.On)}
		assert.False(t, seen[key], "%s on %s listed twice", rule.From, rule.On)
		seen[key] = true
		assert.Contains(t, []subscriptionlane.Verdict{subscriptionlane.Allowed, subscriptionlane.Rejected},
			rule.Transition.Verdict, "%s on %s", rule.From, rule.On)
		if rule.Transition.Verdict == subscriptionlane.Allowed {
			assert.Contains(t, subscriptionlane.Bindings(), rule.Transition.To)
		}
	}
	assert.Len(t, seen, len(subscriptionlane.Bindings())*len(subscriptionlane.BindingEvents()))
}

func TestEveryBindingIsReachableFromUnbound(t *testing.T) {
	reached := map[subscriptionlane.Binding]bool{subscriptionlane.Unbound: true}
	frontier := []subscriptionlane.Binding{subscriptionlane.Unbound}
	for len(frontier) > 0 {
		from := frontier[0]
		frontier = frontier[1:]
		for _, on := range subscriptionlane.BindingEvents() {
			if next, ok := subscriptionlane.StepBinding(from, on); ok && !reached[next] {
				reached[next] = true
				frontier = append(frontier, next)
			}
		}
	}
	for _, b := range subscriptionlane.Bindings() {
		assert.True(t, reached[b], "%s is unreachable", b)
	}
}

func TestBindingNamesAreUniqueAcrossTheLane(t *testing.T) {
	names := map[string]bool{}
	for _, s := range subscriptionlane.States() {
		names[s.String()] = true
	}
	for _, e := range subscriptionlane.Events() {
		names[e.String()] = true
	}
	for _, b := range subscriptionlane.Bindings() {
		assert.Regexp(t, `^[A-Z][A-Za-z]+$`, b.String())
		assert.False(t, names[b.String()], b.String())
		names[b.String()] = true
	}
	for _, e := range subscriptionlane.BindingEvents() {
		assert.Regexp(t, `^[A-Z][A-Za-z]+$`, e.String())
		assert.False(t, names[e.String()], e.String())
		names[e.String()] = true
	}
}

func TestEveryBindingAdmitsTheCallersSubscription(t *testing.T) {
	for _, b := range subscriptionlane.Bindings() {
		for _, lane := range pendingStates() {
			served, ok := subscriptionlane.Commit(lane, b, subscriptionlane.SubscriptionServed)
			assert.True(t, ok, "%s / %s", b, lane)
			assert.Equal(t, subscriptionlane.ServedOnSubscription, served)
		}
	}
}

func TestPlanBoundBindingsRefuseEveryPaidServe(t *testing.T) {
	for _, b := range planBoundBindings {
		for _, lane := range pendingStates() {
			for _, on := range paidServingEvents {
				next, ok := subscriptionlane.Commit(lane, b, on)
				assert.False(t, ok, "%s / %s on %s", b, lane, on)
				assert.Equal(t, lane, next)
			}
		}
	}
}

func TestOpenBindingsLeaveThePaidServeToTheLane(t *testing.T) {
	for _, b := range subscriptionlane.Bindings() {
		if slices.Contains(planBoundBindings, b) {
			continue
		}
		for _, lane := range pendingStates() {
			for _, on := range servingEvents {
				wantNext, wantOK := subscriptionlane.Step(lane, on)
				next, ok := subscriptionlane.Commit(lane, b, on)
				assert.Equal(t, wantOK, ok, "%s / %s on %s", b, lane, on)
				assert.Equal(t, wantNext, next, "%s / %s on %s", b, lane, on)
			}
		}
	}
}

func TestCommitOnlyAcceptsServingEvents(t *testing.T) {
	for _, on := range subscriptionlane.Events() {
		if slices.Contains(servingEvents, on) {
			continue
		}
		for _, b := range subscriptionlane.Bindings() {
			assert.Equal(t, subscriptionlane.Rejected, b.Admission(on), "%s on %s", b, on)
			next, ok := subscriptionlane.Commit(subscriptionlane.Funded, b, on)
			assert.False(t, ok)
			assert.Equal(t, subscriptionlane.Funded, next)
		}
	}
}

func TestOnlyTheNamedReleasesLiftAPlanBinding(t *testing.T) {
	releases := map[subscriptionlane.Binding]subscriptionlane.BindingEvent{
		subscriptionlane.IncludedLane:   subscriptionlane.IncludedLaneExhausted,
		subscriptionlane.EnterpriseSeat: subscriptionlane.EnterpriseCapRefused,
	}
	for _, from := range planBoundBindings {
		for _, on := range subscriptionlane.BindingEvents() {
			next, _ := subscriptionlane.StepBinding(from, on)
			if release, named := releases[from]; named && release == on {
				assert.NotContains(t, planBoundBindings, next, "%s on %s", from, on)
				continue
			}
			assert.Contains(t, planBoundBindings, next, "%s on %s lifted the binding", from, on)
		}
	}
}

func TestEnterpriseCapHandoverIsOnlyReachedFromTheSeat(t *testing.T) {
	for _, rule := range subscriptionlane.BindingRules() {
		if rule.Transition.Verdict == subscriptionlane.Allowed && rule.Transition.To == subscriptionlane.EnterpriseCapHandover {
			assert.Equal(t, subscriptionlane.EnterpriseSeat, rule.From)
			assert.Equal(t, subscriptionlane.EnterpriseCapRefused, rule.On)
		}
	}
}

func TestRandomBindingAndLaneWalksNeverFundAPlanBoundOrUnfundedTurn(t *testing.T) {
	laneEvents := subscriptionlane.Events()
	bindingEvents := subscriptionlane.BindingEvents()
	for seed := range uint64(2000) {
		rng := splitMix64(seed)
		lane, binding := subscriptionlane.Funded, subscriptionlane.Unbound
		walk := []string{lane.String(), binding.String()}
		for range 1 + rng.next(16) {
			if rng.next(2) == 0 {
				on := bindingEvents[rng.next(len(bindingEvents))]
				binding, _ = subscriptionlane.StepBinding(binding, on)
				walk = append(walk, on.String())
				continue
			}
			on := laneEvents[rng.next(len(laneEvents))]
			if next, ok := subscriptionlane.Step(lane, on); ok && !next.Terminal() {
				lane = next
				walk = append(walk, on.String())
			}
		}
		for _, on := range paidServingEvents {
			served, ok := subscriptionlane.Commit(lane, binding, on)
			if slices.Contains(planBoundBindings, binding) {
				require.False(t, ok, "%v: plan-bound turn served on %s", walk, on)
			}
			if on == subscriptionlane.OrganizationCreditsServed && slices.Contains(unfundedStates, lane) {
				require.False(t, ok, "%v: unfunded turn served on organization credits", walk)
			}
			if ok {
				require.Equal(t, subscriptionlane.Success, served.Outcome(), "%v", walk)
			}
		}
	}
}
