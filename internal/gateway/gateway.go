// Package gateway owns authenticated release admission and a single streaming worker hop.
// It deliberately has no provider clients, billing service, or model-selection runtime.
package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/translate"
)

// CredentialVerifier does not resolve BYOK secrets; workers retain inference authorization/billing.
type CredentialVerifier interface {
	VerifyRoutingCredential(context.Context, string) (*auth.Installation, *auth.APIKey, error)
}

// RevisionAuthorizer obtains IAM for a controller-selected service audience, never a client URL.
type RevisionAuthorizer interface {
	IdentityToken(context.Context, string) (string, error)
}

// Handler dependencies are assembled only by cmd/router-gateway.
type Handler struct {
	credentials CredentialVerifier
	admissions  policyregistry.ServingAdmissionStore
	registry    policyregistry.ServingStore
	signer      *policyregistry.AssertionSigner
	authorizer  RevisionAuthorizer
	transport   http.RoundTripper
	products    *ProductSurfaces
}

// NewHandler requires authoritative storage and signed, IAM-authenticated forwarding.
func NewHandler(credentials CredentialVerifier, admissions policyregistry.ServingAdmissionStore, registry policyregistry.ServingStore, signer *policyregistry.AssertionSigner, authorizer RevisionAuthorizer, transport http.RoundTripper, products ...ProductSurfaces) (*Handler, error) {
	if credentials == nil || admissions == nil || registry == nil || signer == nil || authorizer == nil || transport == nil {
		return nil, errors.New("gateway requires authentication, admission, registry, assertion, IAM and transport dependencies")
	}
	h := &Handler{credentials: credentials, admissions: admissions, registry: registry, signer: signer, authorizer: authorizer, transport: transport}
	if len(products) > 1 {
		return nil, errors.New("gateway accepts one product-surface configuration")
	}
	if len(products) == 1 {
		if err := products[0].validate(); err != nil {
			return nil, err
		}
		h.products = &products[0]
	}
	return h, nil
}

// ServeHTTP preserves original ordinary-request bytes and streams without replay or response buffering.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	policyregistry.StripServingHeaders(r.Header)
	if h.serveProductSurface(w, r) {
		return
	}
	surface, ok := inferenceSurface(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 600*time.Second)
	defer cancel()
	r = r.Clone(ctx)
	credential := auth.RoutingTokenFromHeaders(r.Header)
	authCtx, authCancel := context.WithTimeout(ctx, 10*time.Second)
	installation, key, err := h.credentials.VerifyRoutingCredential(authCtx, credential)
	authCancel()
	if err != nil {
		h.fail(w, r, surface, err)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, requestcontext.MaxRequestBodyBytes+1))
	if err != nil {
		observability.FromContext(ctx).Debug("Gateway request body read failed", "surface", surface, "method", r.Method, "err", err)
		writeError(w, surface, http.StatusBadRequest, "Failed to read request body.")
		return
	}
	if len(body) > requestcontext.MaxRequestBodyBytes {
		writeError(w, surface, http.StatusRequestEntityTooLarge, "Request body too large.")
		return
	}
	if (r.Method == http.MethodPost || r.Method == http.MethodPatch) && (!gjson.ValidBytes(body) || !gjson.ParseBytes(body).IsObject()) {
		writeError(w, surface, http.StatusBadRequest, "Request body must be a JSON object.")
		return
	}
	retired := false
	if acceptsControlCommands(r) {
		retired, err = translate.WriteRetiredBetaRequest(w, r, body, surface)
	}
	if retired {
		if err != nil {
			observability.FromContext(ctx).Debug("Retired beta response could not be delivered", "surface", surface, "method", r.Method, "err", err)
		}
		return
	}
	if err != nil {
		observability.FromContext(ctx).Debug("Retired beta request envelope invalid", "surface", surface, "method", r.Method, "err", err)
		writeError(w, surface, http.StatusBadRequest, "Request envelope is invalid.")
		return
	}
	conversationID := requestcontext.CanonicalConversationID(r.Header, body, surface)
	admissionDecider := policyregistry.ServingAdmission{Store: h.registry}
	scope, admission, err := h.admissions.Admit(ctx, installation.ID, key.ID, conversationID, admissionDecider.Decide)
	if err != nil {
		h.fail(w, r, surface, err)
		return
	}
	prepareCtx, prepareCancel := context.WithTimeout(ctx, 30*time.Second)
	defer prepareCancel()
	binding, err := policyregistry.ResolveAdmissionBinding(prepareCtx, h.registry, admission)
	if err != nil {
		h.fail(w, r, surface, err)
		return
	}
	assertion, err := h.signer.Sign(policyregistry.ServingAssertion{APIKeyID: key.ID, Scope: scope, Admission: admission}, r, body, credential)
	if err != nil {
		h.fail(w, r, surface, err)
		return
	}
	h.forward(w, r, surface, body, binding, assertion)
}

