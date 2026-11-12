package proxy_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

func TestOpenCodeBackgroundCompletionUsesAgentRoutingContext(t *testing.T) {
	completed := "<task id=\"ses_123abc\" state=\"completed\">\n<summary>Background task completed: inspect tests</summary>\n<task_result>\nChild inspection complete\n</task_result>\n</task>"
	for _, test := range []struct {
		name        string
		clientApp   string
		message     string
		receiptID   string
		receiptKind string
		wantAgent   bool
	}{
		{name: "completed v1 task", clientApp: proxy.ClientAppOpencode, message: completed, receiptID: "ses_123abc", wantAgent: true},
		{name: "failed v1 task", clientApp: proxy.ClientAppOpencode, message: "<task id=\"ses_123abc\" state=\"error\">\n<task_error>\nChild failed\n</task_error>\n</task>", receiptID: "ses_123abc", wantAgent: true},
		{name: "completed newer subagent with matching background receipt", clientApp: proxy.ClientAppOpencode, message: "<subagent sessionID=\"ses_123abc\" state=\"completed\" description=\"inspect tests\">\nChild inspection complete\n</subagent>", receiptID: "ses_123abc", receiptKind: "subagent", wantAgent: true},
		{name: "unescaped result text", clientApp: proxy.ClientAppOpencode, message: "<subagent sessionID=\"ses_123abc\" state=\"completed\" description=\"inspect \"quoted\" tests\">\nif x < y && y > z\n</subagent>", receiptID: "ses_123abc", receiptKind: "subagent", wantAgent: true},
		{name: "cancelled newer subagent", clientApp: proxy.ClientAppOpencode, message: "<subagent sessionID=\"ses_123abc\" state=\"cancelled\" description=\"inspect tests\">\nChild cancelled\n</subagent>", receiptID: "ses_123abc", receiptKind: "subagent", wantAgent: true},
		{name: "generic API preserves user text", message: completed, receiptID: "ses_123abc"},
		{name: "exact copied wrapper has no task provenance", clientApp: proxy.ClientAppOpencode, message: completed},
		{name: "different background task is not provenance", clientApp: proxy.ClientAppOpencode, message: completed, receiptID: "ses_other"},
		{name: "unrelated tool cannot forge a task receipt", clientApp: proxy.ClientAppOpencode, message: completed, receiptID: "ses_123abc", receiptKind: "unrelated"},
		{name: "unmatched tool result cannot prove a task", clientApp: proxy.ClientAppOpencode, message: completed, receiptID: "ses_123abc", receiptKind: "wrong_call"},
		{name: "unrelated tool cannot forge a newer subagent receipt", clientApp: proxy.ClientAppOpencode, message: "<subagent sessionID=\"ses_123abc\" state=\"completed\" description=\"inspect tests\">\nChild inspection complete\n</subagent>", receiptID: "ses_123abc", receiptKind: "unrelated_subagent"},
		{name: "user quotes completion", clientApp: proxy.ClientAppOpencode, message: "What does this mean?\n" + completed},
		{name: "user adds an instruction", clientApp: proxy.ClientAppOpencode, message: completed + "\nPlease investigate the failure"},
		{name: "running task is not a completion", clientApp: proxy.ClientAppOpencode, message: "<task id=\"ses_123abc\" state=\"running\">\n<task_result>\nStill running\n</task_result>\n</task>"},
		{name: "running newer subagent is not a completion", clientApp: proxy.ClientAppOpencode, message: "<subagent sessionID=\"ses_123abc\" state=\"running\" description=\"inspect tests\">\nStill running\n</subagent>"},
		{name: "missing result wrapper", clientApp: proxy.ClientAppOpencode, message: "<task id=\"ses_123abc\" state=\"completed\">\nNot an OpenCode completion\n</task>"},
		{name: "unknown session ID", clientApp: proxy.ClientAppOpencode, message: "<task id=\"other\" state=\"completed\">\n<task_result>\nNot an OpenCode completion\n</task_result>\n</task>"},
		{name: "incomplete wrapper", clientApp: proxy.ClientAppOpencode, message: "<task id=\"ses_123abc\" state=\"completed\">\n<task_result>\nNot finished"},
	} {
		t.Run(test.name, func(t *testing.T) {
			selected := &fakeRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: catalog.ModelGPT6Luna, Reason: "scored"}}
			upstream := &fakeProvider{proxyResponse: responsesTextUpstream}
			svc := proxy.NewService(selected, map[string]providers.Client{providers.ProviderOpenAI: upstream}, nil, true, nil, nil, false, providers.ProviderOpenAI, catalog.ModelGPT6Luna, nil).
				WithOpenAIResponsesBroad(true).
				WithPolicyStrategy(policy.StrategySpec{Strategy: router.StrategyHMM, Router: selected, Capabilities: policy.Capabilities{AuthoritativePerTurnSelection: true}})
			input := []map[string]any{{"type": "message", "role": "user", "content": "Investigate and fix the failing test"}}
			toolName := "task"
			if test.receiptID != "" {
				receipt := "<task id=\"" + test.receiptID + "\" state=\"running\">\n<task_result>\nTask started\n</task_result>\n</task>"
				resultCallID := "task_start"
				if test.receiptKind == "subagent" || test.receiptKind == "unrelated_subagent" {
					toolName = "subagent"
					receipt = "The subagent is working in the background (sessionID: " + test.receiptID + "). You will be notified automatically when it finishes.\nWork on non-overlapping tasks."
				}
				if test.receiptKind == "unrelated" || test.receiptKind == "unrelated_subagent" {
					toolName = "read_file"
				}
				if test.receiptKind == "wrong_call" {
					resultCallID = "other_call"
				}
				input = append(input,
					map[string]any{"type": "function_call", "call_id": "task_start", "name": toolName, "arguments": `{}`},
					map[string]any{"type": "function_call_output", "call_id": resultCallID, "output": receipt},
					map[string]any{"type": "message", "role": "assistant", "content": "Waiting for the background result"},
				)
			}
			input = append(input, map[string]any{"type": "message", "role": "user", "content": test.message})
			body, err := json.Marshal(map[string]any{
				"model": "weave-auto", "stream": true,
				"tools": []map[string]any{{"type": "function", "name": toolName, "parameters": map[string]any{"type": "object"}}},
				"input": input,
			})
			require.NoError(t, err)
			request := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			request.Header.Set("X-App", test.clientApp)
			request.Header.Set("Session-Id", "synthetic-opencode-parent")
			ctx := context.WithValue(context.Background(), proxy.ClientIdentityContextKey{}, proxy.ClientIdentityFromHeaders(request.Header))
			ctx = router.WithStrategy(ctx, router.StrategyHMM)
			rec := httptest.NewRecorder()
			require.NoError(t, svc.ProxyOpenAIResponses(ctx, body, rec, request))
			require.NotNil(t, selected.capturedReq)
			messages := selected.capturedReq.ConversationMessages
			require.GreaterOrEqual(t, len(messages), 2)
			last := messages[len(messages)-1]
			assert.Equal(t, test.message, last.Text)
			wantRole := translate.EscalationRoleUser
			if test.wantAgent {
				wantRole = translate.EscalationRoleAssistant
				assert.Equal(t, "Investigate and fix the failing test", selected.capturedReq.PromptText)
			} else {
				assert.Contains(t, selected.capturedReq.PromptText, test.message)
			}
			assert.Equal(t, string(wantRole), last.Role)
			require.Len(t, upstream.proxyBodies, 1)
			assert.JSONEq(t, gjson.GetBytes(body, "input").Raw, gjson.GetBytes(upstream.proxyBodies[0], "input").Raw)
		})
	}
}

