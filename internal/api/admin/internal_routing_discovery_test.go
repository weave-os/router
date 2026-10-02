package admin_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/api/admin"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/hmm/rosterdata"
	"weave-os/router/internal/server/middleware"
)

const discoveryProfileKey = "10000000-0000-4000-8000-000000000001"

type discoveryPolicyReader struct {
	current policyregistry.CurrentPolicy
	target  policyregistry.ServingTarget
	profile string
	err     error
}

func (reader *discoveryPolicyReader) ReadCurrentPolicy(_ context.Context, target policyregistry.ServingTarget, profile string) (policyregistry.CurrentPolicy, error) {
	reader.target, reader.profile = target, profile
	if reader.err != nil {
		return policyregistry.CurrentPolicy{}, reader.err
	}
	return reader.current, nil
}

func discoveryEngine(reader admin.CurrentPolicyReader) *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/internal/v1/routing-discovery", middleware.WithInternalServiceAuth("test-token"), admin.InternalRoutingDiscoveryHandler(reader, discoveryWiredProviders(), nil))
	return engine
}

func discoveryWiredProviders() map[string]struct{} {
	return map[string]struct{}{
		providers.ProviderAnthropic: {}, providers.ProviderAnthropicGateway: {},
		providers.ProviderOpenAIGateway: {}, providers.ProviderOpenAI: {}, providers.ProviderXAI: {},
	}
}

func discoveryRequest(body string, token string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/internal/v1/routing-discovery", strings.NewReader(body))
	if token != "" {
		request.Header.Set("X-Weave-Internal-Token", token)
	}
	return request
}

func discoveryFixture() *discoveryPolicyReader {
	return &discoveryPolicyReader{current: policyregistry.CurrentPolicy{
		Target: policyregistry.TargetStable, ProfileKey: discoveryProfileKey,
		ActivationID: "active-1", SelectionSetSHA256: "selection-1", CandidateSHA256: "candidate-1", PolicySHA256: "policy-1",
		Roster: &rosterdata.Roster{
			SchemaVersion: rosterdata.SchemaVersionPolicyV1,
			Ranking: rosterdata.Ranking{
				Alpha: map[string]float64{"low": 0.5}, AlphaMin: map[string]float64{"low": 0.1},
				AlphaMax: map[string]float64{"low": 0.9}, QualityBiasNeutral: 0.7,
			},
			Clusters: map[string]rosterdata.Cluster{"low": {
				Arms: []string{"openai/gpt-5.6-luna", "x-ai/grok-4.6"},
				ArmsByHarness: map[rosterdata.Harness][]string{
					rosterdata.HarnessCodex: {"anthropic/claude-haiku-4.5"},
				},
				ArmScores: map[string]float64{"openai/gpt-5.6-luna": 10, "x-ai/grok-4.6": 9},
				ArmIndices: map[string]rosterdata.ArmIndices{
					"openai/gpt-5.6-luna": {WII: 80, WPI: 10}, "x-ai/grok-4.6": {WII: 60, WPI: 5},
				},
			}},
		},
	}}
}

