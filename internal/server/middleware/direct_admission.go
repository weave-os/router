package middleware

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
)

var errServingFleetMismatch = errors.New("serving admission belongs to another fleet")

func prepareDirectTest(c *gin.Context, svc *auth.Service, cfg *ServingAdmissionConfig) (*policyregistry.TestPlanScope, error) {
	grant, session := c.GetHeader(policyregistry.TestPlanGrantHeader), c.GetHeader(policyregistry.TestPlanSessionHeader)
	c.Request.Header.Del(policyregistry.TestPlanGrantHeader)
	c.Request.Header.Del(policyregistry.TestPlanSessionHeader)
	policyregistry.StripServingHeaders(c.Request.Header)
	if grant == "" {
		return nil, nil
	}
	if cfg.TestPlans == nil || !cfg.TestBudgetEnabled || !directTestSurface(c.Request) {
		return nil, errors.New("internal test launch unavailable")
	}
	installation, key, _, err := svc.VerifyPlatformAPIKey(c.Request.Context(), extractToken(c))
	if err != nil {
		return nil, err
	}
	assertion, err := cfg.TestPlans.Admit(c.Request.Context(), grant, installation.ID, key.ID, session)
	if err != nil {
		return nil, err
	}
	if assertion.Admission.Target != cfg.Identity.Target {
		return nil, errors.New("test grant belongs to another fleet")
	}
	credential := extractToken(c)
	c.Request.Header.Del("Authorization")
	c.Request.Header.Del("x-api-key")
	c.Request.Header.Del("X-Weave-User-Email")
	c.Request.Header.Del("X-Weave-User-Name")
	c.Request.Header.Set(auth.RouterKeyHeader, credential)
	c.Request = c.Request.WithContext(policyregistry.WithServingAssertion(c.Request.Context(), assertion))
	return assertion.TestPlan, nil
}

func directAssertion(ctx context.Context, cfg *ServingAdmissionConfig, installationID, keyID string, r *http.Request, body []byte) (policyregistry.ServingAssertion, error) {
	if assertion := policyregistry.ServingAssertionFromContext(ctx); assertion != nil {
		return *assertion, nil
	}
	session := directConversationID(r, body)
	decider := policyregistry.ServingAdmission{Store: cfg.Store}
	var scope policyregistry.AdmissionScope
	var binding policyregistry.SessionReleaseBinding
	var err error
	if model, inference := directRoutingModel(r, body); inference {
		scope, binding, err = cfg.Decisions.AdmitRoutingModel(ctx, installationID, keyID, session, model, decider.Decide)
	} else {
		scope, binding, err = cfg.Decisions.Admit(ctx, installationID, keyID, session, decider.Decide)
	}
	if err != nil {
		return policyregistry.ServingAssertion{}, err
	}
	if binding.Target != cfg.Identity.Target {
		return policyregistry.ServingAssertion{}, errServingFleetMismatch
	}
	// Historical policy/classifier references survive a code release; physical
	// revision identity remains mandatory for release preparation, not retention.
	if _, err := policyregistry.ResolveAdmissionBinding(ctx, cfg.Store, binding); err != nil {
		return policyregistry.ServingAssertion{}, err
	}
	return policyregistry.ServingAssertion{APIKeyID: keyID, BodySHA256: policyregistry.Digest(body), Scope: scope, Admission: binding}, nil
}

// directRoutingModel treats implicit routing as the same classifier as weave-auto.
// Preparation and inspection endpoints must not choose a conversation's classifier.
func directRoutingModel(r *http.Request, body []byte) (router.RoutingModel, bool) {
	if r.Method != http.MethodPost {
		return "", false
	}
	model := gjson.GetBytes(body, "model").String()
	switch r.URL.Path {
	case "/v1/messages", "/v1/chat/completions", "/v1/responses", "/v1/route", "/v1/route/preview", "/v1/route/handoff":
	default:
		if !strings.HasPrefix(r.URL.Path, "/v1beta/models/") || (!strings.HasSuffix(r.URL.Path, ":generateContent") && !strings.HasSuffix(r.URL.Path, ":streamGenerateContent")) {
			return "", false
		}
		// Gemini's handler injects this URL model over any synthetic body.model.
		model, _, _ = strings.Cut(strings.TrimPrefix(r.URL.Path, "/v1beta/models/"), ":")
	}
	if router.IsRoutingModel(model) {
		return router.RoutingModel(model), true
	}
	if strings.HasPrefix(model, "weave-") {
		return "", false // Unknown selectors are rejected by routing-model middleware.
	}
	return router.ModelAutomatic, true
}

func directConversationID(r *http.Request, body []byte) string {
	surface := requestcontext.ConversationChat
	switch r.URL.Path {
	case "/v1/messages", "/v1/messages/count_tokens", "/v1/route", "/v1/route/preview", "/v1/route/handoff":
		surface = requestcontext.ConversationAnthropic
	case "/v1/responses":
		surface = requestcontext.ConversationResponses
	default:
		if strings.HasPrefix(r.URL.Path, "/v1beta/models/") {
			surface = requestcontext.ConversationGemini
		}
	}
	return requestcontext.CanonicalConversationID(r.Header, body, surface)
}

func directTestSurface(r *http.Request) bool {
	if r.Method == http.MethodGet {
		return r.URL.Path == "/v1/test-plan/validate" || r.URL.Path == "/v1/models" || r.URL.Path == "/v1/router/hmm-roster" || r.URL.Path == "/v1/display-settings"
	}
	if r.Method != http.MethodPost {
		return false
	}
	switch r.URL.Path {
	case "/v1/messages", "/v1/messages/count_tokens", "/v1/route", "/v1/route/preview", "/v1/chat/completions", "/v1/responses", "/v1/router/threads":
		return true
	}
	return strings.HasPrefix(r.URL.Path, "/v1beta/models/") &&
		(strings.HasSuffix(r.URL.Path, ":generateContent") || strings.HasSuffix(r.URL.Path, ":streamGenerateContent"))
}
