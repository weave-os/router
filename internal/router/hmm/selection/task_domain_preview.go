package selection

import (
	"log/slog"

	"weave-os/router/internal/router/hmm/rosterdata"
	"weave-os/router/internal/router/taskdomain"
)

type TaskDomainPreviewer struct{}

func (TaskDomainPreviewer) PreviewRecipes() taskdomain.PreviewRecipes {
	return taskdomain.PreviewRecipes{Version: DomainRecipeVersion, Influence: DomainInfluence, Recipes: DomainRecipes()}
}

func (TaskDomainPreviewer) Preview(logger *slog.Logger, req taskdomain.PreviewRequest, profile taskdomain.Profile) taskdomain.PreviewItem {
	invalid := taskdomain.PreviewItem{Status: taskdomain.PreviewInvalidRequest}
	roster, err := rosterdata.Parse(req.Policy)
	if err != nil {
		return invalid
	}
	roster.SHA256 = rosterdata.SHA256Hex(req.Policy)
	item := taskdomain.PreviewItem{RosterSHA256: roster.SHA256, EvidenceSHA256: rosterdata.SHA256Hex(req.Evidence)}
	if _, exists := roster.Clusters[req.Cluster]; !exists {
		item.Status = taskdomain.PreviewInvalidRequest
		return item
	}
	evidence, err := ParseDomainEvidence(req.Evidence, roster)
	if err != nil {
		logger.Debug("Task-domain evidence rejected for preview", "roster_sha256", roster.SHA256, "err", err)
		item.Status = taskdomain.PreviewEvidenceRejected
		return item
	}
	preview, err := PreviewDomainRanking(roster, evidence, req.Cluster, req.Harness, req.SubscriptionProviders, profile)
	if err != nil {
		logger.Debug("Task-domain preview failed", "roster_sha256", roster.SHA256, "cluster", req.Cluster, "err", err)
		item.Status = taskdomain.PreviewFailed
		return item
	}
	item.Status = taskdomain.PreviewReady
	item.BenchmarkSnapshotSHA256, item.BenchmarkIngestDate = evidence.BenchmarkSnapshotSHA256, evidence.BenchmarkIngestDate
	item.Preview = &preview
	return item
}
