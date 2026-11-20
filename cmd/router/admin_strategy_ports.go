package main

import (
	"weave-os/router/internal/api/admin"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/router/hmm"
	"weave-os/router/internal/router/hmm/selection"
	"weave-os/router/internal/server"
	"weave-os/router/internal/server/middleware"
)

func hmmRoutingDistribution(rosters selection.DistributionRosters) admin.DistributionRosterSource {
	return selection.DistributionSource{Rosters: rosters}
}

func withHMMAdminPorts(features server.Features, admission *middleware.ServingAdmissionConfig, distribution admin.DistributionRosterSource) server.Features {
	features.RoutingDistribution = distribution
	if admission != nil {
		features.RoutingDistribution = hmmRoutingDistribution(policyregistry.AdmittedRosterSource{})
	}
	features.RosterCatalogID = hmm.CatalogIDForRoster
	features.TaskDomainPreview = selection.TaskDomainPreviewer{}
	return features
}
