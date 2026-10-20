package gemini

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/server/middleware"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/sjson"
)

const (
	generateContentSuffix       = ":generateContent"
	streamGenerateContentSuffix = ":streamGenerateContent"
)

// GenerateContentHandler wires POST /v1beta/models/:modelAction to proxy.Service.ProxyGeminiGenerateContent.
// The colon-suffixed action lives inside a single Gin path parameter because Gin treats `:` outside the
// leading position as a literal.
func GenerateContentHandler(svc *proxy.Service, authSvc *auth.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		log := observability.FromGin(c)

		modelAction := c.Param("modelAction")
		model, stream, ok := splitModelAction(modelAction)
		if !ok {
			writeGeminiError(c, http.StatusNotFound, "INVALID_ARGUMENT",
				fmt.Sprintf("Unknown action %q: expected :generateContent or :streamGenerateContent.", modelAction))
			return
		}

		body, err := io.ReadAll(io.LimitReader(c.Request.Body, proxy.MaxRequestBodyBytes+1))
		if err != nil {
			log.Debug("Failed to read request body", "err", err)
			writeGeminiError(c, http.StatusBadRequest, "INVALID_ARGUMENT", "Failed to read request body.")
			return
		}
		if len(body) > proxy.MaxRequestBodyBytes {
			writeGeminiError(c, http.StatusRequestEntityTooLarge, "INVALID_ARGUMENT", "Request body too large.")
			return
		}

		body, err = injectModelAndStream(body, model, stream)
		if err != nil {
			log.Debug("Failed to inject Gemini synthetic fields", "err", err)
			writeGeminiError(c, http.StatusBadRequest, "INVALID_ARGUMENT", "Request body must be a JSON object.")
			return
		}

		ctx := requestcontext.WithClientIdentity(c.Request.Context(), proxy.ClientIdentityFromHeaders(c.Request.Header))
		ctx = proxy.ResolveUserFromContext(ctx, authSvc, middleware.InstallationFrom(c))
		ctx, err = proxy.ResolveRoutingAssignment(ctx, authSvc, middleware.InstallationFrom(c))
		if err != nil {
			log.Error("Failed to resolve routing assignment", "installation_id", middleware.InstallationFrom(c).ID, "err", err)
			c.Header("Retry-After", "1")
			writeGeminiError(c, http.StatusServiceUnavailable, "UNAVAILABLE", "Routing policy temporarily unavailable.")
			return
		}
		c.Request = c.Request.WithContext(ctx)

		_ = svc.ProxyGeminiGenerateContent(c.Request.Context(), body, c.Writer, c.Request)
	}
}

func splitModelAction(modelAction string) (model string, stream bool, ok bool) {
	switch {
	case strings.HasSuffix(modelAction, streamGenerateContentSuffix):
		model = strings.TrimSuffix(modelAction, streamGenerateContentSuffix)
		stream = true
	case strings.HasSuffix(modelAction, generateContentSuffix):
		model = strings.TrimSuffix(modelAction, generateContentSuffix)
	default:
		return "", false, false
	}
	if model == "" {
		return "", false, false
	}
	return model, stream, true
}

func injectModelAndStream(body []byte, model string, stream bool) ([]byte, error) {
	out, err := sjson.SetBytes(body, "model", model)
	if err != nil {
		return nil, err
	}
	out, err = sjson.SetBytes(out, "stream", stream)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func writeGeminiError(c *gin.Context, status int, errStatus, message string) {
	c.AbortWithStatusJSON(status, gin.H{
		"error": gin.H{
			"code":    status,
			"message": message,
			"status":  errStatus,
		},
	})
}
