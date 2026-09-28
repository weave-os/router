package poolconfig

import "github.com/jackc/pgx/v5/pgxpool"

const (
	lockTimeout      = "250ms"
	statementTimeout = "5s"
)

// ConfigureTimeouts bounds lock waits and active statements on every pool connection.
func ConfigureTimeouts(config *pgxpool.Config) {
	if config.ConnConfig.RuntimeParams == nil {
		config.ConnConfig.RuntimeParams = make(map[string]string)
	}
	config.ConnConfig.RuntimeParams["lock_timeout"] = lockTimeout
	config.ConnConfig.RuntimeParams["statement_timeout"] = statementTimeout
}
