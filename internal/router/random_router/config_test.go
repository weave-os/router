package randomrouter

import (
	"encoding/json"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
	"time"
)

func TestConfigExpiresAndPinsPolicy(t *testing.T) {
	now := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
	config := Config{SchemaVersion: "random_router_named.v2", InstallationID: "00000000-0000-4000-8000-000000000002", OrganizationID: "synthetic-org", PolicySHA256: PolicySHA256, ExpiresAt: now.Add(time.Hour)}
	for _, test := range []struct {
		name   string
		expiry time.Time
		digest string
		valid  bool
	}{
		{"valid", config.ExpiresAt, PolicySHA256, true}, {"expired", now, PolicySHA256, false}, {"too long", now.Add(8 * 24 * time.Hour), PolicySHA256, false}, {"wrong policy", config.ExpiresAt, strings.Repeat("b", 64), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := config
			candidate.ExpiresAt = test.expiry
			candidate.PolicySHA256 = test.digest
			encoded, err := json.Marshal(candidate)
			require.NoError(t, err)
			_, err = ParseConfig(string(encoded), now)
			if test.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}