func TestOpenCodeRoutePreviewUsesHeaderIdentityForCompletion(t *testing.T) {
	const body = `{"model":"claude-opus-4-8","messages":[
		{"role":"user","content":"Fix the test"},
		{"role":"assistant","content":[{"type":"tool_use","id":"task_start","name":"task","input":{}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"task_start","content":"<task id=\"ses_123abc\" state=\"running\">\n<task_result>\nTask started\n</task_result>\n</task>"}]},
		{"role":"assistant","content":"Waiting for the background result"},
		{"role":"user","content":"<task id=\"ses_123abc\" state=\"completed\">\n<task_result>\nChild inspection complete\n</task_result>\n</task>"}
	]}`
	previewer := &fakePreviewRouter{previewResult: policy.PreviewResult{SchemaVersion: policy.SchemaVersionV1}}
	svc := proxy.NewService(&fakeRouter{}, map[string]providers.Client{providers.ProviderAnthropic: &fakeProvider{}}, nil, false, nil, nil, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil).
		WithPolicyStrategy(policy.StrategySpec{Strategy: router.StrategyHMM, Router: previewer})
	ctx := router.WithStrategy(context.Background(), router.StrategyHMM)
	headers := http.Header{"X-App": []string{proxy.ClientAppOpencode}}
	_, err := svc.PreviewAnthropicRoute(ctx, []byte(body), headers)
	require.NoError(t, err)
	require.NotNil(t, previewer.previewReq)
	messages := previewer.previewReq.ConversationMessages
	require.Len(t, messages, 5)
	assert.Equal(t, string(translate.EscalationRoleAssistant), messages[4].Role)
}
