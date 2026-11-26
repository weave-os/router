package admin_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/api/admin"
	anthropicapi "weave-os/router/internal/api/anthropic"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/ingress"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/hmm"
)

type fixedDecisionRouter struct{ decision router.Decision }

func (r fixedDecisionRouter) Route(context.Context, router.Request) (router.Decision, error) {
	return r.decision, nil
}

type servedBodyClient struct{ plainClient }

func (servedBodyClient) Proxy(_ context.Context, _ router.Decision, _ providers.PreparedRequest, w http.ResponseWriter, _ *http.Request) error {
	w.WriteHeader(http.StatusOK)
	_, err := w.Write([]byte(`{"id":"msg_1","type":"message"}`))
	return err
}

// TestServingReleaseHeadersAppearOnlyOnDisplaySettings serves a model turn and
// the status route for the same opted-in managed installation. The golden
// harness records no custom release headers, so this pins them here instead.
func TestServingReleaseHeadersAppearOnlyOnDisplaySettings(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const (
		releaseDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
		policyDigest  = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
	)
	managedOptedIn := func(c *gin.Context) {
		c.Set("router_installation", &auth.Installation{ID: "inst-1"})
		ctx := requestcontext.WithServingIdentity(c.Request.Context(), requestcontext.ServingIdentity{
			Target: "prod/stable", ReleaseID: releaseDigest, Recipe: requestcontext.ServingRecipe{PolicySHA256: policyDigest},
		})
		c.Request = c.Request.WithContext(context.WithValue(ctx, requestcontext.InstallationServingReleaseHeadersContextKey{}, true))
	}
	service := proxy.WithStrategyPorts(proxy.NewService(
		fixedDecisionRouter{decision: router.Decision{Provider: providers.ProviderAnthropic, Model: "claude-sonnet-4-5", Reason: "test_reason"}},
		dispatch.NewClients(map[string]providers.Client{providers.ProviderAnthropic: servedBodyClient{}}),
		nil, nil, false, "", "", nil), hmm.ServingPorts())
	engine := gin.New()
	engine.POST("/v1/messages", managedOptedIn, anthropicapi.MessagesHandler(service, nil, ingress.ErrorVocabulary{Classify: proxy.ClassifyDispatchError, TerminalMessage: proxy.TerminalErrorMessage}))
	engine.GET("/v1/display-settings", managedOptedIn, admin.DisplaySettingsHandler(&fakeAccountLister{}, nil, fakeExperimentChecker{}, nil))

	turnRec := httptest.NewRecorder()
	engine.ServeHTTP(turnRec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/messages",
		strings.NewReader(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hi"}],"max_tokens":4096}`)))
	require.Equal(t, http.StatusOK, turnRec.Code, turnRec.Body.String())
	assert.Empty(t, turnRec.Header().Get(requestcontext.HeaderServingReleaseSHA), "a model response never names the release")
	assert.Empty(t, turnRec.Header().Get(requestcontext.HeaderServingPolicySHA), "a model response never names the policy")

	settingsRec := httptest.NewRecorder()
	engine.ServeHTTP(settingsRec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/display-settings", nil))
	require.Equal(t, http.StatusOK, settingsRec.Code, settingsRec.Body.String())
	assert.Equal(t, releaseDigest[:12], settingsRec.Header().Get(requestcontext.HeaderServingReleaseSHA))
	assert.Equal(t, policyDigest[:12], settingsRec.Header().Get(requestcontext.HeaderServingPolicySHA))
}
