package translate

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestSanitizeOrphanedToolCalls_OpenAI_HasOrphan(t *testing.T) {
	body := `{"messages":[` +
		`{"role":"user","content":"go"},` +
		`{"role":"assistant","tool_calls":[` +
		`{"id":"a","type":"function","function":{"name":"f","arguments":"{}"}},` +
		`{"id":"b","type":"function","function":{"name":"g","arguments":"{}"}}` +
		`]},` +
		`{"role":"tool","tool_call_id":"a","content":"result a"}` +
		`]}`
	e, err := ParseOpenAI([]byte(body))
	require.NoError(t, err)

	sanitized := e.SanitizeOrphanedToolCalls()
	assert.Equal(t, 1, sanitized, "the one orphaned tool_call (id b) should be stripped")

	msgs := gjson.GetBytes(e.body, "messages").Array()
	require.Len(t, msgs, 3)
	assistant := msgs[1]
	tc := assistant.Get("tool_calls").Array()
	require.Len(t, tc, 1)
	assert.Equal(t, "a", tc[0].Get("id").String())
}

func TestSanitizeOrphanedToolCalls_OpenAI_AllOrphaned(t *testing.T) {
	body := `{"messages":[` +
		`{"role":"user","content":"go"},` +
		`{"role":"assistant","tool_calls":[` +
		`{"id":"a","type":"function","function":{"name":"f","arguments":"{}"}},` +
		`{"id":"b","type":"function","function":{"name":"g","arguments":"{}"}}` +
		`]}` +
		`]}`
	e, err := ParseOpenAI([]byte(body))
	require.NoError(t, err)

	sanitized := e.SanitizeOrphanedToolCalls()
	assert.Equal(t, 2, sanitized, "both orphaned tool_calls should be stripped")

	msgs := gjson.GetBytes(e.body, "messages").Array()
	require.Len(t, msgs, 2, "the assistant message itself is preserved, just without tool_calls")
	assistant := msgs[1]
	assert.False(t, assistant.Get("tool_calls").Exists(), "tool_calls key must be removed entirely, not left as []")
}

func TestSanitizeOrphanedToolCalls_OpenAI_NoOrphans(t *testing.T) {
	body := `{"messages":[` +
		`{"role":"user","content":"go"},` +
		`{"role":"assistant","tool_calls":[{"id":"a","type":"function","function":{"name":"f","arguments":"{}"}}]},` +
		`{"role":"tool","tool_call_id":"a","content":"result a"}` +
		`]}`
	e, err := ParseOpenAI([]byte(body))
	require.NoError(t, err)
	before := string(e.body)

	sanitized := e.SanitizeOrphanedToolCalls()
	assert.Equal(t, 0, sanitized)
	assert.Equal(t, before, string(e.body), "body must be byte-identical when nothing is orphaned")
}

func TestSanitizeOrphanedToolCalls_OpenAI_TrailingUnansweredCallIsStripped(t *testing.T) {
	// A well-formed client request never ends on an assistant tool_calls
	// message with nothing after it — the client always appends the tool
	// result (or a new user message) before calling the router again. A
	// trailing unanswered tool_calls is therefore always anomalous (the
	// mid-stream-failure artifact this fix targets), not a legitimate
	// in-flight turn, so it must be stripped unconditionally.
	body := `{"messages":[` +
		`{"role":"user","content":"go"},` +
		`{"role":"assistant","tool_calls":[{"id":"a","type":"function","function":{"name":"f","arguments":"{}"}}]}` +
		`]}`
	e, err := ParseOpenAI([]byte(body))
	require.NoError(t, err)

	sanitized := e.SanitizeOrphanedToolCalls()
	assert.Equal(t, 1, sanitized)

	msgs := gjson.GetBytes(e.body, "messages").Array()
	require.Len(t, msgs, 2)
	assert.False(t, msgs[1].Get("tool_calls").Exists())
}