func (h *Handler) forward(w http.ResponseWriter, r *http.Request, surface requestcontext.ConversationSurface, body []byte, binding policyregistry.LaneBinding, assertion string) {
	authorizeCtx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	identityToken, err := h.authorizer.IdentityToken(authorizeCtx, binding.Router.Audience)
	if err != nil {
		h.fail(w, r, surface, err)
		return
	}
	destination, err := url.Parse(binding.Router.URL)
	if err != nil {
		h.fail(w, r, surface, err)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	r.GetBody = nil
	proxy := httputil.ReverseProxy{
		Transport:     h.transport,
		FlushInterval: -1,
		Rewrite: func(request *httputil.ProxyRequest) {
			// SetURL preserves path/query; never follow a worker redirect or a client target header.
			request.SetURL(destination)
			request.Out.Header.Set(policyregistry.ServerlessAuthorizationHeader, "Bearer "+identityToken)
			if assertion != "" {
				request.Out.Header.Set(policyregistry.ServingAssertionHeader, assertion)
			}
		},
		ModifyResponse: func(response *http.Response) error {
			policyregistry.StripServingHeaders(response.Header)
			return nil
		},
		ErrorHandler: func(writer http.ResponseWriter, request *http.Request, forwardErr error) {
			h.fail(writer, request, surface, forwardErr)
		},
	}
	proxy.ServeHTTP(w, r)
}

func inferenceSurface(r *http.Request) (requestcontext.ConversationSurface, bool) {
	if subscriptionSurface(r) {
		return requestcontext.ConversationChat, true
	}
	if r.Method == http.MethodGet {
		switch r.URL.Path {
		case "/validate", "/v1/models", "/v1/display-settings", "/v1/router/models", "/v1/router/policies", "/v1/router/hmm-roster", "/v1/router/routing-distribution":
			return requestcontext.ConversationChat, true
		}
		if singlePathParameter(r.URL.Path, "/v1/models/", "") || singlePathParameter(r.URL.Path, "/v1/sessions/", "/cost") {
			return requestcontext.ConversationChat, true
		}
	}
	if r.Method != http.MethodPost {
		return "", false
	}
	switch r.URL.Path {
	case "/v1/messages", "/v1/messages/count_tokens", "/v1/route", "/v1/route/preview":
		return requestcontext.ConversationAnthropic, true
	case "/v1/chat/completions":
		return requestcontext.ConversationChat, true
	case "/v1/responses":
		return requestcontext.ConversationResponses, true
	default:
		if singlePathParameter(r.URL.Path, "/v1beta/models/", ":generateContent") || singlePathParameter(r.URL.Path, "/v1beta/models/", ":streamGenerateContent") {
			return requestcontext.ConversationGemini, true
		}
		return "", false
	}
}

func acceptsControlCommands(r *http.Request) bool {
	if r.Method != http.MethodPost {
		return false
	}
	switch r.URL.Path {
	case "/v1/messages", "/v1/chat/completions", "/v1/responses":
		return true
	default:
		return strings.HasPrefix(r.URL.Path, "/v1beta/models/")
	}
}

func subscriptionSurface(r *http.Request) bool {
	if r.URL.Path == "/v1/subscriptions/accounts" {
		return r.Method == http.MethodGet || r.Method == http.MethodPost
	}
	return (r.Method == http.MethodPatch || r.Method == http.MethodDelete) && singlePathParameter(r.URL.Path, "/v1/subscriptions/accounts/", "")
}

func singlePathParameter(path, prefix, suffix string) bool {
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return false
	}
	parameter := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	return parameter != "" && parameter != "." && parameter != ".." && !strings.Contains(parameter, "/")
}

func (h *Handler) fail(w http.ResponseWriter, r *http.Request, surface requestcontext.ConversationSurface, err error) {
	// Feedback tokens are credentials embedded in paths/queries: never log the URL.
	log := observability.FromContext(r.Context()).With("surface", surface, "method", r.Method)
	if errors.Is(err, auth.ErrInvalidPrefix) || errors.Is(err, auth.ErrInvalidToken) || errors.Is(err, auth.ErrWrongKeyScope) || errors.Is(err, auth.ErrPersonalCredentialRequired) {
		log.Debug("Gateway credential admission denied", "err", err)
		writeError(w, surface, http.StatusUnauthorized, "Routing credential is invalid or no longer eligible.")
		return
	}
	log.Error("Gateway admission or worker forwarding failed", "err", err)
	writeError(w, surface, http.StatusServiceUnavailable, "Selected router release is unavailable; no fallback was attempted.")
}

func writeError(w http.ResponseWriter, surface requestcontext.ConversationSurface, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	errorType := "api_error"
	if status >= 400 && status < 500 {
		errorType = "invalid_request_error"
	}
	envelope := map[string]any{"error": map[string]any{"type": errorType, "message": message}}
	if surface == requestcontext.ConversationAnthropic {
		envelope["type"] = "error"
	}
	if surface == requestcontext.ConversationGemini {
		envelope["error"] = map[string]any{"code": status, "message": message, "status": http.StatusText(status)}
	}
	// The envelope contains only fixed primitives; a write failure means the client disconnected.
	_ = json.NewEncoder(w).Encode(envelope)
}
