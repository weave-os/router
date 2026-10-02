package gateway

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/requestcontext"
)

const maxRoutingDiscoveryBodyBytes = 16 << 10
const routingDiscoveryTokenHeader = "X-Weave-Internal-Token" //nolint:gosec // header name, not a credential

type gatewayDiscoveryRequest struct {
	Target     policyregistry.ServingTarget `json:"target"`
	ProfileKey string                       `json:"profile_key"`
}

// serveRoutingDiscovery resolves the current target binding before forwarding
// the content-free read to that binding's worker. It never mints an inference admission.
func (h *Handler) serveRoutingDiscovery(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if h.products == nil || h.products.InternalToken == "" || subtle.ConstantTimeCompare([]byte(strings.TrimSpace(r.Header.Get(routingDiscoveryTokenHeader))), []byte(h.products.InternalToken)) != 1 {
		http.Error(w, "invalid internal credential", http.StatusUnauthorized)
		return
	}
	policyregistry.StripServingHeaders(r.Header)
	r.Header.Set(routingDiscoveryTokenHeader, h.products.InternalToken)
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRoutingDiscoveryBodyBytes+1))
	if err != nil || len(body) > maxRoutingDiscoveryBodyBytes {
		http.Error(w, "invalid discovery request", http.StatusBadRequest)
		return
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	var request gatewayDiscoveryRequest
	if err := decoder.Decode(&request); err != nil {
		http.Error(w, "invalid discovery request", http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(new(json.RawMessage)); err != io.EOF {
		http.Error(w, "invalid discovery request", http.StatusBadRequest)
		return
	}
	environment, err := request.Target.Environment()
	if err != nil || environment != h.products.Environment || invalidDiscoveryProfile(request.ProfileKey) {
		http.Error(w, "invalid discovery target or profile", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	r = r.Clone(ctx)
	admission, err := (policyregistry.ServingAdmission{Store: h.registry}).Decide(ctx, policyregistry.SerializedAdmission{
		Projection: policyregistry.AdmissionProjection{Target: request.Target, ProfileKey: request.ProfileKey},
		Clock:      func(context.Context) (time.Time, error) { return time.Now(), nil },
	})
	if err != nil {
		observability.FromContext(ctx).Warn("Managed discovery activation unavailable", "target", request.Target, "profile_key", request.ProfileKey, "err", err)
		http.Error(w, "managed policy unavailable", http.StatusServiceUnavailable)
		return
	}
	binding, err := policyregistry.ResolveAdmissionBinding(ctx, h.registry, admission)
	if err != nil {
		observability.FromContext(ctx).Warn("Managed discovery binding unavailable", "target", request.Target, "profile_key", request.ProfileKey, "err", err)
		http.Error(w, "managed policy unavailable", http.StatusServiceUnavailable)
		return
	}
	// Forward preserves the original bounded body and internal token. The worker
	// independently validates the request and reads the current policy again.
	h.forward(w, r, requestcontext.ConversationChat, body, binding, "")
}

func invalidDiscoveryProfile(profileKey string) bool {
	if profileKey == "" {
		return false
	}
	parsed, err := uuid.Parse(profileKey)
	return err != nil || parsed == uuid.Nil || parsed.String() != profileKey
}