func TestInternalRoutingDiscoveryRequiresTokenAndReturnsPolicyIdentity(t *testing.T) {
	reader := discoveryFixture()
	engine := discoveryEngine(reader)
	body := `{"target":"prod/stable","profile_key":"` + discoveryProfileKey + `","grid":2}`
	for _, token := range []string{"", "wrong-token"} {
		recorder := httptest.NewRecorder()
		engine.ServeHTTP(recorder, discoveryRequest(body, token))
		assert.Equal(t, http.StatusUnauthorized, recorder.Code)
	}
	assert.Empty(t, reader.target)

	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, discoveryRequest(body, "test-token"))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.Equal(t, "no-store", recorder.Header().Get("Cache-Control"))
	assert.Equal(t, policyregistry.TargetStable, reader.target)
	assert.Equal(t, discoveryProfileKey, reader.profile)
	var response struct {
		ActivationID string `json:"activation_id"`
		PolicySHA256 string `json:"policy_sha256"`
		Clusters     []struct {
			Arms   []string `json:"arms"`
			Models []string `json:"models"`
		} `json:"clusters"`
		Harnesses map[string][]struct {
			Cluster string   `json:"cluster"`
			Arms    []string `json:"arms"`
			Models  []string `json:"models"`
		} `json:"harnesses"`
		Models []struct {
			Model    string `json:"model"`
			Provider string `json:"provider"`
		} `json:"models"`
		Catalog []struct {
			Provider string `json:"provider"`
		} `json:"catalog"`
		Distribution []json.RawMessage `json:"distribution"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, "active-1", response.ActivationID)
	assert.Equal(t, "policy-1", response.PolicySHA256)
	require.Len(t, response.Clusters, 1)
	assert.Equal(t, []string{"gpt-5.6-luna", "grok-4.6"}, response.Clusters[0].Models)
	assert.Len(t, response.Distribution, 2)
	modelIDs := make(map[string]struct{}, len(response.Models))
	for _, model := range response.Models {
		modelIDs[model.Model] = struct{}{}
		assert.NotEqual(t, providers.ProviderOpenRouter, model.Provider)
	}
	_, hasHarnessOnlyModel := modelIDs["claude-haiku-4-5"]
	assert.True(t, hasHarnessOnlyModel)
	for _, model := range response.Catalog {
		assert.NotEqual(t, providers.ProviderOpenRouter, model.Provider)
	}
	codexClusters, hasCodexOrder := response.Harnesses[string(rosterdata.HarnessCodex)]
	require.True(t, hasCodexOrder)
	require.Len(t, codexClusters, 1)
	assert.Equal(t, []string{"anthropic/claude-haiku-4.5"}, codexClusters[0].Arms)
	assert.Equal(t, []string{"claude-haiku-4-5"}, codexClusters[0].Models)

	reader.current.ActivationID = "active-2"
	reader.current.PolicySHA256 = "policy-2"
	recorder = httptest.NewRecorder()
	engine.ServeHTTP(recorder, discoveryRequest(body, "test-token"))
	require.Equal(t, http.StatusOK, recorder.Code)
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, "active-2", response.ActivationID)
	assert.Equal(t, "policy-2", response.PolicySHA256)
	recorder = httptest.NewRecorder()
	engine.ServeHTTP(recorder, discoveryRequest(`{"target":"prod/stable","grid":101}`, "test-token"))
	require.Equal(t, http.StatusOK, recorder.Code)
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Len(t, response.Distribution, 101)
}

func TestInternalRoutingDiscoveryRejectsInvalidAndUnavailableRequests(t *testing.T) {
	reader := discoveryFixture()
	engine := discoveryEngine(reader)
	for _, body := range []string{
		`{}`, `{"target":"unknown"}`, `{"target":"prod/stable","profile_key":"not-a-uuid"}`,
		`{"target":"prod/stable","grid":1}`, `{"target":"prod/stable","grid":102}`,
		`{"target":"prod/stable","excluded_models":[""]}`,
		`{"target":"prod/stable","excluded_models":["unknown-model"]}`,
		`{"target":"prod/stable","excluded_providers":["unknown-provider"]}`,
		`{"target":"prod/stable","excluded_models":["gpt-5.6-luna","gpt-5.6-luna"]}`,
		`{"target":"prod/stable","unexpected":true}`,
		`{"target":"prod/stable"}{"target":"prod/stable"}`,
	} {
		recorder := httptest.NewRecorder()
		engine.ServeHTTP(recorder, discoveryRequest(body, "test-token"))
		assert.Equal(t, http.StatusBadRequest, recorder.Code, body)
	}
	oversized := `{"target":"prod/stable","excluded_models":["` + strings.Repeat("x", 17000) + `"]}`
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, discoveryRequest(oversized, "test-token"))
	assert.Equal(t, http.StatusBadRequest, recorder.Code)

	reader.err = errors.New("registry unavailable")
	recorder = httptest.NewRecorder()
	engine.ServeHTTP(recorder, discoveryRequest(`{"target":"prod/stable"}`, "test-token"))
	assert.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	reader.err = nil
	recorder = httptest.NewRecorder()
	engine.ServeHTTP(recorder, discoveryRequest(`{"target":"prod/stable","grid":2,"excluded_providers":["openai"]}`, "test-token"))
	require.Equal(t, http.StatusOK, recorder.Code)
	var providerProjection struct {
		Distribution []struct {
			Models []struct {
				Model string `json:"model"`
			} `json:"models"`
		} `json:"distribution"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &providerProjection))
	require.Len(t, providerProjection.Distribution, 2)
	assert.Equal(t, "grok-4.6", providerProjection.Distribution[0].Models[0].Model)

	recorder = httptest.NewRecorder()
	engine.ServeHTTP(recorder, discoveryRequest(`{"target":"prod/stable","excluded_models":["gpt-5.6-luna","grok-4.6"]}`, "test-token"))
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
}

