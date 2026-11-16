package translate_test

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"weave-os/router/internal/router"
	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestPrepareOpenAIResponses_ReplaysReasoningCarriedOnToolUseID(t *testing.T) {
	sig := openAIReasoningTestSignature(t, "rs_prev", "enc_prev")
	toolID := "toolu_1__openai_reasoning__" + base64.RawURLEncoding.EncodeToString([]byte(sig))
	body := []byte(`{
		"model":"claude-opus-4-8","max_tokens":1024,
		"thinking":{"type":"enabled","budget_tokens":8192},
		"messages":[
			{"role":"user","content":"continue"},
			{"role":"assistant","content":[
				{"type":"tool_use","id":` + strconv.Quote(toolID) + `,"name":"Read","input":{"file_path":"main.go"}}
			]}
		]
	}`)
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	prep, err := env.PrepareOpenAIResponses(http.Header{}, translate.EmitOptions{TargetModel: "gpt-5.5", Capabilities: router.Lookup("gpt-5.5"), ForceReasoningEffort: "high", ReasoningReplayScope: "scope_a"})
	require.NoError(t, err)

	var out map[string]any
	require.NoError(t, json.Unmarshal(prep.Body, &out))
	input, _ := out["input"].([]any)
	require.Len(t, input, 3, "a dropped thinking block is replayed from the tool_use id")
	reasoning, _ := input[1].(map[string]any)
	assert.Equal(t, "reasoning", reasoning["type"])
	assert.Equal(t, "rs_prev", reasoning["id"])
	toolCall, _ := input[2].(map[string]any)
	assert.Equal(t, "toolu_1", toolCall["call_id"])
}

func TestPrepareAnthropic_FromOpenAI_ArraySystemContentBecomesTextBlocks(t *testing.T) {
	env, err := translate.ParseOpenAI([]byte(`{"model":"gpt-4o","messages":[
		{"role":"system","content":[{"type":"text","text":"be brief"}]},
		{"role":"user","content":"hi"}]}`))
	require.NoError(t, err)
	prep, err := env.PrepareAnthropic(http.Header{}, translate.EmitOptions{TargetModel: "claude-opus-4-8"})
	require.NoError(t, err)
	assert.Equal(t, "be brief", gjson.GetBytes(prep.Body, "system.0.text").String(), string(prep.Body))
}

func TestPrepareAnthropic_SystemOnlyBlockStaysInSystemRoleMessage(t *testing.T) {
	out := prepareAnthropicBody(t, []byte(`{
		"model": "claude-opus-4-8",
		"max_tokens": 1024,
		"messages": [
			{"role": "system", "content": [{"type": "tool_addition", "toolset_id": "skills"}]},
			{"role": "user", "content": [{"type": "text", "text": "hello"}]}
		]
	}`))
	msgs := gjson.GetBytes(out, "messages").Array()
	require.Len(t, msgs, 2, string(out))
	assert.Equal(t, "user", msgs[0].Get("role").String(), "system-only blocks move after the conversation")
	assert.Equal(t, "system", msgs[1].Get("role").String())
	assert.Equal(t, "tool_addition", msgs[1].Get("content.0.type").String())
}

func TestPrepareGemini_AllOfConflictKeepsSiblingConstraint(t *testing.T) {
	env, err := translate.ParseAnthropic([]byte(`{
		"messages": [{"role":"user","content":"hi"}],
		"tools": [{"name":"NotifyTeammate","input_schema":{"type":"object","properties":{
			"to":{"type":"string","allOf":[{"type":"integer"}]}}}}]
	}`))
	require.NoError(t, err)
	prep, err := env.PrepareGemini(http.Header{}, translate.EmitOptions{TargetModel: "gemini-3.1-pro-preview"})
	require.NoError(t, err)
	params := gjson.GetBytes(prep.Body, "tools.0.functionDeclarations.0.parameters")
	require.True(t, params.Exists(), string(prep.Body))
	assert.Equal(t, "string", params.Get("properties.to.type").String(), params.Raw)
}
