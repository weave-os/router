package translate_test

import (
	"encoding/json"
	"testing"

	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestConvertResponsesToChatCompletionsWithOptions_PortableCodexTitleGenerationShape(t *testing.T) {
	body := []byte(`{
		"model":"gpt-5.6-sol",
		"tools":[{"type":"function","name":"shell","parameters":{"type":"object"}}],
		"text":{"format":{"type":"json_schema","schema":{
			"type":"object","properties":{"title":{"type":"string"}},"required":["title"],"additionalProperties":false
		}}},
		"input":"Generate a concise task title."
	}`)

	converted, err := translate.ConvertResponsesToChatCompletionsWithOptions(body, translate.ResponsesConversionOptions{PortableCodex: true})
	require.NoError(t, err)
	assert.True(t, converted.TitleGeneration)
	assert.True(t, converted.Requirements.FunctionTools)
	assert.True(t, gjson.GetBytes(converted.Body, "tools").IsArray(), "routing projection must retain tool requirements")
}

func TestConvertResponsesToChatCompletionsWithOptions_PortableCodexTitleShapeIsStrict(t *testing.T) {
	body := []byte(`{
		"model":"gpt-5.6-sol",
		"text":{"format":{"type":"json_schema","schema":{
			"type":"object",
			"properties":{"title":{"type":"string"},"summary":{"type":"string"}},
			"required":["title","summary"],
			"additionalProperties":false
		}}},
		"input":"Return structured metadata."
	}`)

	converted, err := translate.ConvertResponsesToChatCompletionsWithOptions(body, translate.ResponsesConversionOptions{PortableCodex: true})
	require.NoError(t, err)
	assert.False(t, converted.TitleGeneration, "other structured-output objects must not be treated as hidden title calls")
}

func TestConvertResponsesToChatCompletionsWithOptions_PortableCodexTools(t *testing.T) {
	body := []byte(`{
		"model":"gpt-5.6-sol",
		"stream":true,
		"input":[
			{"type":"additional_tools","role":"developer","tools":[
				{"type":"namespace","name":"functions","description":"default","tools":[
					{"type":"function","name":"lookup","description":"look up","parameters":{"type":"object","$defs":{"query":{"type":"string","minLength":1}},"properties":{"q":{"$ref":"#/$defs/query"}}}},
					{"type":"custom","name":"exec","description":"run code","format":{"type":"grammar","syntax":"javascript","definition":"program"}}
				]},
				{"type":"namespace","name":"a","description":"first","tools":[
					{"type":"function","name":"b__c","parameters":{"type":"object"}}
				]},
				{"type":"namespace","name":"a__b","description":"collision","tools":[
					{"type":"function","name":"c","parameters":{"type":"object"}}
				]}
			]},
			{"type":"message","role":"developer","content":[{"type":"input_text","text":"base instructions"}]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"do it"}]}
		]
	}`)

	converted, err := translate.ConvertResponsesToChatCompletionsWithOptions(body, translate.ResponsesConversionOptions{PortableCodex: true})
	require.NoError(t, err)
	assert.Equal(t, body, converted.OriginalBody)
	assert.False(t, converted.Requirements.NativeOnly)
	assert.True(t, converted.Requirements.FunctionTools)
	assert.True(t, converted.Requirements.CustomTools)

	tools := gjson.GetBytes(converted.Body, "tools").Array()
	require.Len(t, tools, 4)
	assert.Equal(t, "lookup", tools[0].Get("function.name").Str)
	assert.Equal(t, "string", tools[0].Get("function.parameters.properties.q.type").Str)
	assert.Equal(t, int64(1), tools[0].Get("function.parameters.properties.q.minLength").Int())
	assert.False(t, tools[0].Get("function.parameters.$defs").Exists())
	assert.NotContains(t, tools[0].Get("function.parameters").Raw, `"$ref"`)
	assert.Equal(t, "exec", tools[1].Get("function.name").Str)
	assert.Equal(t, "string", tools[1].Get("function.parameters.properties.input.type").Str)
	assert.Equal(t, "a__b__c", tools[2].Get("function.name").Str)
	collisionAlias := tools[3].Get("function.name").Str
	assert.Regexp(t, `^a__b__c__[0-9a-f]{10}$`, collisionAlias)

	assert.Equal(t, translate.ResponsesToolMapping{Alias: "lookup", Name: "lookup"}, converted.ToolMappings["lookup"])
	assert.Equal(t, translate.ResponsesToolMapping{Alias: "exec", Name: "exec", Custom: true}, converted.ToolMappings["exec"])
	assert.Equal(t, translate.ResponsesToolMapping{Alias: "a__b__c", Name: "b__c", Namespace: "a"}, converted.ToolMappings["a__b__c"])
	assert.Equal(t, translate.ResponsesToolMapping{Alias: collisionAlias, Name: "c", Namespace: "a__b"}, converted.ToolMappings[collisionAlias])
	assert.Equal(t, "system", gjson.GetBytes(converted.Body, "messages.0.role").Str)
	assert.Equal(t, "base instructions", gjson.GetBytes(converted.Body, "messages.0.content.0.text").Str)
	assert.Equal(t, "user", gjson.GetBytes(converted.Body, "messages.1.role").Str)
	assert.Equal(t, "do it", gjson.GetBytes(converted.Body, "messages.1.content.0.text").Str)
	assertReportCode(t, converted.Report, "responses_developer_message_projected")
	assertReportCode(t, converted.Report, "responses_function_schema_inlined")
	assert.NotEmpty(t, converted.Report)
}

func TestConvertResponsesToChatCompletionsWithOptions_PortableCodexHistory(t *testing.T) {
	body := []byte("{ \"model\":\"gpt-5.6-sol\",\"input\":[" +
		`{"type":"reasoning","id":"rs_1","encrypted_content":"opaque","summary":[]},` +
		`{"type":"agent_message","id":"am_1","author":"child","recipient":"parent","content":[{"type":"input_text","text":"plain update"},{"type":"encrypted_content","encrypted_content":"opaque-agent"}]},` +
		`{"type":"function_call","call_id":"call_fn","name":"lookup","namespace":"collaboration","arguments":"{\"q\":1}"},` +
		`{"type":"custom_tool_call","call_id":"call_custom","name":"exec","namespace":"functions","input":"text(true);"},` +
		`{"type":"function_call_output","call_id":"call_fn","output":[{"type":"input_text","text":"one"},{"type":"input_text","text":"two"}]},` +
		`{"type":"custom_tool_call_output","call_id":"call_custom","name":"exec","output":"ok"},` +
		`{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"final"}]},` +
		`{"type":"message","role":"user","content":"continue"}` +
		"] }\n")

	converted, err := translate.ConvertResponsesToChatCompletionsWithOptions(body, translate.ResponsesConversionOptions{PortableCodex: true})
	require.NoError(t, err)
	assert.Equal(t, body, converted.OriginalBody, "native replay bytes must remain exact")
	assert.False(t, converted.Requirements.NativeOnly)

	messages := gjson.GetBytes(converted.Body, "messages").Array()
	require.Len(t, messages, 6)
	assert.Equal(t, "assistant", messages[0].Get("role").Str)
	assert.Equal(t, "plain update", messages[0].Get("content").Str)

	assert.Equal(t, "assistant", messages[1].Get("role").Str)
	require.Len(t, messages[1].Get("tool_calls").Array(), 2)
	assert.Equal(t, "collaboration__lookup", messages[1].Get("tool_calls.0.function.name").Str)
	assert.JSONEq(t, `{"q":1}`, messages[1].Get("tool_calls.0.function.arguments").Str)
	assert.Equal(t, "exec", messages[1].Get("tool_calls.1.function.name").Str)
	assert.JSONEq(t, `{"input":"text(true);"}`, messages[1].Get("tool_calls.1.function.arguments").Str)

	assert.Equal(t, "tool", messages[2].Get("role").Str)
	assert.Equal(t, "call_fn", messages[2].Get("tool_call_id").Str)
	assert.Equal(t, "one\ntwo", messages[2].Get("content").Str)
	assert.Equal(t, "call_custom", messages[3].Get("tool_call_id").Str)
	assert.Equal(t, "ok", messages[3].Get("content").Str)
	assert.Equal(t, "final", messages[4].Get("content.0.text").Str)
	assert.Equal(t, "continue", messages[5].Get("content").Str)

	assert.Equal(t, translate.ResponsesToolMapping{Alias: "collaboration__lookup", Name: "lookup", Namespace: "collaboration"}, converted.ToolMappings["collaboration__lookup"])
	assert.Equal(t, translate.ResponsesToolMapping{Alias: "exec", Name: "exec", Custom: true}, converted.ToolMappings["exec"])
	assertReportCode(t, converted.Report, "responses_encrypted_reasoning_dropped")
	assertReportCode(t, converted.Report, "responses_encrypted_agent_content_dropped")
	assertReportCode(t, converted.Report, "responses_agent_message_projected")
	assertReportCode(t, converted.Report, "responses_structured_tool_output_projected")
	assertReportCode(t, converted.Report, "responses_message_phase_dropped")
}

func TestConvertResponsesToChatCompletionsWithOptions_PortableCodexUnknownMessagePhaseFailsClosed(t *testing.T) {
	body := []byte(`{
		"model":"gpt-5.6-sol",
		"input":[{"type":"message","role":"assistant","phase":"future_phase","content":[{"type":"output_text","text":"hello"}]}]
	}`)

	converted, err := translate.ConvertResponsesToChatCompletionsWithOptions(body, translate.ResponsesConversionOptions{PortableCodex: true})
	require.NoError(t, err)
	assert.True(t, converted.Requirements.NativeOnly)
	assertReportCode(t, converted.Report, "responses_message_phase_native_only")
}

func TestConvertResponsesToChatCompletionsWithOptions_PortableCodexReasoningFailsClosed(t *testing.T) {
	tests := []struct {
		name string
		item string
		code string
	}{
		{
			name: "public summary",
			item: `{"type":"reasoning","encrypted_content":"opaque","summary":[{"type":"summary_text","text":"public"}]}`,
			code: "responses_reasoning_summary_native_only",
		},
		{
			name: "plaintext content",
			item: `{"type":"reasoning","encrypted_content":"opaque","summary":[],"content":[{"type":"reasoning_text","text":"private"}]}`,
			code: "responses_reasoning_content_native_only",
		},
		{
			name: "unknown plaintext field",
			item: `{"type":"reasoning","encrypted_content":"opaque","summary":[],"plaintext":"private"}`,
			code: "responses_reasoning_unknown_native_only",
		},
		{
			name: "missing encrypted replay",
			item: `{"type":"reasoning","summary":[]}`,
			code: "responses_reasoning_replay_native_only",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := []byte(`{"model":"gpt-5.6-sol","input":[` + test.item + `]}`)
			converted, err := translate.ConvertResponsesToChatCompletionsWithOptions(body, translate.ResponsesConversionOptions{PortableCodex: true})
			require.NoError(t, err)
			assert.True(t, converted.Requirements.NativeOnly)
			assert.True(t, converted.Requirements.ReasoningReplay)
			assertReportCode(t, converted.Report, test.code)
		})
	}
}

