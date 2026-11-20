package admin

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router/taskdomain"

	"github.com/gin-gonic/gin"
)

const (
	maxTaskDomainPreviewBytes = 16 << 20
	maxTaskDomainPreviewItems = 64
)

type TaskDomainPreviewStatus string

const (
	TaskDomainPreviewReady            TaskDomainPreviewStatus = "ready"
	TaskDomainPreviewInvalidRequest   TaskDomainPreviewStatus = "invalid_request"
	TaskDomainPreviewEvidenceRejected TaskDomainPreviewStatus = "evidence_rejected"
	TaskDomainPreviewFailed           TaskDomainPreviewStatus = "failed"
)

type TaskDomainPreviewItemRequest struct {
	Policy                json.RawMessage     `json:"policy"`
	Evidence              json.RawMessage     `json:"evidence"`
	Cluster               string              `json:"cluster"`
	Harness               string              `json:"harness"`
	SubscriptionProviders []string            `json:"subscription_providers,omitempty"`
	Domains               []taskdomain.Domain `json:"domains"`
}

type taskDomainPreviewRequest struct {
	Items []TaskDomainPreviewItemRequest `json:"items"`
}

type TaskDomainPreviewItem struct {
	Status                  TaskDomainPreviewStatus `json:"status"`
	RosterSHA256            string                  `json:"roster_sha256,omitempty"`
	EvidenceSHA256          string                  `json:"evidence_sha256,omitempty"`
	BenchmarkSnapshotSHA256 string                  `json:"benchmark_snapshot_sha256,omitempty"`
	BenchmarkIngestDate     string                  `json:"benchmark_ingest_date,omitempty"`
	Preview                 any                     `json:"preview,omitempty"`
}

type TaskDomainRecipes struct {
	Version   string
	Influence float64
	Recipes   any
}

type TaskDomainPreviewer interface {
	TaskDomainRecipes() TaskDomainRecipes
	PreviewTaskDomain(logger *slog.Logger, req TaskDomainPreviewItemRequest, profile taskdomain.Profile) TaskDomainPreviewItem
}

type taskDomainPreviewResponse struct {
	RecipeVersion   string                  `json:"recipe_version"`
	RecipeInfluence float64                 `json:"recipe_influence"`
	Recipes         any                     `json:"recipes"`
	Items           []TaskDomainPreviewItem `json:"items"`
}

func InternalTaskDomainPreviewHandler(previewer TaskDomainPreviewer) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req taskDomainPreviewRequest
		if err := decodeTaskDomainPreviewRequest(c, &req); err != nil || len(req.Items) == 0 || len(req.Items) > maxTaskDomainPreviewItems {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "between 1 and 64 preview items are required"})
			return
		}
		recipes := previewer.TaskDomainRecipes()
		response := taskDomainPreviewResponse{
			RecipeVersion: recipes.Version, RecipeInfluence: recipes.Influence, Recipes: recipes.Recipes,
			Items: make([]TaskDomainPreviewItem, 0, len(req.Items)),
		}
		unscored := make(map[TaskDomainPreviewStatus]int)
		for _, item := range req.Items {
			previewed := previewTaskDomainItem(c, previewer, item)
			if previewed.Status != TaskDomainPreviewReady {
				unscored[previewed.Status]++
			}
			response.Items = append(response.Items, previewed)
		}
		if len(unscored) > 0 {
			observability.FromGin(c).Warn("Task-domain preview items not scored",
				"items", len(req.Items),
				"invalid_request", unscored[TaskDomainPreviewInvalidRequest],
				"evidence_rejected", unscored[TaskDomainPreviewEvidenceRejected],
				"failed", unscored[TaskDomainPreviewFailed],
			)
		}
		c.JSON(http.StatusOK, response)
	}
}

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

func previewTaskDomainItem(c *gin.Context, previewer TaskDomainPreviewer, req TaskDomainPreviewItemRequest) TaskDomainPreviewItem {
	invalid := TaskDomainPreviewItem{Status: TaskDomainPreviewInvalidRequest}
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
	return previewer.PreviewTaskDomain(observability.FromGin(c), req, profile)
}
