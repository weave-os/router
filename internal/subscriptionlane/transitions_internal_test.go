package subscriptionlane

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEveryPendingPairHasAnExplicitVerdict(t *testing.T) {
	for on := range eventCount {
		for from := range pendingStateCount {
			assert.NotEqual(t, unspecified, transitions[on][from].Verdict, "%s on %s", from, on)
		}
	}
}

func TestPendingRowsCoverExactlyThePendingStates(t *testing.T) {
	for _, s := range States() {
		assert.Equal(t, s < pendingStateCount, !s.Terminal(), s.String())
	}
}

func TestRejectedEntriesNameNoTarget(t *testing.T) {
	for on := range eventCount {
		for from := range pendingStateCount {
			if transitions[on][from].Verdict == Rejected {
				assert.Equal(t, rejected, transitions[on][from], "%s on %s", from, on)
			}
		}
	}
}