func TestConvertResponsesToChatCompletionsWithOptions_PortableCodexEncryptedOnlyAgentMessageFailsClosed(t *testing.T) {
	body := []byte(`{
		"model":"gpt-5.6-sol",
		"input":[{"type":"agent_message","author":"child","recipient":"parent","content":[{"type":"encrypted_content","encrypted_content":"opaque"}]}]
	}`)

	converted, err := translate.ConvertResponsesToChatCompletionsWithOptions(body, translate.ResponsesConversionOptions{PortableCodex: true})
	require.NoError(t, err)
	assert.True(t, converted.Requirements.NativeOnly)
	assert.Empty(t, gjson.GetBytes(converted.Body, "messages").Array())
	assertReportCode(t, converted.Report, "responses_encrypted_agent_content_dropped")
	assertReportCode(t, converted.Report, "responses_agent_message_native_only")
}

func TestConvertResponsesToChatCompletionsWithOptions_PortableCodexCustomStructuredOutputIsString(t *testing.T) {
	body := []byte(`{
		"model":"gpt-5.6-sol",
		"input":[
			{"type":"custom_tool_call","call_id":"call_1","name":"exec","input":"text(true);"},
			{"type":"custom_tool_call_output","call_id":"call_1","name":"exec","output":[{"type":"input_text","text":"first"},{"type":"input_text","text":"second"}]}
		]
	}`)

	converted, err := translate.ConvertResponsesToChatCompletionsWithOptions(body, translate.ResponsesConversionOptions{PortableCodex: true})
	require.NoError(t, err)
	assert.False(t, converted.Requirements.NativeOnly)
	assert.Equal(t, "first\nsecond", gjson.GetBytes(converted.Body, "messages.1.content").Str)
	assertReportCode(t, converted.Report, "responses_structured_tool_output_projected")
}

