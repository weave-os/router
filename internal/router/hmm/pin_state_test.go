package hmm_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/hmm"
	"weave-os/router/internal/router/sessionpin"
)

func TestPinStateOfReadsHMMColumns(t *testing.T) {
	pin := sessionpin.Pin{PolicyGroup: "g2", ConsecutiveDowngradeVotes: 1, ConsecutiveUpgradeVotes: 2}
	assert.Equal(t, hmm.PinState{PolicyGroup: "g2", DowngradeVotes: 1, UpgradeVotes: 2}, hmm.PinStateOf(pin))
}

func TestClassifierMarginWithoutMetadataIsNil(t *testing.T) {
	assert.Nil(t, hmm.ClassifierMargin(router.Decision{}))
	margin := 0.3
	assert.Equal(t, &margin, hmm.ClassifierMargin(router.Decision{Metadata: &router.RoutingMetadata{ClassifierMargin: &margin}}))
}
