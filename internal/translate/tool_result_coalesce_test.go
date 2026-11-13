package translate

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const coalesceAssistantCallA = `{"role":"assistant","tool_calls":[{"id":"a","type":"function","function":{"name":"exec","arguments":"{}"}}]}`

func TestCoalesceDuplicateToolResults_OpenAI(t *testing.T) {
	for _, tc := range []struct {
		name         string
		messages     string
		wantRemoved  int
		wantMessages string
	}{
		{
			name: "adjacent string duplicates",
			messages: `[{"role":"user","content":"go"},` + coalesceAssistantCallA + `,` +
				`{"role":"tool","tool_call_id":"a","content":"final"},` +
				`{"role":"tool","tool_call_id":"a","content":"progress 1"},` +
				`{"role":"tool","tool_call_id":"a","content":"progress 2"}]`,
			wantRemoved: 2,
			wantMessages: `[{"role":"user","content":"go"},` + coalesceAssistantCallA + `,` +
				`{"role":"tool","tool_call_id":"a","content":"final\n\nprogress 1\n\nprogress 2"}]`,
		},
		{
			name: "non-adjacent duplicate keeps the first position",
			messages: `[` + coalesceAssistantCallA + `,` +
				`{"role":"tool","tool_call_id":"a","content":"first"},` +
				`{"role":"user","content":"between"},` +
				`{"role":"tool","tool_call_id":"a","content":"later"}]`,
			wantRemoved: 1,
			wantMessages: `[` + coalesceAssistantCallA + `,` +
				`{"role":"tool","tool_call_id":"a","content":"first\n\nlater"},` +
				`{"role":"user","content":"between"}]`,
		},
		{
			name: "mixed string and array content becomes a part array",
			messages: `[` + coalesceAssistantCallA + `,` +
				`{"role":"tool","tool_call_id":"a","content":"text"},` +
				`{"role":"tool","tool_call_id":"a","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}}]}]`,
			wantRemoved: 1,
			wantMessages: `[` + coalesceAssistantCallA + `,` +
				`{"role":"tool","tool_call_id":"a","content":[{"type":"text","text":"text"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}}]}]`,
		},
		{
			name: "two ids each duplicated",
			messages: `[{"role":"assistant","tool_calls":[{"id":"a","type":"function","function":{"name":"exec","arguments":"{}"}},{"id":"b","type":"function","function":{"name":"exec","arguments":"{}"}}]},` +
				`{"role":"tool","tool_call_id":"a","content":"a1"},` +
				`{"role":"tool","tool_call_id":"b","content":"b1"},` +
				`{"role":"tool","tool_call_id":"a","content":"a2"},` +
				`{"role":"tool","tool_call_id":"b","content":"b2"}]`,
			wantRemoved: 2,
			wantMessages: `[{"role":"assistant","tool_calls":[{"id":"a","type":"function","function":{"name":"exec","arguments":"{}"}},{"id":"b","type":"function","function":{"name":"exec","arguments":"{}"}}]},` +
				`{"role":"tool","tool_call_id":"a","content":"a1\n\na2"},` +
				`{"role":"tool","tool_call_id":"b","content":"b1\n\nb2"}]`,
		},
		{
			name: "a reused id merges only within each call",
			messages: `[` + coalesceAssistantCallA + `,` +
				`{"role":"tool","tool_call_id":"a","content":"first call"},` +
				`{"role":"tool","tool_call_id":"a","content":"first call progress"},` +
				coalesceAssistantCallA + `,` +
				`{"role":"tool","tool_call_id":"a","content":"second call"},` +
				`{"role":"tool","tool_call_id":"a","content":"second call progress"}]`,
			wantRemoved: 2,
			wantMessages: `[` + coalesceAssistantCallA + `,` +
				`{"role":"tool","tool_call_id":"a","content":"first call\n\nfirst call progress"},` +
				coalesceAssistantCallA + `,` +
				`{"role":"tool","tool_call_id":"a","content":"second call\n\nsecond call progress"}]`,
		},
		{
			name: "empty-string duplicate adds no separator",
			messages: `[` + coalesceAssistantCallA + `,` +
				`{"role":"tool","tool_call_id":"a","content":"only"},` +
				`{"role":"tool","tool_call_id":"a","content":""}]`,
			wantRemoved: 1,
			wantMessages: `[` + coalesceAssistantCallA + `,` +
				`{"role":"tool","tool_call_id":"a","content":"only"}]`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := ParseOpenAI([]byte(`{"model":"m","messages":` + tc.messages + `}`))
			require.NoError(t, err)

			assert.Equal(t, tc.wantRemoved, e.CoalesceDuplicateToolResults())
			assert.JSONEq(t, tc.wantMessages, gjson.GetBytes(e.body, "messages").Raw)
		})
	}
}

