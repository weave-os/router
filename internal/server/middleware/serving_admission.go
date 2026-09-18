package middleware

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/requestcontext"
)

// ServingAdmissionConfig is assembled only when the worker is running behind the managed gateway.
type ServingAdmissionConfig struct {
	Signer      *policyregistry.AssertionSigner
	Store       policyregistry.ServingStore
	Identity    policyregistry.WorkerIdentity
	Cache       *policyregistry.ServingRuntimeCache
	Attribution policyregistry.RequestAttributionStore
}

// WithServingAdmission verifies the gateway assertion and loads the admitted snapshot.
// A nil config is a no-op so existing self-hosted and old-config managed workers boot unchanged.
func WithServingAdmission(cfg *ServingAdmissionConfig) gin.HandlerFunc {
	if cfg == nil {
		return func(c *gin.Context) { c.Next() }
	}
	return func(c *gin.Context) {
		body, err := io.ReadAll(io.LimitReader(c.Request.Body, requestcontext.MaxRequestBodyBytes+1))
		if err != nil {
			observability.FromGin(c).Debug("Worker admission body read failed", "method", c.Request.Method, "err", err)
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid_request_body"})
			return
		}
		if len(body) > requestcontext.MaxRequestBodyBytes {
			c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{"error": "request_body_too_large"})
			return
		}
		c.Request.Body = io.NopCloser(bytes.NewReader(body))
		encoded := c.GetHeader(policyregistry.ServingAssertionHeader)
		credential := extractToken(c)
		assertion, err := cfg.Signer.Verify(encoded, c.Request, body, credential)
		if err != nil {
			observability.FromGin(c).Debug("Worker serving assertion rejected", "method", c.Request.Method, "err", err)
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "serving_assertion_required"})
			return
		}
		key := APIKeyFrom(c)
		installation := InstallationFrom(c)
		if key == nil || installation == nil {
			observability.FromGin(c).Warn("Worker admission identity missing", "method", c.Request.Method, "has_api_key", key != nil, "has_installation", installation != nil)
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "serving_assertion_required"})
			return
		}
		if _, err := policyregistry.ValidateWorkerAdmission(c.Request.Context(), cfg.Store, cfg.Identity, assertion, installation.ID, key.ID); err != nil {
			observability.FromGin(c).Warn("Worker admission identity rejected", "target", assertion.Admission.Target, "activation_id", assertion.Admission.ActivationID, "err", err)
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "serving_admission_rejected"})
			return
		}
		snapshot, err := cfg.Cache.Snapshot(c.Request.Context(), assertion.Admission)
		if err != nil {
			observability.FromGin(c).Error("Admitted worker snapshot unavailable", "target", assertion.Admission.Target, "activation_id", assertion.Admission.ActivationID, "release_sha256", assertion.Admission.Selection.Release.SHA256, "err", err)
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "serving_snapshot_unavailable"})
			return
		}
		ctx := policyregistry.WithServingAssertion(c.Request.Context(), assertion)
		ctx = policyregistry.WithServingSnapshot(ctx, snapshot)
		identity, _ := requestcontext.ServingIdentityFromContext(ctx)
		log := observability.FromContext(ctx).With("serving_target", identity.Target, "serving_activation_id", identity.ActivationID, "serving_release_id", identity.ReleaseID, "serving_binding_id", identity.BindingID, "serving_profile_key", identity.ProfileKey, "serving_profile_revision", identity.ProfileRevision, "serving_binding_generation", identity.BindingGeneration)
		ctx = observability.PromoteRequestLogger(ctx, log)
		requestID := observability.RequestIDFromContext(ctx)
		if cfg.Attribution == nil || requestID == "" {
			log.Error("Managed worker request attribution is not configured")
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "serving_attribution_unavailable"})
			return
		}
		recordCtx, cancelRecord := context.WithTimeout(ctx, 5*time.Second)
		err = cfg.Attribution.RecordServingRequest(recordCtx, requestID, assertion)
		cancelRecord()
		if err != nil {
			log.Error("Managed worker request attribution failed", "err", err)
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "serving_attribution_unavailable"})
			return
		}
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}
