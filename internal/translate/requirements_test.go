package translate

import (
	"testing"

	"weave-os/router/internal/router"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTranslationRequirements_DetectsAnthropicPreservationSemantics(t *testing.T) {
	env, err := ParseAnthropic([]byte(`{
        "model":"claude-sonnet-4-5",
        "thinking":{"type":"enabled"},
        "output_config":{"format":{"type":"json_schema"}},
        "messages":[{"role":"user","content":[
          {"type":"document","source":{"type":"text","media_type":"text/plain","data":"notes"}},
          {"type":"audio","source":{"type":"base64","media_type":"audio/wav","data":"AA=="}}
        ]}],
        "tools":[{"type":"web_search_20250305","name":"web_search"}],
        "system":[{"type":"text","text":"remember","cache_control":{"type":"ephemeral"}}]
    }`))
	require.NoError(t, err)

	req := env.TranslationRequirements(router.EndpointAnthropicMessages)
	assert.True(t, req.ReasoningReplay)
	assert.True(t, req.PromptCacheControl)
	assert.True(t, req.StructuredOutput)
	assert.True(t, req.Audio)
	assert.True(t, req.Files)
	assert.True(t, req.CitationsOrSearch)
	upload, err := ParseAnthropic([]byte(`{"messages":[{"role":"user","content":[{"type":"container_upload","file_id":"file_123"}]}]}`))
	require.NoError(t, err)
	assert.True(t, upload.TranslationRequirements(router.EndpointAnthropicMessages).Files)
}

func TestTranslationRequirements_NativeServerToolsAreStructural(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "Claude Code client WebSearch and WebFetch",
			body: `{"tools":[{"name":"WebSearch","input_schema":{"type":"object"}},{"name":"WebFetch","input_schema":{"type":"object"}}]}`,
			want: false,
		},
		{
			name: "Anthropic native web search",
			body: `{"tools":[{"type":"web_search_20250305","name":"web_search"}]}`,
			want: true,
		},
		{
			name: "newer Anthropic web search revisions",
			body: `{"tools":[{"type":"web_search_20260209","name":"web_search"},{"type":"web_search_20260318","name":"web_search"}]}`,
			want: true,
		},
		{
			name: "MCP tool named web_search",
			body: `{"tools":[{"name":"web_search","input_schema":{"type":"object"}}]}`,
			want: false,
		},
		{
			name: "prior tool use only",
			body: `{"messages":[{"role":"assistant","content":[{"type":"tool_use","name":"web_search","id":"toolu_1","input":{}}]}]}`,
			want: false,
		},
		{
			name: "user text only",
			body: `{"messages":[{"role":"user","content":"please inspect the literal string web_search"}]}`,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, err := ParseAnthropic([]byte(tt.body))
			require.NoError(t, err)
			assert.Equal(t, tt.want, env.TranslationRequirements(router.EndpointAnthropicMessages).CitationsOrSearch)
		})
	}
}

func TestTranslationRequirements_DetectsOpenAIMediaAndSearch(t *testing.T) {
	env, err := ParseOpenAI([]byte(`{
        "model":"gpt-5.5",
        "messages":[{"role":"user","content":[
          {"type":"input_audio","input_audio":{"data":"AA==","format":"wav"}},
          {"type":"input_file","file_id":"file_123"}
        ]}],
        "tools":[{"type":"web_search_preview"}],
        "response_format":{"type":"json_object"}
    }`))
	require.NoError(t, err)

	req := env.TranslationRequirements(router.EndpointOpenAIChat)
	assert.True(t, req.Audio)
	assert.True(t, req.Files)
	assert.True(t, req.CitationsOrSearch)
	assert.True(t, req.StructuredOutput)
}

func TestTranslationRequirements_DetectsGeminiAudioAndFiles(t *testing.T) {
	env, err := ParseGemini([]byte(`{
        "contents":[{"role":"user","parts":[
          {"inlineData":{"mimeType":"audio/wav","data":"AA=="}},
          {"fileData":{"mimeType":"application/pdf","fileUri":"gs://bucket/doc.pdf"}}
        ]}]
    }`))
	require.NoError(t, err)

	req := env.TranslationRequirements(router.EndpointGeminiGenerate)
	assert.True(t, req.Audio)
	assert.True(t, req.Files)
}

func TestTranslationRequirements_DetectsParallelToolLimit(t *testing.T) {
	const tool = `{"type":"function","function":{"name":"Read","parameters":{"type":"object"}}}`
	const anthropicTool = `{"name":"Read","input_schema":{"type":"object"}}`
	cases := []struct {
		name   string
		format Format
		body   string
		want   bool
	}{
		{"openai limit", FormatOpenAI, `{"messages":[{"role":"user","content":"hi"}],"tools":[` + tool + `],"parallel_tool_calls":false}`, true},
		{"openai parallel allowed", FormatOpenAI, `{"messages":[{"role":"user","content":"hi"}],"tools":[` + tool + `],"parallel_tool_calls":true}`, false},
		{"openai unset", FormatOpenAI, `{"messages":[{"role":"user","content":"hi"}],"tools":[` + tool + `]}`, false},
		{"openai limit without tools", FormatOpenAI, `{"messages":[{"role":"user","content":"hi"}],"parallel_tool_calls":false}`, false},
		{"anthropic limit", FormatAnthropic, `{"messages":[{"role":"user","content":"hi"}],"tools":[` + anthropicTool + `],"tool_choice":{"type":"auto","disable_parallel_tool_use":true}}`, true},
		{"anthropic parallel allowed", FormatAnthropic, `{"messages":[{"role":"user","content":"hi"}],"tools":[` + anthropicTool + `],"tool_choice":{"type":"auto","disable_parallel_tool_use":false}}`, false},
		{"anthropic non-boolean limit", FormatAnthropic, `{"messages":[{"role":"user","content":"hi"}],"tools":[` + anthropicTool + `],"tool_choice":{"type":"auto","disable_parallel_tool_use":"true"}}`, false},
		{"anthropic limit without tools", FormatAnthropic, `{"messages":[{"role":"user","content":"hi"}],"tool_choice":{"type":"auto","disable_parallel_tool_use":true}}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var env *RequestEnvelope
			var err error
			endpoint := router.EndpointOpenAIChat
			if tc.format == FormatAnthropic {
				env, err = ParseAnthropic([]byte(tc.body))
				endpoint = router.EndpointAnthropicMessages
			} else {
				env, err = ParseOpenAI([]byte(tc.body))
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, env.TranslationRequirements(endpoint).ParallelToolLimit)
		})
	}
}
