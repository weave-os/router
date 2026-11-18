package admin_test

import (
	"context"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"weave-os/router/internal/api/admin"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/hmm/rosterdata"
	"weave-os/router/internal/router/hmm/selectionpolicy"
	"weave-os/router/internal/router/policy"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

var updateAdminGoldens = flag.Bool("update", false, "rewrite admin golden files from current output")

const (
	sharedSyntheticPolicyPath   = "../../router/hmm/selection/testdata/golden/policy_v1_synthetic.json"
	sharedSyntheticPolicySHA256 = "2a384cad8c6b8e06c785b99e9bbc39079d177e78419abdd0a356658101415a31"
	syntheticReleaseSHA256      = "8888888888888888888888888888888888888888888888888888888888888888"
)

type unusedPolicyLoader struct{}

func (unusedPolicyLoader) RootURI() string { return "gs://synthetic-registry" }
func (unusedPolicyLoader) ReadHead(context.Context, policyregistry.Environment, policyregistry.Lane) (policyregistry.HeadSnapshot, error) {
	return policyregistry.HeadSnapshot{}, policyregistry.ErrNoActivePolicy
}
func (unusedPolicyLoader) ReadRelease(context.Context, policyregistry.ObjectRef) (policyregistry.Release, error) {
	return policyregistry.Release{}, policyregistry.ErrNoActivePolicy
}
func (unusedPolicyLoader) ReadPolicy(context.Context, policyregistry.ObjectRef) (*selectionpolicy.Policy, error) {
	return nil, policyregistry.ErrNoActivePolicy
}

type admittedDeployedModels struct{ admitted *selectionpolicy.Policy }

func (d admittedDeployedModels) HMMDeployedModels(context.Context) ([]router.DeployedEntry, error) {
	return d.admitted.DeployedModels(), nil
}

func loadSharedSyntheticPolicy(t *testing.T) *selectionpolicy.Policy {
	t.Helper()
	payload, err := os.ReadFile(sharedSyntheticPolicyPath)
	require.NoError(t, err)
	admitted, err := selectionpolicy.Admit(payload)
	require.NoError(t, err)
	require.Equal(t, sharedSyntheticPolicySHA256, admitted.SHA256())
	return admitted
}

func syntheticServingSnapshot(admitted *selectionpolicy.Policy) *policyregistry.Snapshot {
	return &policyregistry.Snapshot{Candidate: policyregistry.Candidate{
		HeadSnapshot: policyregistry.HeadSnapshot{Generation: 3, Head: policyregistry.LaneHead{ReleaseSHA256: syntheticReleaseSHA256}},
		Release:      policyregistry.Release{Policy: policyregistry.PolicyObject{SHA256: admitted.SHA256(), SchemaVersion: rosterdata.SchemaVersionPolicyV1}},
		Policy:       admitted,
	}}
}

func requireAdminGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", "golden", name)
	if *updateAdminGoldens {
		require.NoError(t, os.WriteFile(path, got, 0o600))
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "run go test ./internal/api/admin -run Golden -update to create it")
	require.Equal(t, string(want), string(got))
}

func TestHMMRosterHandlerGolden(t *testing.T) {
	gin.SetMode(gin.TestMode)
	roster := loadSharedSyntheticPolicy(t)
	manager, err := policyregistry.NewManager(unusedPolicyLoader{}, policyregistry.EnvironmentStaging, policyregistry.LaneStable,
		func(context.Context, policyregistry.Candidate) (map[router.Strategy]router.Router, error) {
			return nil, nil
		}, nil)
	require.NoError(t, err)
	snapshot := syntheticServingSnapshot(roster)

	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(policyregistry.WithServingSnapshot(c.Request.Context(), snapshot))
	})
	engine.GET("/v1/router/hmm-roster", admin.HMMRosterHandler(map[router.Strategy]policy.RosterSource{router.StrategyHMM: manager}))

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/router/hmm-roster", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	requireAdminGolden(t, "hmm_roster.json", rec.Body.Bytes())
}

func TestDeployedModelsHandlerGolden(t *testing.T) {
	gin.SetMode(gin.TestMode)
	roster := loadSharedSyntheticPolicy(t)
	engine := gin.New()
	engine.GET("/v1/router/models", admin.CatalogModelsHandler(admittedDeployedModels{admitted: roster}))

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/router/models", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	requireAdminGolden(t, "deployed_models.json", rec.Body.Bytes())
}
