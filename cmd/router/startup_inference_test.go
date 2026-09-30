package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/openaicompat"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/policy"
)

type startupInferenceClient struct {
	providers.Client
	mu                  sync.Mutex
	calls               map[string]int
	body                string
	failure             error
	wait                bool
	receivedCredentials bool
	credentials         *requestcontext.Credentials
}

func (c *startupInferenceClient) Proxy(ctx context.Context, decision router.Decision, prepared providers.PreparedRequest, w http.ResponseWriter, r *http.Request) error {
	c.mu.Lock()
	if c.calls == nil {
		c.calls = make(map[string]int)
	}
	c.calls[decision.Model]++
	c.receivedCredentials = c.receivedCredentials || requestcontext.CredentialsFromContext(ctx) != nil || r.Header.Get("Authorization") != ""
	if credentials := requestcontext.CredentialsFromContext(ctx); credentials != nil {
		c.credentials = credentials
	}
	c.mu.Unlock()
	if c.wait {
		<-ctx.Done()
		return ctx.Err()
	}
	if c.failure != nil {
		return c.failure
	}
	response := c.body
	if response == "" {
		response = `{"choices":[{"message":{"content":"OK"}}]}`
	}
	_, err := io.WriteString(w, response)
	return err
}

func TestStartupGeneratesOnceForEachRoutableModel(t *testing.T) {
	client := &startupInferenceClient{}
	models := map[string]struct{}{catalog.ModelIDGPT55.String(): {}, "gpt-5.6-luna": {}}
	err := warmStartupModels(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), dispatch.NewClients(map[string]providers.Client{providers.ProviderOpenAI: client}), models, map[string]struct{}{providers.ProviderOpenAI: {}})
	require.NoError(t, err)
	require.Equal(t, map[string]int{catalog.ModelIDGPT55.String(): 1, "gpt-5.6-luna": 1}, client.calls)
	require.False(t, client.receivedCredentials)
}

func TestStartupWarmsFixedCatalogUtilityModels(t *testing.T) {
	const legacyOpus catalog.ModelID = "claude-opus-4-0"
	const fable catalog.ModelID = "claude-fable-5"
	client := &startupInferenceClient{body: `{"content":[{"type":"text","text":"OK"}]}`}
	providersEnabled := map[string]struct{}{providers.ProviderAnthropic: {}}
	models := startupModelSet(providersEnabled, policy.DeploymentPolicyConfig{})
	err := warmStartupModels(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), dispatch.NewClients(map[string]providers.Client{providers.ProviderAnthropic: client}), models, providersEnabled)
	require.NoError(t, err)
	require.Equal(t, 1, client.calls[legacyOpus.String()])
	require.Equal(t, 1, client.calls[fable.String()])
	require.False(t, client.receivedCredentials)
}

func TestStartupWarmsJudgeWithItsPlatformCredentialInBYOKOnlyMode(t *testing.T) {
	fireworks := &startupInferenceClient{}
	other := &startupInferenceClient{}
	providersEnabled := map[string]struct{}{}
	models := startupModelSet(providersEnabled, policy.DeploymentPolicyConfig{})
	const platformKey = "synthetic-judge-platform-key"
	judge := startupModelTarget{
		CatalogID:   policy.EscalationJudgeModel,
		Provider:    providers.ProviderFireworks,
		Credentials: &requestcontext.Credentials{APIKey: []byte(platformKey), BaseURL: openaicompat.FireworksBaseURL},
	}
	err := warmStartupModels(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), dispatch.NewClients(map[string]providers.Client{providers.ProviderFireworks: fireworks, providers.ProviderDeepInfra: other}), models, providersEnabled, judge)
	require.NoError(t, err)
	require.Equal(t, map[string]int{policy.EscalationJudgeModel: 1}, fireworks.calls)
	require.Empty(t, other.calls)
	require.NotNil(t, fireworks.credentials)
	require.Equal(t, platformKey, string(fireworks.credentials.APIKey))
	require.Equal(t, openaicompat.FireworksBaseURL, fireworks.credentials.BaseURL)
	require.Empty(t, models, "feature credentials must not broaden ordinary model availability")
	require.Empty(t, providersEnabled)
}

