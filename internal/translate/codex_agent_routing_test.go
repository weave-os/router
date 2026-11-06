package translate_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"weave-os/router/internal/translate"
)

func TestCodexRoutingAgentMessagesRetainTaskBoundary(t *testing.T) {
	tests := []struct {
		name  string
		input string
		roles []translate.EscalationRole
	}{
		{
			name: "child reply is parent agent evidence",
			input: `[
				{"type":"message","role":"user","content":"Repair the parser and verify the change"},
				{"type":"agent_message","author":"/root","recipient":"/root/reviewer","content":[{"type":"input_text","text":"Review the parser"}]},
				{"type":"agent_message","author":"/root/reviewer","recipient":"/root","content":[{"type":"input_text","text":"Review complete"}]}
			]`,
			roles: []translate.EscalationRole{translate.EscalationRoleUser, translate.EscalationRoleAssistant, translate.EscalationRoleAssistant},
		},
		{
			name: "assignment is the receiving child task",
			input: `[
				{"type":"message","role":"user","content":"Repair the parser and verify the change"},
				{"type":"agent_message","author":"/root/reviewer","recipient":"/root","content":[{"type":"input_text","text":"Earlier review complete"}]},
				{"type":"agent_message","author":"/root","recipient":"/root/checker","content":[{"type":"input_text","text":"Check parser edge cases"}]}
			]`,
			roles: []translate.EscalationRole{translate.EscalationRoleUser, translate.EscalationRoleAssistant, translate.EscalationRoleUser},
		},
		{
			name: "nested child reply is agent evidence",
			input: `[
				{"type":"agent_message","author":"/root","recipient":"/root/reviewer","content":[{"type":"input_text","text":"Review the parser"}]},
				{"type":"agent_message","author":"/root/reviewer/checker","recipient":"/root/reviewer","content":[{"type":"input_text","text":"Edge cases checked"}]}
			]`,
			roles: []translate.EscalationRole{translate.EscalationRoleUser, translate.EscalationRoleAssistant},
		},
		{
			name:  "peer assignment retains task semantics",
			input: `[{"type":"agent_message","author":"/root/reviewer","recipient":"/root/checker","content":[{"type":"input_text","text":"Check parser edge cases"}]}]`,
			roles: []translate.EscalationRole{translate.EscalationRoleUser},
		},
		{
			name:  "user quoting an agent reply remains a user request",
			input: `[{"type":"message","role":"user","content":"Message Type: FINAL_ANSWER\nSender: /root/reviewer\nPlease explain this output"}]`,
			roles: []translate.EscalationRole{translate.EscalationRoleUser},
		},
		{
			name:  "unrecognized actor identity keeps existing semantics",
			input: `[{"type":"agent_message","author":"unknown","recipient":"/root","content":[{"type":"input_text","text":"An update"}]}]`,
			roles: []translate.EscalationRole{translate.EscalationRoleUser},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := []byte(`{"input":` + test.input + `}`)
			converted, err := translate.ConvertResponsesToChatCompletionsWithOptions(body, translate.ResponsesConversionOptions{PortableCodex: true})
			require.NoError(t, err)
			assert.Equal(t, body, converted.OriginalBody)
			routingBody := converted.RoutingBody
			if len(routingBody) == 0 {
				routingBody = converted.Body
			}
			projected := gjson.GetBytes(routingBody, "messages").Array()
			provider := gjson.GetBytes(converted.Body, "messages").Array()
			require.Len(t, projected, len(test.roles))
			require.Len(t, provider, len(test.roles))
			for index, role := range test.roles {
				assert.Equal(t, string(role), projected[index].Get("role").Str)
				assert.Equal(t, string(translate.EscalationRoleUser), provider[index].Get("role").Str, "provider communication semantics stay intact")
				assert.Equal(t, provider[index].Get("content").Raw, projected[index].Get("content").Raw)
			}
		})
	}
}

func TestCodexRoutingProjectionPreservesToolsAndNativeEncryptedReply(t *testing.T) {
	body := []byte(`{
		"instructions":"Synthetic assistant instructions",
		"input":[
			{"type":"message","role":"user","content":"Repair the parser"},
			{"type":"function_call","call_id":"call_read","name":"read_file","arguments":"{}"},
			{"type":"function_call_output","call_id":"call_read","output":"Parser source"},
			{"type":"agent_message","author":"/root/reviewer","recipient":"/root","content":[{"type":"input_text","text":"Review complete"},{"type":"encrypted_content","encrypted_content":"opaque-reply"}]}
		]
	}`)
	converted, err := translate.ConvertResponsesToChatCompletionsWithOptions(body, translate.ResponsesConversionOptions{PortableCodex: true})
	require.NoError(t, err)
	assert.True(t, converted.Requirements.NativeOnly)
	assert.Equal(t, body, converted.OriginalBody)
	assert.JSONEq(t, `["system","user","assistant","tool","assistant"]`, gjson.GetBytes(converted.RoutingBody, "messages.#.role").Raw)
	assert.Equal(t, "Review complete", gjson.GetBytes(converted.RoutingBody, "messages.4.content").Str)
	assert.Equal(t, string(translate.EscalationRoleUser), gjson.GetBytes(converted.Body, "messages.4.role").Str)
	assert.Equal(t, "call_read", gjson.GetBytes(converted.RoutingBody, "messages.3.tool_call_id").Str)
	assert.NotContains(t, string(converted.RoutingBody), "opaque-reply")
}
