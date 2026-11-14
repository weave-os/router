package translate

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Codex advertises OpenAI's hosted web_search and replays its web_search_call
// items. Neither may pin the session to OpenAI: search recency is scoped by the
// CitationsOrSearch requirement, and other providers get the rest of the turn.
func TestPortableCodex_WebSearchStaysPortable(t *testing.T) {
	body := []byte(`{"model":"auto","stream":true,"tools":[{"type":"function","name":"exec_command","parameters":{"type":"object","properties":{}}},{"type":"web_search","external_web_access":true}],"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"find the docs"}]},{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"router docs"}},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Found them."}]}]}`)

	conversion, err := convertPortableCodexResponses(body)
	require.NoError(t, err)

	assert.False(t, conversion.Requirements.NativeOnly, "hosted web search must not make the request native-only")
	assert.True(t, conversion.Requirements.CitationsOrSearch, "search recency still governs native routing")
	var dropped []string
	for _, transform := range conversion.Report {
		if transform.Action == "dropped" {
			dropped = append(dropped, transform.Code+"@"+transform.Path)
		}
	}
	assert.Contains(t, dropped, "responses_hosted_web_search_dropped@tools.1")
	assert.Contains(t, dropped, "responses_web_search_call_dropped@input.1")
	assert.NotContains(t, string(conversion.Body), "web_search")
}

func TestPortableCodex_ForcedWebSearchStaysNative(t *testing.T) {
	for name, body := range map[string]string{
		"hosted choice":       `{"model":"auto","tools":[{"type":"function","name":"exec_command","parameters":{"type":"object","properties":{}}},{"type":"web_search"}],"tool_choice":{"type":"web_search"},"input":"search it"}`,
		"required, only tool": `{"model":"auto","tools":[{"type":"web_search_preview"}],"tool_choice":"required","input":"search it"}`,
	} {
		t.Run(name, func(t *testing.T) {
			conversion, err := convertPortableCodexResponses([]byte(body))
			require.NoError(t, err)
			assert.True(t, conversion.Requirements.NativeOnly)
		})
	}

	conversion, err := convertPortableCodexResponses([]byte(`{"model":"auto","tools":[{"type":"function","name":"exec_command","parameters":{"type":"object","properties":{}}},{"type":"web_search"}],"tool_choice":"required","input":"run it"}`))
	require.NoError(t, err)
	assert.False(t, conversion.Requirements.NativeOnly, "a portable tool can still satisfy required")
}
