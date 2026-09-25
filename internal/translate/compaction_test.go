package translate

import (
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// anthropicToolTurn builds a user message carrying a single tool_result whose
// body is the given text, for the given tool_use_id.
func anthropicToolResultMsg(id, text string) string {
	return `{"role":"user","content":[{"type":"tool_result","tool_use_id":"` + id + `","content":"` + text + `"}]}`
}

func anthropicAssistantToolUse(id string) string {
	return `{"role":"assistant","content":[{"type":"tool_use","id":"` + id + `","name":"read","input":{}}]}`
}

func TestClearOldToolResults_Anthropic(t *testing.T) {
	body := `{"model":"claude-opus-4-8","system":"sys","messages":[` +
		anthropicAssistantToolUse("t1") + `,` + anthropicToolResultMsg("t1", "OLD_ONE") + `,` +
		anthropicAssistantToolUse("t2") + `,` + anthropicToolResultMsg("t2", "OLD_TWO") + `,` +
		anthropicAssistantToolUse("t3") + `,` + anthropicToolResultMsg("t3", "RECENT") +
		`]}`
	e, err := ParseAnthropic([]byte(body))
	require.NoError(t, err)

	cleared := e.ClearOldToolResults(1)
	assert.Equal(t, 2, cleared, "two of three tool results should be cleared")

	got := string(e.body)
	assert.Contains(t, got, ClearedToolResultPlaceholder)
	assert.NotContains(t, got, "OLD_ONE")
	assert.NotContains(t, got, "OLD_TWO")
	assert.Contains(t, got, "RECENT", "the most recent tool result must be preserved verbatim")

	// Structure intact: still 6 messages, tool_use blocks untouched.
	msgs := gjson.GetBytes(e.body, "messages").Array()
	assert.Len(t, msgs, 6)
	assert.Equal(t, 2, strings.Count(got, ClearedToolResultPlaceholder))
}

func TestClearOldToolResults_NoOpWhenWithinKeep(t *testing.T) {
	body := `{"messages":[` + anthropicAssistantToolUse("t1") + `,` + anthropicToolResultMsg("t1", "ONLY") + `]}`
	e, err := ParseAnthropic([]byte(body))
	require.NoError(t, err)
	before := string(e.body)
	assert.Equal(t, 0, e.ClearOldToolResults(5))
	assert.Equal(t, before, string(e.body), "body must be unchanged when nothing is cleared")
}

func TestClearOldToolResults_OpenAI(t *testing.T) {
	body := `{"messages":[` +
		`{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]},` +
		`{"role":"tool","tool_call_id":"c1","content":"OLD_OUTPUT"},` +
		`{"role":"assistant","tool_calls":[{"id":"c2","type":"function","function":{"name":"f","arguments":"{}"}}]},` +
		`{"role":"tool","tool_call_id":"c2","content":"RECENT_OUTPUT"}` +
		`]}`
	e, err := ParseOpenAI([]byte(body))
	require.NoError(t, err)

	assert.Equal(t, 1, e.ClearOldToolResults(1))
	got := string(e.body)
	assert.NotContains(t, got, "OLD_OUTPUT")
	assert.Contains(t, got, "RECENT_OUTPUT")
	assert.Contains(t, got, ClearedToolResultPlaceholder)
}

func TestCompactionChunk_PreservesToolPairsAndSignatures(t *testing.T) {
	tests := []struct {
		name, body string
		parse      func([]byte) (*RequestEnvelope, error)
		field      string
		signature  string
	}{
		{
			name: "anthropic", parse: ParseAnthropic, field: "messages", signature: "signed-thinking",
			body: `{"messages":[{"role":"user","content":"start"},{"role":"assistant","content":[{"type":"thinking","thinking":"plan","signature":"signed-thinking"},{"type":"tool_use","id":"t1","name":"read","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"result"}]},{"role":"assistant","content":"next"},{"role":"user","content":"continue"}]}`,
		},
		{
			name: "openai", parse: ParseOpenAI, field: "messages", signature: "c1",
			body: `{"messages":[{"role":"user","content":"start"},{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"read","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c1","content":"result"},{"role":"assistant","content":"next"},{"role":"user","content":"continue"}]}`,
		},
		{
			name: "gemini", parse: ParseGemini, field: "contents", signature: "signed-thought",
			body: `{"contents":[{"role":"user","parts":[{"text":"start"}]},{"role":"model","parts":[{"functionCall":{"name":"read","args":{}},"thoughtSignature":"signed-thought"}]},{"role":"user","parts":[{"functionResponse":{"name":"read","response":{"result":"result"}}}]},{"role":"model","parts":[{"text":"next"}]},{"role":"user","parts":[{"text":"continue"}]}]}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, err := tt.parse([]byte(tt.body))
			require.NoError(t, err)
			assert.Equal(t, []int{0, 3, 4, 5}, env.CompactionBoundaries())
			first, err := env.CompactionChunk(0, 3, "")
			require.NoError(t, err)
			firstMessages := gjson.GetBytes(first.body, tt.field).Array()
			assert.Len(t, firstMessages, 3)
			assert.Contains(t, string(first.body), tt.signature)
			second, err := env.CompactionChunk(3, 5, "remember result")
			require.NoError(t, err)
			assert.Contains(t, string(second.body), "remember result")
			assert.NotContains(t, string(second.body), tt.signature)
			assert.NotContains(t, string(second.body), "functionResponse")
			assert.NotContains(t, string(second.body), "tool_result")
			assert.NotContains(t, string(second.body), `"role":"tool"`)
			_, err = env.CompactionChunk(1, 3, "")
			require.ErrorIs(t, err, ErrUnsafeCompactionBoundary)
		})
	}
}

func TestCompactionChunk_RejectsMidConversationInstructions(t *testing.T) {
	body := []byte(`{"messages":[{"role":"system","content":"initial"},{"role":"user","content":"first"},{"role":"assistant","content":"answer"},{"role":"developer","content":"new constraint"},{"role":"user","content":"second"}]}`)
	env, err := ParseOpenAI(body)
	require.NoError(t, err)
	assert.False(t, env.SupportsHistoryCompaction())
	_, err = env.CompactionChunk(3, 5, "previous answer")
	require.ErrorIs(t, err, ErrUnsafeCompactionBoundary)
}

func TestRewriteForCompaction_Anthropic_KeepsSummaryAndRecent(t *testing.T) {
	// 8 alternating messages; keep recent 3 turns.
	var b strings.Builder
	b.WriteString(`{"model":"claude-opus-4-8","system":"sys","messages":[`)
	parts := []string{
		`{"role":"user","content":"u1 old"}`,
		`{"role":"assistant","content":"a1 old"}`,
		`{"role":"user","content":"u2 old"}`,
		`{"role":"assistant","content":"a2 old"}`,
		`{"role":"user","content":"u3 keep"}`,
		`{"role":"assistant","content":"a3 keep"}`,
		`{"role":"user","content":"u4 latest"}`,
	}
	b.WriteString(strings.Join(parts, ","))
	b.WriteString(`]}`)
	e, err := ParseAnthropic([]byte(b.String()))
	require.NoError(t, err)

	elided := e.RewriteForCompaction("THE SUMMARY", 3)
	assert.Positive(t, elided)

	msgs := gjson.GetBytes(e.body, "messages").Array()
	require.GreaterOrEqual(t, len(msgs), 2)
	// First message is the tagged assistant summary.
	assert.Equal(t, "assistant", msgs[0].Get("role").String())
	assert.Contains(t, msgs[0].Get("content").Array()[0].Get("text").String(), HandoverSummaryTag)
	assert.Contains(t, msgs[0].Get("content").Array()[0].Get("text").String(), "THE SUMMARY")
	// The message after the summary must be a user turn (valid alternation).
	assert.Equal(t, "user", msgs[1].Get("role").String())
	// Latest user turn preserved; oldest elided.
	got := string(e.body)
	assert.Contains(t, got, "u4 latest")
	assert.NotContains(t, got, "u1 old")
	// System field untouched.
	assert.Equal(t, "sys", gjson.GetBytes(e.body, "system").String())
}

func TestRewriteForCompaction_Anthropic_StripsOrphanedToolResult(t *testing.T) {
	// The recent window begins on a tool_result whose tool_use is elided.
	body := `{"messages":[` +
		anthropicAssistantToolUse("t1") + `,` +
		anthropicToolResultMsg("t1", "orphan-after-trim") + `,` +
		`{"role":"assistant","content":"a"},` +
		`{"role":"user","content":"latest"}` +
		`]}`
	e, err := ParseAnthropic([]byte(body))
	require.NoError(t, err)

	e.RewriteForCompaction("S", 2)
	got := string(e.body)
	// The orphaned tool_result (its tool_use t1 was elided) must not survive.
	assert.NotContains(t, got, "orphan-after-trim")
	assert.Contains(t, got, "latest")
}

func TestRewriteForCompaction_OpenAI_PreservesSystem(t *testing.T) {
	body := `{"messages":[` +
		`{"role":"system","content":"SYS"},` +
		`{"role":"user","content":"u1 old"},` +
		`{"role":"assistant","content":"a1"},` +
		`{"role":"user","content":"u2 latest"}` +
		`]}`
	e, err := ParseOpenAI([]byte(body))
	require.NoError(t, err)

	e.RewriteForCompaction("SUM", 1)
	msgs := gjson.GetBytes(e.body, "messages").Array()
	require.NotEmpty(t, msgs)
	assert.Equal(t, "system", msgs[0].Get("role").String())
	assert.Equal(t, "SYS", msgs[0].Get("content").String())
	got := string(e.body)
	assert.Contains(t, got, "u2 latest")
	assert.NotContains(t, got, "u1 old")
	assert.Contains(t, got, "SUM")
}

func TestRewriteForCompaction_Gemini_KeepsSummaryModelTurn(t *testing.T) {
	body := `{"contents":[` +
		`{"role":"user","parts":[{"text":"u1 old"}]},` +
		`{"role":"model","parts":[{"text":"m1"}]},` +
		`{"role":"user","parts":[{"text":"u2 latest"}]}` +
		`]}`
	e, err := ParseGemini([]byte(body))
	require.NoError(t, err)

	e.RewriteForCompaction("GSUM", 1)
	contents := gjson.GetBytes(e.body, "contents").Array()
	require.GreaterOrEqual(t, len(contents), 2)
	assert.Equal(t, "model", contents[0].Get("role").String())
	assert.Contains(t, contents[0].Get("parts").Array()[0].Get("text").String(), "GSUM")
	assert.Equal(t, "user", contents[1].Get("role").String())
	assert.Contains(t, string(e.body), "u2 latest")
}

func TestRewriteForCompaction_Gemini_StripsOrphanedFunctionResponse(t *testing.T) {
	body := `{"contents":[` +
		`{"role":"user","parts":[{"text":"u1 old"}]},` +
		`{"role":"model","parts":[{"functionCall":{"name":"read","args":{}}}]},` +
		`{"role":"user","parts":[{"functionResponse":{"name":"read","response":{"r":1}}},{"text":"also text"}]},` +
		`{"role":"model","parts":[{"functionCall":{"name":"ls","args":{}}}]},` +
		`{"role":"user","parts":[{"functionResponse":{"name":"ls","response":{"r":2}}}]},` +
		`{"role":"model","parts":[{"text":"m3"}]},` +
		`{"role":"user","parts":[{"text":"u4 latest"}]}` +
		`]}`
	e, err := ParseGemini([]byte(body))
	require.NoError(t, err)

	// Window starts at the user turn carrying the "read" response; its
	// functionCall is elided, so that part must go while "ls" stays paired.
	e.RewriteForCompaction("GSUM", 5)
	contents := gjson.GetBytes(e.body, "contents").Array()
	require.Len(t, contents, 6)
	assert.Equal(t, "model", contents[0].Get("role").String())
	head := contents[1]
	assert.Equal(t, "user", head.Get("role").String())
	assert.False(t, head.Get(`parts.#(functionResponse)`).Exists(), "orphaned functionResponse must be stripped")
	assert.Equal(t, "also text", head.Get("parts.0.text").String())
	assert.True(t, contents[3].Get(`parts.#(functionResponse)`).Exists(), "paired functionResponse kept")
}

func TestTrimLastNMessages_Gemini_DropsHeadLeftWithOnlyOrphanResponse(t *testing.T) {
	body := `{"contents":[` +
		`{"role":"user","parts":[{"text":"u1"}]},` +
		`{"role":"model","parts":[{"functionCall":{"name":"ls","args":{}}}]},` +
		`{"role":"user","parts":[{"functionResponse":{"name":"ls","response":{"r":2}}}]},` +
		`{"role":"model","parts":[{"text":"m2"}]},` +
		`{"role":"user","parts":[{"text":"u3 latest"}]}` +
		`]}`
	e, err := ParseGemini([]byte(body))
	require.NoError(t, err)

	elided := e.TrimLastNMessages(3)
	assert.Equal(t, 3, elided)
	contents := gjson.GetBytes(e.body, "contents").Array()
	require.Len(t, contents, 2)
	assert.Equal(t, "m2", contents[0].Get("parts.0.text").String())
	assert.NotContains(t, string(e.body), "functionResponse")
}

func TestCompactionPreservesUserTextBoundaryAcrossToolLoop(t *testing.T) {
	const toolTurns = 6

	anthropicMessages := []string{`{"role":"user","content":"actual request"}`}
	openAIMessages := []string{`{"role":"user","content":"actual request"}`}
	geminiContents := []string{`{"role":"user","parts":[{"text":"actual request"}]}`}
	for i := 0; i < toolTurns; i++ {
		toolUseID := "tool-" + strconv.Itoa(i)
		anthropicMessages = append(anthropicMessages,
			anthropicAssistantToolUse(toolUseID),
			anthropicToolResultMsg(toolUseID, "tool output"),
		)
		openAIMessages = append(openAIMessages,
			`{"role":"assistant","tool_calls":[{"id":"`+toolUseID+`","type":"function","function":{"name":"read","arguments":"{}"}}]}`,
			`{"role":"tool","tool_call_id":"`+toolUseID+`","content":"tool output"}`,
		)
		geminiContents = append(geminiContents,
			`{"role":"model","parts":[{"functionCall":{"name":"`+toolUseID+`","args":{}}}]}`,
			`{"role":"user","parts":[{"functionResponse":{"name":"`+toolUseID+`","response":{"result":"tool output"}}}]}`,
		)
	}
	// Claude Code can append an injected reminder beside a tool result. The
	// reminder is not a routing boundary, but it keeps the user message alive
	// if orphan cleanup removes only the tool_result block.
	anthropicMessages[2] = `{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool-0","content":"tool output"},{"type":"text","text":"<system-reminder>injected</system-reminder>"}]}`

	testCases := []struct {
		name              string
		parse             func([]byte) (*RequestEnvelope, error)
		body              string
		messageArrayPath  string
		wireAssistantRole string
	}{
		{
			name:              "anthropic",
			parse:             ParseAnthropic,
			body:              `{"messages":[` + strings.Join(anthropicMessages, ",") + `]}`,
			messageArrayPath:  "messages",
			wireAssistantRole: "assistant",
		},
		{
			name:              "openai",
			parse:             ParseOpenAI,
			body:              `{"messages":[` + strings.Join(openAIMessages, ",") + `]}`,
			messageArrayPath:  "messages",
			wireAssistantRole: "assistant",
		},
		{
			name:              "gemini",
			parse:             ParseGemini,
			body:              `{"contents":[` + strings.Join(geminiContents, ",") + `]}`,
			messageArrayPath:  "contents",
			wireAssistantRole: "model",
		},
	}

	assertUserTextBoundary := func(t *testing.T, envelope *RequestEnvelope) {
		t.Helper()
		for _, message := range envelope.ConversationMessages() {
			if message.Role == "user" && message.Text == "actual request" {
				return
			}
		}
		assert.Fail(t, "text-bearing user boundary was elided")
	}

	for _, testCase := range testCases {
		t.Run(testCase.name+"/emergency trim", func(t *testing.T) {
			envelope, err := testCase.parse([]byte(testCase.body))
			require.NoError(t, err)

			envelope.TrimLastNMessages(12)
			assertUserTextBoundary(t, envelope)
			assert.NotContains(t, string(envelope.body), "tool-0", "orphaned first tool result must be removed")
			wireMessages := gjson.GetBytes(envelope.body, testCase.messageArrayPath).Array()
			require.GreaterOrEqual(t, len(wireMessages), 2)
			assert.Equal(t, "user", wireMessages[0].Get("role").String())
			assert.Equal(t, testCase.wireAssistantRole, wireMessages[1].Get("role").String())
		})

		t.Run(testCase.name+"/summary rewrite", func(t *testing.T) {
			envelope, err := testCase.parse([]byte(testCase.body))
			require.NoError(t, err)

			envelope.RewriteForCompaction("summary", 12)
			assertUserTextBoundary(t, envelope)
		})
	}
}
