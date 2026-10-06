package admin

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router/hmm/rosterdata"
	"weave-os/router/internal/router/hmm/selection"
	"weave-os/router/internal/router/taskdomain"

	"github.com/gin-gonic/gin"
)

const (
	// maxTaskDomainPreviewBytes bounds every policy and evidence document in one batch.
	maxTaskDomainPreviewBytes = 16 << 20
	// maxTaskDomainPreviewItems covers every serving lane of one control-plane read.
	maxTaskDomainPreviewItems = 64
)

// TaskDomainPreviewStatus explains whether one batch item could be scored.
type TaskDomainPreviewStatus string

const (
	TaskDomainPreviewReady            TaskDomainPreviewStatus = "ready"
	TaskDomainPreviewInvalidRequest   TaskDomainPreviewStatus = "invalid_request"
	TaskDomainPreviewEvidenceRejected TaskDomainPreviewStatus = "evidence_rejected"
	TaskDomainPreviewFailed           TaskDomainPreviewStatus = "failed"
)

type taskDomainPreviewItemRequest struct {
	Policy   json.RawMessage     `json:"policy"`
	Evidence json.RawMessage     `json:"evidence"`
	Cluster  string              `json:"cluster"`
	Harness  string              `json:"harness"`
	Domains  []taskdomain.Domain `json:"domains"`
}

type taskDomainPreviewRequest struct {
	Items []taskDomainPreviewItemRequest `json:"items"`
}

type taskDomainPreviewItem struct {
	Status                  TaskDomainPreviewStatus  `json:"status"`
	RosterSHA256            string                   `json:"roster_sha256,omitempty"`
	EvidenceSHA256          string                   `json:"evidence_sha256,omitempty"`
	BenchmarkSnapshotSHA256 string                   `json:"benchmark_snapshot_sha256,omitempty"`
	BenchmarkIngestDate     string                   `json:"benchmark_ingest_date,omitempty"`
	Preview                 *selection.DomainPreview `json:"preview,omitempty"`
}

type taskDomainPreviewResponse struct {
	RecipeVersion   string                   `json:"recipe_version"`
	RecipeInfluence float64                  `json:"recipe_influence"`
	Recipes         []selection.DomainRecipe `json:"recipes"`
	Items           []taskDomainPreviewItem  `json:"items"`
}

// InternalTaskDomainPreviewHandler scores published policies and their
// evidence with this worker's task-domain recipe and selector, so the control
// plane displays exactly what serving computes instead of a second
// implementation. Pure computation over caller-supplied immutable documents;
// one request carries every lane of a control-plane read.
func InternalTaskDomainPreviewHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		var req taskDomainPreviewRequest
		if err := decodeTaskDomainPreviewRequest(c, &req); err != nil || len(req.Items) == 0 || len(req.Items) > maxTaskDomainPreviewItems {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "between 1 and 64 preview items are required"})
			return
		}
		response := taskDomainPreviewResponse{
			RecipeVersion: selection.DomainRecipeVersion, RecipeInfluence: selection.DomainInfluence, Recipes: selection.DomainRecipes(),
			Items: make([]taskDomainPreviewItem, 0, len(req.Items)),
		}
		for _, item := range req.Items {
			response.Items = append(response.Items, previewTaskDomainItem(c, item))
		}
		c.JSON(http.StatusOK, response)
	}
}

// decodeTaskDomainPreviewRequest reads the whole bounded body so trailing
// content cannot slip past the size limit.
func decodeTaskDomainPreviewRequest(c *gin.Context, req *taskDomainPreviewRequest) error {
	body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, maxTaskDomainPreviewBytes))
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(req); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing content after preview request")
	}
	return nil
}

func previewTaskDomainItem(c *gin.Context, req taskDomainPreviewItemRequest) taskDomainPreviewItem {
	invalid := taskDomainPreviewItem{Status: TaskDomainPreviewInvalidRequest}
	if len(req.Policy) == 0 || len(req.Evidence) == 0 || req.Cluster == "" {
		return invalid
	}
	profile := taskdomain.Profile{taskdomain.UI: false, taskdomain.Logic: false, taskdomain.Data: false, taskdomain.Infra: false, taskdomain.Docs: false}
	for _, domain := range req.Domains {
		if _, known := profile[domain]; !known {
			return invalid
		}
		profile[domain] = true
	}
	roster, err := rosterdata.Parse(req.Policy)
	if err != nil {
		return invalid
	}
	roster.SHA256 = rosterdata.SHA256Hex(req.Policy)
	item := taskDomainPreviewItem{RosterSHA256: roster.SHA256, EvidenceSHA256: rosterdata.SHA256Hex(req.Evidence)}
	if _, exists := roster.Clusters[req.Cluster]; !exists {
		item.Status = TaskDomainPreviewInvalidRequest
		return item
	}
	evidence, err := selection.ParseDomainEvidence(req.Evidence, roster)
	if err != nil {
		observability.FromGin(c).Error("Task-domain evidence rejected for preview", "roster_sha256", roster.SHA256, "err", err)
		item.Status = TaskDomainPreviewEvidenceRejected
		return item
	}
	preview, err := selection.PreviewDomainRanking(roster, evidence, req.Cluster, req.Harness, profile)
	if err != nil {
		observability.FromGin(c).Error("Task-domain preview failed", "roster_sha256", roster.SHA256, "cluster", req.Cluster, "err", err)
		item.Status = TaskDomainPreviewFailed
		return item
	}
	item.Status = TaskDomainPreviewReady
	item.BenchmarkSnapshotSHA256, item.BenchmarkIngestDate = evidence.BenchmarkSnapshotSHA256, evidence.BenchmarkIngestDate
	item.Preview = &preview
	return item
}
