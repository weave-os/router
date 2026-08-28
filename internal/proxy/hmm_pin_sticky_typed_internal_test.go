package proxy

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"workweave/router/internal/providers"
	"workweave/router/internal/router"
	"workweave/router/internal/router/sessionpin"
)

// TestStickPinTypedFieldPrecedence proves the typed pin-sticky field, when
// reported, takes precedence over the legacy reason-string sentinel, and that
// its absence leaves the sentinel path exactly as before.
func TestStickPinTypedFieldPrecedence(t *testing.T) {
	const pinnedModel = "claude-opus-4-7"
	const sameTierFresh = "claude-opus-4-6"
	const pinnedGroup = "high"

	pin := sessionpin.Pin{
		Provider:    providers.ProviderAnthropic,
		Model:       pinnedModel,
		Reason:      "hmm_policy(classifier 'high' (p=0.32))",
		PolicyGroup: pinnedGroup,
	}
	boolPtr := func(v bool) *bool { return &v }
	decision := func(reason string, eligible *bool) router.Decision {
		return router.Decision{
			Model:  sameTierFresh,
			Reason: reason,
			Metadata: &router.RoutingMetadata{
				PolicyGroup:               pinnedGroup,
				PinStickyOverrideEligible: eligible,
			},
		}
	}

	tests := []struct {
		name  string
		fresh router.Decision
		want  bool
	}{
		{
			name:  "typed true without sentinel sticks",
			fresh: decision("hmm_policy(legacy fallback draw)", boolPtr(true)),
			want:  true,
		},
		{
			name:  "typed false vetoes even when the sentinel is present",
			fresh: decision(hmmPinStickyTestFallbackReason, boolPtr(false)),
			want:  false,
		},
		{
			name:  "typed absent with sentinel keeps legacy behavior",
			fresh: decision(hmmPinStickyTestFallbackReason, nil),
			want:  true,
		},
		{
			name:  "typed absent without sentinel keeps legacy behavior",
			fresh: decision("hmm_policy(classifier 'high' (p=0.91))", nil),
			want:  false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := stickPinOnArmSelectorUnavailable(test.fresh, pin, true, false)
			assert.Equal(t, test.want, got)
		})
	}
}
