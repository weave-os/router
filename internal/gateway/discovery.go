package gateway

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/requestcontext"
)

// DiscoveryServiceAuthorizationHeader retains the signed token for application validation.
// Cloud Run may remove the signature from its own serverless authorization header.
const DiscoveryServiceAuthorizationHeader = "X-Weave-Internal-Service-Authorization"

// ServiceIdentityVerifier authenticates the backend independently of routing credentials.
type ServiceIdentityVerifier interface {
	VerifyServiceIdentity(context.Context, string) error
}

type discoverySelectionKind string

const (
	discoveryDefault discoverySelectionKind = "default"
	discoveryProfile discoverySelectionKind = "profile"
)

func privateDiscoverySurface(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	switch r.URL.Path {
	case "/internal/v1/router/models", "/internal/v1/router/policies", "/internal/v1/router/hmm-roster", "/internal/v1/router/routing-distribution":
		return true
	default:
		return false
	}
}

func (h *Handler) serveDiscovery(w http.ResponseWriter, r *http.Request) bool {
	if !privateDiscoverySurface(r) {
		return false
	}
	credentials := r.Header.Values(DiscoveryServiceAuthorizationHeader)
	policyregistry.StripServingHeaders(r.Header)
	if h.products == nil || h.products.Discovery == nil {
		writeError(w, requestcontext.ConversationChat, http.StatusServiceUnavailable, "Private discovery is not configured.")
		return true
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	r = r.Clone(ctx)
	var token string
	if len(credentials) == 1 && len(credentials[0]) <= 16*1024 {
		token = auth.BearerToken(credentials[0])
	}
	authCtx, authCancel := context.WithTimeout(ctx, 10*time.Second)
	err := errors.New("discovery service identity required")
	if token != "" {
		err = h.products.Discovery.VerifyServiceIdentity(authCtx, token)
	}
	authCancel()
	if err != nil {
		observability.FromContext(ctx).Debug("Private discovery identity rejected", "path", r.URL.Path, "credential_count", len(credentials), "err", err)
		writeError(w, requestcontext.ConversationChat, http.StatusUnauthorized, "Discovery service identity required.")
		return true
	}
	query := r.URL.Query()
	profileKey := query.Get("profile_key")
	valid := len(query["selection"]) == 1 && len(query["profile_key"]) <= 1
	switch discoverySelectionKind(query.Get("selection")) {
	case discoveryDefault:
		valid = valid && profileKey == ""
	case discoveryProfile:
		parsed, parseErr := uuid.Parse(profileKey)
		valid = valid && parseErr == nil && parsed != uuid.Nil && parsed.String() == profileKey
	default:
		valid = false
	}
	if !valid {
		observability.FromContext(ctx).Debug("Private discovery selection rejected", "selection_count", len(query["selection"]), "profile_key_count", len(query["profile_key"]))
		writeError(w, requestcontext.ConversationChat, http.StatusBadRequest, "Invalid discovery selection.")
		return true
	}
	query.Del("selection")
	query.Del("profile_key")
	ctx = observability.WithLogger(ctx, observability.FromContext(ctx).With("target", h.defaultTarget(), "profile_key", profileKey))
	r = r.WithContext(ctx)
	r.URL.RawQuery = query.Encode()
	r.Header.Del("Authorization")
	r.Header.Del("X-Weave-Router-Key")
	r.Header.Del("X-Api-Key")
	decider := policyregistry.ServingAdmission{Store: h.registry}
	admission, err := decider.Decide(ctx, policyregistry.SerializedAdmission{
		Projection: policyregistry.AdmissionProjection{Target: h.defaultTarget(), ProfileKey: profileKey},
		Clock:      func(context.Context) (time.Time, error) { return time.Now(), nil },
	})
	if err != nil {
		if errors.Is(err, policyregistry.ErrAssignedProfileUnavailable) {
			observability.FromContext(ctx).Warn("Private discovery profile is unavailable", "err", err)
			writeError(w, requestcontext.ConversationChat, http.StatusNotFound, "Assigned discovery profile is unavailable.")
		} else {
			h.fail(w, r, requestcontext.ConversationChat, err)
		}
		return true
	}
	binding, err := policyregistry.ResolveAdmissionBinding(ctx, h.registry, admission)
	if err != nil {
		h.fail(w, r, requestcontext.ConversationChat, err)
		return true
	}
	selection, err := policyregistry.EncodeDiscoverySelection(policyregistry.WorkerValidationRequest{Target: admission.Target, ProfileKey: admission.ProfileKey, Selection: admission.Selection})
	if err != nil {
		h.fail(w, r, requestcontext.ConversationChat, err)
		return true
	}
	h.forward(w, r, requestcontext.ConversationChat, nil, binding, "", selection)
	return true
}
