package main

import (
	"time"

	"weave-os/router/internal/analytics"
	"weave-os/router/internal/postgres"
	"weave-os/router/internal/server"
	"weave-os/router/internal/telemetry/turnrecord"
)

func withRepositoryQueries(features server.Features, repo *postgres.Repository) server.Features {
	features.TelemetryQueries = turnrecord.Queries{Repo: repo.Telemetry}
	features.UsageAnalytics = analytics.NewUsageService(repo.Usage, time.Now)
	return features
}
