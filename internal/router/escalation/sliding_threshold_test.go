package escalation_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/router/escalation"
)

func TestSlidingThresholdTargetsPredictedGroup(t *testing.T) {
	for _, test := range []struct {
		name        string
		pinned      escalation.Group
		classified  escalation.Group
		probability float64
		want        escalation.Group
		covered     bool
	}{
		{"low to high below threshold", escalation.Low, escalation.High, 0.299, "", true},
		{"low to high at threshold", escalation.Low, escalation.High, 0.30, escalation.High, true},
		{"low to high partial confidence", escalation.Low, escalation.High, 0.38, escalation.High, true},
		{"low to high majority", escalation.Low, escalation.High, 0.50, escalation.High, true},
		{"low to maximum at threshold", escalation.Low, escalation.Maximum, 0.30, escalation.Maximum, true},
		{"medium to maximum at threshold", escalation.Medium, escalation.Maximum, 0.30, escalation.Maximum, true},
		{"high to maximum below threshold", escalation.High, escalation.Maximum, 0.499, "", true},
		{"high to maximum at threshold", escalation.High, escalation.Maximum, 0.50, escalation.Maximum, true},
		{"medium to high below threshold", escalation.Medium, escalation.High, 0.399, "", true},
		{"medium to high at threshold", escalation.Medium, escalation.High, 0.40, escalation.High, true},
		{"low to medium below threshold", escalation.Low, escalation.Medium, 0.299, "", true},
		{"low to medium at threshold", escalation.Low, escalation.Medium, 0.30, escalation.Medium, true},
		{"same group is not an upgrade", escalation.High, escalation.High, 0.9, "", false},
		{"downgrade is not an upgrade", escalation.High, escalation.Low, 0.9, "", false},
		{"absent pin is not an upgrade", "", escalation.High, 0.9, "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			target, covered := escalation.SlidingThresholdTarget(test.pinned, test.classified, test.probability)
			assert.Equal(t, test.want, target)
			assert.Equal(t, test.covered, covered)
		})
	}
}
