package admin_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/api/admin"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/server/middleware"
)

type validateKeyRepo struct {
	auth.APIKeyRepository
	apiKey *auth.APIKey
}

func (r validateKeyRepo) GetActiveByHashWithInstallation(context.Context, string) (*auth.APIKey, *auth.Installation, error) {
	return r.apiKey, &auth.Installation{ID: "installation", Name: "Synthetic"}, nil
}

func (validateKeyRepo) MarkUsed(context.Context, string) (bool, error) { return false, nil }

type validateInstallationRepo struct{ auth.InstallationRepository }

func (validateInstallationRepo) MarkFirstRequestServed(context.Context, string) error { return nil }

func TestValidateReportsWhetherTheKeyIsShared(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for name, tc := range map[string]struct {
		subjectID string
		shared    bool
	}{
		"personal key": {subjectID: "subject-sam", shared: false},
		"shared key":   {subjectID: "", shared: true},
	} {
		t.Run(name, func(t *testing.T) {
			key := &auth.APIKey{ID: "key", ExternalID: "kid-key", InstallationID: "installation", CredentialSubjectID: tc.subjectID, Scope: auth.ScopeRouting}
			svc := auth.NewService(validateInstallationRepo{}, validateKeyRepo{apiKey: key}, nil, nil, auth.NoOpAPIKeyCache{}, nil, time.Now)
			engine := gin.New()
			engine.GET("/validate", middleware.WithAuth(svc, false), admin.ValidateHandler)
			request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/validate", nil)
			request.Header.Set("Authorization", "Bearer rk_test")
			response := httptest.NewRecorder()
			engine.ServeHTTP(response, request)

			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			var body struct {
				Valid  bool `json:"valid"`
				Shared bool `json:"shared"`
			}
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
			require.True(t, body.Valid)
			require.Equal(t, tc.shared, body.Shared)
		})
	}
}