func TestCoalesceDuplicateToolResults_OpenAI_CleanHistoryIsByteIdentical(t *testing.T) {
	body := `{"model":"m", "messages":[{"role":"user","content":"go"},` + coalesceAssistantCallA + `,` +
		`{"role":"tool","tool_call_id":"a","content":"done"},{"role":"tool","content":"no id"},{"role":"tool","content":"no id"}]}`
	e, err := ParseOpenAI([]byte(body))
	require.NoError(t, err)

	assert.Zero(t, e.CoalesceDuplicateToolResults())
	assert.Equal(t, body, string(e.body))
}

func TestCoalesceDuplicateToolResults_OpenAI_SeparateCallsReusingAnIDAreUntouched(t *testing.T) {
	body := `{"model":"m","messages":[` + coalesceAssistantCallA + `,{"role":"tool","tool_call_id":"a","content":"one"},` +
		coalesceAssistantCallA + `,{"role":"tool","tool_call_id":"a","content":"two"}]}`
	e, err := ParseOpenAI([]byte(body))
	require.NoError(t, err)

	assert.Zero(t, e.CoalesceDuplicateToolResults())
	assert.Equal(t, body, string(e.body))
}

const coalesceAnthropicCallA = `{"role":"assistant","content":[{"type":"tool_use","id":"a","name":"exec","input":{}}]}`

func TestCoalesceDuplicateToolResults_Anthropic(t *testing.T) {
	for _, tc := range []struct {
		name         string
		messages     string
		wantRemoved  int
		wantMessages string
	}{
		{
			name: "duplicates in one user message",
			messages: `[` + coalesceAnthropicCallA + `,{"role":"user","content":[` +
				`{"type":"tool_result","tool_use_id":"a","content":"final"},` +
				`{"type":"tool_result","tool_use_id":"a","content":"progress"},` +
				`{"type":"text","text":"next"}]}]`,
			wantRemoved: 1,
			wantMessages: `[` + coalesceAnthropicCallA + `,{"role":"user","content":[` +
				`{"type":"tool_result","tool_use_id":"a","content":"final\n\nprogress"},` +
				`{"type":"text","text":"next"}]}]`,
		},
		{
			name: "duplicate in a later message drops the emptied message",
			messages: `[` + coalesceAnthropicCallA + `,` +
				`{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":[{"type":"text","text":"final"}]}]},` +
				`{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"progress"}]}]`,
			wantRemoved: 1,
			wantMessages: `[` + coalesceAnthropicCallA + `,` +
				`{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":[{"type":"text","text":"final"},{"type":"text","text":"progress"}]}]}]`,
		},
		{
			name: "is_error is set when any merged result errored",
			messages: `[` + coalesceAnthropicCallA + `,{"role":"user","content":[` +
				`{"type":"tool_result","tool_use_id":"a","content":"ok"},` +
				`{"type":"tool_result","tool_use_id":"a","content":"boom","is_error":true}]}]`,
			wantRemoved: 1,
			wantMessages: `[` + coalesceAnthropicCallA + `,{"role":"user","content":[` +
				`{"type":"tool_result","tool_use_id":"a","content":"ok\n\nboom","is_error":true}]}]`,
		},
		{
			name: "cache_control on the surviving block is kept",
			messages: `[` + coalesceAnthropicCallA + `,{"role":"user","content":[` +
				`{"type":"tool_result","tool_use_id":"a","content":"x","cache_control":{"type":"ephemeral","ttl":"1h"}},` +
				`{"type":"tool_result","tool_use_id":"a","content":"y","cache_control":{"type":"ephemeral"}}]}]`,
			wantRemoved: 1,
			wantMessages: `[` + coalesceAnthropicCallA + `,{"role":"user","content":[` +
				`{"type":"tool_result","tool_use_id":"a","content":"x\n\ny","cache_control":{"type":"ephemeral","ttl":"1h"}}]}]`,
		},
		{
			name: "cache_control on a removed block moves to the survivor",
			messages: `[` + coalesceAnthropicCallA + `,{"role":"user","content":[` +
				`{"type":"tool_result","tool_use_id":"a","content":"x"},` +
				`{"type":"tool_result","tool_use_id":"a","content":"y","cache_control":{"type":"ephemeral"}}]}]`,
			wantRemoved: 1,
			wantMessages: `[` + coalesceAnthropicCallA + `,{"role":"user","content":[` +
				`{"type":"tool_result","tool_use_id":"a","content":"x\n\ny","cache_control":{"type":"ephemeral"}}]}]`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := ParseAnthropic([]byte(`{"model":"m","max_tokens":10,"messages":` + tc.messages + `}`))
			require.NoError(t, err)

			assert.Equal(t, tc.wantRemoved, e.CoalesceDuplicateToolResults())
			assert.JSONEq(t, tc.wantMessages, gjson.GetBytes(e.body, "messages").Raw)
		})
	}
}

