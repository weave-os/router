package translate

import (
	"testing"

	"weave-os/router/internal/router"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestToolDescriptorsPreserveExecutionFacts(t *testing.T) {
	env, err := ParseAnthropic([]byte(`{
		"tools":[
			{"name":"WebSearch","type":"custom","input_schema":{"type":"object"}},
			{"name":"web_search","type":"web_search_20260318"}
		]
	}`))
	require.NoError(t, err)

	assert.Equal(t, []router.ToolDescriptor{
		{Name: "WebSearch", Type: "custom", ServerExecuted: false},
		{Name: "web_search", Type: "web_search_20260318", ServerExecuted: true},
	}, env.ToolDescriptors())
}

func TestNativeServerToolsRecognizeHostedToolsBySourceShape(t *testing.T) {
	openAI, err := ParseOpenAI([]byte(`{
		"tools":[
			{"type":"function","function":{"name":"web_search"}},
			{"type":"web_search_preview"},
			{"type":"computer_use_preview"}
		]
	}`))
	require.NoError(t, err)
	assert.Equal(t, []NativeServerTool{
		{Name: "web_search_preview", Type: "web_search_preview"},
		{Name: "computer_use_preview", Type: "computer_use_preview"},
	}, openAI.NativeServerTools())

	gemini, err := ParseGemini([]byte(`{"tools":[{"googleSearch":{}}]}`))
	require.NoError(t, err)
	assert.Equal(t, []NativeServerTool{{Name: "googleSearch", Type: "googleSearch"}}, gemini.NativeServerTools())
}

func TestAdvisorToolModel(t *testing.T) {
	withAdvisor, err := ParseAnthropic([]byte(`{"tools":[{"name":"Bash","input_schema":{"type":"object"}},{"type":"advisor_20260301","name":"advisor","model":"claude-opus-5-5"}]}`))
	require.NoError(t, err)
	assert.Equal(t, "claude-opus-5-5", withAdvisor.AdvisorToolModel())

	withoutAdvisor, err := ParseAnthropic([]byte(`{"tools":[{"name":"advisor","input_schema":{"type":"object"}}]}`))
	require.NoError(t, err)
	assert.Empty(t, withoutAdvisor.AdvisorToolModel())

	openAI, err := ParseOpenAI([]byte(`{"tools":[{"type":"advisor_20260301","model":"claude-opus-5-5"}]}`))
	require.NoError(t, err)
	assert.Empty(t, openAI.AdvisorToolModel())
}
