package subscriptionlane_test

import (
	"testing"

	"weave-os/router/internal/subscriptionlane"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var windowRank = map[subscriptionlane.AllowanceEvent]int{
	subscriptionlane.SixHourWindowSpent: 1,
	subscriptionlane.WeeklyWindowSpent:  2,
	subscriptionlane.BillingWindowSpent: 3,
}

var allowanceRank = map[subscriptionlane.Allowance]int{
	subscriptionlane.AllowanceUnspent:      0,
	subscriptionlane.AllowanceSixHourSpent: 1,
	subscriptionlane.AllowanceWeeklySpent:  2,
	subscriptionlane.AllowanceBillingSpent: 3,
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
	assert.Len(t, allowanceRank, len(subscriptionlane.Allowances()))
	assert.Len(t, windowRank, len(subscriptionlane.AllowanceEvents()))
}

func TestEveryAllowanceIsReachableFromUnspent(t *testing.T) {
	reached := map[subscriptionlane.Allowance]bool{subscriptionlane.AllowanceUnspent: true}
	frontier := []subscriptionlane.Allowance{subscriptionlane.AllowanceUnspent}
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

func TestASpentWindowKeepsTheLongerOfTheTwo(t *testing.T) {
	for _, rule := range subscriptionlane.AllowanceRules() {
		require.Equal(t, subscriptionlane.Allowed, rule.Transition.Verdict, "%s on %s", rule.From, rule.On)
		assert.Equal(t, max(allowanceRank[rule.From], windowRank[rule.On]), allowanceRank[rule.Transition.To], "%s on %s", rule.From, rule.On)
	}
}

func TestSpentWindowsSettleOnTheLongestInAnyOrder(t *testing.T) {
	events := subscriptionlane.AllowanceEvents()
	for seed := range uint64(2000) {
		rng := splitMix64(seed)
		allowance := subscriptionlane.AllowanceUnspent
		rank := 0
		var walk []string
		for range 1 + rng.next(8) {
			on := events[rng.next(len(events))]
			walk = append(walk, on.String())
			next, ok := subscriptionlane.StepAllowance(allowance, on)
			require.True(t, ok, "%v", walk)
			require.GreaterOrEqual(t, allowanceRank[next], allowanceRank[allowance], "%v: a spent window was forgotten", walk)
			allowance = next
			rank = max(rank, windowRank[on])
		}
		assert.Equal(t, rank, allowanceRank[allowance], "%v", walk)
	}
}
