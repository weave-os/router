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

func billingPeriod(start, end time.Time) Period {
	return Period{Kind: PeriodKindBilling, Start: start, End: end}
}

func TestSixHourWindowsInCountsPartialWindowsAtBothEnds(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		period  Period
		windows int64
	}{
		{
			name:    "aligned month",
			period:  billingPeriod(time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)),
			windows: 124,
		},
		{
			name:    "month anchored mid-window",
			period:  billingPeriod(time.Date(2026, 3, 1, 14, 30, 0, 0, time.UTC), time.Date(2026, 4, 1, 14, 30, 0, 0, time.UTC)),
			windows: 125,
		},
		{
			name:    "single window",
			period:  billingPeriod(time.Date(2026, 3, 1, 1, 0, 0, 0, time.UTC), time.Date(2026, 3, 1, 5, 0, 0, 0, time.UTC)),
			windows: 1,
		},
		{
			name:    "empty period",
			period:  billingPeriod(time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)),
			windows: 0,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			assert.Equal(t, testCase.windows, SixHourWindowsIn(testCase.period))
		})
	}
}

func TestSixHourAllowanceGivesTheRemainderToTheEarliestWindows(t *testing.T) {
	period := billingPeriod(time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC))
	nominal := int64(50_000_000)

	var total int64
	for index := range SixHourWindowsIn(period) {
		window := SixHourWindowAt(period.Start.Add(time.Duration(index) * sixHourWindow))
		allowance := SixHourAllowanceUsdMicros(nominal, period, window)
		if index < 100 {
			assert.Equal(t, int64(403_226), allowance, "window %d takes one micro of the remainder", index)
		} else {
			assert.Equal(t, int64(403_225), allowance, "window %d is past the remainder", index)
		}
		total += allowance
	}

	assert.Equal(t, nominal, total, "the windows of a period must sum back to the nominal allowance")
}

func TestSixHourAllowanceIgnoresWindowsOutsideTheBillingPeriod(t *testing.T) {
	period := billingPeriod(time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC))

	assert.Zero(t, SixHourAllowanceUsdMicros(50_000_000, period, SixHourWindowAt(period.Start.Add(-time.Hour))))
	assert.Zero(t, SixHourAllowanceUsdMicros(50_000_000, period, SixHourWindowAt(period.End)))
	assert.Zero(t, SixHourAllowanceUsdMicros(0, period, SixHourWindowAt(period.Start)))
}

func TestSixHourAllowanceGrantsAFullCapInTheWindowAPeriodStartsMidway(t *testing.T) {
	period := billingPeriod(time.Date(2026, 3, 14, 9, 30, 0, 0, time.UTC), time.Date(2026, 4, 14, 9, 30, 0, 0, time.UTC))
	windows := SixHourWindowsIn(period)

	first := SixHourAllowanceUsdMicros(50_000_000, period, SixHourWindowAt(period.Start))

	assert.Equal(t, 50_000_000/windows, first,
		"a purchase mid-window buys the whole window, not the sliver left in it")
}

func TestPeriodCoversIsHalfOpen(t *testing.T) {
	window := SixHourWindowAt(time.Date(2026, 3, 1, 7, 0, 0, 0, time.UTC))

	assert.True(t, window.Covers(window.Start))
	assert.False(t, window.Covers(window.End))
}
