package entitlement_test

import (
	"testing"
	"time"

	"weave-os/router/internal/subscriptions/entitlement"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWeeklyPeriodMayBeShortButNeverLongerThanAWeek(t *testing.T) {
	start := time.Date(2026, 3, 29, 0, 0, 0, 0, time.UTC)
	week := 7 * 24 * time.Hour

	shortFinalWeek := entitlement.Period{Kind: entitlement.PeriodKindWeekly, Start: start, End: start.Add(3 * 24 * time.Hour)}
	require.NoError(t, shortFinalWeek.Validate(), "the last week of a period that is not a whole number of weeks is short")

	fullWeek := entitlement.Period{Kind: entitlement.PeriodKindWeekly, Start: start, End: start.Add(week)}
	require.NoError(t, fullWeek.Validate())

	overlongWeek := entitlement.Period{Kind: entitlement.PeriodKindWeekly, Start: start, End: start.Add(week + time.Nanosecond)}
	assert.ErrorIs(t, overlongWeek.Validate(), entitlement.ErrInvalidContract)
}
