package proxy_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const (
	budgetIngressModel     = "claude-fable-5-1"
	budgetIngressUserAgent = "claude-cli/2.1.257 (external, sdk-ts)"
	budgetIngressSummary   = "This session is being continued from a previous conversation that ran out of context. Summary: the fix is in parser.go; next run its focused tests."
)

func TestClientBudgetServingAndPreviewUseOriginalVariantEachTurn(t *testing.T) {
	provider := &fakeProvider{}
	serving := &fakeRouter{decision: router.Decision{Provider: providers.ProviderAnthropic, Model: budgetIngressModel}}
	preview := &fakePreviewRouter{}
	svc := proxy.NewService(serving, map[string]providers.Client{providers.ProviderAnthropic: provider}, nil, false, nil, nil, false, providers.ProviderAnthropic, budgetIngressModel, nil).
		WithPolicyStrategy(policy.StrategySpec{Strategy: router.StrategyHMM, Router: preview})
	ctx := requestcontext.WithClientIdentity(context.Background(), proxy.ClientIdentity{SessionID: "session-a", ClientApp: proxy.ClientAppClaudeCode, UserAgent: budgetIngressUserAgent})

	for _, tt := range []struct {
		suffix string
		window int
	}{
		{"[1m]", 1_000_000}, {"", 200_000}, {"[1m]", 1_000_000},
	} {
		body := []byte(fmt.Sprintf(`{"model":%q,"max_tokens":64000,"messages":[{"role":"user","content":"continue the approved task"}]}`, budgetIngressModel+tt.suffix))
		r := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		err := svc.ProxyMessages(ctx, body, httptest.NewRecorder(), r)
		require.NoError(t, err)
		require.NotNil(t, serving.capturedReq)
		assert.Equal(t, budgetIngressModel, serving.capturedReq.RequestedModel)
		assert.Equal(t, tt.window, serving.capturedReq.ClientBudget.DefaultWindow)
		assert.Equal(t, tt.suffix != "", serving.capturedReq.ClientBudget.ModelVariant1M)
		assert.Equal(t, budgetIngressModel, gjson.GetBytes(provider.proxyBodies[len(provider.proxyBodies)-1], "model").String())

		_, err = svc.PreviewAnthropicRoute(router.WithStrategy(ctx, router.StrategyHMM), body, r.Header)
		require.NoError(t, err)
		require.NotNil(t, preview.previewReq)
		assert.Equal(t, serving.capturedReq.ClientBudget, preview.previewReq.ClientBudget)
	}
}

func TestClientCompactionGuidanceAndResumeReachProvider(t *testing.T) {
	provider := &fakeProvider{}
	svc := makeProxyService(router.Decision{Provider: providers.ProviderAnthropic, Model: budgetIngressModel}, map[string]providers.Client{providers.ProviderAnthropic: provider})
	ctx := requestcontext.WithClientIdentity(context.Background(), proxy.ClientIdentity{ClientApp: proxy.ClientAppClaudeCode, UserAgent: budgetIngressUserAgent})
	for _, tt := range []struct {
		name         string
		model        string
		user         string
		wantGuidance bool
		wantSerial   bool
	}{
		{"summary", budgetIngressModel, "Your task is to create a detailed summary. Do not call any tools.", true, false},
		{"small window continuation", budgetIngressModel, budgetIngressSummary, false, true},
		{"long context continuation", budgetIngressModel + "[1m]", budgetIngressSummary, false, false},
		{"ordinary task", budgetIngressModel, "read the relevant file", false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(fmt.Sprintf(`{"model":%q,"max_tokens":64000,"system":"existing rules","messages":[{"role":"user","content":%q}],"tools":[{"name":"Read","description":"read a file","input_schema":{"type":"object","properties":{"path":{"type":"string"}}}}]}`, tt.model, tt.user))
			err := svc.ProxyMessages(ctx, body, httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
			require.NoError(t, err)
			upstream := provider.proxyBodies[len(provider.proxyBodies)-1]
			assert.Equal(t, tt.wantSerial, gjson.GetBytes(upstream, "tool_choice.disable_parallel_tool_use").Bool())
			assert.Equal(t, tt.wantGuidance, strings.Contains(string(upstream), "Write a compact working-state handoff"))
			assert.Contains(t, string(upstream), tt.user)
			assert.Contains(t, gjson.GetBytes(upstream, "system").Raw, "existing rules")
			assert.Equal(t, int64(64000), gjson.GetBytes(upstream, "max_tokens").Int())
		})
	}
}
