package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/policyregistry"
)

const channelTestBetaBody = `{"model":"weave-auto-beta","messages":[{"role":"user","content":"hello"}]}`

func channelTestConfig(t *testing.T, err error) (*ServingAdmissionConfig, policyregistry.ServingAssertion, *directAdmissionStore) {
	t.Helper()
	cfg, admitted, _, _ := admissionMiddlewareFixture(t)
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
	cfg, _, store := channelTestConfig(t, nil)
	response := runAdmissionMiddlewareWithBody(t, cfg, policyregistry.ServingAssertion{}, channelTestBetaBody, func(*gin.Context) {
		t.Fatal("misdirected beta model reached inference")
	})
	require.Equal(t, http.StatusMisdirectedRequest, response.Code, response.Body.String())
	var body map[string]string
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	assert.Equal(t, "misdirected_serving_channel", body["error"])
	assert.Equal(t, "https://beta.example.test", body["base_url"])
	assert.Zero(t, store.calls, "a misdirected request must not create or refresh a session binding")
}

func TestServingAdmissionRefusesMissingChannelPermissionWithStableURL(t *testing.T) {
	cfg, admitted, store := channelTestConfig(t, policyregistry.ErrServingChannelNotPermitted)
	response := runAdmissionMiddleware(t, cfg, admitted, func(*gin.Context) { t.Fatal("unpermitted channel reached inference") })
	require.Equal(t, http.StatusForbidden, response.Code, response.Body.String())
	var body map[string]string
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	assert.Equal(t, "serving_channel_not_permitted", body["error"])
	assert.Equal(t, "stable", body["channel"])
	assert.Equal(t, "https://stable.example.test", body["stable_base_url"])
	assert.Equal(t, 1, store.calls)
}

func TestServingAdmissionRefusesChannelSwitchMidSession(t *testing.T) {
	cfg, admitted, _ := channelTestConfig(t, policyregistry.ErrServingChannelChanged)
	response := runAdmissionMiddleware(t, cfg, admitted, func(*gin.Context) { t.Fatal("channel switch reached inference") })
	require.Equal(t, http.StatusConflict, response.Code, response.Body.String())
	assert.Contains(t, response.Body.String(), "start_new_thread_to_change_router")
}
