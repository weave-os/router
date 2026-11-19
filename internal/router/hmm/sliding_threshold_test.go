package hmm_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/hmm"
	"weave-os/router/internal/router/sessionpin"
)

func TestSlidingThresholdServesWithoutPinGroupHolds(t *testing.T) {
	fresh := router.Decision{Model: "fresh", Metadata: &router.RoutingMetadata{PolicyGroup: "g2", ClassifierPredictedLabel: "g2"}}
	got := hmm.SlidingThresholdServes(router.Request{}, sessionpin.Pin{Model: "pinned"}, fresh)
	assert.Equal(t, hmm.SlidingThreshold{}, got)
}

func TestSlidingThresholdServesUnscoredLabelHolds(t *testing.T) {
	fresh := router.Decision{Model: "fresh", Metadata: &router.RoutingMetadata{PolicyGroup: "g2", ClassifierPredictedLabel: "g2"}}
	req := router.Request{SlidingThresholdPinGroup: escalation.Group("g1")}
	got := hmm.SlidingThresholdServes(req, sessionpin.Pin{Model: "pinned"}, fresh)
	assert.False(t, got.Served)
	assert.Empty(t, got.FreshGroup)
}
