package poolconfig

import "github.com/jackc/pgx/v5/pgxpool"

const applicationNameParam = "application_name"

// ConfigureApplicationName labels pool connections in pg_stat_activity. A name
// already supplied by the DSN or PGAPPNAME wins so operators can override it.
func ConfigureApplicationName(config *pgxpool.Config, name string) {
	if name == "" {
		return
	}
	if config.ConnConfig.RuntimeParams[applicationNameParam] != "" {
		return
	}
	config.ConnConfig.RuntimeParams[applicationNameParam] = name
}
