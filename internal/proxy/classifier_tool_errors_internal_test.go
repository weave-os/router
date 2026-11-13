package proxy

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/router"
)

func TestClassifierCodexToolErrorsRequireIdentifiedResponsesIngress(t *testing.T) {
	const failure = `"Chunk ID: abc123\nWall time: 0.0001 seconds\nProcess exited with code 7\nOriginal token count: 1\nOutput:\nSynthetic output\n"`
	const anthropic = `{"model":"auto","max_tokens":128,"tools":[{"name":"TOOL","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"Synthetic task"},{"role":"assistant","content":[{"type":"tool_use","id":"c1","name":"TOOL","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"c1","content":OUTPUT ERROR}]}]}`
	const chat = `{"model":"auto","tools":[{"type":"function","function":{"name":"TOOL","parameters":{"type":"object"}}}],"messages":[{"role":"user","content":"Synthetic task"},{"role":"assistant","tool_calls":[{"type":"function","id":"c1","function":{"name":"TOOL","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c1","content":OUTPUT ERROR}]}`
	const responses = `{"model":"auto","codex_tool_results":true,"tools":[{"type":"function","name":"TOOL","parameters":{"type":"object"}}],"input":[{"role":"user","content":"Synthetic task"},{"type":"function_call","call_id":"c1","name":"TOOL","arguments":"{}"},{"type":"function_call_output","call_id":"c1","output":OUTPUT ERROR}]}`
	failed, succeeded := true, false
	for _, fixture := range []struct {
		name         string
		endpoint     router.TranslationEndpoint
		client       string
		body         string
		inferFailure bool
	}{
		{name: "Anthropic client tool", endpoint: router.EndpointAnthropicMessages, client: ClientAppClaudeCode, body: anthropic},
		{name: "Chat client tool", endpoint: router.EndpointOpenAIChat, client: ClientAppCursor, body: chat},
		{name: "Responses client tool", endpoint: router.EndpointOpenAIResponses, body: responses},
		{name: "OpenCode Responses tool", endpoint: router.EndpointOpenAIResponses, client: ClientAppOpencode, body: responses},
		{name: "Codex identity on Anthropic", endpoint: router.EndpointAnthropicMessages, client: ClientAppCodex, body: anthropic},
		{name: "Codex identity on Chat", endpoint: router.EndpointOpenAIChat, client: ClientAppCodex, body: chat},
		{name: "Codex Responses tool", endpoint: router.EndpointOpenAIResponses, client: ClientAppCodex, body: responses, inferFailure: true},
		{name: "Codex unrelated namespace", endpoint: router.EndpointOpenAIResponses, client: ClientAppCodex, body: strings.Replace(responses, `"call_id":"c1","name"`, `"call_id":"c1","namespace":"client_tools","name"`, 1)},
	} {
		for _, tool := range []string{"exec_command", "write_stdin"} {
			for _, verdict := range []struct {
				name     string
				field    string
				explicit *bool
			}{
				{name: "absent"},
				{name: "explicit failure", field: `,"is_error":true`, explicit: &failed},
				{name: "explicit success", field: `,"is_error":false`, explicit: &succeeded},
			} {
				t.Run(fixture.name+"/"+tool+"/"+verdict.name, func(t *testing.T) {
					ctx := router.WithStrategy(context.Background(), router.StrategyLLMClassifier)
					ctx = context.WithValue(ctx, ClientIdentityContextKey{}, ClientIdentity{ClientApp: fixture.client})
					body := strings.NewReplacer("TOOL", tool, "OUTPUT", failure, "ERROR", verdict.field).Replace(fixture.body)
					captured, err := (&Service{}).withClassifierInput(ctx, []byte(body), fixture.endpoint)
					require.NoError(t, err)
					input := captured.Value(classifierInputContextKey{}).(router.ClassifierContext)
					wantFailure := fixture.inferFailure
					if verdict.explicit != nil {
						wantFailure = *verdict.explicit
					}
					wantErrors := 0
					if wantFailure {
						wantErrors = 1
					}
					require.Equal(t, router.ClassifierFeatures{UserMessageCount: 1, ToolCallCount: 1, ToolErrorCount: wantErrors}, input.Features)
				})
			}
		}
	}
}
