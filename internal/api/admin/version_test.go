package admin_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	engine.GET("/v1/version", admin.VersionHandler(nil))

	req := httptest.NewRequest(http.MethodGet, "/v1/version", nil)
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
	}))

	req := httptest.NewRequest(http.MethodGet, "/v1/version", nil)
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
