package proxy_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/translate"
)

func TestCodexRoutesAgentRepliesWithoutChangingProviderInput(t *testing.T) {
	for _, test := range []struct {
		name      string
		delivery  string
		wantTask  string
		wantReply bool
	}{
		{
			name:      "parent receives a child reply",
			delivery:  `{"type":"agent_message","author":"/root/reviewer","recipient":"/root","content":[{"type":"input_text","text":"Review complete"}]}`,
			wantTask:  "Repair the parser and verify edge cases",
			wantReply: true,
		},
		{
			name:     "child receives its delegated task",
			delivery: `{"type":"agent_message","author":"/root","recipient":"/root/reviewer","content":[{"type":"input_text","text":"Review parser edge cases"}]}`,
			wantTask: "Review parser edge cases",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			selected := &fakeRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: catalog.ModelGPT6Luna, Reason: "scored"}}
			upstream := &fakeProvider{proxyResponse: responsesTextUpstream}
			svc := proxy.NewService(selected, map[string]providers.Client{providers.ProviderOpenAI: upstream}, nil, nil, false, providers.ProviderOpenAI, catalog.ModelGPT6Luna, nil).
				WithPolicyStrategy(policy.StrategySpec{Strategy: router.StrategyHMM, Router: selected, Capabilities: policy.Capabilities{AuthoritativePerTurnSelection: true}})
			body := []byte(`{"model":"weave-auto","stream":true,"tools":[{"type":"function","name":"read_file","parameters":{"type":"object"}}],"input":[
				{"type":"message","role":"user","content":"Repair the parser and verify edge cases"},` + test.delivery + `]}`)
			ctx := context.WithValue(context.Background(), proxy.ClientIdentityContextKey{}, proxy.ClientIdentity{ClientApp: proxy.ClientAppCodex, SessionID: "synthetic-agent-projection"})
			ctx = router.WithStrategy(ctx, router.StrategyHMM)
			rec := httptest.NewRecorder()
			require.NoError(t, svc.ProxyOpenAIResponses(ctx, body, rec, httptest.NewRequest(http.MethodPost, "/v1/responses", nil)))
			require.NotNil(t, selected.capturedReq)
			var latestTask string
			for _, message := range selected.capturedReq.ConversationMessages {
				if message.Role == string(translate.EscalationRoleUser) && message.Text != "" {
					latestTask = message.Text
				}
			}
			assert.Equal(t, test.wantTask, latestTask)
			if test.wantReply {
				last := selected.capturedReq.ConversationMessages[len(selected.capturedReq.ConversationMessages)-1]
				assert.Equal(t, string(translate.EscalationRoleAssistant), last.Role)
				assert.Equal(t, "Review complete", last.Text)
			}
			require.Len(t, upstream.proxyBodies, 1)
			assert.JSONEq(t, gjson.GetBytes(body, "input").Raw, gjson.GetBytes(upstream.proxyBodies[0], "input").Raw)
			assert.Equal(t, "agent_message", gjson.GetBytes(upstream.proxyBodies[0], "input.1.type").Str)
		})
	}
}

func TestCodexRoutingHistoryStripsConsumedToolCommands(t *testing.T) {
	for _, test := range []struct {
		name            string
		command         string
		remainingOutput string
	}{
		{name: "force model", command: "$unforce-model\nTool output remains", remainingOutput: "Tool output remains"},
		{name: "feedback", command: "$router-feedback up synthetic feedback"},
	} {
		t.Run(test.name, func(t *testing.T) {
			selected := &fakeRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: catalog.ModelGPT6Luna, Reason: "scored"}}
			upstream := &fakeProvider{proxyResponse: responsesTextUpstream}
			svc := proxy.NewService(selected, map[string]providers.Client{providers.ProviderOpenAI: upstream}, nil, newFakePinStore(), false, providers.ProviderOpenAI, catalog.ModelGPT6Luna, nil).
				WithPolicyStrategy(policy.StrategySpec{Strategy: router.StrategyHMM, Router: selected, Capabilities: policy.Capabilities{AuthoritativePerTurnSelection: true}})
			body := []byte(`{"model":"weave-auto","stream":true,"tools":[{"type":"function","name":"read_file","parameters":{"type":"object"}}],"input":[
				{"type":"message","role":"user","content":"Repair the parser"},
				{"type":"agent_message","author":"/root/reviewer","recipient":"/root","content":[{"type":"input_text","text":"Review complete"}]},
				{"type":"function_call","call_id":"read","name":"read_file","arguments":"{}"},
				{"type":"function_call_output","call_id":"read","output":` + strconv.Quote(test.command) + `}]}`)
			ctx := context.WithValue(context.Background(), proxy.ClientIdentityContextKey{}, proxy.ClientIdentity{ClientApp: proxy.ClientAppCodex, SessionID: "synthetic-command-projection"})
			ctx = router.WithStrategy(ctx, router.StrategyHMM)
			require.NoError(t, svc.ProxyOpenAIResponses(ctx, body, httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/responses", nil)))
			require.NotNil(t, selected.capturedReq)
			assert.NotContains(t, selected.capturedReq.PromptText, "$unforce-model")
			assert.NotContains(t, selected.capturedReq.PromptText, "$router-feedback")
			var toolOutput string
			var sawReply bool
			for _, message := range selected.capturedReq.ConversationMessages {
				if message.Text == "Review complete" {
					sawReply = true
					assert.Equal(t, string(translate.EscalationRoleAssistant), message.Role)
				}
				for _, output := range message.ToolResults {
					assert.NotContains(t, output.Text, "$unforce-model")
					assert.NotContains(t, output.Text, "$router-feedback")
					toolOutput += output.Text
				}
			}
			assert.True(t, sawReply)
			if test.remainingOutput != "" {
				assert.Contains(t, toolOutput, test.remainingOutput)
			}
		})
	}
}
