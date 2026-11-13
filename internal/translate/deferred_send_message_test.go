package translate_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"weave-os/router/internal/translate"
)

func TestDeferredSendMessageSchemaSurvivesCrossVendorEmit(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-5-5","max_tokens":32000,"messages":[{"role":"user","content":"Load the messaging tool."},{"role":"assistant","content":[{"type":"tool_use","id":"load_message","name":"ToolSearch","input":{"query":"select:SendMessage"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"load_message","content":[{"type":"tool_reference","tool_name":"SendMessage"}]}]}],"tools":[{"name":"ToolSearch","input_schema":{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}},{"name":"SendMessage","defer_loading":true,"description":"Send a message to an existing peer session.","input_schema":{"type":"object","properties":{"to":{"type":"string"},"message":{"type":"string"}},"required":["to","message"]}}]}`)
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	opts := translate.EmitOptions{TargetModel: "gpt-6.1-sol", KeepCrossVendorOrchestrationTools: true}
	t.Run("Responses", func(t *testing.T) {
		out, err := env.PrepareOpenAIResponses(nil, opts)
		require.NoError(t, err)
		require.Contains(t, emittedResponsesToolNames(t, out.Body), "SendMessage")
		tool := gjson.GetBytes(out.Body, `tools.#(name=="SendMessage")`)
		require.Equal(t, "string", tool.Get("parameters.properties.to.type").String())
		require.Equal(t, "string", tool.Get("parameters.properties.message.type").String())
		require.Equal(t, "Send a message to an existing peer session.", tool.Get("description").String())
	})
	t.Run("Chat", func(t *testing.T) {
		out, err := env.PrepareOpenAI(nil, opts)
		require.NoError(t, err)
		require.Contains(t, emittedToolNames(t, out.Body), "SendMessage")
		tool := gjson.GetBytes(out.Body, `tools.#(function.name=="SendMessage").function`)
		require.Equal(t, "string", tool.Get("parameters.properties.to.type").String())
	})
	t.Run("Gemini", func(t *testing.T) {
		opts.TargetModel = "gemini-2.5-pro"
		out, err := env.PrepareGemini(http.Header{}, opts)
		require.NoError(t, err)
		require.Contains(t, emittedGeminiToolNames(t, out.Body), "SendMessage")
	})
}
