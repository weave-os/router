package admin_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"weave-os/router/internal/api/admin"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/server/middleware"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const inspectionToken = "internal-secret"

func inferencePoliciesEngine(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	available := map[string]struct{}{providers.ProviderAnthropic: {}, providers.ProviderOpenAI: {}}
	plans, err := policy.NewPlanResolver(policy.DefaultRegistry(), policy.NewResolver(
		catalog.RoutingTargetSet(available), available, func(model catalog.Model) string { return model.ID }, policy.ProviderPolicy{}))
	require.NoError(t, err)
	proxySvc := upstreamModelsProxyService(map[string]providers.Client{
		providers.ProviderAnthropic: &modelListingClient{},
		providers.ProviderOpenAI:    &modelListingClient{},
	}).WithInferencePlans(plans).WithInferenceDeployment(policy.DeploymentPolicyConfig{
		AvailableProviders: available,
		TargetOverrides: []policy.PurposeTargetOverride{{
			Purpose: policy.PurposeSubAgentDispatch,
			Target:  policy.TargetOverride{Source: policy.OverrideSourceDeployment, CatalogID: "claude-haiku-4-5", Provider: providers.ProviderAnthropic},
		}},
	})
	engine := gin.New()
	group := engine.Group("/internal/v1", middleware.WithInternalServiceAuth(inspectionToken))
	group.GET("/inference-policies", admin.InternalInferencePoliciesHandler(proxySvc))
	group.GET("/inference-policies/deployment", admin.InternalInferenceDeploymentHandler(proxySvc))
	group.POST("/inference-policies/resolve", admin.InternalInferenceResolveHandler(proxySvc))
	return engine
}

func inspectionRequest(t *testing.T, method, path string, body any) *http.Request {
	t.Helper()
	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		require.NoError(t, err)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(payload))
	req.Header.Set("X-Weave-Internal-Token", inspectionToken)
	return req
}

func TestInternalInferencePolicies_RequireServiceToken(t *testing.T) {
	engine := inferencePoliciesEngine(t)
	for _, path := range []string{"/internal/v1/inference-policies", "/internal/v1/inference-policies/deployment"} {
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		assert.Equal(t, http.StatusUnauthorized, rec.Code, path)
	}
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/internal/v1/inference-policies/resolve", bytes.NewReader([]byte(`{"purpose":"handover_summary"}`))))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestInternalInferencePolicies_StaticMatchesRegistry(t *testing.T) {
	engine := inferencePoliciesEngine(t)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, inspectionRequest(t, http.MethodGet, "/internal/v1/inference-policies", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	var body policy.StaticRegistryProjection
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, policy.DefaultRegistry().Revision(), body.RegistryRevision)
	assert.Len(t, body.Policies, len(policy.DefaultRegistry().Specs()))
	assert.NotContains(t, rec.Body.String(), "sk-")
}

func TestInternalInferencePolicies_DeploymentReportsBindings(t *testing.T) {
	engine := inferencePoliciesEngine(t)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, inspectionRequest(t, http.MethodGet, "/internal/v1/inference-policies/deployment", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	var body policy.DeploymentProjection
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, []string{providers.ProviderAnthropic, providers.ProviderOpenAI}, body.AvailableProviders)
	assert.Positive(t, body.RoutableModels)

	byPurpose := make(map[policy.Purpose]policy.DeploymentPolicyProjection, len(body.Policies))
	for _, entry := range body.Policies {
		byPurpose[entry.Purpose] = entry
	}
	handover := byPurpose[policy.PurposeHandoverSummary]
	require.NotEmpty(t, handover.CandidateBindings)
	assert.Equal(t, "claude-haiku-4-5", handover.CandidateBindings[0].CatalogID)
	assert.Equal(t, providers.ProviderAnthropic, handover.CandidateBindings[0].Provider)

	subAgent := byPurpose[policy.PurposeSubAgentDispatch]
	require.NotNil(t, subAgent.DeploymentTarget)
	assert.Equal(t, "claude-haiku-4-5", subAgent.DeploymentTarget.CatalogID)
	require.Len(t, subAgent.CandidateBindings, 1)

	messages := byPurpose[policy.PurposeAnthropicMessages]
	assert.Empty(t, messages.CandidateBindings)
	assert.Equal(t, body.RoutableModels, messages.RoutableModels)
}