func TestConvertResponsesToChatCompletionsWithOptions_PortableCodexStructuredTextFormat(t *testing.T) {
	body := []byte(`{
			"model":"gpt-5.6-sol",
			"input":"answer",
			"service_tier":"priority",
			"prompt_cache_key":"caller-key",
			"text":{"verbosity":"low","format":{"type":"json_schema","name":"answer","strict":true,"schema":{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"]}}}
	}`)

	converted, err := translate.ConvertResponsesToChatCompletionsWithOptions(body, translate.ResponsesConversionOptions{PortableCodex: true})
	require.NoError(t, err)
	assert.Equal(t, "json_schema", gjson.GetBytes(converted.Body, "response_format.type").Str)
	assert.Equal(t, "answer", gjson.GetBytes(converted.Body, "response_format.json_schema.name").Str)
	assert.True(t, gjson.GetBytes(converted.Body, "response_format.json_schema.strict").Bool())
	assert.Equal(t, "string", gjson.GetBytes(converted.Body, "response_format.json_schema.schema.properties.answer.type").Str)
	assert.Equal(t, "system", gjson.GetBytes(converted.Body, "messages.0.role").Str)
	assert.Equal(t, "Keep the response concise and focused.", gjson.GetBytes(converted.Body, "messages.0.content").Str)
	assert.Equal(t, "answer", gjson.GetBytes(converted.Body, "messages.1.content").Str)
	assert.False(t, gjson.GetBytes(converted.Body, "service_tier").Exists())
	assert.False(t, gjson.GetBytes(converted.Body, "prompt_cache_key").Exists())
	assert.Equal(t, "priority", gjson.GetBytes(converted.OriginalBody, "service_tier").Str)
	assert.Equal(t, "caller-key", gjson.GetBytes(converted.OriginalBody, "prompt_cache_key").Str)
	assertReportCode(t, converted.Report, "responses_text_verbosity_projected")
	assertReportCode(t, converted.Report, "responses_service_tier_dropped")
	assertReportCode(t, converted.Report, "responses_prompt_cache_key_dropped")
}

