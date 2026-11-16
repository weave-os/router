package subscriptionlane_test

import (
	"testing"

	"weave-os/router/internal/subscriptionlane"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var spentEvents = []subscriptionlane.AllowanceEvent{
	subscriptionlane.SixHourWindowSpent,
	subscriptionlane.WeeklyWindowSpent,
	subscriptionlane.BillingWindowSpent,
}

func TestAllowanceRulesEnumerateEveryPairOnceWithAVerdict(t *testing.T) {
	seen := make(map[[2]int]bool)
	for _, rule := range subscriptionlane.AllowanceRules() {
		key := [2]int{int(rule.From), int(rule.On)}
		require.False(t, seen[key], "%s on %s listed twice", rule.From, rule.On)
		seen[key] = true
		require.Contains(t, []subscriptionlane.Verdict{subscriptionlane.Allowed, subscriptionlane.Rejected}, rule.Transition.Verdict, "%s on %s", rule.From, rule.On)
		if rule.Transition.Verdict == subscriptionlane.Rejected {
			assert.Zero(t, rule.Transition.To, "%s on %s", rule.From, rule.On)
		}
	}
	assert.Len(t, seen, len(subscriptionlane.Allowances())*len(subscriptionlane.AllowanceEvents()))
}

func TestEveryAllowanceIsReachableFromUnscoped(t *testing.T) {
	reached := map[subscriptionlane.Allowance]bool{subscriptionlane.AllowanceUnscoped: true}
	frontier := []subscriptionlane.Allowance{subscriptionlane.AllowanceUnscoped}
	for len(frontier) > 0 {
		from := frontier[0]
		frontier = frontier[1:]
		for _, on := range subscriptionlane.AllowanceEvents() {
			if next, ok := subscriptionlane.StepAllowance(from, on); ok && !reached[next] {
				reached[next] = true
				frontier = append(frontier, next)
			}
		}
	}
	for _, allowance := range subscriptionlane.Allowances() {
		assert.True(t, reached[allowance], "%s unreachable", allowance)
	}
}

func TestAllowanceIsAdmittedOnlyAfterThePlanIsScoped(t *testing.T) {
	for _, on := range subscriptionlane.AllowanceEvents() {
		next, ok := subscriptionlane.StepAllowance(subscriptionlane.AllowanceUnscoped, on)
		if on == subscriptionlane.PlanScoped {
			assert.True(t, ok)
			assert.Equal(t, subscriptionlane.AllowanceNotSubscribed, next)
			continue
		}
		assert.False(t, ok, "%s", on)
		assert.Equal(t, subscriptionlane.AllowanceUnscoped, next, "%s", on)
	}
}

func TestPlanScopingNeverChangesAnAdmission(t *testing.T) {
	for _, from := range subscriptionlane.Allowances() {
		if from == subscriptionlane.AllowanceUnscoped {
			continue
		}
		next, ok := subscriptionlane.StepAllowance(from, subscriptionlane.PlanScoped)
		assert.True(t, ok, "%s", from)
		assert.Equal(t, from, next, "%s", from)
	}
}

func TestCoveredAndSpentAllowancesNeverMeet(t *testing.T) {
	for _, on := range spentEvents {
		_, ok := subscriptionlane.StepAllowance(subscriptionlane.AllowanceCovered, on)
		assert.False(t, ok, "covered allowance took %s", on)
	}
	for _, from := range subscriptionlane.Allowances() {
		if !from.Spent() {
			continue
		}
		_, ok := subscriptionlane.StepAllowance(from, subscriptionlane.IncludedUsageCovered)
		assert.False(t, ok, "%s became covered", from)
		assert.False(t, from.Covers(), "%s", from)
	}
	assert.True(t, subscriptionlane.AllowanceCovered.Covers())
	assert.False(t, subscriptionlane.AllowanceCovered.Spent())
}

func TestSpentWindowsSettleOnTheLongestInAnyOrder(t *testing.T) {
	longest := map[subscriptionlane.AllowanceEvent]int{
		subscriptionlane.SixHourWindowSpent: 1,
		subscriptionlane.WeeklyWindowSpent:  2,
		subscriptionlane.BillingWindowSpent: 3,
	}
	settled := map[int]subscriptionlane.Allowance{
		1: subscriptionlane.AllowanceSixHourSpent,
		2: subscriptionlane.AllowanceWeeklySpent,
		3: subscriptionlane.AllowanceBillingSpent,
	}
	for seed := range uint64(2000) {
		rng := splitMix64(seed)
		allowance := subscriptionlane.AllowanceNotSubscribed
		rank := 0
		var walk []string
		for range 1 + rng.next(8) {
			on := spentEvents[rng.next(len(spentEvents))]
			walk = append(walk, on.String())
			next, ok := subscriptionlane.StepAllowance(allowance, on)
			require.True(t, ok, "%v", walk)
			allowance = next
			rank = max(rank, longest[on])
			require.True(t, allowance.Spent(), "%v", walk)
		}
		assert.Equal(t, settled[rank], allowance, "%v", walk)
	}
}
