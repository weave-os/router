package hmm_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/hmm"
)

func TestEscalationTier(t *testing.T) {
	for group, want := range map[escalation.Group]catalog.Tier{
		escalation.Low:     catalog.TierLow,
		escalation.Medium:  catalog.TierMid,
		escalation.High:    catalog.TierHigh,
		escalation.Maximum: catalog.TierHigh,
		"":                 catalog.TierUnknown,
	} {
		assert.Equal(t, want, hmm.EscalationTier(group), group)
	}
}
