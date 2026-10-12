package subscriptions

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestEarliestResetRandomizesOnlyTiedEarliestCredits(t *testing.T) {
	early := time.Now().Add(time.Hour)
	candidates := []resetCandidate{
		{credit: ResetCredit{ID: "never-expires"}},
		{credit: ResetCredit{ID: "later", ExpiresAt: early.Add(time.Hour)}},
		{credit: ResetCredit{ID: "first-tie", ExpiresAt: early}},
		{credit: ResetCredit{ID: "second-tie", ExpiresAt: early}},
	}
	for i, want := range []string{"first-tie", "second-tie"} {
		winner, ok := earliestReset(candidates, func(n int) int {
			require.Equal(t, 2, n)
			return i
		})
		require.True(t, ok)
		require.Equal(t, want, winner.credit.ID)
	}
	winner, ok := earliestReset(candidates[:1], func(int) int { return 0 })
	require.True(t, ok)
	require.Equal(t, "never-expires", winner.credit.ID)
	_, ok = earliestReset(nil, func(int) int { t.Fatal("empty inventory must not choose"); return 0 })
	require.False(t, ok)
}
