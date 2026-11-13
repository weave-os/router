package sessionpin_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/router/sessionpin"
)

func TestActiveCooldowns(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	cooldowns := map[string]time.Time{
		"zeta":    now.Add(time.Second),
		"alpha":   now.Add(time.Minute),
		"expired": now.Add(-time.Second),
		"at-now":  now,
	}

	assert.Equal(t, []string{"alpha", "zeta"}, sessionpin.ActiveCooldowns(cooldowns, now),
		"sorted; an expiry at or before now is over")
	assert.Empty(t, sessionpin.ActiveCooldowns(cooldowns, now.Add(time.Hour)))
	assert.Empty(t, sessionpin.ActiveCooldowns(nil, now))
}
