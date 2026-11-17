package translate

import (
	"reflect"
	"runtime"
	"strings"
	"testing"
	"unsafe"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestViewJSONPathMatchesGJSONGetBytes(t *testing.T) {
	body := []byte(`{"model":"m","input":[{"role":"user","content":"a\"b"}],"n":3,"s":"x\u00e9"}`)
	for _, path := range []string{"model", "input", "input.0.content", "n", "s", "missing", "input.#", "input.0"} {
		want := gjson.GetBytes(body, path)
		got := viewJSONPath(body, path)
		assert.Equal(t, want.Raw, got.Raw, path)
		assert.Equal(t, want.Str, got.Str, path)
		assert.Equal(t, want.Type, got.Type, path)
		assert.Equal(t, want.Exists(), got.Exists(), path)
	}
	assert.Equal(t, gjson.ParseBytes(body).Raw, viewJSON(body).Raw)
	for _, empty := range [][]byte{nil, {}} {
		assert.False(t, viewJSONPath(empty, "model").Exists())
		assert.False(t, viewJSON(empty).Exists())
	}
}

func TestViewJSONPathDoesNotCopyMatchedValue(t *testing.T) {
	body := []byte(`{"input":[` + strings.Repeat(`{"role":"user","content":"x"},`, 4096) + `{}]}`)
	allocs := testing.AllocsPerRun(20, func() {
		if !viewJSONPath(body, "input").IsArray() || !viewJSON(body).IsObject() {
			t.Fatal("unexpected JSON shape")
		}
	})
	assert.Zero(t, allocs)
}

func largeResponsesRequest(targetBytes int) []byte {
	turn := `{"type":"message","role":"user","content":[{"type":"input_text","text":"` + strings.Repeat("lorem ipsum ", 1024) + `"}]},`
	var b strings.Builder
	b.WriteString(`{"model":"gpt-5.5","stream":true,"instructions":"be helpful","input":[`)
	for b.Len() < targetBytes {
		b.WriteString(turn)
	}
	b.WriteString(`{"type":"message","role":"user","content":[{"type":"input_text","text":"go"}]}]}`)
	return []byte(b.String())
}

func TestLargeResponsesRequestInspectionAllocatesBoundedBytes(t *testing.T) {
	body := largeResponsesRequest(8 << 20)
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	env, err := ParseOpenAI(body)
	require.NoError(t, err)
	_ = env.RoutingFeatures()
	_ = FeedbackFooterInResponsesHistory(body)
	_ = CodexSubagentPath(body)
	_ = LastAnnouncedMessagesModel(body)
	_, err = ParseResponsesEscalationObservation(body)
	require.NoError(t, err)
	runtime.ReadMemStats(&after)
	allocated := after.TotalAlloc - before.TotalAlloc
	t.Logf("allocated %.1fx the request body", float64(allocated)/float64(len(body)))
	assert.Less(t, allocated, uint64(2*len(body)), "request inspection must not copy whole-body JSON values on every lookup")
}

func richResponsesRequest() []byte {
	return []byte(`{"model":"gpt-5.5","stream":true,"instructions":"be helpful","input":[` +
		`{"type":"message","role":"developer","content":[{"type":"input_text","text":"system rules"}]},` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"/force-model gpt-5.5 first question"}]},` +
		`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer one"}]},` +
		`{"type":"function_call","call_id":"call_1","name":"shell","arguments":"{\"cmd\":\"ls\"}"},` +
		`{"type":"function_call_output","call_id":"call_1","output":"file.txt"},` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"second question"}]}` +
		`],"tools":[{"type":"function","name":"shell","parameters":{"type":"object","properties":{"cmd":{"type":"string"}}}}]}`)
}

func requireNoStringAliasesBody(t *testing.T, body []byte, value any, label string) {
	t.Helper()
	start := uintptr(unsafe.Pointer(unsafe.SliceData(body)))
	end := start + uintptr(len(body))
	var walk func(reflect.Value)
	walk = func(v reflect.Value) {
		switch v.Kind() {
		case reflect.String:
			if v.Len() == 0 {
				return
			}
			data := uintptr(unsafe.Pointer(unsafe.StringData(v.String())))
			require.Falsef(t, data >= start && data < end, "%s returned a string aliasing the request body: %q", label, v.String())
		case reflect.Struct:
			for i := range v.NumField() {
				walk(v.Field(i))
			}
		case reflect.Slice, reflect.Array:
			if v.Type().Elem().Kind() == reflect.Uint8 {
				return
			}
			for i := range v.Len() {
				walk(v.Index(i))
			}
		case reflect.Pointer, reflect.Interface:
			if !v.IsNil() {
				walk(v.Elem())
			}
		case reflect.Map:
			entries := v.MapRange()
			for entries.Next() {
				walk(entries.Key())
				walk(entries.Value())
			}
		default:
		}
	}
	walk(reflect.ValueOf(value))
}

func richChatCompletionsRequest() []byte {
	return []byte(`{"model":"gpt-5.5","stream":true,"messages":[` +
		`{"role":"system","content":"system rules"},` +
		`{"role":"user","content":"first question"},` +
		`{"role":"assistant","content":"looking","tool_calls":[{"id":"call_1","type":"function","function":{"name":"shell","arguments":"{}"}}]},` +
		`{"role":"tool","tool_call_id":"call_1","content":"file.txt"},` +
		`{"role":"user","content":"/force-model gpt-5\nhelp me debug this"}` +
		`],"tools":[{"type":"function","function":{"name":"shell","parameters":{"type":"object"}}}]}`)
}

func TestRequestInspectionResultsDoNotRetainRequestBody(t *testing.T) {
	chatBody := richChatCompletionsRequest()
	env, err := ParseOpenAI(chatBody)
	require.NoError(t, err)
	envObservation, err := env.EscalationObservation()
	require.NoError(t, err)
	forceModel, found := env.ExtractForceModelCommand()
	require.True(t, found)
	forcedBody := env.body
	require.NotEqual(t, chatBody, forcedBody)
	features := env.RoutingFeatures()
	messages := env.ConversationMessages()
	require.NotEmpty(t, messages)
	require.NotEmpty(t, features.PromptText)
	require.NotEmpty(t, env.SystemText())
	toolCallBody := []byte(`{"messages":[{"role":"user","content":"run it"},{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"shell","arguments":"{\"cmd\":\"ls\"}"}}]}]}`)
	toolCallEntries := openAIAssistantToolCallEntries(toolCallBody)
	require.NotEmpty(t, toolCallEntries)
	requireNoStringAliasesBody(t, toolCallBody, toolCallEntries, "AssistantToolCallEntries")

	responsesBody := richResponsesRequest()
	responsesObservation, err := ParseResponsesEscalationObservation(responsesBody)
	require.NoError(t, err)
	require.NotEmpty(t, responsesObservation.Messages)
	conversion, err := ConvertResponsesToChatCompletions(responsesBody)
	require.NoError(t, err)
	portable, err := ConvertResponsesToChatCompletionsWithOptions(responsesBody, ResponsesConversionOptions{PortableCodex: true})
	require.NoError(t, err)

	chatResults := map[string]any{
		"RoutingFeatures":                    features,
		"LastUserMessage":                    env.LastUserMessage(),
		"ConversationMessages":               messages,
		"ConversationMessagesWithClientText": env.ConversationMessagesWithClientText(),
		"FirstUserMessageText":               env.FirstUserMessageText(),
		"SystemText":                         env.SystemText(),
		"EscalationObservation":              envObservation,
		"ExtractForceModelCommand":           forceModel,
		"LastAnnouncedMessagesModel":         LastAnnouncedMessagesModel(chatBody),
	}
	for label, value := range chatResults {
		requireNoStringAliasesBody(t, chatBody, value, label)
		requireNoStringAliasesBody(t, forcedBody, value, label)
	}
	responsesResults := map[string]any{
		"ParseResponsesEscalationObservation": responsesObservation,
		"ConvertResponsesToChatCompletions":   []any{conversion.Model, conversion.Report, conversion.ToolMappings, conversion.Requirements},
		"PortableCodexConversion":             []any{portable.Model, portable.Report, portable.ToolMappings, portable.Requirements},
		"CodexSubagentPath":                   CodexSubagentPath(responsesBody),
	}
	for label, value := range responsesResults {
		requireNoStringAliasesBody(t, responsesBody, value, label)
	}
	assert.Equal(t, "gpt-5", forceModel.Model)
}
