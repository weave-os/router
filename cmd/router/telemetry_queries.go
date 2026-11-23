package main

import (
	"weave-os/router/internal/server"
	"weave-os/router/internal/telemetry/turnrecord"
)

func withTelemetryQueries(features server.Features, repo turnrecord.TelemetryRepository) server.Features {
	features.TelemetryQueries = turnrecord.Queries{Repo: repo}
	return features
}
