package translate

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestRewriteForHandover_AnthropicKeepsSystemSummaryAndLatestUserWithoutToolResults(t *testing.T) {
	body := `{"system":"sys","messages":[` +
		`{"role":"user","content":"first"},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"f","input":{}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"r"},{"type":"text","text":"latest"}]}` +
		`]}`
	e, err := ParseAnthropic([]byte(body))
	require.NoError(t, err)

	elided := e.RewriteForHandover("prior work")

	assert.Equal(t, 2, elided)
	assert.Equal(t, "sys", gjson.GetBytes(e.body, "system").String())
	msgs := gjson.GetBytes(e.body, "messages").Array()
	require.Len(t, msgs, 2)
	assert.Equal(t, "assistant", msgs[0].Get("role").String())
	assert.Equal(t, HandoverSummaryTag+"prior work", msgs[0].Get("content.0.text").String())
	assert.Equal(t, "user", msgs[1].Get("role").String())
	blocks := msgs[1].Get("content").Array()
	require.Len(t, blocks, 1)
	assert.Equal(t, "latest", blocks[0].Get("text").String())
}

func TestTrimLastNMessages_NonPositiveKeepsThree(t *testing.T) {
	body := `{"messages":[` +
		`{"role":"user","content":"a"},{"role":"assistant","content":"b"},` +
		`{"role":"user","content":"c"},{"role":"assistant","content":"d"},` +
		`{"role":"user","content":"e"}]}`
	e, err := ParseOpenAI([]byte(body))
	require.NoError(t, err)

	assert.Equal(t, 2, e.TrimLastNMessages(0))
	msgs := gjson.GetBytes(e.body, "messages").Array()
	require.Len(t, msgs, 3)
	assert.Equal(t, "c", msgs[0].Get("content").String())
}

func TestTrimLastNMessages_PullsBackUserTextAndResumesOnAssistant(t *testing.T) {
	body := `{"messages":[` +
		`{"role":"user","content":"task"},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"f","input":{}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"r1"}]},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"t2","name":"f","input":{}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t2","content":"r2"}]}` +
		`]}`
	e, err := ParseAnthropic([]byte(body))
	require.NoError(t, err)

	e.TrimLastNMessages(3)

	msgs := gjson.GetBytes(e.body, "messages").Array()
	require.Len(t, msgs, 3)
	assert.Equal(t, "task", msgs[0].Get("content").String())
	assert.Equal(t, "assistant", msgs[1].Get("role").String())
	assert.Equal(t, "t2", msgs[1].Get("content.0.id").String())
	assert.Equal(t, "t2", msgs[2].Get("content.0.tool_use_id").String())
	for i := 1; i < len(msgs); i++ {
		assert.NotEqual(t, msgs[i-1].Get("role").String(), msgs[i].Get("role").String(), "adjacent messages must alternate roles")
	}
}

func TestTrimLastNMessages_GeminiDropsLeadingOrphanFunctionResponse(t *testing.T) {
	body := `{"contents":[` +
		`{"role":"user","parts":[{"text":"a"}]},` +
		`{"role":"model","parts":[{"functionCall":{"name":"f","args":{}}}]},` +
		`{"role":"user","parts":[{"functionResponse":{"name":"f","response":{}}},{"text":"b"}]},` +
		`{"role":"model","parts":[{"text":"c"}]}` +
		`]}`
	e, err := ParseGemini([]byte(body))
	require.NoError(t, err)

	e.TrimLastNMessages(2)

	contents := gjson.GetBytes(e.body, "contents").Array()
	require.NotEmpty(t, contents)
	for _, part := range contents[0].Get("parts").Array() {
		assert.False(t, part.Get("functionResponse").Exists(), "a functionResponse cannot open the window without its functionCall")
	}
}

func TestRequestsTitleSchema(t *testing.T) {
	cases := []struct {
		name   string
		parse  func([]byte) (*RequestEnvelope, error)
		body   string
		expect bool
	}{
		{"anthropic output_config", ParseAnthropic, `{"output_config":{"format":{"type":"json_schema","schema":{"properties":{"title":{"type":"string"}}}}},"messages":[]}`, true},
		{"anthropic other schema", ParseAnthropic, `{"output_config":{"format":{"type":"json_schema","schema":{"properties":{"name":{"type":"string"}}}}},"messages":[]}`, false},
		{"openai response_format", ParseOpenAI, `{"response_format":{"type":"json_schema","json_schema":{"schema":{"properties":{"title":{"type":"string"}}}}},"messages":[]}`, true},
		{"openai title prompt without schema", ParseOpenAI, `{"messages":[{"role":"user","content":"Generate a title for this conversation"}]}`, false},
		{"gemini", ParseGemini, `{"contents":[]}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, err := tc.parse([]byte(tc.body))
			require.NoError(t, err)
			assert.Equal(t, tc.expect, e.RequestsTitleSchema())
		})
	}
}
