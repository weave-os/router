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

type taskDomainPreviewReply struct {
	RecipeVersion   string  `json:"recipe_version"`
	RecipeInfluence float64 `json:"recipe_influence"`
	Recipes         []struct {
		Domain string `json:"domain"`
	} `json:"recipes"`
	Items []struct {
		Status         admin.TaskDomainPreviewStatus `json:"status"`
		EvidenceSHA256 string                        `json:"evidence_sha256"`
		Preview        *struct {
			EffectiveWinner string `json:"effective_winner"`
			Arms            []struct {
				Arm        string  `json:"arm"`
				Correction float64 `json:"correction"`
			} `json:"arms"`
		} `json:"preview"`
	} `json:"items"`
}

func TestInternalTaskDomainPreviewScoresEveryItemWithWorkerRecipe(t *testing.T) {
	policy, evidence := taskDomainPreviewDocuments(t)
	otherPolicy := json.RawMessage(bytes.Replace(policy, []byte(`"low":0.4`), []byte(`"low":0.45`), 1))
	recorder := postTaskDomainPreview(t, map[string]any{"items": []map[string]any{
		{"policy": policy, "evidence": evidence, "cluster": "low", "domains": []string{"infra", "docs"}},
		{"policy": policy, "evidence": evidence, "cluster": "low", "domains": []string{"docs"}},
		{"policy": otherPolicy, "evidence": evidence, "cluster": "low"},
		{"policy": policy, "evidence": evidence, "cluster": "maximum"},
		{"policy": policy, "evidence": evidence, "cluster": "low", "domains": []string{"ops"}},
		{"policy": policy, "cluster": "low"},
	}})
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var reply taskDomainPreviewReply
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &reply))
	assert.Equal(t, "domain_wmi_weighted_v1", reply.RecipeVersion)
	assert.Equal(t, 0.15, reply.RecipeInfluence)
	assert.Len(t, reply.Recipes, 4, "docs carries no recipe")
	require.Len(t, reply.Items, 6)

	infra := reply.Items[0]
	assert.Equal(t, admin.TaskDomainPreviewReady, infra.Status)
	assert.Equal(t, rosterdata.SHA256Hex(evidence), infra.EvidenceSHA256)
	assert.Equal(t, previewCheapArm, infra.Preview.EffectiveWinner)
	assert.InDelta(t, -4.86, infra.Preview.Arms[0].Correction, 1e-5)
	assert.Equal(t, previewQualityArm, reply.Items[1].Preview.EffectiveWinner, "docs alone keeps baseline")

	for index, expected := range []admin.TaskDomainPreviewStatus{
		admin.TaskDomainPreviewEvidenceRejected,
		admin.TaskDomainPreviewInvalidRequest,
		admin.TaskDomainPreviewInvalidRequest,
		admin.TaskDomainPreviewInvalidRequest,
	} {
		assert.Equal(t, expected, reply.Items[index+2].Status)
		assert.Nil(t, reply.Items[index+2].Preview)
	}
}

func TestInternalTaskDomainPreviewRejectsEmptyAndOversizedBatches(t *testing.T) {
	policy, evidence := taskDomainPreviewDocuments(t)
	oversized := make([]map[string]any, 65)
	for index := range oversized {
		oversized[index] = map[string]any{"policy": policy, "evidence": evidence, "cluster": "low"}
	}
	for _, body := range []map[string]any{{"items": []map[string]any{}}, {"items": oversized}} {
		assert.Equal(t, http.StatusBadRequest, postTaskDomainPreview(t, body).Code)
	}
}
