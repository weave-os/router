package health

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCapacityDefaultsAndExplicitRecovery(t *testing.T) {
	settings := map[string]string{}
	lookup := func(name string) (string, bool) { value, ok := settings[name]; return value, ok }
	limits, err := limitsFromEnv(lookup, 500, 128<<20)
	require.NoError(t, err)
	assert.Equal(t, 400, limits.ResumeRequests)
	assert.EqualValues(t, 96<<20, limits.ResumeBufferedBytes)
	assert.Zero(t, limits.MemoryHighBytes)
	settings["ROUTER_CAPACITY_MAX_REQUESTS"] = "1"
	limits, err = limitsFromEnv(lookup, 500, 128<<20)
	require.NoError(t, err)
	assert.Zero(t, limits.ResumeRequests)
	settings["ROUTER_CAPACITY_RESUME_REQUESTS"] = "1"
	_, err = limitsFromEnv(lookup, 500, 128<<20)
	require.Error(t, err, "a full instance cannot recover at the same watermark")
}

func TestInvalidCapacitySettingsFailStartup(t *testing.T) {
	for _, raw := range []string{"", "abc", "-1", "9223372036854775808"} {
		t.Run(raw, func(t *testing.T) {
			_, err := limitsFromEnv(func(name string) (string, bool) {
				return raw, name == "ROUTER_CAPACITY_MAX_BUFFERED_BYTES"
			}, 250, 0)
			require.ErrorContains(t, err, "ROUTER_CAPACITY_MAX_BUFFERED_BYTES")
		})
	}
}
