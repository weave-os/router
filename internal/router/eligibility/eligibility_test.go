package eligibility

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestUnrestrictedBoundaryPermitsEverySource(t *testing.T) {
	unrestricted := Unrestricted()
	assert.False(t, unrestricted.Restricts())
	for _, source := range []Source{SourceOpenSource, SourceClosedSource, SourceUnknown, "", "made-up"} {
		assert.Truef(t, unrestricted.PermitsSource(source), "unrestricted boundary refused %q", source)
	}
}

func TestMaxPermitsOnlyOpenSource(t *testing.T) {
	assert.True(t, MaxOpenSourceOnly.Restricts())
	assert.Equal(t, ProductMaxSubscription, MaxOpenSourceOnly.Product())
	assert.True(t, MaxOpenSourceOnly.PermitsSource(SourceOpenSource))
	assert.False(t, MaxOpenSourceOnly.PermitsSource(SourceClosedSource))
	assert.False(t, MaxOpenSourceOnly.PermitsSource(SourceUnknown))
}

func TestBoundaryRefusesUnclassifiedSources(t *testing.T) {
	// A row that never got classified, or one carrying a value this build does
	// not recognize, must not read as permitted by any restricted boundary.
	for _, source := range []Source{"", "open", "OPEN_SOURCE"} {
		assert.Falsef(t, MaxOpenSourceOnly.PermitsSource(source), "%q was permitted", source)
	}
}

func TestNewWithNoSourcesPermitsNothing(t *testing.T) {
	denyAll := New("nothing_sold")
	assert.True(t, denyAll.Restricts())
	for _, source := range []Source{SourceOpenSource, SourceClosedSource, SourceUnknown} {
		assert.Falsef(t, denyAll.PermitsSource(source), "%q was permitted by an empty boundary", source)
	}
}