func TestSanitizeOrphanedToolCalls_OpenAI_OrphanedResultAlsoStripped(t *testing.T) {
	// role:"tool" with a tool_call_id that matches nothing (the reverse
	// direction, already handled by stripOrphanedOpenAIToolMessages) must
	// also be cleaned up by the same pass.
	body := `{"messages":[` +
		`{"role":"user","content":"go"},` +
		`{"role":"assistant","tool_calls":[{"id":"a","type":"function","function":{"name":"f","arguments":"{}"}}]},` +
		`{"role":"tool","tool_call_id":"a","content":"result a"},` +
		`{"role":"tool","tool_call_id":"stale","content":"orphaned result"}` +
		`]}`
	e, err := ParseOpenAI([]byte(body))
	require.NoError(t, err)

	sanitized := e.SanitizeOrphanedToolCalls()
	assert.Equal(t, 1, sanitized)

	msgs := gjson.GetBytes(e.body, "messages").Array()
	require.Len(t, msgs, 3)
	for _, m := range msgs {
		assert.NotEqual(t, "stale", m.Get("tool_call_id").String())
	}
}

func TestSanitizeOrphanedToolCalls_Anthropic_HasOrphan(t *testing.T) {
	body := `{"messages":[` +
		`{"role":"user","content":[{"type":"text","text":"go"}]},` +
		`{"role":"assistant","content":[` +
		`{"type":"tool_use","id":"x","name":"read","input":{}},` +
		`{"type":"text","text":"done"}` +
		`]},` +
		`{"role":"user","content":[{"type":"text","text":"continuing anyway"}]}` +
		`]}`
	e, err := ParseAnthropic([]byte(body))
	require.NoError(t, err)

	sanitized := e.SanitizeOrphanedToolCalls()
	assert.Equal(t, 1, sanitized, "the orphaned tool_use (id x, no matching tool_result) should be stripped")

	msgs := gjson.GetBytes(e.body, "messages").Array()
	require.Len(t, msgs, 3)
	assistant := msgs[1]
	blocks := assistant.Get("content").Array()
	require.Len(t, blocks, 1, "only the text block should remain")
	assert.Equal(t, "text", blocks[0].Get("type").String())
}

func TestSanitizeOrphanedToolCalls_Anthropic_NoOrphans(t *testing.T) {
	body := `{"messages":[` +
		`{"role":"user","content":[{"type":"text","text":"go"}]},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"x","name":"read","input":{}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"x","content":"ok"}]}` +
		`]}`
	e, err := ParseAnthropic([]byte(body))
	require.NoError(t, err)
	before := string(e.body)

	sanitized := e.SanitizeOrphanedToolCalls()
	assert.Equal(t, 0, sanitized)
	assert.Equal(t, before, string(e.body))
}

func TestSanitizeOrphanedToolCalls_Anthropic_AllBlocksOrphaned(t *testing.T) {
	// Assistant message whose only content is an orphaned tool_use is dropped
	// entirely rather than left as an empty-content message (which upstreams
	// reject just as readily as an orphaned tool_use).
	body := `{"messages":[` +
		`{"role":"user","content":[{"type":"text","text":"go"}]},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"x","name":"read","input":{}}]},` +
		`{"role":"user","content":[{"type":"text","text":"still going"}]}` +
		`]}`
	e, err := ParseAnthropic([]byte(body))
	require.NoError(t, err)

	sanitized := e.SanitizeOrphanedToolCalls()
	assert.Equal(t, 1, sanitized)

	msgs := gjson.GetBytes(e.body, "messages").Array()
	require.Len(t, msgs, 2, "the now-empty assistant message must be dropped")
	assert.Equal(t, "user", msgs[0].Get("role").String())
	assert.Equal(t, "user", msgs[1].Get("role").String())
}

func TestSanitizeOrphanedToolCalls_Gemini_NoOp(t *testing.T) {
	body := `{"contents":[{"role":"user","parts":[{"text":"go"}]}]}`
	e, err := ParseGemini([]byte(body))
	require.NoError(t, err)
	before := string(e.body)

	sanitized := e.SanitizeOrphanedToolCalls()
	assert.Equal(t, 0, sanitized, "Gemini doesn't validate tool-call pairing; sanitization is a no-op")
	assert.Equal(t, before, string(e.body))
}
