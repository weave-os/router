package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/refusal"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/subscriptions/entitlement"
)

type TestPlanAdmitter interface {
	Admit(context.Context, string, string, string, string) (policyregistry.ServingAssertion, error)
}

type ServingAdmissionConfig struct {
	Decisions         *policyregistry.AdmissionDecisionCache
	TestPlans         TestPlanAdmitter
	TestBudgetEnabled bool
	Store             policyregistry.ServingStore
	Identity          policyregistry.WorkerIdentity
	Cache             *policyregistry.ServingRuntimeCache
	Attribution       policyregistry.RequestAttributionStore
	StableBaseURL     string
	BetaBaseURL       string
	Experiments       InstallationExperimentChecker
}

type InstallationExperimentChecker interface {
	InstallationBlindExperimentEnabled(ctx context.Context, installationID string) bool
}

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
		if c.Request.Method == http.MethodPost {
			var requestObject map[string]json.RawMessage
			if len(bytes.TrimSpace(body)) == 0 || json.Unmarshal(body, &requestObject) != nil || requestObject == nil {
				c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid_request_body"})
				return
			}
		}
		c.Request.Body = io.NopCloser(bytes.NewReader(body))
		if rejectMisdirectedChannelModel(c, cfg, body) {
			return
		}
		key := APIKeyFrom(c)
		installation := InstallationFrom(c)
		if key == nil || installation == nil {
			abortRefusal(c, refusal.InvalidKey, nil)
			return
		}
		assertion, err := directAssertion(c.Request.Context(), cfg, installation.ID, key.ID, c.Request, body)
		if err != nil {
			observability.FromGin(c).Warn("Worker admission rejected", "err", err)
			abortAdmissionRejected(c, cfg, err)
			return
		}
		snapshot, err := cfg.Cache.Snapshot(c.Request.Context(), assertion.Admission)
		if err != nil {
			observability.FromGin(c).Error("Admitted worker snapshot unavailable", "target", assertion.Admission.Target, "activation_id", assertion.Admission.ActivationID, "release_sha256", assertion.Admission.Selection.Release.SHA256, "err", err)
			abortRefusal(c, refusal.ServingSnapshotUnavailable, nil)
			return
		}
		if assertion.TestPlan != nil && (!cfg.TestBudgetEnabled || assertion.TestPlan.PolicyRevision != snapshot.Release.Policy.SHA256) {
			observability.FromGin(c).Warn("Internal test snapshot or billing scope rejected", "activation_id", assertion.Admission.ActivationID)
			abortRefusal(c, refusal.TestScopeRejected, nil)
			return
		}
		ctx := policyregistry.WithServingAssertion(c.Request.Context(), assertion)
		ctx = policyregistry.WithServingSnapshot(ctx, snapshot)
		ctx = policyregistry.WithServingRecipe(ctx, cfg.Identity, snapshot)
		if assertion.TestPlan != nil && assertion.TestPlan.Plan != policyregistry.TestPlanStable {
			ctx = entitlement.WithProductScope(ctx, entitlement.Plan(assertion.TestPlan.Plan))
		}
		identity, _ := requestcontext.ServingIdentityFromContext(ctx)
		log := observability.FromContext(ctx).With("serving_target", identity.Target, "serving_activation_id", identity.ActivationID, "serving_release_id", identity.ReleaseID, "serving_binding_id", identity.BindingID, "serving_profile_key", identity.ProfileKey, "serving_profile_revision", identity.ProfileRevision, "serving_binding_generation", identity.BindingGeneration)
		ctx = observability.PromoteRequestLogger(ctx, log)
		requestID := observability.RequestIDFromContext(ctx)
		if cfg.Attribution == nil || requestID == "" {
			log.Error("Managed worker request attribution is not configured")
			abortRefusal(c, refusal.ServingAttributionUnavailable, nil)
			return
		}
		recordCtx, cancelRecord := context.WithTimeout(ctx, 5*time.Second)
		err = cfg.Attribution.RecordServingRequest(recordCtx, requestID, assertion)
		cancelRecord()
		if err != nil {
			log.Error("Managed worker request attribution failed", "err", err)
			abortRefusal(c, refusal.ServingAttributionUnavailable, nil)
			return
		}
		c.Request = c.Request.WithContext(ctx)
		c.Writer = &servingReleaseHeaderWriter{ResponseWriter: c.Writer, request: func() *http.Request { return c.Request }, experiments: cfg.Experiments, installationID: installation.ID}
		c.Next()
	}
}

func abortAdmissionRejected(c *gin.Context, cfg *ServingAdmissionConfig, err error) {
	if status, body, rejected := routingModelAdmissionError(err); rejected {
		c.AbortWithStatusJSON(status, body)
		return
	}
	if status, body, rejected := servingChannelAdmissionError(cfg, err); rejected {
		c.AbortWithStatusJSON(status, body)
		return
	}
	if isAuthFailure(err) {
		handleAuthError(c, err)
		return
	}
	if errors.Is(err, errServingFleetMismatch) {
		abortRefusal(c, refusal.ServingAdmissionRejected, nil)
		return
	}
	abortRefusal(c, refusal.ServingAdmissionUnavailable, nil)
}

func routingModelAdmissionError(err error) (int, gin.H, bool) {
	switch {
	case errors.Is(err, policyregistry.ErrRoutingModelChanged):
		return http.StatusConflict, gin.H{"error": "start_new_thread_to_change_router"}, true
	case errors.Is(err, policyregistry.ErrRoutingModelNotAssigned):
		return http.StatusForbidden, gin.H{"error": "routing_model_not_permitted"}, true
	case errors.Is(err, policyregistry.ErrRoutingModelSessionRequired):
		return http.StatusBadRequest, gin.H{"error": "routing_model_requires_conversation_id", "message": err.Error()}, true
	}
	return 0, nil, false
}