func TestConvertResponsesToChatCompletionsWithOptions_PortableCodexUnsupportedVerbosityStaysNative(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-sol","input":"answer","text":{"verbosity":"future"}}`)

	converted, err := translate.ConvertResponsesToChatCompletionsWithOptions(body, translate.ResponsesConversionOptions{PortableCodex: true})
	require.NoError(t, err)
	assert.True(t, converted.Requirements.NativeOnly)
	assertReportCode(t, converted.Report, "responses_text_verbosity_native_only")
}

func TestConvertResponsesToChatCompletionsWithOptions_PortableCodexUnresolvedToolRefStaysNative(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-sol","input":"answer","tools":[{"type":"function","name":"lookup","parameters":{"type":"object","properties":{"q":{"$ref":"https://example.com/query.json"}}}}]}`)

	converted, err := translate.ConvertResponsesToChatCompletionsWithOptions(body, translate.ResponsesConversionOptions{PortableCodex: true})
	require.NoError(t, err)
	assert.True(t, converted.Requirements.NativeOnly)
	assert.Empty(t, gjson.GetBytes(converted.Body, "tools").Array())
	assertReportCode(t, converted.Report, "responses_function_schema_native_only")
}

func TestConvertResponsesToChatCompletionsWithOptions_PortableCodexBadgeNeedsProvenance(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		want string
	}{
		{name: "injected badge", text: "\u2063\u2060\u2063\u2060**Weave Router** — gpt-5.6-terra\n\nanswer", want: "answer"},
		{name: "organic heading", text: "**Weave Router** — an ordinary heading\n\nkeep this", want: "**Weave Router** — an ordinary heading\n\nkeep this"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			encoded, err := json.Marshal(tc.text)
			require.NoError(t, err)
			body := []byte(`{"model":"gpt-5.6-sol","input":[{"type":"message","role":"assistant","content":` + string(encoded) + `}]}`)
			converted, err := translate.ConvertResponsesToChatCompletionsWithOptions(body, translate.ResponsesConversionOptions{PortableCodex: true})
			require.NoError(t, err)
			assert.Equal(t, tc.want, gjson.GetBytes(converted.Body, "messages.0.content").Str)
		})
	}
}

