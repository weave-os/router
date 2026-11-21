package admin

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router/taskdomain"

	"github.com/gin-gonic/gin"
)

const (
	maxTaskDomainPreviewBytes = 16 << 20
	maxTaskDomainPreviewItems = 64
)

type TaskDomainPreviewStatus = taskdomain.PreviewStatus

const (
	TaskDomainPreviewReady            = taskdomain.PreviewReady
	TaskDomainPreviewInvalidRequest   = taskdomain.PreviewInvalidRequest
	TaskDomainPreviewEvidenceRejected = taskdomain.PreviewEvidenceRejected
	TaskDomainPreviewFailed           = taskdomain.PreviewFailed
)

type taskDomainPreviewRequest struct {
	Items []taskdomain.PreviewRequest `json:"items"`
}

type taskDomainPreviewResponse struct {
	RecipeVersion   string                   `json:"recipe_version"`
	RecipeInfluence float64                  `json:"recipe_influence"`
	Recipes         any                      `json:"recipes"`
	Items           []taskdomain.PreviewItem `json:"items"`
}

func InternalTaskDomainPreviewHandler(previewer taskdomain.Previewer) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req taskDomainPreviewRequest
		if err := decodeTaskDomainPreviewRequest(c, &req); err != nil || len(req.Items) == 0 || len(req.Items) > maxTaskDomainPreviewItems {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "between 1 and 64 preview items are required"})
			return
		}
		recipes := previewer.PreviewRecipes()
		response := taskDomainPreviewResponse{
			RecipeVersion: recipes.Version, RecipeInfluence: recipes.Influence, Recipes: recipes.Recipes,
			Items: make([]taskdomain.PreviewItem, 0, len(req.Items)),
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

func previewTaskDomainItem(c *gin.Context, previewer taskdomain.Previewer, req taskdomain.PreviewRequest) taskdomain.PreviewItem {
	invalid := taskdomain.PreviewItem{Status: TaskDomainPreviewInvalidRequest}
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
	return previewer.Preview(observability.FromGin(c), req, profile)
}
