package translate_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"weave-os/router/internal/translate"
)

// forceModelAck mirrors the control reply the router appends after a pin, as
// Codex replays it on the next turn (reference line included).
const forceModelAck = "Weave Router: force-model applied: claude-fable-5-1 (anthropic). Use $unforce-model to clear.\n\n[weave-router-command-v1]: <weave-router:eyJ2ZXJzaW9uIjoxLCJpZCI6ImE5MjMzMzU0LTZkNzQtNDFkZC04ZGEzLWQ4NTFiM2U3NjA5YSIsImNvbW1hbmQiOiJmb3JjZV9tb2RlbCIsImRpc3Bvc2l0aW9uIjoiYWNjZXB0ZWQiLCJwaW4iOnsidHJhbnNpdGlvbiI6ImFwcGxpZWQiLCJtb2RlbCI6ImNsYXVkZS1mYWJsZS01LTEiLCJwcm92aWRlciI6ImFudGhyb3BpYyJ9fQ>"

func responsesBody(t *testing.T, items ...any) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{"model": "gpt-5.6-sol", "input": items})
	require.NoError(t, err)
	return body
}

func inputTexts(t *testing.T, body []byte) []string {
	t.Helper()
	var texts []string
	for _, item := range gjson.GetBytes(body, "input").Array() {
		if item.Get("role").Str != "user" {
			continue
		}
		for _, part := range item.Get("content").Array() {
			texts = append(texts, part.Get("text").Str)
		}
	}
	return texts
}

func TestStripHistoricalCommandsFromResponsesInput_CodexRollout(t *testing.T) {
	// Shape captured from a Codex desktop session: a /fm turn, its ack, then
	// the user's real question on the following turn.
	body := responsesBody(t,
		codexUserItem("<environment_context><cwd>/x</cwd></environment_context>"),
		codexUserItem("/fm fable \n"),
		codexAssistantItem(forceModelAck),
		codexUserItem("why does the agent keep reading my fm command?"),
	)
	out, err := translate.StripHistoricalCommandsFromResponsesInput(body)
	require.NoError(t, err)
	assert.Equal(t, []string{
		"<environment_context><cwd>/x</cwd></environment_context>",
		"why does the agent keep reading my fm command?",
	}, inputTexts(t, out))
	assert.Equal(t, int64(3), gjson.GetBytes(out, "input.#").Int(), "the ack stays for StripCommandOutcomes to handle")
}

func TestStripHistoricalCommandsFromResponsesInput_SkillAttachmentGoesWithDirective(t *testing.T) {
	body := responsesBody(t,
		codexUserItem("$fm astra"),
		codexUserItem(codexSkillBlob),
		codexAssistantItem(forceModelAck),
		codexUserItem("now fix the bug"),
	)
	out, err := translate.StripHistoricalCommandsFromResponsesInput(body)
	require.NoError(t, err)
	assert.Equal(t, []string{"now fix the bug"}, inputTexts(t, out))
}

func TestStripHistoricalCommandsFromResponsesInput_TrailingDirectiveUntouched(t *testing.T) {
	body := responsesBody(t,
		codexUserItem("hello"),
		codexAssistantItem("hi"),
		codexUserItem("$fm astra"),
		codexUserItem(codexSkillBlob),
	)
	out, err := translate.StripHistoricalCommandsFromResponsesInput(body)
	require.NoError(t, err)
	assert.Equal(t, body, out, "the live directive must survive for ExtractForceModelCommand")
}

func TestStripHistoricalCommandsFromResponsesInput_MixedMessageKeepsRemainder(t *testing.T) {
	body := responsesBody(t,
		codexUserItem("/fm astra\nplease refactor the parser"),
		codexAssistantItem("done"),
		codexUserItem("thanks, now the tests"),
	)
	out, err := translate.StripHistoricalCommandsFromResponsesInput(body)
	require.NoError(t, err)
	assert.Equal(t, []string{"please refactor the parser", "thanks, now the tests"}, inputTexts(t, out))
}

func TestStripHistoricalCommandsFromResponsesInput_EveryDirectiveFamily(t *testing.T) {
	for _, directive := range []string{
		"/force-model sonnet", "$fm opus", "/model gpt", "/force-parent astra",
		"/unforce-model", "$ufm", "/rf- too slow", "$router-feedback 👍",
		"/router-session", "$router-models", "/models",
	} {
		t.Run(directive, func(t *testing.T) {
			body := responsesBody(t,
				codexUserItem(directive),
				codexAssistantItem("ack"),
				codexUserItem("next"),
			)
			out, err := translate.StripHistoricalCommandsFromResponsesInput(body)
			require.NoError(t, err)
			assert.Equal(t, []string{"next"}, inputTexts(t, out))
		})
	}
}

func TestStripHistoricalCommandsFromResponsesInput_ProseMentionIsNotADirective(t *testing.T) {
	body := responsesBody(t,
		codexUserItem("I typed /fm fable earlier and it worked"),
		codexAssistantItem("great"),
		codexUserItem("next"),
	)
	out, err := translate.StripHistoricalCommandsFromResponsesInput(body)
	require.NoError(t, err)
	assert.Equal(t, body, out)
}

func TestStripHistoricalCommandsFromResponsesInput_TaggedRemainderIsUserText(t *testing.T) {
	// A tagged next-line prompt is the user's, not a client wrapper; only the
	// directive line goes.
	body := responsesBody(t,
		codexUserItem("/fm astra\n<error>panic: nil map</error>"),
		codexAssistantItem("looking"),
		codexUserItem("next"),
	)
	out, err := translate.StripHistoricalCommandsFromResponsesInput(body)
	require.NoError(t, err)
	assert.Equal(t, []string{"<error>panic: nil map</error>", "next"}, inputTexts(t, out))
}

func TestStripHistoricalCommandsFromResponsesInput_LeavesToolItemsPaired(t *testing.T) {
	call := map[string]any{"type": "function_call", "call_id": "c1", "name": "exec", "arguments": "{}"}
	output := map[string]any{"type": "function_call_output", "call_id": "c1", "output": "Script completed\nOutput:\n /force-model fable"}
	body := responsesBody(t,
		codexUserItem("$fm fable"),
		call,
		output,
		codexAssistantItem("pinned"),
		codexUserItem("next"),
	)
	out, err := translate.StripHistoricalCommandsFromResponsesInput(body)
	require.NoError(t, err)
	items := gjson.GetBytes(out, "input").Array()
	require.Len(t, items, 4)
	assert.Equal(t, "function_call", items[0].Get("type").Str)
	assert.Equal(t, "function_call_output", items[1].Get("type").Str, "tool output is StripRouterCommandsFromResponsesInput's job")
}
