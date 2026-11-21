package sessionpin_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/sessionpin"
)

func TestPinDecisionCarriesTheServedBinding(t *testing.T) {
	pin := sessionpin.Pin{Provider: "anthropic", Model: string(catalog.ModelIDClaudeSonnet46), Effort: "high", Reason: "pinned"}

	assert.Equal(t, router.Decision{Provider: "anthropic", Model: string(catalog.ModelIDClaudeSonnet46), Effort: "high", Reason: "pinned"}, pin.Decision())
}

func TestPinBandPairPutsTheHigherTierModelFirst(t *testing.T) {
	large := sessionpin.Pin{Provider: "anthropic", Model: string(catalog.ModelIDClaudeOpus47), Reason: "band", PairedProvider: "anthropic", PairedModel: string(catalog.ModelIDClaudeHaiku45)}
	small := sessionpin.Pin{Provider: "anthropic", Model: string(catalog.ModelIDClaudeHaiku45), Reason: "band", PairedProvider: "anthropic", PairedModel: string(catalog.ModelIDClaudeOpus47)}

	for _, pin := range []sessionpin.Pin{large, small} {
		gotLarge, gotSmall := pin.BandPair()
		assert.Equal(t, string(catalog.ModelIDClaudeOpus47), gotLarge.Model)
		assert.Equal(t, string(catalog.ModelIDClaudeHaiku45), gotSmall.Model)
	}
}
