package middleware

import (
	"bytes"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/translate"
)

// ServingAdmissionConfig is assembled only when the worker is running behind the managed gateway.
type ServingAdmissionConfig struct {
	Signer   *policyregistry.AssertionSigner
	Store    policyregistry.ServingStore
	Identity policyregistry.WorkerIdentity
	Cache    *policyregistry.ServingRuntimeCache
}

func workerInferenceSurface(r *http.Request) requestcontext.ConversationSurface {
	switch r.URL.Path {
	case "/v1/messages":
		return requestcontext.ConversationAnthropic
	case "/v1/chat/completions":
		return requestcontext.ConversationChat
	case "/v1/responses":
		return requestcontext.ConversationResponses
	default:
		if strings.HasPrefix(r.URL.Path, "/v1beta/models/") {
			return requestcontext.ConversationGemini
		}
		return requestcontext.ConversationChat
	}
}

// WithServingAdmission verifies the gateway assertion, loads the admitted snapshot, and retires beta locally.
// A nil config is a no-op so existing self-hosted and old-config managed workers boot unchanged.
func WithServingAdmission(cfg *ServingAdmissionConfig) gin.HandlerFunc {
	if cfg == nil {
		return func(c *gin.Context) { c.Next() }
	}
	return func(c *gin.Context) {
		body, err := c.GetRawData()
		if err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid_request_body"})
			return
		}
		c.Request.Body = io.NopCloser(bytes.NewReader(body))
		surface := workerInferenceSurface(c.Request)
		retired, err := translate.WriteRetiredBetaRequest(c.Writer, c.Request, body, surface)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid_request"})
			return
		}
		if retired {
			c.Abort()
			return
		}
		c.Request.Body = io.NopCloser(bytes.NewReader(body))
		encoded := c.GetHeader(policyregistry.ServingAssertionHeader)
		credential := extractToken(c)
		assertion, err := cfg.Signer.Verify(encoded, c.Request, body, credential)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "serving_assertion_required"})
			return
		}
		key := APIKeyFrom(c)
		installation := InstallationFrom(c)
		if key == nil || installation == nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "serving_assertion_required"})
			return
		}
		if _, err := policyregistry.ValidateWorkerAdmission(c.Request.Context(), cfg.Store, cfg.Identity, assertion, installation.ID, key.ID); err != nil {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "serving_admission_rejected"})
			return
		}
		snapshot, err := cfg.Cache.Snapshot(c.Request.Context(), assertion.Admission)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "serving_snapshot_unavailable"})
			return
		}
		ctx := policyregistry.WithServingAssertion(c.Request.Context(), assertion)
		ctx = policyregistry.WithServingSnapshot(ctx, snapshot)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}
