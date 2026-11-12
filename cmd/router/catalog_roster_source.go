package main

import (
	"context"

	"weave-os/router/internal/api/admin"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
)

// catalogRosterSource is the deployed-model universe for workers with no
// process-wide HMM policy: every catalog model. Managed admission workers use
// it too, because the unauthed /v1/router/models listing has no admitted
// policy to read.
type catalogRosterSource struct{}

func (catalogRosterSource) HMMDeployedModels(context.Context) ([]router.DeployedEntry, error) {
	rows := catalog.Listing()
	entries := make([]router.DeployedEntry, 0, len(rows))
	for _, row := range rows {
		entries = append(entries, router.DeployedEntry{Model: row.Model, Provider: row.Provider})
	}
	return entries, nil
}

// deployedModelsSource falls back to the catalog when no process-wide HMM
// roster is configured.
func deployedModelsSource(hmmRoster admin.HMMRosterSource) admin.HMMRosterSource {
	if hmmRoster == nil {
		return catalogRosterSource{}
	}
	return hmmRoster
}
