package translate_test

import (
	"encoding/json"
	"maps"
	"strings"
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
		codexUserItem("hello"),
		codexAssistantItem("hi"),
		codexUserItem("$fm astra"),
		codexUserItem(codexSkillBlob),
		codexAssistantItem(forceModelAck),
		codexUserItem("now fix the bug"),
	)
	out, err := translate.StripHistoricalCommandsFromResponsesInput(body)
	require.NoError(t, err)
	assert.Equal(t, []string{"hello", "now fix the bug"}, inputTexts(t, out))
}

func TestStripHistoricalCommandsFromResponsesInput_DropsSkillExecCallAndOutput(t *testing.T) {
	for _, directive := range []string{"$fm fable", "$force-model fable", "$rf too slow"} {
		for _, callType := range []string{"function", "custom_tool"} {
			t.Run(directive+"/"+callType, func(t *testing.T) {
				skillName := strings.TrimPrefix(strings.Fields(directive)[0], "$")
				emitPath := "/x/skills/" + skillName + "/scripts/emit.sh"
				call := map[string]any{"type": callType + "_call", "call_id": "call_skill", "name": "exec", "namespace": "functions"}
				if callType == "function" {
					call["arguments"] = `{"cmd":"` + emitPath + ` fable"}`
				} else {
					call["input"] = `await tools.exec_command({cmd: "` + emitPath + ` fable"});`
				}
				directiveOutput := " /force-model fable"
				if skillName == "rf" {
					directiveOutput = " /router-feedback too slow"
				}
				output := map[string]any{"type": callType + "_call_output", "call_id": "call_skill", "output": "Script completed\nOutput:\n" + directiveOutput}
				parallelCall := maps.Clone(call)
				parallelCall["call_id"] = "call_skill_parallel"
				parallelOutput := maps.Clone(output)
				parallelOutput["call_id"] = "call_skill_parallel"
				ordinaryCall := map[string]any{"type": "function_call", "call_id": "call_read", "name": "exec", "arguments": `{}`}
				ordinaryOutput := map[string]any{"type": "function_call_output", "call_id": "call_read", "output": "source"}
				body := responsesBody(t,
					codexUserItem(directive), codexUserItem(strings.ReplaceAll(codexSkillBlob, "fm", skillName)),
					codexAssistantItem("Running the skill."),
					map[string]any{"type": "reasoning", "id": "rs_skill", "encrypted_content": "opaque", "summary": []any{}},
					call, parallelCall, output, parallelOutput, output,
					codexUserItem("read source"), ordinaryCall, ordinaryOutput, codexUserItem("next turn"),
				)

				out, err := translate.StripHistoricalCommandsFromResponsesInput(body)
				require.NoError(t, err)
				items := gjson.GetBytes(out, "input").Array()
				require.Len(t, items, 6)
				assert.Equal(t, "Running the skill.", items[0].Get("content.0.text").Str)
				assert.Equal(t, "reasoning", items[1].Get("type").Str)
				assert.Equal(t, "call_read", items[3].Get("call_id").Str)
				assert.Equal(t, "call_read", items[4].Get("call_id").Str)
				assert.Equal(t, []string{"read source", "next turn"}, inputTexts(t, out))
				assert.NotContains(t, string(out), "call_skill")
				assert.NotContains(t, string(out), "/force-model fable")
				assert.NotContains(t, string(out), "/router-feedback too slow")

				conversion, err := translate.ConvertResponsesToChatCompletionsWithOptions(body, translate.ResponsesConversionOptions{PortableCodex: true})
				require.NoError(t, err)
				env, err := translate.ParseOpenAI(conversion.Body)
				require.NoError(t, err)
				_, err = env.StripHistoricalCommands()
				require.NoError(t, err)
				prepared, err := env.PrepareGemini(nil, translate.EmitOptions{TargetModel: "gemini-2.5-flash"})
				require.NoError(t, err)
				assert.Equal(t, "user", gjson.GetBytes(prepared.Body, "contents.0.role").Str)
				assert.Equal(t, "read source", gjson.GetBytes(prepared.Body, "contents.0.parts.0.text").Str)
				assert.Contains(t, string(prepared.Body), "source")
				assert.NotContains(t, string(prepared.Body), "call_skill")
				assert.NotContains(t, string(prepared.Body), "/force-model fable")
				assert.NotContains(t, string(prepared.Body), "/router-feedback too slow")
			})
		}
	}
}

func TestStripHistoricalCommandsFromResponsesInput_TrailingDirectiveUntouched(t *testing.T) {
	body := responsesBody(t,
		codexUserItem("hello"),
		codexAssistantItem("hi"),
		codexUserItem("$fm astra"),
		codexUserItem(codexSkillBlob),
		map[string]any{"type": "custom_tool_call", "call_id": "call_skill", "name": "exec", "input": "/x/skills/fm/scripts/emit.sh astra"},
		map[string]any{"type": "custom_tool_call_output", "call_id": "call_skill", "output": " /force-model astra"},
	)
	out, err := translate.StripHistoricalCommandsFromResponsesInput(body)
	require.NoError(t, err)
	assert.Equal(t, body, out, "the live directive must survive for ExtractForceModelCommand")
}

