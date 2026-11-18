package subscriptionlane

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEveryBindingPairHasAnExplicitVerdict(t *testing.T) {
	for on := range bindingEventCount {
		for from := range bindingCount {
			assert.NotEqual(t, unspecified, bindingTransitions[on][from].Verdict, "%s on %s", from, on)
			if bindingTransitions[on][from].Verdict == Rejected {
				assert.Equal(t, unbindable, bindingTransitions[on][from], "%s on %s", from, on)
			}
		}
	}
}

func TestEveryBindingHasAnExplicitAdmissionPerServingEvent(t *testing.T) {
	for b := range bindingCount {
		for _, verdict := range []Verdict{admissions[b].subscription, admissions[b].customerKey, admissions[b].organizationCredits} {
			assert.NotEqual(t, unspecified, verdict, b.String())
		}
	}
}
