package openai

import (
	"context"
	"net/http"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/requestcontext"

	"github.com/gin-gonic/gin"
)

type codexModelsResponse struct {
	Models []struct{} `json:"models"`
}

func ModelsHandler(fallback gin.HandlerFunc, codexCatalog func(context.Context, http.Header, string) ([]byte, error)) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := requestcontext.ClientIdentityFromHeaders(c.Request.Header)
		if id.ClientApp != requestcontext.ClientAppCodex {
			fallback(c)
			return
		}
		if c.GetHeader(requestcontext.CodexNativeModelPinHeader) != "1" {
			c.JSON(http.StatusOK, codexModelsResponse{Models: []struct{}{}})
			return
		}
		catalog, err := codexCatalog(c.Request.Context(), c.Request.Header, c.Query("client_version"))
		if err != nil {
			observability.FromGin(c).Warn("Codex model catalog unavailable", "err", err)
			c.JSON(http.StatusOK, codexModelsResponse{Models: []struct{}{}})
			return
		}
		c.Data(http.StatusOK, "application/json", catalog)
	}
}

func CodexCatalogHandler(catalog func(context.Context, http.Header, string) ([]byte, error)) gin.HandlerFunc {
	return func(c *gin.Context) {
		body, err := catalog(c.Request.Context(), c.Request.Header, c.Query("client_version"))
		if err != nil {
			observability.FromGin(c).Error("Codex model catalog unavailable", "err", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "codex_model_catalog_unavailable"})
			return
		}
		c.Data(http.StatusOK, "application/json", body)
	}
}
