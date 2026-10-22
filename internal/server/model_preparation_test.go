package server_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/router"
	"weave-os/router/internal/server"
	"weave-os/router/internal/server/middleware"
)

func TestModelPreparationMountedOnlyWithManagedAdmissionAndRequiresAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name      string
		mode      server.DeploymentMode
		admission *middleware.ServingAdmissionConfig
		mounted   bool
	}{
		{"managed admission", server.DeploymentModeManaged, &middleware.ServingAdmissionConfig{}, true},
		{"managed without admission", server.DeploymentModeManaged, nil, false},
		{"self hosted", server.DeploymentModeSelfHosted, &middleware.ServingAdmissionConfig{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine := gin.New()
			server.RegisterWithFeatures(engine, nil, nil, nil, nil, tc.mode, nil, nil, nil, nil, server.Features{ServingAdmission: tc.admission})
			routes := routeSet(engine)
			for _, route := range []string{"POST /v1/router/models/:model/prepare", "GET /v1/router/models/:model/prepare/:operation"} {
				_, mounted := routes[route]
				require.Equal(t, tc.mounted, mounted, route)
			}
			if !tc.mounted {
				return
			}
			for _, method := range []string{http.MethodGet, http.MethodPost} {
				path := "/v1/router/models/" + string(router.ModelQwen) + "/prepare"
				if method == http.MethodGet {
					path += "/fc-synthetic1"
				}
				response := httptest.NewRecorder()
				engine.ServeHTTP(response, httptest.NewRequest(method, path, strings.NewReader(`{}`)))
				require.Equal(t, http.StatusUnauthorized, response.Code)
			}
		})
	}
}
