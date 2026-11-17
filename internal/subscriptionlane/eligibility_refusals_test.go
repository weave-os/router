package subscriptionlane_test

import (
	"testing"

	"weave-os/router/internal/refusal"
	"weave-os/router/internal/subscriptionlane"

	"github.com/stretchr/testify/assert"
)

func TestEligibilityRefusalsOutrankEveryLaneMark(t *testing.T) {
	refusals := map[subscriptionlane.Event]refusal.Kind{
		subscriptionlane.ModelOutsidePlan:           refusal.ProductIneligible,
		subscriptionlane.ClientAppRefused:           refusal.SubscriptionClientAppRefused,
		subscriptionlane.ClaudeEnterpriseUnservable: refusal.ClaudeEnterpriseOnly,
	}
	for on, want := range refusals {
		for _, from := range subscriptionlane.States() {
			if from.Terminal() {
				continue
			}
			next, ok := subscriptionlane.Step(from, on)
			assert.True(t, ok, "%s on %s", from, on)
			kind, refused := next.Refusal()
			assert.True(t, refused, "%s on %s", from, on)
			assert.Equal(t, want, kind, "%s on %s", from, on)
		}
	}
}