func TestStartupPreservesOrdinaryGenerationAlongsideRequiredJudgeBinding(t *testing.T) {
	fireworks := &startupInferenceClient{}
	ordinary := &startupInferenceClient{}
	models := map[string]struct{}{policy.EscalationJudgeModel: {}}
	providersEnabled := map[string]struct{}{providers.ProviderDeepInfra: {}}
	judge := startupModelTarget{
		CatalogID:   policy.EscalationJudgeModel,
		Provider:    providers.ProviderFireworks,
		Credentials: &requestcontext.Credentials{APIKey: []byte("synthetic-judge-key"), BaseURL: openaicompat.FireworksBaseURL},
	}
	err := warmStartupModels(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), dispatch.NewClients(map[string]providers.Client{providers.ProviderFireworks: fireworks, providers.ProviderDeepInfra: ordinary}), models, providersEnabled, judge)
	require.NoError(t, err)
	require.Equal(t, map[string]int{policy.EscalationJudgeModel: 1}, ordinary.calls)
	require.False(t, ordinary.receivedCredentials)
	require.Equal(t, map[string]int{policy.EscalationJudgeModel: 1}, fireworks.calls)
	require.True(t, fireworks.receivedCredentials)
}

func TestStartupWarmsConfiguredProviderAndDeduplicatesUtilityBindings(t *testing.T) {
	model := catalog.ModelIDClaudeHaiku45.String()
	providersEnabled := map[string]struct{}{providers.ProviderAnthropic: {}, providers.ProviderAnthropicGateway: {}}
	deployment := policy.DeploymentPolicyConfig{TargetOverrides: []policy.PurposeTargetOverride{
		{Purpose: policy.PurposeProbe, Target: policy.TargetOverride{CatalogID: model, Provider: providers.ProviderAnthropicGateway}},
		{Purpose: policy.PurposeTitleGeneration, Target: policy.TargetOverride{CatalogID: model, Provider: providers.ProviderAnthropicGateway}},
	}}
	ordinary := &startupInferenceClient{body: `{"content":[{"text":"OK"}]}`}
	gateway := &startupInferenceClient{body: `{"content":[{"text":"OK"}]}`}
	err := warmStartupModels(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), dispatch.NewClients(map[string]providers.Client{providers.ProviderAnthropic: ordinary, providers.ProviderAnthropicGateway: gateway}), map[string]struct{}{model: {}}, providersEnabled, startupDeploymentTargets(providersEnabled, deployment)...)
	require.NoError(t, err)
	require.Equal(t, map[string]int{model: 1}, ordinary.calls)
	require.Equal(t, map[string]int{model: 1}, gateway.calls)
	require.False(t, gateway.receivedCredentials)
}

func TestStartupRejectsUncredentialedRequiredBindingBeforePaidWork(t *testing.T) {
	client := &startupInferenceClient{}
	err := warmStartupModels(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), dispatch.NewClients(map[string]providers.Client{providers.ProviderFireworks: client}), nil, nil, startupModelTarget{CatalogID: policy.EscalationJudgeModel, Provider: providers.ProviderFireworks})
	require.Error(t, err)
	require.Empty(t, client.calls)
}

func TestStartupInferenceFailsWithoutRetryOrFallback(t *testing.T) {
	primary := &startupInferenceClient{failure: &providers.UpstreamStatusError{Status: http.StatusTooManyRequests}}
	secondary := &startupInferenceClient{}
	model := catalog.ModelIDGPT55.String()
	err := warmStartupModels(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), dispatch.NewClients(map[string]providers.Client{providers.ProviderOpenAI: primary, providers.ProviderOpenRouter: secondary}), map[string]struct{}{model: {}}, map[string]struct{}{providers.ProviderOpenAI: {}, providers.ProviderOpenRouter: {}})
	require.Error(t, err)
	require.Equal(t, map[string]int{model: 1}, primary.calls)
	require.Empty(t, secondary.calls)
}

func TestStartupRejectsUnroutableModelBeforeGeneration(t *testing.T) {
	client := &startupInferenceClient{}
	err := warmStartupModels(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), dispatch.NewClients(map[string]providers.Client{providers.ProviderOpenAI: client}), map[string]struct{}{catalog.ModelIDClaudeHaiku45.String(): {}}, map[string]struct{}{providers.ProviderOpenAI: {}})
	require.Error(t, err)
	require.Empty(t, client.calls)
}

func TestStartupGenerationHonorsWholeBootDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client := &startupInferenceClient{wait: true}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		err := warmStartupModels(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), dispatch.NewClients(map[string]providers.Client{providers.ProviderOpenAI: client}), map[string]struct{}{catalog.ModelIDGPT55.String(): {}}, map[string]struct{}{providers.ProviderOpenAI: {}})
		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.Equal(t, 1, client.calls[catalog.ModelIDGPT55.String()])
	})
}

func TestStartupGenerationBudgetsAndEffortSurviveServingTranslation(t *testing.T) {
	for _, test := range []struct {
		model, provider, field string
		effortField, effort    string
		thinkingType           string
		tokens                 int
		endpoint               providers.Endpoint
	}{
		{model: catalog.ModelIDClaudeHaiku45.String(), provider: providers.ProviderAnthropic, field: "max_tokens", tokens: 32},
		{model: catalog.ModelIDClaudeOpus48.String(), provider: providers.ProviderAnthropic, field: "max_tokens", tokens: 1024, effortField: "output_config.effort", effort: "low", thinkingType: "adaptive"},
		{model: catalog.ModelIDGPT55.String(), provider: providers.ProviderOpenAI, field: "max_output_tokens", tokens: 1024, endpoint: providers.EndpointResponses, effortField: "reasoning.effort", effort: "low"},
		{model: "gemini-3-pro-preview", provider: providers.ProviderGoogle, field: "generationConfig.maxOutputTokens", tokens: 1024, effortField: "generationConfig.thinkingConfig.thinkingLevel", effort: "low"},
	} {
		t.Run(test.provider+"/"+test.model, func(t *testing.T) {
			prepared, request, err := prepareStartupGeneration(context.Background(), inference.Target{CatalogID: test.model, Provider: test.provider})
			require.NoError(t, err)
			require.Equal(t, int64(test.tokens), gjson.GetBytes(prepared.Body, test.field).Int())
			require.False(t, gjson.GetBytes(prepared.Body, "stream").Bool())
			require.Equal(t, test.endpoint, prepared.Endpoint)
			require.Empty(t, request.Header.Get("Authorization"))
			if test.effortField != "" {
				require.Equal(t, test.effort, gjson.GetBytes(prepared.Body, test.effortField).String())
			} else {
				require.False(t, gjson.GetBytes(prepared.Body, "output_config").Exists())
			}
			require.Equal(t, test.thinkingType, gjson.GetBytes(prepared.Body, "thinking.type").String())
			require.False(t, gjson.GetBytes(prepared.Body, "thinking.budget_tokens").Exists(), "adaptive models must not receive a legacy thinking budget")
			require.False(t, gjson.GetBytes(prepared.Body, "generationConfig.thinkingConfig.thinkingBudget").Exists(), "Gemini 3 must receive a level instead of a numeric thinking budget")
			require.False(t, gjson.GetBytes(prepared.Body, "effort").Exists(), "Anthropic rejects top-level effort")
			wireBody, err := io.ReadAll(request.Body)
			require.NoError(t, err)
			require.Equal(t, prepared.Body, wireBody)
		})
	}
}

func TestStartupGenerationRequiresInferenceEvidence(t *testing.T) {
	for _, test := range []struct {
		name, body string
		wantErr    bool
	}{
		{name: "empty", body: `{}`, wantErr: true},
		{name: "invalid", body: `not JSON`, wantErr: true},
		{name: "wrong type", body: `{"choices":[{"message":{"content":{"invalid":"shape"}}}]}`, wantErr: true},
		{name: "error", body: `{"error":{"message":"failed"}}`, wantErr: true},
		{name: "text", body: `{"choices":[{"message":{"content":"OK"}}]}`},
		{name: "measured reasoning", body: `{"usage":{"output_tokens_details":{"reasoning_tokens":12}}}`},
		{name: "coerced reasoning", body: `{"usage":{"output_tokens_details":{"reasoning_tokens":"12"}}}`, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateStartupGeneration(&http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(test.body))}, inference.Target{Provider: providers.ProviderOpenAI})
			if test.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestStartupGenerationRejectsOversizedResponseBeforeRetainingIt(t *testing.T) {
	client := &startupInferenceClient{body: strings.Repeat("x", (1<<20)+1)}
	err := warmStartupModels(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), dispatch.NewClients(map[string]providers.Client{providers.ProviderOpenAI: client}), map[string]struct{}{catalog.ModelIDGPT55.String(): {}}, map[string]struct{}{providers.ProviderOpenAI: {}})
	require.ErrorIs(t, err, dispatch.ErrBufferedResponseTooLarge)
}
