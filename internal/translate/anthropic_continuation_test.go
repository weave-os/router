package translate_test

import (
	"net/http"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestAnthropicTranslatedAssistantContinuation(t *testing.T) {
	for _, tc := range []struct {
		name     string
		messages string
		wantText string
		wantLen  int
	}{
		{"assistant text", `[{"role":"user","content":"Inspect the project"},{"role":"assistant","content":"I will inspect the project."}]`, "I will inspect the project.", 3},
		{"assistant parts", `[{"role":"user","content":"Inspect the project"},{"role":"assistant","content":[{"type":"text","text":"I will inspect the project.\n\n"}]}]`, "I will inspect the project.", 3},
		{"empty assistant suffix", `[{"role":"user","content":"Inspect the project"},{"role":"assistant","content":"I will inspect the project."},{"role":"assistant","content":" \n"}]`, "I will inspect the project.", 3},
		{"consecutive assistants", `[{"role":"user","content":"Inspect the project"},{"role":"assistant","content":"First observation."},{"role":"assistant","content":"Next observation."}]`, "Next observation.", 4},
		{"after tool result", `[{"role":"user","content":"Inspect the project"},{"role":"assistant","tool_calls":[{"id":"call_read","type":"function","function":{"name":"read_file","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_read","content":"Project overview"},{"role":"assistant","content":"I will inspect its tests next."}]`, "I will inspect its tests next.", 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			envelope, err := translate.ParseOpenAI([]byte(`{"model":"gpt-6.1-sol","messages":` + tc.messages + `}`))
			require.NoError(t, err)
			prepared, err := envelope.PrepareAnthropic(http.Header{}, translate.EmitOptions{
				TargetModel: "claude-opus-5-5", TargetProvider: providers.ProviderAnthropic,
				Capabilities: router.Lookup("claude-opus-5-5"),
			})
			require.NoError(t, err)
			messages := gjson.GetBytes(prepared.Body, "messages").Array()
			require.Len(t, messages, tc.wantLen)
			assert.Equal(t, string(translate.EscalationRoleAssistant), messages[len(messages)-2].Get("role").String())
			content := messages[len(messages)-2].Get("content")
			if content.Type == gjson.String {
				assert.Equal(t, tc.wantText, content.String())
			} else {
				assert.Equal(t, tc.wantText, content.Get("0.text").String())
			}
			assert.Equal(t, string(translate.EscalationRoleUser), messages[len(messages)-1].Get("role").String())
			assert.Equal(t, "Continue.", messages[len(messages)-1].Get("content.0.text").String())
			assert.Equal(t, "Inspect the project", messages[0].Get("content").String())
			if tc.name == "after tool result" {
				assert.Equal(t, "call_read", messages[1].Get("content.0.id").String())
				assert.Equal(t, "call_read", messages[2].Get("content.0.tool_use_id").String())
				assert.Equal(t, "Project overview", messages[2].Get("content.0.content").String())
			}
		})
	}
}

func TestAnthropicContinuationPreservesRequestBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name     string
		messages string
		wantRole translate.EscalationRole
		wantLen  int
	}{
		{"user ending", `[{"role":"user","content":"Inspect the project"}]`, translate.EscalationRoleUser, 1},
		{"empty assistant", `[{"role":"user","content":"Inspect the project"},{"role":"assistant","content":" \n"}]`, translate.EscalationRoleUser, 1},
		{"pending tool call", `[{"role":"user","content":"Inspect the project"},{"role":"assistant","content":"Reading a file","tool_calls":[{"id":"call_read","type":"function","function":{"name":"read_file","arguments":"{}"}}]}]`, translate.EscalationRoleAssistant, 2},
		{"tool result ending", `[{"role":"user","content":"Inspect the project"},{"role":"assistant","tool_calls":[{"id":"call_read","type":"function","function":{"name":"read_file","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_read","content":"Project overview"}]`, translate.EscalationRoleUser, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			envelope, err := translate.ParseOpenAI([]byte(`{"messages":` + tc.messages + `}`))
			require.NoError(t, err)
			prepared, err := envelope.PrepareAnthropic(http.Header{}, translate.EmitOptions{TargetModel: "claude-opus-5-5", Capabilities: router.Lookup("claude-opus-5-5")})
			require.NoError(t, err)
			messages := gjson.GetBytes(prepared.Body, "messages").Array()
			require.Len(t, messages, tc.wantLen)
			assert.Equal(t, string(tc.wantRole), messages[len(messages)-1].Get("role").String())
			assert.NotContains(t, string(prepared.Body), "Continue.")
		})
	}
}
