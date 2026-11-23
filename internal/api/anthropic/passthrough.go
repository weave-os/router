package anthropic

import (
	"context"
	"io"
	"net/http"

	"weave-os/router/internal/ingress"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/requestcontext"

	"github.com/gin-gonic/gin"
)

type Passthrough interface {
	PassthroughToProvider(ctx context.Context, body []byte, w http.ResponseWriter, r *http.Request) error
}

func PassthroughHandler(svc Passthrough, vocab ingress.ErrorVocabulary) gin.HandlerFunc {
	return func(c *gin.Context) {
		log := observability.FromGin(c)

		body, err := io.ReadAll(io.LimitReader(c.Request.Body, requestcontext.MaxRequestBodyBytes+1))
		if err != nil {
			log.Debug("Failed to read passthrough request body", "err", err)
			writeAnthropicError(c, http.StatusBadRequest, "invalid_request_error", "Failed to read request body.")
			return
		}
		if len(body) > requestcontext.MaxRequestBodyBytes {
			logOversizeBody(log, c.Request)
			writeAnthropicError(c, http.StatusRequestEntityTooLarge, "invalid_request_error", "Request body too large.")
			return
		}

		if err := svc.PassthroughToProvider(c.Request.Context(), body, c.Writer, c.Request); err != nil {
			cls, ok := vocab.Classify(err)
			if ok && cls.Kind == ingress.DispatchErrorUpstreamStatus {
				if c.Writer.Written() {
					return
				}
				writeAnthropicError(c, cls.Status, "api_error", cls.Message)
				return
			}
			if c.Writer.Written() {
				log.Error("Passthrough failed mid-stream", "err", err)
				return
			}
			if ok && cls.Kind == ingress.DispatchErrorNotImplemented {
				writeAnthropicError(c, cls.Status, anthropicErrorType(cls), cls.Message)
				return
			}
			log.Error("Passthrough failed", "err", err)
			writeAnthropicError(c, http.StatusBadGateway, "api_error", "Upstream call failed.")
			return
		}
	}
}
