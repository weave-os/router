package openai

import (
	"context"
	"net/http"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/proxy"

	"github.com/gin-gonic/gin"
)

type codexModelsResponse struct {
	Models []struct{} `json:"models"`
}

// ModelsHandler adds Weave's automatic choice to the Codex catalog and
// delegates other clients to the existing provider passthrough.
func ModelsHandler(fallback gin.HandlerFunc, codexCatalog func(context.Context, http.Header, string) ([]byte, error)) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := proxy.ClientIdentityFromHeaders(c.Request.Header)
		if id.ClientApp != proxy.ClientAppCodex {
			fallback(c)
			return
		}
		if c.GetHeader(proxy.CodexNativeModelPinHeader) != "1" {
			c.JSON(http.StatusOK, codexModelsResponse{Models: []struct{}{}})
			return
		}
		catalog, err := codexCatalog(c.Request.Context(), c.Request.Header, c.Query("client_version"))
		if err != nil {
			// Codex merges an empty response with its bundled catalog. Keep the
			// picker usable when account discovery is unavailable.
			observability.FromGin(c).Warn("Codex model catalog unavailable", "err", err)
			c.JSON(http.StatusOK, codexModelsResponse{Models: []struct{}{}})
			return
		}
		c.Data(http.StatusOK, "application/json", catalog)
	}
}
