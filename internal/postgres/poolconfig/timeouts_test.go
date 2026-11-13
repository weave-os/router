package poolconfig

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func TestConfigureTimeoutsReplacesLongerConnectionDefaults(t *testing.T) {
	config, err := pgxpool.ParseConfig("postgres://router:router@localhost/router?sslmode=disable")
	require.NoError(t, err)
	config.ConnConfig.RuntimeParams["lock_timeout"] = "10min"
	config.ConnConfig.RuntimeParams["statement_timeout"] = "10min"

	ConfigureTimeouts(config)

	require.Equal(t, lockTimeout, config.ConnConfig.RuntimeParams["lock_timeout"])
	require.Equal(t, statementTimeout, config.ConnConfig.RuntimeParams["statement_timeout"])
}