func TestCoalesceDuplicateToolResults_Anthropic_CleanHistoryIsByteIdentical(t *testing.T) {
	body := `{"model":"m","max_tokens":10, "messages":[{"role":"user","content":"go"},` + coalesceAnthropicCallA + `,` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"done"},{"type":"text","text":"continue"}]}]}`
	e, err := ParseAnthropic([]byte(body))
	require.NoError(t, err)

	assert.Zero(t, e.CoalesceDuplicateToolResults())
	assert.Equal(t, body, string(e.body))
}

func TestCoalesceDuplicateToolResults_Anthropic_SeparateCallsReusingAnIDAreUntouched(t *testing.T) {
	body := `{"model":"m","max_tokens":10,"messages":[` + coalesceAnthropicCallA + `,` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"one"}]},` + coalesceAnthropicCallA + `,` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"two"}]}]}`
	e, err := ParseAnthropic([]byte(body))
	require.NoError(t, err)

	assert.Zero(t, e.CoalesceDuplicateToolResults())
	assert.Equal(t, body, string(e.body))
}

func TestCoalesceDuplicateToolResults_Anthropic_CacheMarkerDoesNotJumpAnIntervening1hMarker(t *testing.T) {
	body := `{"model":"claude-opus-5","max_tokens":10,"messages":[` + coalesceAnthropicCallA + `,{"role":"user","content":[` +
		`{"type":"tool_result","tool_use_id":"a","content":"final"},` +
		`{"type":"text","text":"note","cache_control":{"type":"ephemeral","ttl":"1h"}},` +
		`{"type":"tool_result","tool_use_id":"a","content":"progress","cache_control":{"type":"ephemeral","ttl":"5m"}}]}]}`
	e, err := ParseAnthropic([]byte(body))
	require.NoError(t, err)

	assert.Equal(t, 1, e.CoalesceDuplicateToolResults())
	assert.JSONEq(t, `[`+coalesceAnthropicCallA+`,{"role":"user","content":[`+
		`{"type":"tool_result","tool_use_id":"a","content":"final\n\nprogress"},`+
		`{"type":"text","text":"note","cache_control":{"type":"ephemeral","ttl":"1h"}}]}]`,
		gjson.GetBytes(e.body, "messages").Raw)
	_, err = e.PrepareAnthropic(nil, EmitOptions{TargetModel: "claude-opus-5"})
	require.NoError(t, err, "coalescing must not reverse breakpoint TTL order")
}

func TestCoalesceDuplicateToolResults_Gemini_NoOp(t *testing.T) {
	body := `{"contents":[{"role":"user","parts":[{"functionResponse":{"name":"f","response":{}}},{"functionResponse":{"name":"f","response":{}}}]}]}`
	e, err := ParseGemini([]byte(body))
	require.NoError(t, err)

	assert.Zero(t, e.CoalesceDuplicateToolResults())
	assert.Equal(t, body, string(e.body))
}