func TestInternalRoutingDiscoveryReportsEveryProviderBinding(t *testing.T) {
	reader := discoveryFixture()
	cluster := reader.current.Roster.Clusters["low"]
	cluster.Arms = append(cluster.Arms, "anthropic/claude-sonnet-4.6")
	cluster.ArmScores["anthropic/claude-sonnet-4.6"] = 8
	cluster.ArmIndices["anthropic/claude-sonnet-4.6"] = rosterdata.ArmIndices{WII: 70, WPI: 20}
	reader.current.Roster.Clusters["low"] = cluster

	recorder := httptest.NewRecorder()
	discoveryEngine(reader).ServeHTTP(recorder, discoveryRequest(`{"target":"prod/stable"}`, "test-token"))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var response struct {
		Models  []struct{ Model, Provider string } `json:"models"`
		Catalog []struct{ Model, Provider string } `json:"catalog"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	providers := make(map[string]bool)
	for _, model := range response.Models {
		if model.Model == "claude-sonnet-4-6" {
			providers[model.Provider] = true
		}
	}
	assert.Equal(t, map[string]bool{"anthropic": true, "anthropic_gateway": true, "openai_gateway": true}, providers)
	catalogProviders := make(map[string]bool)
	for _, model := range response.Catalog {
		if model.Model == "claude-sonnet-4-6" {
			catalogProviders[model.Provider] = true
		}
	}
	assert.Equal(t, providers, catalogProviders)
}

func TestInternalRoutingDiscoveryRequiresCurrentPolicyToMatchWorker(t *testing.T) {
	reader := discoveryFixture()
	configuration := policyregistry.ObjectRef{
		URI: "gs://test-bucket/configuration.json", SHA256: strings.Repeat("a", 64), Generation: 1,
	}
	identity := policyregistry.WorkerIdentity{
		Target: policyregistry.TargetStable, Project: "project", Region: "region",
		Revision: "worker-0001", ImageDigest: "sha256:" + strings.Repeat("b", 64), Configuration: configuration,
	}
	reader.current.Binding = policyregistry.LaneBinding{
		Project: identity.Project, Region: identity.Region,
		Router: policyregistry.RevisionBinding{Name: identity.Revision, ImageDigest: identity.ImageDigest, Configuration: configuration},
	}
	engine := gin.New()
	engine.POST("/internal/v1/routing-discovery", middleware.WithInternalServiceAuth("test-token"), admin.InternalRoutingDiscoveryHandler(reader, discoveryWiredProviders(), &identity))
	requestBody := `{"target":"prod/stable","grid":2}`
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, discoveryRequest(requestBody, "test-token"))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	reader.current.Binding.Router.ImageDigest = "sha256:" + strings.Repeat("c", 64)
	recorder = httptest.NewRecorder()
	engine.ServeHTTP(recorder, discoveryRequest(requestBody, "test-token"))
	assert.Equal(t, http.StatusServiceUnavailable, recorder.Code)

	reader.current.Binding.Router.ImageDigest = identity.ImageDigest
	reader.current.Target = policyregistry.TargetInternal
	recorder = httptest.NewRecorder()
	engine.ServeHTTP(recorder, discoveryRequest(requestBody, "test-token"))
	assert.Equal(t, http.StatusServiceUnavailable, recorder.Code)
}
