package openai

import (
	"context"
	"errors"
	"io"
	"net/http"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/requestcontext"

	"github.com/gin-gonic/gin"
)

type CodexImageForwarder func(ctx context.Context, headers http.Header, operation string, body []byte, w http.ResponseWriter) error

func ImagesHandler(operation string, forward CodexImageForwarder) gin.HandlerFunc {
	return func(c *gin.Context) {
		body, err := io.ReadAll(io.LimitReader(c.Request.Body, requestcontext.MaxRequestBodyBytes+1))
		if err != nil {
			writeOpenAIError(c, http.StatusBadRequest, "invalid_request_error", "Failed to read request body.")
			return
		}
		if len(body) > requestcontext.MaxRequestBodyBytes {
			writeOpenAIError(c, http.StatusRequestEntityTooLarge, "invalid_request_error", "Request body too large.")
			return
		}
		err = forward(c.Request.Context(), c.Request.Header, operation, body, c.Writer)
		if err == nil || c.Writer.Written() {
			return
		}
		if errors.Is(err, proxy.ErrCodexImagesRequireOwnCredential) || errors.Is(err, proxy.ErrCodexImagesUnavailableWithGateway) {
			writeOpenAIError(c, http.StatusForbidden, "invalid_request_error", err.Error())
			return
		}
		var upstreamErr *providers.UpstreamErrorResponse
		if errors.As(err, &upstreamErr) {
			c.Data(upstreamErr.Status, "application/json", upstreamErr.Body)
			return
		}
		observability.FromGin(c).Error("Codex image request failed", "operation", operation, "err", err)
		writeOpenAIError(c, http.StatusBadGateway, "api_error", "Image request failed upstream.")
	}
}
