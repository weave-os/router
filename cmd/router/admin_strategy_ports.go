package main

import (
	"log/slog"

	"weave-os/router/internal/api/admin"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/router/hmm"
	"weave-os/router/internal/router/hmm/rosterdata"
	"weave-os/router/internal/router/hmm/selection"
	"weave-os/router/internal/router/taskdomain"
	"weave-os/router/internal/server"
	"weave-os/router/internal/server/middleware"
)

type taskDomainPreviewer struct{}

func (taskDomainPreviewer) TaskDomainRecipes() admin.TaskDomainRecipes {
	return admin.TaskDomainRecipes{Version: selection.DomainRecipeVersion, Influence: selection.DomainInfluence, Recipes: selection.DomainRecipes()}
}

func (taskDomainPreviewer) PreviewTaskDomain(logger *slog.Logger, req admin.TaskDomainPreviewItemRequest, profile taskdomain.Profile) admin.TaskDomainPreviewItem {
	invalid := admin.TaskDomainPreviewItem{Status: admin.TaskDomainPreviewInvalidRequest}
	roster, err := rosterdata.Parse(req.Policy)
	if err != nil {
		return invalid
	}
	roster.SHA256 = rosterdata.SHA256Hex(req.Policy)
	item := admin.TaskDomainPreviewItem{RosterSHA256: roster.SHA256, EvidenceSHA256: rosterdata.SHA256Hex(req.Evidence)}
	if _, exists := roster.Clusters[req.Cluster]; !exists {
		item.Status = admin.TaskDomainPreviewInvalidRequest
		return item
	}
	evidence, err := selection.ParseDomainEvidence(req.Evidence, roster)
	if err != nil {
		logger.Debug("Task-domain evidence rejected for preview", "roster_sha256", roster.SHA256, "err", err)
		item.Status = admin.TaskDomainPreviewEvidenceRejected
		return item
	}
	preview, err := selection.PreviewDomainRanking(roster, evidence, req.Cluster, req.Harness, req.SubscriptionProviders, profile)
	if err != nil {
		logger.Debug("Task-domain preview failed", "roster_sha256", roster.SHA256, "cluster", req.Cluster, "err", err)
		item.Status = admin.TaskDomainPreviewFailed
		return item
	}
	item.Status = admin.TaskDomainPreviewReady
	item.BenchmarkSnapshotSHA256, item.BenchmarkIngestDate = evidence.BenchmarkSnapshotSHA256, evidence.BenchmarkIngestDate
	item.Preview = &preview
	return item
}

func hmmRoutingDistribution(rosters selection.DistributionRosters) admin.DistributionRosterSource {
	return selection.DistributionSource{Rosters: rosters}
}

func withHMMAdminPorts(features server.Features, admission *middleware.ServingAdmissionConfig, distribution admin.DistributionRosterSource) server.Features {
	features.RoutingDistribution = distribution
	if admission != nil {
		features.RoutingDistribution = hmmRoutingDistribution(policyregistry.AdmittedRosterSource{})
	}
	features.RosterCatalogID = hmm.CatalogIDForRoster
	features.TaskDomainPreview = taskDomainPreviewer{}
	return features
}
