package main

import (
	"context"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
)

// catalogRosterSource is the deployed-model universe for workers with no HMM
// policy configured: every catalog model, so /v1/router/models and admin
// validation keep working without the legacy cluster bundle.
type catalogRosterSource struct{}

func (catalogRosterSource) HMMDeployedModels(context.Context) ([]router.DeployedEntry, error) {
	rows := catalog.Listing()
	entries := make([]router.DeployedEntry, 0, len(rows))
	for _, row := range rows {
		entries = append(entries, router.DeployedEntry{Model: row.Model, Provider: row.Provider})
	}
	return entries, nil
}