func TestInternalInferencePolicies_ResolvePreviewsFixedPurpose(t *testing.T) {
	engine := inferencePoliciesEngine(t)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, inspectionRequest(t, http.MethodPost, "/internal/v1/inference-policies/resolve", map[string]string{"purpose": string(policy.PurposeHandoverSummary)}))

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body policy.PlanProjection
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, policy.PolicyID("aux-handover-summary"), body.PolicyID)
	assert.Equal(t, policy.DefaultRegistry().Revision(), body.RegistryRevision)
	assert.Equal(t, "claude-haiku-4-5", body.SelectedTarget.CatalogID)
	assert.Equal(t, providers.ProviderAnthropic, body.SelectedTarget.Provider)
	assert.Equal(t, policy.OverrideSourcePolicyDefault, body.Provenance.OverrideSource)
	assert.LessOrEqual(t, len(body.Alternatives), 8)
}

func TestInternalInferencePolicies_ResolveRouterPurposeUsesProposedModel(t *testing.T) {
	engine := inferencePoliciesEngine(t)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, inspectionRequest(t, http.MethodPost, "/internal/v1/inference-policies/resolve", map[string]string{
		"purpose": string(policy.PurposeAnthropicMessages),
		"model":   "claude-haiku-4-5",
	}))

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body policy.PlanProjection
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, policy.PolicyID("main-anthropic-messages"), body.PolicyID)
	assert.Equal(t, "claude-haiku-4-5", body.SelectedTarget.CatalogID)
	assert.Equal(t, policy.SelectionStrategyRouter, body.Provenance.SelectionStrategy)
}

func TestInternalInferencePolicies_ResolveAppliesSnakeCaseBudgetOverride(t *testing.T) {
	engine := inferencePoliciesEngine(t)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, inspectionRequest(t, http.MethodPost, "/internal/v1/inference-policies/resolve", map[string]any{
		"purpose": string(policy.PurposeAnthropicMessages),
		"model":   "claude-haiku-4-5",
		"budget": map[string]any{
			"source":            string(policy.BudgetSourceRequest),
			"max_attempts":      2,
			"timeout_millis":    1500,
			"max_output_tokens": 256,
			"max_spend_usd":     0.5,
		},
	}))

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body policy.PlanProjection
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, policy.BudgetSourceRequest, body.Budget.Source)
	assert.Equal(t, 2, body.Budget.MaxAttempts)
	assert.Equal(t, int64(1500), body.Budget.TimeoutMillis)
	assert.Equal(t, 256, body.Budget.MaxOutputTokens)
	assert.InDelta(t, 0.5, body.Budget.MaxSpendUSD, 1e-9)
}

func TestInternalInferencePolicies_ResolveFailsClosedWithStableCode(t *testing.T) {
	engine := inferencePoliciesEngine(t)
	cases := map[string]struct {
		body map[string]string
		code policy.ResolutionErrorCode
	}{
		"unknown purpose":        {body: map[string]string{"purpose": "nonexistent"}, code: policy.ResolutionErrorUnknownPurpose},
		"router without model":   {body: map[string]string{"purpose": string(policy.PurposeAnthropicMessages)}, code: policy.ResolutionErrorMissingSelection},
		"override not permitted": {body: map[string]string{"purpose": string(policy.PurposeHandoverSummary), "model": "claude-haiku-4-5"}, code: policy.ResolutionErrorOverrideNotAllowed},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			engine.ServeHTTP(rec, inspectionRequest(t, http.MethodPost, "/internal/v1/inference-policies/resolve", tc.body))
			require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
			var body struct {
				Error policy.ResolutionErrorProjection `json:"error"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			assert.Equal(t, tc.code, body.Error.Code)
		})
	}

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, inspectionRequest(t, http.MethodPost, "/internal/v1/inference-policies/resolve", map[string]string{}))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}
