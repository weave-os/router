package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/llmescalation"
	"weave-os/router/internal/router/policy"
)

func TestEscalationJudgmentKeepsUsageOnInvalidVerdict(t *testing.T) {
	for _, verdict := range []string{`{"reason":"missing boolean"}`, `{"escalate":"true","reason":"wrong type"}`, `{"escalate":true,"reason":"x","extra":1}`, `{"escalate":false,"reason":""}`, `{"escalate":true,"reason":"x"} {}`} {
		judgment, err := parseEscalationJudgment(escalationTestResponse(t, verdict))
		require.Error(t, err)
		assert.True(t, judgment.Usage.Known)
		assert.Equal(t, 100, judgment.Usage.InputTokens)
		assert.Positive(t, judgment.CostUSD)
		assert.True(t, judgment.CostKnown)
		assert.Equal(t, llmescalation.CostSourceCatalogEstimate, judgment.CostSource)
		assert.False(t, judgment.Escalate)
	}
	judgment, err := parseEscalationJudgment(escalationTestResponse(t, `{"escalate":true,"reason":"Repeated failed attempts."}`))
	require.NoError(t, err)
	assert.True(t, judgment.Escalate)
}

func TestEscalationJudgmentPrefersProviderReportedCost(t *testing.T) {
	encoded := escalationTestResponse(t, `{"escalate":false,"reason":"Progressing."}`)
	encoded, err := sjson.SetBytes(encoded, "usage.cost", 0.123)
	require.NoError(t, err)
	judgment, err := parseEscalationJudgment(encoded)
	require.NoError(t, err)
	assert.Equal(t, 0.123, judgment.CostUSD)
	assert.Equal(t, llmescalation.CostSourceProviderReported, judgment.CostSource)
}

func escalationTestResponse(t *testing.T, verdict string) []byte {
	t.Helper()
	encoded, err := json.Marshal(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"content": verdict}}}, "usage": map[string]any{"prompt_tokens": 100, "completion_tokens": 20}})
	require.NoError(t, err)
	return encoded
}

type escalationJudgeTestProvider struct {
	fakeHandoverProvider
	credentials *requestcontext.Credentials
	prepared    providers.PreparedRequest
}

func (p *escalationJudgeTestProvider) Proxy(ctx context.Context, decision router.Decision, prepared providers.PreparedRequest, writer http.ResponseWriter, request *http.Request) error {
	p.credentials = requestcontext.CredentialsFromContext(ctx)
	p.prepared = prepared
	return p.fakeHandoverProvider.Proxy(ctx, decision, prepared, writer, request)
}

func TestEscalationJudgeIsolatesCredentialsAndPreservesPrompt(t *testing.T) {
	provider := &escalationJudgeTestProvider{fakeHandoverProvider: fakeHandoverProvider{respBody: string(escalationTestResponse(t, `{"escalate":false,"reason":"Progressing."}`))}}
	plans, err := policy.NewPlanResolver(policy.DefaultRegistry(), policy.NewResolver(map[string]struct{}{policy.EscalationJudgeModel: {}}, map[string]struct{}{providers.ProviderFireworks: {}}, func(model catalog.Model) string { return model.ID }, policy.ProviderPolicy{}))
	require.NoError(t, err)
	executor, err := dispatch.NewExecutor(dispatch.NewClients(map[string]providers.Client{providers.ProviderFireworks: provider}))
	require.NoError(t, err)
	judge, err := NewEscalationJudge(plans, executor, "dedicated-test-key")
	require.NoError(t, err)
	ctx := requestcontext.WithCredentials(context.Background(), &requestcontext.Credentials{APIKey: []byte("tenant-test-key"), BaseURL: "https://tenant.invalid", IdentityHeader: "X-Tenant"})
	judgment, err := judge.Judge(ctx, llmescalation.JudgeRequest{Transcript: "Synthetic conversation", RequestID: "judge-test"})
	require.NoError(t, err)
	assert.True(t, judgment.Usage.Known)
	assert.Equal(t, "dedicated-test-key", string(provider.credentials.APIKey))
	assert.Equal(t, escalationJudgeBaseURL, provider.credentials.BaseURL)
	assert.Empty(t, provider.credentials.IdentityHeader)
	assert.Equal(t, llmescalation.SystemPrompt, gjson.GetBytes(provider.prepared.Body, "messages.0.content").String())
	assert.JSONEq(t, string(llmescalation.ResponseSchema), gjson.GetBytes(provider.prepared.Body, "response_format").Raw)
	assert.Equal(t, "accounts/fireworks/models/glm-5p3-flash", gjson.GetBytes(provider.prepared.Body, "model").String())
	assert.False(t, gjson.GetBytes(provider.prepared.Body, "reasoning").Exists())
	assert.EqualValues(t, 4096, gjson.GetBytes(provider.prepared.Body, "max_tokens").Int())
	provider.upstreamErr = &providers.UpstreamStatusError{Status: http.StatusTooManyRequests}
	_, err = judge.Judge(ctx, llmescalation.JudgeRequest{Transcript: "Synthetic conversation"})
	require.Error(t, err)
	assert.Equal(t, 2, provider.calls)
}
