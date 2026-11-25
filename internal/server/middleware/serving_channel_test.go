package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
)

const channelTestBetaBody = `{"model":"weave-auto-beta","messages":[{"role":"user","content":"hello"}]}`

func channelTestConfig(t *testing.T, target policyregistry.ServingTarget, err error) (*ServingAdmissionConfig, policyregistry.ServingAssertion, *directAdmissionStore) {
	t.Helper()
	cfg, admitted, _, _ := admissionMiddlewareFixtureForTarget(t, target)
	store := &directAdmissionStore{assertion: admitted, err: err}
	decisions, cacheErr := policyregistry.NewAdmissionDecisionCache(store, 10, time.Minute, time.Now)
	require.NoError(t, cacheErr)
	cfg.Decisions = decisions
	cfg.StableBaseURL = "https://stable.example.test"
	cfg.BetaBaseURL = "https://beta.example.test"
	cfg.Attribution = admissionAttributionFunc(func(context.Context, string, policyregistry.ServingAssertion) error {
		t.Fatal("refused request wrote attribution")
		return nil
	})
	return cfg, admitted, store
}

func TestServingAdmissionSendsBetaModelToBetaHostBeforeAdmission(t *testing.T) {
	for _, target := range []policyregistry.ServingTarget{policyregistry.TargetStable, policyregistry.TargetInternal} {
		t.Run(string(target), func(t *testing.T) {
			cfg, _, store := channelTestConfig(t, target, nil)
			response := runAdmissionMiddlewareWithBody(t, cfg, policyregistry.ServingAssertion{}, channelTestBetaBody, func(*gin.Context) {
				t.Fatal("misdirected beta model reached inference")
			})
			require.Equal(t, http.StatusMisdirectedRequest, response.Code, response.Body.String())
			var body map[string]string
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
			assert.Equal(t, "misdirected_serving_channel", body["error"])
			assert.Equal(t, "https://beta.example.test", body["base_url"])
			assert.Zero(t, store.calls, "a misdirected request must not create or refresh a session binding")
		})
	}
}

func TestServingAdmissionAdmitsBetaModelOnBetaWorker(t *testing.T) {
	cfg, admitted, store := channelTestConfig(t, policyregistry.TargetBeta, nil)
	require.Equal(t, policyregistry.TargetBeta, admitted.Admission.Target)
	attributed := false
	cfg.Attribution = admissionAttributionFunc(func(_ context.Context, _ string, assertion policyregistry.ServingAssertion) error {
		attributed = true
		assert.Equal(t, policyregistry.TargetBeta, assertion.Admission.Target)
		return nil
	})
	response := runAdmissionMiddlewareWithBody(t, cfg, admitted, channelTestBetaBody, func(c *gin.Context) {
		identity, ok := requestcontext.ServingIdentityFromContext(c.Request.Context())
		require.True(t, ok)
		assert.Equal(t, string(policyregistry.TargetBeta), identity.Target)
		c.Status(http.StatusOK)
	})
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.Equal(t, 1, store.calls)
	assert.True(t, attributed)
}

func TestServingAdmissionRefusesMissingChannelPermissionWithStableURL(t *testing.T) {
	cfg, admitted, store := channelTestConfig(t, policyregistry.TargetBeta, policyregistry.ErrServingChannelNotPermitted)
	response := runAdmissionMiddlewareWithBody(t, cfg, admitted, channelTestBetaBody, func(*gin.Context) { t.Fatal("unpermitted channel reached inference") })
	require.Equal(t, http.StatusForbidden, response.Code, response.Body.String())
	var body map[string]string
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	assert.Equal(t, "serving_channel_not_permitted", body["error"])
	assert.Equal(t, "beta", body["channel"])
	assert.Equal(t, "https://stable.example.test", body["stable_base_url"])
	assert.Equal(t, 1, store.calls)
}

func TestServingAdmissionRefusesChannelSwitchMidSession(t *testing.T) {
	cfg, admitted, _ := channelTestConfig(t, policyregistry.TargetBeta, policyregistry.ErrServingChannelChanged)
	response := runAdmissionMiddleware(t, cfg, admitted, func(*gin.Context) { t.Fatal("channel switch reached inference") })
	require.Equal(t, http.StatusConflict, response.Code, response.Body.String())
	assert.Contains(t, response.Body.String(), "start_new_thread_to_change_router")
}

func TestDirectRoutingModelAdmitsBetaModelAsAutomatic(t *testing.T) {
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/messages", nil)
	model, selectsRouter := directRoutingModel(request, []byte(channelTestBetaBody))
	require.True(t, selectsRouter)
	assert.Equal(t, router.ModelAutomatic, model, "beta admission must project the default role, not an assigned-profile role")
}
