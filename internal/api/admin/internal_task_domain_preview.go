package admin

import (
	"encoding/json"
	"net/http"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router/hmm/rosterdata"
	"weave-os/router/internal/router/hmm/selection"
	"weave-os/router/internal/router/taskdomain"

	"github.com/gin-gonic/gin"
)

// maxTaskDomainPreviewBytes bounds the policy plus evidence a caller may send.
const maxTaskDomainPreviewBytes = 4 << 20

type taskDomainPreviewRequest struct {
	Policy   json.RawMessage     `json:"policy"`
	Evidence json.RawMessage     `json:"evidence"`
	Cluster  string              `json:"cluster"`
	Harness  string              `json:"harness"`
	Domains  []taskdomain.Domain `json:"domains"`
}

type taskDomainPreviewResponse struct {
	RecipeVersion           string                   `json:"recipe_version"`
	RecipeInfluence         float64                  `json:"recipe_influence"`
	Recipes                 []selection.DomainRecipe `json:"recipes"`
	RosterSHA256            string                   `json:"roster_sha256"`
	EvidenceSHA256          string                   `json:"evidence_sha256"`
	BenchmarkSnapshotSHA256 string                   `json:"benchmark_snapshot_sha256"`
	BenchmarkIngestDate     string                   `json:"benchmark_ingest_date"`
	Preview                 selection.DomainPreview  `json:"preview"`
}

// InternalTaskDomainPreviewHandler scores a published policy and its evidence
// with this worker's task-domain recipe and selector, so the control plane
// displays exactly what serving computes instead of a second implementation.
// Pure computation: the caller supplies both immutable documents.
func InternalTaskDomainPreviewHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxTaskDomainPreviewBytes)
		var req taskDomainPreviewRequest
		if err := c.ShouldBindJSON(&req); err != nil || len(req.Policy) == 0 || len(req.Evidence) == 0 || req.Cluster == "" {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "policy, evidence and cluster are required"})
			return
		}
		profile := taskdomain.Profile{taskdomain.UI: false, taskdomain.Logic: false, taskdomain.Data: false, taskdomain.Infra: false, taskdomain.Docs: false}
		for _, domain := range req.Domains {
			if _, known := profile[domain]; !known {
				c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "unknown task domain"})
				return
			}
			profile[domain] = true
		}
		roster, err := rosterdata.Parse(req.Policy)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "policy is not a valid selection policy"})
			return
		}
		roster.SHA256 = rosterdata.SHA256Hex(req.Policy)
		evidence, err := selection.ParseDomainEvidence(req.Evidence, roster)
		if err != nil {
			observability.FromGin(c).Error("Task-domain evidence rejected for preview", "roster_sha256", roster.SHA256, "err", err)
			c.AbortWithStatusJSON(http.StatusUnprocessableEntity, gin.H{"error": "evidence does not match this policy or the worker's evidence contract"})
			return
		}
		if _, exists := roster.Clusters[req.Cluster]; !exists {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "cluster is not in the policy"})
			return
		}
		preview, err := selection.PreviewDomainRanking(roster, evidence, req.Cluster, req.Harness, profile)
		if err != nil {
			observability.FromGin(c).Error("Task-domain preview failed", "roster_sha256", roster.SHA256, "cluster", req.Cluster, "err", err)
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Failed to preview task-domain ranking."})
			return
		}
		c.JSON(http.StatusOK, taskDomainPreviewResponse{
			RecipeVersion: selection.DomainRecipeVersion, RecipeInfluence: selection.DomainInfluence, Recipes: selection.DomainRecipes(),
			RosterSHA256: roster.SHA256, EvidenceSHA256: rosterdata.SHA256Hex(req.Evidence),
			BenchmarkSnapshotSHA256: evidence.BenchmarkSnapshotSHA256, BenchmarkIngestDate: evidence.BenchmarkIngestDate,
			Preview: preview,
		})
	}
}
