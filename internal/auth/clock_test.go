package auth_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/auth"
)

func TestClockNowReadsTheInjectedClock(t *testing.T) {
	fixed := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	assert.Equal(t, fixed, auth.Clock(func() time.Time { return fixed }).Now())
}

func TestZeroClockNowFallsBackToWallClock(t *testing.T) {
	var unset auth.Clock
	before := time.Now()
	assert.False(t, unset.Now().Before(before))
}
