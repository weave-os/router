package entitlement

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSixHourWindowAtAnchorsToFixedUTCBoundaries(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		at    time.Time
		start time.Time
	}{
		{name: "start of day", at: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), start: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)},
		{name: "last instant of window", at: time.Date(2026, 3, 1, 5, 59, 59, 999_999_999, time.UTC), start: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)},
		{name: "midday", at: time.Date(2026, 3, 1, 13, 42, 0, 0, time.UTC), start: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)},
		{name: "end of day", at: time.Date(2026, 3, 1, 23, 59, 0, 0, time.UTC), start: time.Date(2026, 3, 1, 18, 0, 0, 0, time.UTC)},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			window := SixHourWindowAt(testCase.at)

			require.NoError(t, window.Validate())
			assert.Equal(t, PeriodKindSixHour, window.Kind)
			assert.Equal(t, testCase.start, window.Start)
			assert.Equal(t, testCase.start.Add(6*time.Hour), window.End)
			assert.True(t, window.Covers(testCase.at))
		})
	}
}

func TestSixHourWindowAtNormalizesNonUTCInstants(t *testing.T) {
	zone := time.FixedZone("UTC+5", 5*60*60)
	local := time.Date(2026, 3, 1, 2, 30, 0, 0, zone)

	window := SixHourWindowAt(local)

	require.NoError(t, window.Validate())
	assert.Equal(t, time.Date(2026, 2, 28, 18, 0, 0, 0, time.UTC), window.Start)
	assert.True(t, window.Covers(local))
}

func TestPeriodCoversIsHalfOpen(t *testing.T) {
	window := SixHourWindowAt(time.Date(2026, 3, 1, 7, 0, 0, 0, time.UTC))

	assert.True(t, window.Covers(window.Start))
	assert.False(t, window.Covers(window.End))
}
