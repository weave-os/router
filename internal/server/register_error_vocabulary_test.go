package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/hmm"
	"weave-os/router/internal/server"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type unroutableRouter struct{}

func (unroutableRouter) Route(context.Context, router.Request) (router.Decision, error) {
	return router.Decision{}, router.ErrNoEligibleProvider
}

func TestRegisterDefaultErrorVocabularyClassifiesDispatchErrors(t *testing.T) {
	gin.SetMode(gin.TestMode)
	authSvc := auth.NewService(nil, subscriptionBudgetKeys{}, nil, nil, auth.NoOpAPIKeyCache{}, nil, time.Now).WithCredentialSubjectLookup(subscriptionBudgetOwners{})
	proxySvc := proxy.WithStrategyPorts(proxy.NewService(unroutableRouter{}, dispatch.NewClients(nil), nil, nil, false, "", "", nil), hmm.ServingPorts())
	engine := gin.New()
	server.Register(engine, authSvc, proxyServices(proxySvc), nil, server.DeploymentModeManaged, nil, nil, nil)

	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/messages", strings.NewReader(`[]`))
	request.Header.Set("Authorization", "Bearer rk_synthetic")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	require.NotPanics(t, func() { engine.ServeHTTP(response, request) })

	require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
	var envelope struct {
		Type  string `json:"type"`
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &envelope))
	assert.Equal(t, "error", envelope.Type)
	assert.Equal(t, "invalid_request_error", envelope.Error.Type)
}
