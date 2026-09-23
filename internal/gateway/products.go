package gateway

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/feedback"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/requestcontext"
)

// FeedbackAdmissionLookup scopes persisted request attribution to the token's installation.
type FeedbackAdmissionLookup interface {
	GetFeedbackAdmission(context.Context, string, string) (policyregistry.SessionReleaseBinding, error)
}

// AnalyticsCredentialVerifier cannot mint an inference admission.
type AnalyticsCredentialVerifier interface {
	VerifyAnalyticsCredential(context.Context, string) error
}

// ProductSurfaces supplies the independent credentials for existing non-inference endpoints.
// A nil feedback signer retains the worker's feature-disabled behavior.
type ProductSurfaces struct {
	Environment policyregistry.Environment
	Analytics   AnalyticsCredentialVerifier
	Feedback    *feedback.Signer
	Attribution FeedbackAdmissionLookup
}

func (p ProductSurfaces) validate() error {
	if err := policyregistry.ValidateEnvironment(p.Environment); err != nil {
		return err
	}
	if p.Analytics == nil || p.Feedback != nil && p.Attribution == nil {
		return errors.New("gateway product surfaces require analytics authentication and enabled feedback attribution")
	}
	return nil
}

func (h *Handler) serveProductSurface(w http.ResponseWriter, r *http.Request) bool {
	if h.products == nil {
		return false
	}
	switch {
	case analyticsSurface(r):
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()
		r = r.Clone(ctx)
		authCtx, authCancel := context.WithTimeout(ctx, 10*time.Second)
		err := h.products.Analytics.VerifyAnalyticsCredential(authCtx, auth.RoutingTokenFromHeaders(r.Header))
		authCancel()
		if err != nil {
			h.fail(w, r, requestcontext.ConversationChat, err)
			return true
		}
		h.forwardDefault(w, r)
		return true
	case feedbackSurface(r):
		h.serveFeedback(w, r)
		return true
	case r.Method == http.MethodGet && (r.URL.Path == "/v1/version" || h.products.Feedback != nil && (r.URL.Path == "/v1/feedback/assets/wooly-wave.png" || r.URL.Path == "/v1/feedback/assets/weave.svg")):
		h.forwardDefault(w, r)
		return true
	default:
		return false
	}
}

func analyticsSurface(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	switch r.URL.Path {
	case "/v1/analytics/routing-decisions", "/v1/analytics/models", "/v1/analytics/schema":
		return true
	default:
		return false
	}
}

func feedbackSurface(r *http.Request) bool {
	return r.Method == http.MethodPost && r.URL.Path == "/v1/feedback/link" || r.Method == http.MethodGet && (r.URL.Path == "/v1/feedback/rate" || singlePathParameter(r.URL.Path, "/v1/feedback/link/", ""))
}

func (h *Handler) serveFeedback(w http.ResponseWriter, r *http.Request) {
	if h.products.Feedback == nil {
		http.NotFound(w, r)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	r = r.Clone(ctx)
	const maxFeedbackBodyBytes = 64 * 1024
	body, err := io.ReadAll(io.LimitReader(r.Body, maxFeedbackBodyBytes+1))
	if err != nil {
		observability.FromContext(ctx).Debug("Feedback request body read failed", "method", r.Method, "err", err)
		writeError(w, requestcontext.ConversationChat, http.StatusBadRequest, "Invalid feedback body.")
		return
	}
	if len(body) > maxFeedbackBodyBytes {
		writeError(w, requestcontext.ConversationChat, http.StatusBadRequest, "Invalid feedback body.")
		return
	}
	token := strings.TrimPrefix(r.URL.Path, "/v1/feedback/link/")
	if r.URL.Path == "/v1/feedback/rate" {
		token = r.URL.Query().Get("t")
	} else if r.Method == http.MethodPost {
		var submission struct {
			Token string `json:"token"`
		}
		if err := json.Unmarshal(body, &submission); err != nil {
			observability.FromContext(ctx).Debug("Feedback submission JSON rejected", "method", r.Method, "err", err)
			writeError(w, requestcontext.ConversationChat, http.StatusBadRequest, "Invalid feedback body.")
			return
		}
		token = submission.Token
	}
	claims, err := h.products.Feedback.Verify(token)
	if err != nil {
		observability.FromContext(ctx).Debug("Feedback token rejected", "err", err)
		status, message := http.StatusNotFound, "invalid"
		if errors.Is(err, feedback.ErrExpiredToken) {
			status, message = http.StatusGone, "expired"
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
		return
	}
	admission, err := h.products.Attribution.GetFeedbackAdmission(ctx, claims.InstallationID, claims.RequestID)
	if errors.Is(err, sql.ErrNoRows) {
		observability.FromContext(ctx).Debug("Feedback request attribution not found", "installation_id", claims.InstallationID, "rated_request_id", claims.RequestID, "err", err)
		writeError(w, requestcontext.ConversationChat, http.StatusNotFound, "Original request attribution is unavailable.")
		return
	}
	if err != nil {
		h.fail(w, r, requestcontext.ConversationChat, err)
		return
	}
	environment, err := admission.Target.Environment()
	if err != nil || environment != h.products.Environment {
		observability.FromContext(ctx).Warn("Feedback attribution environment rejected", "target", admission.Target, "environment", environment, "expected_environment", h.products.Environment, "err", err)
		h.fail(w, r, requestcontext.ConversationChat, errors.New("feedback attribution belongs to another environment"))
		return
	}
	binding, err := policyregistry.ResolveAdmissionBinding(ctx, h.registry, admission)
	if err != nil {
		h.fail(w, r, requestcontext.ConversationChat, err)
		return
	}
	h.forward(w, r, requestcontext.ConversationChat, body, binding, "")
}

func (h *Handler) forwardDefault(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	r = r.Clone(ctx)
	prepareCtx, prepareCancel := context.WithTimeout(ctx, 10*time.Second)
	defer prepareCancel()
	binding, err := h.defaultBinding(prepareCtx)
	if err != nil {
		h.fail(w, r, requestcontext.ConversationChat, err)
		return
	}
	h.forward(w, r, requestcontext.ConversationChat, nil, binding, "")
}

func (h *Handler) defaultTarget() policyregistry.ServingTarget {
	if h.products.Environment == policyregistry.EnvironmentStaging {
		return policyregistry.TargetStaging
	}
	return policyregistry.TargetStable
}

func (h *Handler) defaultBinding(ctx context.Context) (policyregistry.LaneBinding, error) {
	target := h.defaultTarget()
	// Read-only exports and public assets have no conversation, enrollment or
	// customer policy. Choosing their worker never grants inference authority.
	admissionDecider := policyregistry.ServingAdmission{Store: h.registry}
	admission, err := admissionDecider.Decide(ctx, policyregistry.SerializedAdmission{Projection: policyregistry.AdmissionProjection{Target: target}, Clock: func(context.Context) (time.Time, error) { return time.Now(), nil }})
	if err != nil {
		return policyregistry.LaneBinding{}, err
	}
	return policyregistry.ResolveAdmissionBinding(ctx, h.registry, admission)
}
