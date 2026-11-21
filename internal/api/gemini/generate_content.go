package gemini

import (
	"errors"
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
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	generateContentSuffix       = ":generateContent"
	streamGenerateContentSuffix = ":streamGenerateContent"
)

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

		body, err := io.ReadAll(io.LimitReader(c.Request.Body, requestcontext.MaxRequestBodyBytes+1))
		if err != nil {
			log.Debug("Failed to read request body", "err", err)
			writeGeminiError(c, http.StatusBadRequest, "INVALID_ARGUMENT", "Failed to read request body.")
			return
		}
		if len(body) > requestcontext.MaxRequestBodyBytes {
			writeGeminiError(c, http.StatusRequestEntityTooLarge, "INVALID_ARGUMENT", "Request body too large.")
			return
		}

		body, err = injectModelAndStream(body, model, stream)
		if err != nil {
			log.Debug("Failed to inject Gemini synthetic fields", "err", err)
			writeGeminiError(c, http.StatusBadRequest, "INVALID_ARGUMENT", "Request body must be a JSON object.")
			return
		}

		ctx := requestcontext.WithClientIdentity(c.Request.Context(), requestcontext.ClientIdentityFromHeaders(c.Request.Header))
		ctx = proxy.ResolveUserFromContext(ctx, authSvc, middleware.InstallationFrom(c), middleware.APIKeyFrom(c))
		ctx, err = proxy.ResolveRoutingAssignment(ctx, authSvc, middleware.InstallationFrom(c))
		if err != nil {
			log.Error("Failed to resolve routing assignment", "installation_id", middleware.InstallationFrom(c).ID, "err", err)
			c.Header("Retry-After", "1")
			writeGeminiError(c, http.StatusServiceUnavailable, "UNAVAILABLE", "Routing policy temporarily unavailable.")
			return
		}
		c.Request = c.Request.WithContext(ctx)

		if err := svc.ProxyGeminiGenerateContent(c.Request.Context(), body, c.Writer, c.Request); err != nil {
			cls, ok := proxy.ClassifyDispatchError(err)
			if ok && cls.Kind == proxy.DispatchErrorUpstreamStatus {
				if c.Writer.Written() {
					return
				}
				writeGeminiError(c, cls.Status, "UPSTREAM_ERROR", cls.Message)
				return
			}
			if c.Writer.Written() {
				log.Error("Proxy failed mid-stream", "err", err)
				return
			}
			if errors.Is(err, proxy.ErrGeminiCrossFormatUnsupported) {
				writeGeminiError(c, http.StatusNotImplemented, "UNIMPLEMENTED", "Cross-format request not supported by the upstream Gemini provider.")
				return
			}
			if ok {
				proxy.LogDispatchErrorClass(log, cls, err)
				if cls.RetryAfter {
					c.Header("Retry-After", "1")
				}
				if cls.NoClientRetry {
					c.Header("x-should-retry", "false")
				}
				writeGeminiError(c, cls.Status, geminiErrorStatus(cls), cls.Message)
				return
			}
			log.Error("Gemini proxy failed", "err", err)
			writeGeminiError(c, http.StatusBadGateway, "UPSTREAM_ERROR", proxy.TerminalErrorMessage(err))
		}
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

var errBodyNotJSONObject = errors.New("request body is not a JSON object")

func injectModelAndStream(body []byte, model string, stream bool) ([]byte, error) {
	if !gjson.ValidBytes(body) || !gjson.ParseBytes(body).IsObject() {
		return nil, errBodyNotJSONObject
	}
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

func geminiErrorStatus(cls proxy.DispatchErrorClass) string {
	if cls.Refusal != nil {
		return cls.Refusal.GeminiStatus()
	}
	switch cls.Kind {
	case proxy.DispatchErrorNotImplemented:
		return "UNIMPLEMENTED"
	case proxy.DispatchErrorProviderNotConfigured:
		return "FAILED_PRECONDITION"
	case proxy.DispatchErrorRequestNotJSONObject, proxy.DispatchErrorContextWindowExceeded:
		return "INVALID_ARGUMENT"
	case proxy.DispatchErrorNoEligibleProvider:
		return "FAILED_PRECONDITION"
	case proxy.DispatchErrorInvalidRoutingKnobs, proxy.DispatchErrorPlanOverrideRejected:
		return "INVALID_ARGUMENT"
	case proxy.DispatchErrorHMMUnavailable, proxy.DispatchErrorDecisionModelUnavailable, proxy.DispatchErrorPolicyUnavailable, proxy.DispatchErrorClusterUnavailable, proxy.DispatchErrorPlanUnresolvable:
		return "UNAVAILABLE"
	default:
		return "UPSTREAM_ERROR"
	}
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
