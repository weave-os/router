package admin_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"weave-os/router/internal/api/admin"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/version"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVersionHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)

	origCommit, origPR := version.Commit, version.PR
	t.Cleanup(func() { version.Commit, version.PR = origCommit, origPR })
	version.Commit = "0fb46ee9707c8db7d0ef69b7308a79a95d559e25"
	version.PR = "572"

	engine := gin.New()
	engine.GET("/v1/version", admin.VersionHandler(nil, admin.VersionDetails{}))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/version", nil)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var body struct {
		Commit      string `json:"commit"`
		CommitShort string `json:"commit_short"`
		PR          string `json:"pr"`
		Display     string `json:"display"`
		BuildTime   string `json:"build_time"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))

	assert.Equal(t, "0fb46ee9707c8db7d0ef69b7308a79a95d559e25", body.Commit)
	assert.Equal(t, "0fb46ee", body.CommitShort)
	assert.Equal(t, "572", body.PR)
	assert.Equal(t, "#572 (0fb46ee)", body.Display)
	assert.NotContains(t, rec.Body.String(), "cluster_version")
	assert.NotContains(t, rec.Body.String(), "policy_release_id")
	assert.NotContains(t, rec.Body.String(), "router_schema_version")
	assert.NotContains(t, rec.Body.String(), "serving_source")
}

type fixedPolicyStatus policyregistry.ManagerStatus

func (s fixedPolicyStatus) Status() policyregistry.ManagerStatus {
	return policyregistry.ManagerStatus(s)
}

func TestVersionHandlerReportsActivePolicyRelease(t *testing.T) {
	gin.SetMode(gin.TestMode)

	engine := gin.New()
	engine.GET("/v1/version", admin.VersionHandler(fixedPolicyStatus{
		ActiveReleaseID:    "07c6ce47",
		ActivePolicySHA256: "abc123",
	}, admin.VersionDetails{}))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/version", nil)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body struct {
		PolicyReleaseID string `json:"policy_release_id"`
		PolicySHA256    string `json:"policy_sha256"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "07c6ce47", body.PolicyReleaseID)
	assert.Equal(t, "abc123", body.PolicySHA256)
}

func TestVersionHandlerReportsSchemaVersionAndServingSource(t *testing.T) {
	gin.SetMode(gin.TestMode)

	engine := gin.New()
	engine.GET("/v1/version", admin.VersionHandler(nil, admin.VersionDetails{
		SchemaVersion: func(context.Context) (int64, error) { return 161, nil },
		ServingSource: &admin.ServingSource{Target: "prod/stable", ActivationID: "00000000-0000-4000-8000-000000000a11", SelectionSetSHA256: strings.Repeat("d", 64)},
	}))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/version", nil)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body struct {
		RouterSchemaVersion int64 `json:"router_schema_version"`
		ServingSource       struct {
			Target             string `json:"target"`
			ActivationID       string `json:"activation_id"`
			SelectionSetSHA256 string `json:"selection_set_sha256"`
		} `json:"serving_source"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, int64(161), body.RouterSchemaVersion)
	assert.Equal(t, "prod/stable", body.ServingSource.Target)
	assert.Equal(t, "00000000-0000-4000-8000-000000000a11", body.ServingSource.ActivationID)
	assert.Equal(t, strings.Repeat("d", 64), body.ServingSource.SelectionSetSHA256)
}

func TestVersionHandlerOmitsSchemaVersionWhenUnreadable(t *testing.T) {
	gin.SetMode(gin.TestMode)

	engine := gin.New()
	engine.GET("/v1/version", admin.VersionHandler(nil, admin.VersionDetails{
		SchemaVersion: func(context.Context) (int64, error) { return 0, errors.New("database unavailable") },
	}))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/version", nil)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.NotContains(t, rec.Body.String(), "router_schema_version")
}
