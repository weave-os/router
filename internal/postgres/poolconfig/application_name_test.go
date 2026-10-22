package poolconfig

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func TestConfigureApplicationName(t *testing.T) {
	cases := []struct {
		name      string
		dsn       string
		pgAppName string
		appName   string
		expected  string
	}{
		{
			name:     "sets name when DSN has none",
			dsn:      "postgres://router:router@localhost/router?sslmode=disable",
			appName:  "router-canary",
			expected: "router-canary",
		},
		{
			name:     "keeps name supplied by DSN",
			dsn:      "postgres://router:router@localhost/router?sslmode=disable&application_name=custom",
			appName:  "router-canary",
			expected: "custom",
		},
		{
			name:      "keeps name supplied by PGAPPNAME",
			dsn:       "postgres://router:router@localhost/router?sslmode=disable",
			pgAppName: "from-env",
			appName:   "router-canary",
			expected:  "from-env",
		},
		{
			name:     "leaves name unset when none is given",
			dsn:      "postgres://router:router@localhost/router?sslmode=disable",
			appName:  "",
			expected: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PGAPPNAME", tc.pgAppName)
			config, err := pgxpool.ParseConfig(tc.dsn)
			require.NoError(t, err)

			ConfigureApplicationName(config, tc.appName)

			require.Equal(t, tc.expected, config.ConnConfig.RuntimeParams[applicationNameParam])
		})
	}
}