func TestStripHistoricalCommandsFromResponsesInput_OpeningDirectiveIsStripped(t *testing.T) {
	body := responsesBody(t,
		codexUserItem("/fm astra"),
		codexAssistantItem(forceModelAck),
		codexUserItem("/fm fable"),
		codexAssistantItem(forceModelAck),
		codexUserItem("next"),
	)
	out, err := translate.StripHistoricalCommandsFromResponsesInput(body)
	require.NoError(t, err)
	assert.Equal(t, []string{"next"}, inputTexts(t, out))
}

func TestStripHistoricalCommandsFromResponsesInput_MixedMessageKeepsRemainder(t *testing.T) {
	body := responsesBody(t,
		codexUserItem("<environment_context><cwd>/x</cwd></environment_context>"),
		codexUserItem("/fm astra\nplease refactor the parser"),
		codexAssistantItem("done"),
		codexUserItem("thanks, now the tests"),
	)
	out, err := translate.StripHistoricalCommandsFromResponsesInput(body)
	require.NoError(t, err)
	assert.Equal(t, []string{"<environment_context><cwd>/x</cwd></environment_context>", "please refactor the parser", "thanks, now the tests"}, inputTexts(t, out))
}

func TestStripHistoricalCommandsFromResponsesInput_EveryDirectiveFamily(t *testing.T) {
	for _, directive := range []string{
		"/force-model sonnet", "$fm opus", "/model gpt", "/force-parent astra",
		"/unforce-model", "$ufm", "/rf- too slow", "$router-feedback 👍",
		"/router-session", "$router-models", "/models", "/list-models", "$list-models",
	} {
		t.Run(directive, func(t *testing.T) {
			body := responsesBody(t,
				codexUserItem("hello"),
				codexAssistantItem("hi"),
				codexUserItem(directive),
				codexAssistantItem("ack"),
				codexUserItem("next"),
			)
			out, err := translate.StripHistoricalCommandsFromResponsesInput(body)
			require.NoError(t, err)
			assert.Equal(t, []string{"hello", "next"}, inputTexts(t, out))
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
		codexUserItem("hello"),
		codexAssistantItem("hi"),
		codexUserItem("/fm astra\n<error>panic: nil map</error>"),
		codexAssistantItem("looking"),
		codexUserItem("next"),
	)
	out, err := translate.StripHistoricalCommandsFromResponsesInput(body)
	require.NoError(t, err)
	assert.Equal(t, []string{"hello", "<error>panic: nil map</error>", "next"}, inputTexts(t, out))
}

func TestStripHistoricalCommandsFromResponsesInput_LeavesToolItemsPaired(t *testing.T) {
	call := map[string]any{"type": "function_call", "call_id": "c1", "name": "exec", "arguments": `{"cmd":"/x/skills/fm/scripts/emit.sh fable"}`}
	output := map[string]any{"type": "function_call_output", "call_id": "c1", "output": "Script completed\nOutput:\n /force-model fable"}
	body := responsesBody(t,
		codexUserItem("hello"),
		codexAssistantItem("hi"),
		codexUserItem("$fm fable"),
		codexUserItem(codexSkillBlob),
		codexUserItem("run this script as an ordinary task"),
		call,
		output,
		codexAssistantItem("pinned"),
		codexUserItem("next"),
	)
	out, err := translate.StripHistoricalCommandsFromResponsesInput(body)
	require.NoError(t, err)
	items := gjson.GetBytes(out, "input").Array()
	require.Len(t, items, 7)
	assert.Equal(t, "function_call", items[3].Get("type").Str)
	assert.Equal(t, "function_call_output", items[4].Get("type").Str, "a later user turn ends skill-call cleanup")
	assert.Equal(t, "c1", items[3].Get("call_id").Str)
	assert.Equal(t, "c1", items[4].Get("call_id").Str)

	conversion, err := translate.ConvertResponsesToChatCompletionsWithOptions(body, translate.ResponsesConversionOptions{PortableCodex: true})
	require.NoError(t, err)
	env, err := translate.ParseOpenAI(conversion.Body)
	require.NoError(t, err)
	_, err = env.StripHistoricalCommands()
	require.NoError(t, err)
	prepared, err := env.PrepareGemini(nil, translate.EmitOptions{TargetModel: "gemini-2.5-flash"})
	require.NoError(t, err)
	assert.Contains(t, string(prepared.Body), "/x/skills/fm/scripts/emit.sh fable")
	assert.Contains(t, string(prepared.Body), "functionResponse")
}