func TestConvertResponsesToChatCompletionsWithOptions_PortableCodexDropsMarkerOnlyAssistantBeforeToolCall(t *testing.T) {
	body := []byte(`{
		"model":"gpt-5.6-sol",
		"input":[
			{"type":"message","role":"user","content":"run it"},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"` + codexResponsesBadgeSentinelForTest + `**Weave Router** — claude-sonnet-4-6 ← gpt-5.4\n\n\n_Weave Router feedback:_ $rf + good experience"}]},
			{"type":"function_call","call_id":"call_1","name":"shell","arguments":"{}"}
		]
	}`)

	converted, err := translate.ConvertResponsesToChatCompletionsWithOptions(body, translate.ResponsesConversionOptions{PortableCodex: true})
	require.NoError(t, err)

	messages := gjson.GetBytes(converted.Body, "messages").Array()
	require.Len(t, messages, 2, "marker-only assistant shells must not precede the tool call")
	assert.Equal(t, "user", messages[0].Get("role").Str)
	assert.Equal(t, "assistant", messages[1].Get("role").Str)
	assert.Equal(t, "call_1", messages[1].Get("tool_calls.0.id").Str)

	env, err := translate.ParseOpenAI(converted.Body)
	require.NoError(t, err)
	prepared, err := env.PrepareAnthropic(nil, translate.EmitOptions{TargetModel: "claude-sonnet-4-6"})
	require.NoError(t, err)
	assert.NotContains(t, string(prepared.Body), `"type":"text","text":""`,
		"Anthropic must not receive empty text content blocks")
}

func TestConvertResponsesToChatCompletionsWithOptions_PortableCodexUnknownStaysNative(t *testing.T) {
	body := []byte(`{
		"model":"gpt-5.6-sol",
		"input":[{"type":"computer_call","id":"comp_1","action":{"type":"click"}}],
		"tools":[{"type":"web_search","search_context_size":"medium"}]
	}`)

	converted, err := translate.ConvertResponsesToChatCompletionsWithOptions(body, translate.ResponsesConversionOptions{PortableCodex: true})
	require.NoError(t, err)
	assert.True(t, converted.Requirements.NativeOnly)
	assert.Equal(t, body, converted.OriginalBody)
	assertReportCode(t, converted.Report, "responses_unknown_input_native_only")
	assertReportCode(t, converted.Report, "responses_unknown_tool_native_only")
}

func TestConvertResponsesToChatCompletionsWithOptions_ZeroValueMatchesLegacy(t *testing.T) {
	body := []byte(`{"model":"gpt-5","input":"use it","tools":[{"type":"custom","name":"exec"}]}`)

	legacy, err := translate.ConvertResponsesToChatCompletions(body)
	require.NoError(t, err)
	withOptions, err := translate.ConvertResponsesToChatCompletionsWithOptions(body, translate.ResponsesConversionOptions{})
	require.NoError(t, err)
	assert.Equal(t, legacy, withOptions)
	assert.True(t, withOptions.Requirements.NativeOnly)
}

func assertReportCode(t *testing.T, reports []translate.ResponseTransform, code string) {
	t.Helper()
	for _, report := range reports {
		if report.Code == code {
			return
		}
	}
	assert.Fail(t, "missing response transform report", "code %q in %#v", code, reports)
}
