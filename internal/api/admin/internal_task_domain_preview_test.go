package admin_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/api/admin"
	"weave-os/router/internal/router/hmm/rosterdata"
)

const (
	previewQualityArm = "openai/gpt-6-luna"
	previewCheapArm   = "z-ai/glm-5.3-flash"
)

func taskDomainPreviewDocuments(t *testing.T) (json.RawMessage, json.RawMessage) {
	t.Helper()
	policy, err := json.Marshal(rosterdata.Roster{
		SchemaVersion: rosterdata.SchemaVersionPolicyV1,
		ClassOrder:    []string{"low"},
		Ranking: rosterdata.Ranking{
			Alpha: map[string]float64{"low": 0.4}, AlphaMin: map[string]float64{"low": 0.05}, AlphaMax: map[string]float64{"low": 0.8},
			QualityBiasNeutral: 0.7, WIIScoreVersion: "wii", WIINormalizationSHA256: strings.Repeat("b", 64),
			WPIScoreVersion: "wpi", WPINormalizationSHA256: strings.Repeat("c", 64),
		},
		Clusters: map[string]rosterdata.Cluster{"low": {
			ComplexityLabel: "low",
			CostRefUSD:      0.01,
			LatencyRefMS:    1000,
			Arms:            []string{previewQualityArm, previewCheapArm},
			ArmScores:       map[string]float64{previewQualityArm: 30, previewCheapArm: 25},
			ArmIndices:      map[string]rosterdata.ArmIndices{previewQualityArm: {WII: 90, WPI: 10}, previewCheapArm: {WII: 55, WPI: 0}},
		}},
	})
	require.NoError(t, err)
	evidence, err := json.Marshal(map[string]any{
		"schema_version": "domain_wmi_evidence_v3", "source_snapshot_sha256": strings.Repeat("d", 64), "source_ingest_date": "2026-10-02",
		"benchmark_snapshot_sha256": strings.Repeat("e", 64), "benchmark_ingest_date": "2026-10-05",
		"roster_sha256": rosterdata.SHA256Hex(policy), "wii_score_version": "wii", "wii_normalization_sha256": strings.Repeat("b", 64),
		"wpi_score_version": "wpi", "wpi_normalization_sha256": strings.Repeat("c", 64),
		"arms": map[string]any{
			previewQualityArm: map[string]any{"global_wii": 90, "wpi": 10, "benchmarks": map[string]float64{"terminalbench_v4_0": 0, "itbench_sre": 0}},
			previewCheapArm:   map[string]any{"global_wii": 55, "wpi": 0, "benchmarks": map[string]float64{"terminalbench_v4_0": 100}},
		},
	})
	require.NoError(t, err)
	return policy, evidence
}

func postTaskDomainPreview(t *testing.T, body any) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/preview", admin.InternalTaskDomainPreviewHandler())
	payload, err := json.Marshal(body)
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/preview", bytes.NewReader(payload)))
	return recorder
}

func TestInternalTaskDomainPreviewScoresWithWorkerRecipe(t *testing.T) {
	policy, evidence := taskDomainPreviewDocuments(t)
	recorder := postTaskDomainPreview(t, map[string]any{"policy": policy, "evidence": evidence, "cluster": "low", "domains": []string{"infra", "docs"}})
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var response struct {
		RecipeVersion   string  `json:"recipe_version"`
		RecipeInfluence float64 `json:"recipe_influence"`
		Recipes         []struct {
			Domain string `json:"domain"`
		} `json:"recipes"`
		EvidenceSHA256 string `json:"evidence_sha256"`
		Preview        struct {
			EffectiveWinner string `json:"effective_winner"`
			Arms            []struct {
				Arm        string  `json:"arm"`
				Correction float64 `json:"correction"`
			} `json:"arms"`
		} `json:"preview"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, "domain_wmi_weighted_v1", response.RecipeVersion)
	assert.Equal(t, 0.15, response.RecipeInfluence)
	assert.Len(t, response.Recipes, 4, "docs carries no recipe")
	assert.Equal(t, rosterdata.SHA256Hex(evidence), response.EvidenceSHA256)
	assert.Equal(t, previewCheapArm, response.Preview.EffectiveWinner)
	assert.InDelta(t, -4.86, response.Preview.Arms[0].Correction, 1e-5)
}

func TestInternalTaskDomainPreviewRejectsInvalidRequests(t *testing.T) {
	policy, evidence := taskDomainPreviewDocuments(t)
	for _, test := range []struct {
		name   string
		body   map[string]any
		status int
	}{
		{"missing evidence", map[string]any{"policy": policy, "cluster": "low"}, http.StatusBadRequest},
		{"unknown domain", map[string]any{"policy": policy, "evidence": evidence, "cluster": "low", "domains": []string{"ops"}}, http.StatusBadRequest},
		{"evidence for another policy", map[string]any{"policy": json.RawMessage(bytes.Replace(policy, []byte(`"low":0.4`), []byte(`"low":0.45`), 1)), "evidence": evidence, "cluster": "low"}, http.StatusUnprocessableEntity},
		{"unknown cluster", map[string]any{"policy": policy, "evidence": evidence, "cluster": "maximum"}, http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.status, postTaskDomainPreview(t, test.body).Code)
		})
	}
}
