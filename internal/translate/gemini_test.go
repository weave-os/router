package translate_test

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"weave-os/router/internal/router"
	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ----- OpenAI → Gemini request -----

func TestPrepareGemini_FromOpenAI_SimpleText(t *testing.T) {
	body := []byte(`{
		"model": "gemini-3.1-flash-lite-preview",
		"messages": [
			{"role": "system", "content": "You are helpful."},
			{"role": "user", "content": "list /tmp"}
		]
	}`)
	env, err := translate.ParseOpenAI(body)
	require.NoError(t, err)
	prep, err := env.PrepareGemini(http.Header{}, translate.EmitOptions{TargetModel: "gemini-3.1-flash-lite-preview"})
	require.NoError(t, err)

	out := mustUnmarshal(t, prep.Body)
	sys := must(t, out, "systemInstruction")
	assert.Equal(t, "You are helpful.", mustAs[map[string]any](t, mustAs[[]any](t, mustAs[map[string]any](t, sys)["parts"])[0])["text"])

	contents := mustAs[[]any](t, must(t, out, "contents"))
	require.Len(t, contents, 1)
	first := mustAs[map[string]any](t, contents[0])
	assert.Equal(t, "user", first["role"])
	parts := mustAs[[]any](t, first["parts"])
	assert.Equal(t, "list /tmp", mustAs[map[string]any](t, parts[0])["text"])
}

func TestPrepareGemini_MultipleSystemMessagesConcatenated(t *testing.T) {
	body := []byte(`{
		"messages": [
			{"role": "system", "content": "rule one"},
			{"role": "system", "content": "rule two"},
			{"role": "user", "content": "go"}
		]
	}`)
	env, _ := translate.ParseOpenAI(body)
	prep, err := env.PrepareGemini(http.Header{}, translate.EmitOptions{})
	require.NoError(t, err)

	out := mustUnmarshal(t, prep.Body)
	sys := mustAs[map[string]any](t, out["systemInstruction"])
	text := mustAs[string](t, mustAs[map[string]any](t, mustAs[[]any](t, sys["parts"])[0])["text"])
	assert.Equal(t, "rule one\nrule two", text)
}

func TestPrepareGemini_AssistantToolCallsRoundTripsThoughtSignature(t *testing.T) {
	// Load-bearing: thought_signature on a prior assistant tool_call must land
	// as thoughtSignature on the Gemini functionCall part.
	body := []byte(`{
		"messages": [
			{"role": "user", "content": "list /tmp"},
			{"role": "assistant", "content": null, "tool_calls": [
				{"id": "c1", "type": "function",
				 "function": {"name": "bash", "arguments": "{\"command\":\"ls /tmp\"}",
				              "thought_signature": "OPAQUE_SIG_BYTES"}}
			]},
			{"role": "tool", "tool_call_id": "c1", "content": "f1\nf2"}
		]
	}`)
	env, _ := translate.ParseOpenAI(body)
	prep, err := env.PrepareGemini(http.Header{}, translate.EmitOptions{})
	require.NoError(t, err)

	out := mustUnmarshal(t, prep.Body)
	contents := mustAs[[]any](t, out["contents"])
	require.Len(t, contents, 3)

	// model turn carries functionCall + thoughtSignature.
	model := mustAs[map[string]any](t, contents[1])
	assert.Equal(t, "model", model["role"])
	parts := mustAs[[]any](t, model["parts"])
	require.Len(t, parts, 1)
	p := mustAs[map[string]any](t, parts[0])
	fc := mustAs[map[string]any](t, p["functionCall"])
	assert.Equal(t, "bash", fc["name"])
	args := mustAs[map[string]any](t, fc["args"])
	assert.Equal(t, "ls /tmp", args["command"])
	assert.Equal(t, "OPAQUE_SIG_BYTES", p["thoughtSignature"])

	// tool turn must surface as user-role functionResponse with the same name.
	toolTurn := mustAs[map[string]any](t, contents[2])
	assert.Equal(t, "user", toolTurn["role"])
	tps := mustAs[[]any](t, toolTurn["parts"])
	fr := mustAs[map[string]any](t, mustAs[map[string]any](t, tps[0])["functionResponse"])
	assert.Equal(t, "bash", fr["name"])
	assert.Equal(t, "f1\nf2", mustAs[map[string]any](t, fr["response"])["result"])
}

func TestPrepareGemini_ToolsMappedToFunctionDeclarations(t *testing.T) {
	body := []byte(`{
		"messages": [{"role":"user","content":"hi"}],
		"tools": [
			{"type":"function","function":{
				"name":"bash","description":"Run bash",
				"parameters":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}
			}}
		]
	}`)
	env, _ := translate.ParseOpenAI(body)
	prep, err := env.PrepareGemini(http.Header{}, translate.EmitOptions{})
	require.NoError(t, err)

	out := mustUnmarshal(t, prep.Body)
	tools := mustAs[[]any](t, out["tools"])
	require.Len(t, tools, 1)
	decls := mustAs[[]any](t, mustAs[map[string]any](t, tools[0])["functionDeclarations"])
	require.Len(t, decls, 1)
	d := mustAs[map[string]any](t, decls[0])
	assert.Equal(t, "bash", d["name"])
	assert.Equal(t, "Run bash", d["description"])
	assert.NotNil(t, d["parameters"])
}

func TestPrepareGemini_ToolChoiceVariants(t *testing.T) {
	cases := map[string]string{
		`"tool_choice":"auto"`:     "AUTO",
		`"tool_choice":"none"`:     "NONE",
		`"tool_choice":"required"`: "ANY",
		`"tool_choice":{"type":"function","function":{"name":"bash"}}`: "ANY",
	}
	for tc, mode := range cases {
		body := []byte(`{"messages":[{"role":"user","content":"x"}],` + tc + `}`)
		env, _ := translate.ParseOpenAI(body)
		prep, err := env.PrepareGemini(http.Header{}, translate.EmitOptions{})
		require.NoError(t, err, tc)
		out := mustUnmarshal(t, prep.Body)
		got := mustAs[map[string]any](t, mustAs[map[string]any](t, out["toolConfig"])["functionCallingConfig"])["mode"]
		assert.Equal(t, mode, got, tc)
	}
}

func TestPrepareGemini_FromAnthropic_ToolChoiceVariants(t *testing.T) {
	cases := map[string]string{
		`"tool_choice":{"type":"auto"}`:               "AUTO",
		`"tool_choice":{"type":"none"}`:               "NONE",
		`"tool_choice":{"type":"any"}`:                "ANY",
		`"tool_choice":{"type":"tool","name":"bash"}`: "ANY",
	}
	for tc, mode := range cases {
		body := []byte(`{"messages":[{"role":"user","content":"x"}],` + tc + `}`)
		env, _ := translate.ParseAnthropic(body)
		prep, err := env.PrepareGemini(http.Header{}, translate.EmitOptions{})
		require.NoError(t, err, tc)
		out := mustUnmarshal(t, prep.Body)
		got := mustAs[map[string]any](t, mustAs[map[string]any](t, out["toolConfig"])["functionCallingConfig"])["mode"]
		assert.Equal(t, mode, got, tc)
	}
}

// TestPrepareGemini_UnrecognizedToolChoiceDoesNotEnableValidatedMode guards a
// regression from the tool_choice normalization: a present-but-malformed
// tool_choice (unknown string, or an Anthropic object with an unknown type)
// must NOT be treated the same as a truly absent tool_choice. On Gemini 3.x,
// absent/auto upgrades to mode=VALIDATED; a malformed value must fall back to
// legacy no-toolConfig behavior instead of silently getting that upgrade.
func TestPrepareGemini_UnrecognizedToolChoiceDoesNotEnableValidatedMode(t *testing.T) {
	tools := `"tools":[{"name":"bash","description":"b","input_schema":{"type":"object"}}]`

	t.Run("openai_source_unknown_string", func(t *testing.T) {
		body := []byte(`{"messages":[{"role":"user","content":"x"}],` + tools + `,"tool_choice":"bogus"}`)
		env, err := translate.ParseOpenAI(body)
		require.NoError(t, err)
		prep, err := env.PrepareGemini(http.Header{}, translate.EmitOptions{TargetModel: "gemini-3.1-pro-preview"})
		require.NoError(t, err)
		out := mustUnmarshal(t, prep.Body)
		_, hasToolConfig := out["toolConfig"]
		assert.False(t, hasToolConfig, "malformed tool_choice must not trigger VALIDATED mode")
	})

	t.Run("anthropic_source_unknown_type", func(t *testing.T) {
		body := []byte(`{"messages":[{"role":"user","content":"x"}],` + tools + `,"tool_choice":{"type":"bogus"}}`)
		env, err := translate.ParseAnthropic(body)
		require.NoError(t, err)
		prep, err := env.PrepareGemini(http.Header{}, translate.EmitOptions{TargetModel: "gemini-3.1-pro-preview"})
		require.NoError(t, err)
		out := mustUnmarshal(t, prep.Body)
		_, hasToolConfig := out["toolConfig"]
		assert.False(t, hasToolConfig, "malformed tool_choice must not trigger VALIDATED mode")
	})
}

func TestPrepareGemini_ReasoningEffortMapsToThinkingBudget(t *testing.T) {
	// No target model => legacy (gemini-2.5) path: numeric thinkingBudget.
	cases := map[string]int{"low": 1024, "medium": 8192, "high": 24576, "none": 0}
	for effort, budget := range cases {
		body := []byte(`{"messages":[{"role":"user","content":"x"}],"reasoning_effort":"` + effort + `"}`)
		env, _ := translate.ParseOpenAI(body)
		prep, err := env.PrepareGemini(http.Header{}, translate.EmitOptions{TargetModel: "gemini-2.5-pro"})
		require.NoError(t, err, effort)
		out := mustUnmarshal(t, prep.Body)
		gc := mustAs[map[string]any](t, out["generationConfig"])
		tc := mustAs[map[string]any](t, gc["thinkingConfig"])
		assert.EqualValues(t, budget, tc["thinkingBudget"], effort)
	}
}

func TestPrepareGemini_ReasoningEffortMapsToThinkingLevel_Gemini3x(t *testing.T) {
	// gemini-3.x uses string thinkingLevel, not thinkingBudget. "none" is
	// omitted — 3.x can't disable thinking.
	for _, effort := range []string{"low", "medium", "high"} {
		body := []byte(`{"messages":[{"role":"user","content":"x"}],"reasoning_effort":"` + effort + `"}`)
		env, _ := translate.ParseOpenAI(body)
		prep, err := env.PrepareGemini(http.Header{}, translate.EmitOptions{TargetModel: "gemini-3.1-pro-preview"})
		require.NoError(t, err, effort)
		out := mustUnmarshal(t, prep.Body)
		gc := mustAs[map[string]any](t, out["generationConfig"])
		tc := mustAs[map[string]any](t, gc["thinkingConfig"])
		assert.Equal(t, effort, tc["thinkingLevel"], effort)
		_, hasBudget := tc["thinkingBudget"]
		assert.False(t, hasBudget, "gemini-3.x must not send thinkingBudget (%s)", effort)
	}
}

func TestPrepareGemini_AnthropicAdaptiveThinkingMapsToGemini35Flash(t *testing.T) {
	body := []byte(`{
		"model":"claude-sonnet-5",
		"max_tokens":4096,
		"thinking":{"type":"adaptive"},
		"output_config":{"effort":"high"},
		"messages":[{"role":"user","content":"solve this"}]
	}`)
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)

	prep, err := env.PrepareGemini(http.Header{}, translate.EmitOptions{TargetModel: "gemini-3.5-flash"})
	require.NoError(t, err)
	out := mustUnmarshal(t, prep.Body)
	generationConfig := mustAs[map[string]any](t, out["generationConfig"])
	thinkingConfig := mustAs[map[string]any](t, generationConfig["thinkingConfig"])
	assert.Equal(t, "high", thinkingConfig["thinkingLevel"])
	assert.NotContains(t, thinkingConfig, "thinkingBudget")
}

func TestPrepareGemini_StreamHintHeaderSet(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"x"}],"stream":true}`)
	env, _ := translate.ParseOpenAI(body)
	prep, err := env.PrepareGemini(http.Header{}, translate.EmitOptions{})
	require.NoError(t, err)
	assert.Equal(t, "true", prep.Headers.Get(translate.GeminiStreamHintHeader))
}

func TestPrepareGemini_NoStreamHintWhenNonStreaming(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"x"}]}`)
	env, _ := translate.ParseOpenAI(body)
	prep, err := env.PrepareGemini(http.Header{}, translate.EmitOptions{})
	require.NoError(t, err)
	assert.Empty(t, prep.Headers.Get(translate.GeminiStreamHintHeader))
}

// ----- Anthropic → Gemini request -----

func TestPrepareGemini_FromAnthropic_SystemAndToolUseRoundTripsSignature(t *testing.T) {
	body := []byte(`{
		"system": "be helpful",
		"messages": [
			{"role":"user","content":[{"type":"text","text":"list"}]},
			{"role":"assistant","content":[
				{"type":"tool_use","id":"toolu_x","name":"bash",
				 "input":{"command":"ls"},
				 "thought_signature":"ANTHROPIC_SIG"}
			]},
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"toolu_x","content":"f1"}
			]}
		],
		"tools": [{"name":"bash","description":"b","input_schema":{"type":"object"}}]
	}`)
	env, _ := translate.ParseAnthropic(body)
	prep, err := env.PrepareGemini(http.Header{}, translate.EmitOptions{Capabilities: router.ModelSpec{}})
	require.NoError(t, err)

	out := mustUnmarshal(t, prep.Body)
	assert.NotNil(t, out["systemInstruction"])

	contents := mustAs[[]any](t, out["contents"])
	require.Len(t, contents, 3)

	model := mustAs[map[string]any](t, contents[1])
	parts := mustAs[[]any](t, model["parts"])
	p := mustAs[map[string]any](t, parts[0])
	assert.Equal(t, "ANTHROPIC_SIG", p["thoughtSignature"])
	fc := mustAs[map[string]any](t, p["functionCall"])
	assert.Equal(t, "bash", fc["name"])

	tr := mustAs[map[string]any](t, contents[2])
	frPart := mustAs[map[string]any](t, mustAs[[]any](t, tr["parts"])[0])
	fr := mustAs[map[string]any](t, frPart["functionResponse"])
	assert.Equal(t, "bash", fr["name"])
}

func TestPrepareGemini_FromAnthropic_DropsOrphanedToolResult(t *testing.T) {
	// tool_result without matching tool_use — name lookup yields "".
	// The emitter must skip it rather than producing empty function_response.name.
	body := []byte(`{
		"messages": [
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"orphan","content":"stale result"},
				{"type":"text","text":"hello"}
			]}
		]
	}`)
	env, _ := translate.ParseAnthropic(body)
	prep, err := env.PrepareGemini(http.Header{}, translate.EmitOptions{Capabilities: router.ModelSpec{}})
	require.NoError(t, err)

	out := mustUnmarshal(t, prep.Body)
	contents := mustAs[[]any](t, out["contents"])
	require.Len(t, contents, 1)
	parts := mustAs[[]any](t, mustAs[map[string]any](t, contents[0])["parts"])
	require.Len(t, parts, 1, "orphaned tool_result must be dropped")
	assert.NotNil(t, mustAs[map[string]any](t, parts[0])["text"], "only the text part should remain")
}

func TestPrepareGemini_FromOpenAI_DropsOrphanedToolMessage(t *testing.T) {
	body := []byte(`{
		"model": "gemini-3.1-flash-lite-preview",
		"messages": [
			{"role":"user","content":"hi"},
			{"role":"tool","tool_call_id":"orphan","content":"stale result"},
			{"role":"assistant","content":"done"},
			{"role":"user","content":"next"}
		]
	}`)
	env, _ := translate.ParseOpenAI(body)
	prep, err := env.PrepareGemini(http.Header{}, translate.EmitOptions{Capabilities: router.ModelSpec{}})
	require.NoError(t, err)

	out := mustUnmarshal(t, prep.Body)
	contents := mustAs[[]any](t, out["contents"])
	// user "hi", model "done", user "next" — orphaned tool message skipped.
	require.Len(t, contents, 3)
	for _, c := range contents {
		entry := mustAs[map[string]any](t, c)
		for _, p := range mustAs[[]any](t, entry["parts"]) {
			part := mustAs[map[string]any](t, p)
			_, hasFR := part["functionResponse"]
			assert.False(t, hasFR, "orphaned functionResponse must not appear")
		}
	}
}

// ----- Gemini → OpenAI response -----

func TestGeminiToOpenAIResponse_TextOnly(t *testing.T) {
	body := []byte(`{
		"candidates":[{"content":{"role":"model","parts":[{"text":"hi"}]},"finishReason":"STOP","index":0}],
		"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":1,"totalTokenCount":4}
	}`)
	out, err := translate.GeminiToOpenAIResponse(body, "gemini-3.1-flash-lite-preview")
	require.NoError(t, err)
	resp := mustUnmarshal(t, out)
	assert.Equal(t, "chat.completion", resp["object"])
	assert.Equal(t, "gemini-3.1-flash-lite-preview", resp["model"])
	choice := mustAs[map[string]any](t, mustAs[[]any](t, resp["choices"])[0])
	msg := mustAs[map[string]any](t, choice["message"])
	assert.Equal(t, "hi", msg["content"])
	assert.Equal(t, "stop", choice["finish_reason"])
	usage := mustAs[map[string]any](t, resp["usage"])
	assert.EqualValues(t, 3, usage["prompt_tokens"])
	assert.EqualValues(t, 1, usage["completion_tokens"])
	assert.EqualValues(t, 4, usage["total_tokens"])
	id := mustAs[string](t, resp["id"])
	assert.True(t, strings.HasPrefix(id, "chatcmpl-"))
	assert.Len(t, id, len("chatcmpl-")+16)
}

func TestGeminiToOpenAIResponse_ToolCallPreservesThoughtSignature(t *testing.T) {
	// Signature is smuggled into the tool-call id, not emitted as an off-spec field.
	body := []byte(`{
		"candidates":[{"content":{"role":"model","parts":[
			{"functionCall":{"name":"bash","args":{"command":"ls /tmp"}},
			 "thoughtSignature":"GEMINI_SIG"}
		]},"finishReason":"STOP","index":0}]
	}`)
	out, err := translate.GeminiToOpenAIResponse(body, "gemini-3.1-flash-lite-preview")
	require.NoError(t, err)
	resp := mustUnmarshal(t, out)
	choice := mustAs[map[string]any](t, mustAs[[]any](t, resp["choices"])[0])
	assert.Equal(t, "tool_calls", choice["finish_reason"])
	tc := mustAs[map[string]any](t, mustAs[[]any](t, mustAs[map[string]any](t, choice["message"])["tool_calls"])[0])
	fn := mustAs[map[string]any](t, tc["function"])
	assert.Equal(t, "bash", fn["name"])
	assert.Equal(t, `{"command":"ls /tmp"}`, fn["arguments"])
	assert.NotContains(t, fn, "thought_signature", "off-spec field must not be emitted")
	assert.NotContains(t, tc, "thought_signature", "off-spec field must not be emitted")
	encoded := base64.RawURLEncoding.EncodeToString([]byte("GEMINI_SIG"))
	assert.Contains(t, mustAs[string](t, tc["id"]), encoded, "signature rides in the tool-call id")
}

func TestGeminiToOpenAIResponse_FinishReasonMapping(t *testing.T) {
	cases := map[string]string{
		"STOP":       "stop",
		"MAX_TOKENS": "length",
		"SAFETY":     "content_filter",
		"RECITATION": "content_filter",
		"":           "stop",
	}
	for fr, want := range cases {
		body := []byte(`{"candidates":[{"content":{"parts":[{"text":"x"}]},"finishReason":"` + fr + `"}]}`)
		out, _ := translate.GeminiToOpenAIResponse(body, "m")
		resp := mustUnmarshal(t, out)
		got := mustAs[map[string]any](t, mustAs[[]any](t, resp["choices"])[0])["finish_reason"]
		assert.Equal(t, want, got, fr)
	}
}

// ----- Gemini → Anthropic response -----

// Load-bearing regression: Gemini 3.x multi-turn tool use when the client
// drops the off-spec thought_signature field on deserialization — the
// signature must still be smuggled back via the id channel.
func TestPrepareGemini_FromAnthropic_ToolUseSignatureSurvivesUnknownFieldStripping(t *testing.T) {
	// Turn 1: construct the tool_use block by hand, replicating what
	// embedSignatureInID produces (id + "__thought__" + base64(sig)).
	smuggledID := "toolu_test__thought__" + base64.RawURLEncoding.EncodeToString([]byte("OPAQUE_GEMINI_SIG"))
	tu := map[string]any{
		"type":  "tool_use",
		"id":    smuggledID,
		"name":  "bash",
		"input": map[string]any{"command": "ls"},
	}

	// Turn 3: client sends history back. Router translates Anthropic → Gemini.
	req := map[string]any{
		"model": "claude-opus-4-7",
		"messages": []any{
			map[string]any{"role": "user", "content": "list files"},
			map[string]any{"role": "assistant", "content": []any{tu}},
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": smuggledID, "content": "f1\nf2"},
			}},
		},
	}
	reqBody, err := json.Marshal(req)
	require.NoError(t, err)
	env, err := translate.ParseAnthropic(reqBody)
	require.NoError(t, err)
	prep, err := env.PrepareGemini(http.Header{}, translate.EmitOptions{TargetModel: "gemini-3.1-pro-preview"})
	require.NoError(t, err)

	out := mustUnmarshal(t, prep.Body)
	contents := mustAs[[]any](t, out["contents"])
	require.Len(t, contents, 3)
	model := mustAs[map[string]any](t, contents[1])
	parts := mustAs[[]any](t, model["parts"])
	p := mustAs[map[string]any](t, parts[0])
	// The signature is what Gemini 3.x rejects requests for when missing.
	assert.Equal(t, "OPAQUE_GEMINI_SIG", p["thoughtSignature"])
	fc := mustAs[map[string]any](t, p["functionCall"])
	assert.Equal(t, "bash", fc["name"])
	// And the functionResponse must still resolve the name from the smuggled id.
	tr := mustAs[map[string]any](t, contents[2])
	frPart := mustAs[map[string]any](t, mustAs[[]any](t, tr["parts"])[0])
	fr := mustAs[map[string]any](t, frPart["functionResponse"])
	assert.Equal(t, "bash", fr["name"])
}

// Same as above but via OpenAI Chat Completions wire format; tool_call.id is
// the round-trip channel and is preserved by every Chat Completions client.
func TestPrepareGemini_FromOpenAI_ToolCallSignatureSurvivesUnknownFieldStripping(t *testing.T) {
	geminiResp := []byte(`{
		"candidates":[{"content":{"role":"model","parts":[
			{"functionCall":{"name":"bash","args":{"command":"ls"}},
			 "thoughtSignature":"OPAQUE_SIG_2"}
		]},"finishReason":"STOP"}]
	}`)
	openaiOut, err := translate.GeminiToOpenAIResponse(geminiResp, "gemini-3.1-pro-preview")
	require.NoError(t, err)
	resp := mustUnmarshal(t, openaiOut)
	choice := mustAs[map[string]any](t, mustAs[[]any](t, resp["choices"])[0])
	tc := mustAs[map[string]any](t, mustAs[[]any](t, mustAs[map[string]any](t, choice["message"])["tool_calls"])[0])
	smuggledID := mustAs[string](t, tc["id"])
	require.Contains(t, smuggledID, "__thought__")

	// Strip the explicit signature fields on both the tool_call and the
	// nested function — simulating a client that only preserves spec fields.
	delete(tc, "thought_signature")
	fn := mustAs[map[string]any](t, tc["function"])
	delete(fn, "thought_signature")

	req := map[string]any{
		"model": "gpt-x",
		"messages": []any{
			map[string]any{"role": "user", "content": "list files"},
			map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{tc}},
			map[string]any{"role": "tool", "tool_call_id": smuggledID, "content": "f1\nf2"},
		},
	}
	reqBody, err := json.Marshal(req)
	require.NoError(t, err)
	env, err := translate.ParseOpenAI(reqBody)
	require.NoError(t, err)
	prep, err := env.PrepareGemini(http.Header{}, translate.EmitOptions{TargetModel: "gemini-3.1-pro-preview"})
	require.NoError(t, err)
	out := mustUnmarshal(t, prep.Body)
	contents := mustAs[[]any](t, out["contents"])
	require.GreaterOrEqual(t, len(contents), 2)
	model := mustAs[map[string]any](t, contents[1])
	parts := mustAs[[]any](t, model["parts"])
	p := mustAs[map[string]any](t, parts[0])
	assert.Equal(t, "OPAQUE_SIG_2", p["thoughtSignature"])
}

// ----- Streaming -----

func TestGeminiStream_TextDeltas(t *testing.T) {
	rec := httptest.NewRecorder()
	tr := translate.NewGeminiToOpenAISSETranslator(rec, "gemini-x", nil)
	tr.Header().Set("Content-Type", "text/event-stream")
	tr.WriteHeader(http.StatusOK)

	chunks := []string{
		`data: {"candidates":[{"content":{"parts":[{"text":"hi "}]}}]}` + "\n\n",
		`data: {"candidates":[{"content":{"parts":[{"text":"world"}]}}]}` + "\n\n",
		`data: {"candidates":[{"content":{"parts":[]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2,"totalTokenCount":5}}` + "\n\n",
	}
	for _, c := range chunks {
		_, err := tr.Write([]byte(c))
		require.NoError(t, err)
	}
	require.NoError(t, tr.Finalize())

	body := rec.Body.String()
	assert.Contains(t, body, `"role":"assistant"`)
	assert.Contains(t, body, `"content":"hi "`)
	assert.Contains(t, body, `"content":"world"`)
	assert.Contains(t, body, `"finish_reason":"stop"`)
	assert.Contains(t, body, `"prompt_tokens":3`)
	assert.Contains(t, body, "data: [DONE]")
}

func TestGeminiStream_FunctionCallChunkPreservesThoughtSignature(t *testing.T) {
	rec := httptest.NewRecorder()
	tr := translate.NewGeminiToOpenAISSETranslator(rec, "gemini-x", nil)
	tr.Header().Set("Content-Type", "text/event-stream")
	tr.WriteHeader(http.StatusOK)

	chunk := `data: {"candidates":[{"content":{"parts":[{"functionCall":{"name":"bash","args":{"command":"ls"}},"thoughtSignature":"SIG"}]},"finishReason":"STOP"}]}` + "\n\n"
	_, err := tr.Write([]byte(chunk))
	require.NoError(t, err)
	require.NoError(t, tr.Finalize())

	body := rec.Body.String()
	assert.Contains(t, body, `"tool_calls"`)
	assert.Contains(t, body, `"name":"bash"`)
	assert.NotContains(t, body, `"thought_signature"`, "off-spec field must not be emitted")
	encoded := base64.RawURLEncoding.EncodeToString([]byte("SIG"))
	assert.Contains(t, body, encoded, "signature rides in the tool-call id")
	assert.Contains(t, body, `"finish_reason":"tool_calls"`)
	assert.Contains(t, body, "data: [DONE]")
}

func TestGeminiStream_NonStreamingErrorTranslated(t *testing.T) {
	rec := httptest.NewRecorder()
	tr := translate.NewGeminiToOpenAISSETranslator(rec, "gemini-x", nil)
	tr.Header().Set("Content-Type", "application/json")
	tr.WriteHeader(http.StatusBadRequest)
	_, err := tr.Write([]byte(`{"error":{"code":400,"message":"bad","status":"INVALID_ARGUMENT"}}`))
	require.NoError(t, err)
	require.NoError(t, tr.Finalize())

	resp := mustUnmarshal(t, rec.Body.Bytes())
	e := mustAs[map[string]any](t, resp["error"])
	assert.Equal(t, "bad", e["message"])
	assert.Equal(t, "invalid_argument", e["type"])
}

// ----- helpers -----

func mustUnmarshal(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var out map[string]any
	require.NoError(t, json.Unmarshal(b, &out))
	return out
}

func must(t *testing.T, m map[string]any, k string) any {
	t.Helper()
	v, ok := m[k]
	require.True(t, ok, "missing key %s", k)
	return v
}

func TestPrepareGemini_StripsJSONSchemaFieldsGoogleRejects(t *testing.T) {
	// Regression (#62, re-regressed by #764's switch to an allowlist and
	// fixed again here): Claude Code tool defs — including the Agent/Task
	// subagent tool itself — include JSON Schema fields (`$schema`,
	// `additionalProperties`, `propertyNames`) Google's function-calling API
	// rejects outright. These must be silently dropped, not turned into a
	// hard tool-declaration failure: #764 briefly did the latter, which
	// 502'd every real Claude Code turn against any Gemini 3.x model that
	// included the Agent tool (caught 2026-07-21 onboarding gemini-3.6-flash
	// / gemini-3.5-flash-lite, but affected already-deployed Gemini models
	// too).
	body := []byte(`{
		"messages": [{"role":"user","content":"hi"}],
		"tools": [{
			"name":"WebFetch",
			"description":"Fetch a URL",
			"input_schema":{
				"$schema":"http://json-schema.org/draft-07/schema#",
				"type":"object",
				"additionalProperties":false,
				"properties":{
					"url":{"type":"string","description":"URL to fetch"},
					"params":{
						"type":"object",
						"additionalProperties":{"type":"string"},
						"propertyNames":{"pattern":"^[A-Za-z]+$"}
					}
				},
				"required":["url"]
			}
		}]
	}`)
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	prep, err := env.PrepareGemini(http.Header{}, translate.EmitOptions{})
	require.NoError(t, err)

	out := mustUnmarshal(t, prep.Body)
	tools := mustAs[[]any](t, out["tools"])
	require.Len(t, tools, 1)
	decls := mustAs[[]any](t, mustAs[map[string]any](t, tools[0])["functionDeclarations"])
	require.Len(t, decls, 1)
	params := mustAs[map[string]any](t, mustAs[map[string]any](t, decls[0])["parameters"])

	// The rejected keys are gone at every level...
	assert.NotContains(t, params, "$schema")
	assert.NotContains(t, params, "additionalProperties")
	nested := mustAs[map[string]any](t, mustAs[map[string]any](t, params["properties"])["params"])
	assert.NotContains(t, nested, "additionalProperties")
	assert.NotContains(t, nested, "propertyNames")

	// ...but the schema's actual meaning survives.
	assert.Equal(t, "object", params["type"])
	assert.Equal(t, []any{"url"}, params["required"])
	props := mustAs[map[string]any](t, params["properties"])
	assert.Equal(t, "string", mustAs[map[string]any](t, props["url"])["type"])
}

func TestPrepareGemini_WidensExclusiveBoundsToInclusive(t *testing.T) {
	// Regression: exclusiveMinimum/exclusiveMaximum are unsupported by
	// Gemini's function-calling schema; widen to inclusive bounds.
	body := []byte(`{
		"messages": [{"role":"user","content":"hi"}],
		"tools": [{
			"name":"Read",
			"input_schema":{
				"type":"object",
				"properties":{
					"limit":{"type":"integer","exclusiveMinimum":0},
					"offset":{"type":"integer","exclusiveMaximum":100,"minimum":0},
					"page":{"type":"integer","exclusiveMinimum":0,"minimum":5},
					"strict":{"type":"integer","exclusiveMinimum":5,"minimum":0}
				}
			}
		}]
	}`)
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	prep, err := env.PrepareGemini(http.Header{}, translate.EmitOptions{})
	require.NoError(t, err)

	out := mustUnmarshal(t, prep.Body)
	decls := mustAs[[]any](t, mustAs[map[string]any](t, mustAs[[]any](t, out["tools"])[0])["functionDeclarations"])
	props := mustAs[map[string]any](t, mustAs[map[string]any](t, mustAs[map[string]any](t, decls[0])["parameters"])["properties"])

	limit := mustAs[map[string]any](t, props["limit"])
	assert.NotContains(t, limit, "exclusiveMinimum")
	assert.InDelta(t, float64(0), limit["minimum"], 1e-9, "exclusiveMinimum widens to the same-valued inclusive minimum")

	// exclusiveMaximum widens to maximum with the same value when there's no
	// sibling maximum to preserve; the untouched sibling minimum survives.
	offset := mustAs[map[string]any](t, props["offset"])
	assert.NotContains(t, offset, "exclusiveMaximum")
	assert.InDelta(t, float64(0), offset["minimum"], 1e-9)
	assert.InDelta(t, float64(100), offset["maximum"], 1e-9)

	// When the explicit sibling is the tighter bound, it wins over the
	// widened (weaker) exclusive value.
	page := mustAs[map[string]any](t, props["page"])
	assert.NotContains(t, page, "exclusiveMinimum")
	assert.InDelta(t, float64(5), page["minimum"], 1e-9, "explicit minimum (5) is tighter than widened exclusiveMinimum (0)")

	// When the exclusive bound is the tighter one, the widened value wins
	// over the weaker explicit sibling instead of being discarded.
	strict := mustAs[map[string]any](t, props["strict"])
	assert.NotContains(t, strict, "exclusiveMinimum")
	assert.InDelta(t, float64(5), strict["minimum"], 1e-9, "widened exclusiveMinimum (5) is tighter than explicit minimum (0)")
}

func TestPrepareGemini_DanglingRequiredDegradesToNoParameters(t *testing.T) {
	// Gemini 400s when "required" names a property not in "properties" —
	// valid JSON Schema, so MCP tool schemas can carry it; the backstop
	// degrades that tool instead of failing the whole request.
	body := []byte(`{
		"messages": [{"role":"user","content":"hi"}],
		"tools": [{
			"name":"DoThing",
			"description":"d",
			"input_schema":{
				"type":"object",
				"properties":{"x":{"type":"string"}},
				"required":["x","ghost"]
			}
		},{
			"name":"Healthy",
			"description":"d",
			"input_schema":{
				"type":"object",
				"properties":{"a":{"type":"string"}},
				"required":["a"]
			}
		}]
	}`)
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	prep, err := env.PrepareGemini(http.Header{}, translate.EmitOptions{})
	require.NoError(t, err)

	out := mustUnmarshal(t, prep.Body)
	decls := mustAs[[]any](t, mustAs[map[string]any](t, mustAs[[]any](t, out["tools"])[0])["functionDeclarations"])
	require.Len(t, decls, 2)
	byName := make(map[string]map[string]any, len(decls))
	for _, d := range decls {
		decl := mustAs[map[string]any](t, d)
		byName[mustAs[string](t, decl["name"])] = decl
	}
	assert.NotContains(t, byName["DoThing"], "parameters", "dangling required degrades that tool only")
	assert.Contains(t, byName["Healthy"], "parameters")
}

func TestPrepareGemini_PreservesPropertyNamedRequired(t *testing.T) {
	// A parameter literally named "required" is a property schema, not the
	// "required" keyword, and must survive pruning untouched.
	body := []byte(`{
		"messages": [{"role":"user","content":"hi"}],
		"tools": [{
			"name":"DoThing",
			"description":"d",
			"input_schema":{
				"type":"object",
				"properties":{
					"required":{
						"type":"object",
						"properties":{"inner":{"type":"string"}},
						"required":["inner"]
					}
				},
				"required":["required"]
			}
		}]
	}`)
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	prep, err := env.PrepareGemini(http.Header{}, translate.EmitOptions{})
	require.NoError(t, err)

	out := mustUnmarshal(t, prep.Body)
	params := mustAs[map[string]any](t, mustAs[map[string]any](t, mustAs[[]any](t, mustAs[map[string]any](t, mustAs[[]any](t, out["tools"])[0])["functionDeclarations"])[0])["parameters"])

	// The schema-level "required" naming the real "required" property is kept.
	assert.Equal(t, []any{"required"}, params["required"])
	// The property named "required" is preserved as an object schema, not
	// rewritten into an array or deleted.
	prop, ok := mustAs[map[string]any](t, params["properties"])["required"].(map[string]any)
	require.True(t, ok, `parameter named "required" must remain a schema object`)
	assert.Equal(t, "object", prop["type"])
	// Its own nested "required" keyword (valid: "inner" exists) is intact.
	assert.Equal(t, []any{"inner"}, prop["required"])
}

func TestPrepareGemini_VendorExtensionKeysDegradeToNoParameters(t *testing.T) {
	// MCP schemas derived from Google APIs embed vendor extensions
	// (`x-google-*`) at every level; Gemini 400s on unknown fields,
	// so the per-tool backstop degrades rather than 502ing.
	body := []byte(`{
		"messages": [{"role":"user","content":"hi"}],
		"tools": [{
			"name":"sheets_get",
			"input_schema":{
				"$schema":"http://json-schema.org/draft-07/schema#",
				"$id":"urn:weave:test",
				"type":"object",
				"x-google-resource":"sheet",
				"properties":{
					"range":{
						"type":"string",
						"x-google-enum-descriptions":["A","B"],
						"enum":["A1","B2"]
					},
					"nested":{
						"type":"object",
						"x-google-foo":"bar",
						"properties":{
							"leaf":{"type":"string","x-vendor-thing":"y"}
						}
					}
				}
			}
		}]
	}`)
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	prep, err := env.PrepareGemini(http.Header{}, translate.EmitOptions{})
	require.NoError(t, err)

	out := mustUnmarshal(t, prep.Body)
	decl := mustAs[map[string]any](t, mustAs[[]any](t, mustAs[map[string]any](t, mustAs[[]any](t, out["tools"])[0])["functionDeclarations"])[0])
	assert.Equal(t, "sheets_get", decl["name"])
	assert.NotContains(t, decl, "parameters")
}

func TestSanitizeSchemaForGemini_PreservesSupportedFields(t *testing.T) {
	// Defense-in-depth: exhaustively confirms which keys survive sanitization.
	body := []byte(`{
		"messages": [{"role":"user","content":"hi"}],
		"tools": [{
			"name":"Edit",
			"input_schema":{
				"type":"object",
				"description":"Edit a file",
				"properties":{
					"path":{"type":"string","format":"date-time"},
					"mode":{"type":"string","enum":["replace","append"]},
					"line":{"type":"integer","nullable":true}
				},
				"required":["path","mode"]
			}
		}]
	}`)
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	prep, err := env.PrepareGemini(http.Header{}, translate.EmitOptions{})
	require.NoError(t, err)

	out := mustUnmarshal(t, prep.Body)
	params := mustAs[map[string]any](t, mustAs[map[string]any](t, mustAs[[]any](t, mustAs[map[string]any](t, mustAs[[]any](t, out["tools"])[0])["functionDeclarations"])[0])["parameters"])

	assert.Equal(t, "object", params["type"])
	assert.Equal(t, "Edit a file", params["description"])
	props := mustAs[map[string]any](t, params["properties"])
	assert.Equal(t, "date-time", mustAs[map[string]any](t, props["path"])["format"], "supported format must survive")
	assert.Equal(t, []any{"replace", "append"}, mustAs[map[string]any](t, props["mode"])["enum"], "enum must survive")
	assert.Equal(t, true, mustAs[map[string]any](t, props["line"])["nullable"], "nullable must survive")
	assert.Equal(t, []any{"path", "mode"}, params["required"], "required must survive")
}

func TestPrepareGemini_DropsUnsupportedFormatValues(t *testing.T) {
	// Regression: Gemini's Schema only accepts a narrow "format" set (strings:
	// enum, date-time; numbers: float/double/int32/int64). MCP schemas routinely
	// carry "uri"/"email"/"uuid" etc., which Gemini 400s on; drop unsupported
	// values but keep the rest of the property schema intact.
	body := []byte(`{
		"messages": [{"role":"user","content":"hi"}],
		"tools": [{
			"name":"submit",
			"input_schema":{
				"type":"object",
				"properties":{
					"homepage":{"type":"string","format":"uri","description":"site"},
					"contact":{"type":"string","format":"email"},
					"when":{"type":"string","format":"date-time"},
					"score":{"type":"number","format":"double"}
				},
				"required":["homepage"]
			}
		}]
	}`)
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	prep, err := env.PrepareGemini(http.Header{}, translate.EmitOptions{})
	require.NoError(t, err)

	out := mustUnmarshal(t, prep.Body)
	decls := mustAs[[]any](t, mustAs[map[string]any](t, mustAs[[]any](t, out["tools"])[0])["functionDeclarations"])
	props := mustAs[map[string]any](t, mustAs[map[string]any](t, mustAs[map[string]any](t, decls[0])["parameters"])["properties"])

	homepage := mustAs[map[string]any](t, props["homepage"])
	assert.Equal(t, "string", homepage["type"])
	assert.Equal(t, "site", homepage["description"])
	assert.NotContains(t, homepage, "format", "unsupported format value 'uri' must be dropped silently")

	contact := mustAs[map[string]any](t, props["contact"])
	assert.Equal(t, "string", contact["type"])
	assert.NotContains(t, contact, "format", "unsupported format value 'email' must be dropped silently")

	// Supported formats survive untouched.
	when := mustAs[map[string]any](t, props["when"])
	assert.Equal(t, "date-time", when["format"])
	score := mustAs[map[string]any](t, props["score"])
	assert.Equal(t, "double", score["format"])
}

func TestPrepareGemini_ItemsFalseDegradesToNoParameters(t *testing.T) {
	// MCP schemas use boolean `items`; Gemini's proto Schema.items rejects
	// booleans. `items: true` collapses to the permissive empty schema;
	// `items: false` (unsatisfiable) stays unrepresentable — backstop degrades.
	body := []byte(`{
		"messages": [{"role":"user","content":"hi"}],
		"tools": [{
			"name":"create_dashboard",
			"input_schema":{
				"type":"object",
				"properties":{
					"args":{
						"type":"array",
						"items":true
					},
					"links":{
						"type":"array",
						"items":{"type":"string"}
					}
				}
			}
		},{
			"name":"toggle_widgets",
			"input_schema":{
				"type":"object",
				"properties":{
					"disabled":{
						"type":"array",
						"items":false
					}
				}
			}
		}]
	}`)
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	prep, err := env.PrepareGemini(http.Header{}, translate.EmitOptions{})
	require.NoError(t, err)

	out := mustUnmarshal(t, prep.Body)
	decls := mustAs[[]any](t, mustAs[map[string]any](t, mustAs[[]any](t, out["tools"])[0])["functionDeclarations"])
	require.Len(t, decls, 2)
	byName := make(map[string]map[string]any, len(decls))
	for _, d := range decls {
		decl := mustAs[map[string]any](t, d)
		byName[mustAs[string](t, decl["name"])] = decl
	}

	// items:true collapses to the permissive empty schema.
	params := mustAs[map[string]any](t, byName["create_dashboard"]["parameters"])
	args := mustAs[map[string]any](t, mustAs[map[string]any](t, params["properties"])["args"])
	assert.Equal(t, map[string]any{}, args["items"])

	// items:false degrades only the tool that carries it.
	assert.NotContains(t, byName["toggle_widgets"], "parameters")
}

func TestPrepareGemini_CollapsesArrayTypeField(t *testing.T) {
	// Regression: MCP schemas (e.g. Pylon) use array-typed "type" like
	// ["array","null"]; Gemini expects a single type plus a "nullable" bool.
	body := []byte(`{
		"messages": [{"role":"user","content":"hi"}],
		"tools": [{
			"name":"create_issue",
			"input_schema":{
				"type":"object",
				"properties":{
					"tags":{
						"type":["array","null"],
						"description":"optional tags",
						"items":{"type":"string"}
					},
					"title":{"type":"string"},
					"score":{"type":["number","null"]}
				}
			}
		}]
	}`)
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	prep, err := env.PrepareGemini(http.Header{}, translate.EmitOptions{})
	require.NoError(t, err)

	out := mustUnmarshal(t, prep.Body)
	params := mustAs[map[string]any](t, mustAs[map[string]any](t, mustAs[[]any](t, mustAs[map[string]any](t, mustAs[[]any](t, out["tools"])[0])["functionDeclarations"])[0])["parameters"])
	props := mustAs[map[string]any](t, params["properties"])

	tags := mustAs[map[string]any](t, props["tags"])
	assert.Equal(t, "array", tags["type"], "array-type type must collapse to primary non-null type")
	assert.Equal(t, true, tags["nullable"], "nullable must be set when null appears in type array")
	assert.NotNil(t, tags["items"], "items must survive collapseTypeArray")

	assert.Equal(t, "string", mustAs[map[string]any](t, props["title"])["type"], "single-string type must be unchanged")

	score := mustAs[map[string]any](t, props["score"])
	assert.Equal(t, "number", score["type"], "number type must survive")
	assert.Equal(t, true, score["nullable"], "nullable must be set for [number, null]")
}

func TestPrepareGemini_PreservesArrayMissingItems(t *testing.T) {
	// A missing items constraint accepts arbitrary item values. Inventing an
	// item schema would narrow the client contract, so it must remain absent.
	body := []byte(`{
		"messages": [{"role":"user","content":"hi"}],
		"tools": [{
			"name":"odd-tool",
			"input_schema":{
				"type":"object",
				"properties":{
					"tools":{"type":"array","description":"a list of tools"},
					"nested":{
						"type":"object",
						"properties":{
							"items":{"type":"array"}
						}
					}
				}
			}
		}]
	}`)
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	prep, err := env.PrepareGemini(http.Header{}, translate.EmitOptions{})
	require.NoError(t, err)

	out := mustUnmarshal(t, prep.Body)
	params := mustAs[map[string]any](t, mustAs[map[string]any](t, mustAs[[]any](t, mustAs[map[string]any](t, mustAs[[]any](t, out["tools"])[0])["functionDeclarations"])[0])["parameters"])
	props := mustAs[map[string]any](t, params["properties"])

	toolsField := mustAs[map[string]any](t, props["tools"])
	assert.Equal(t, "array", toolsField["type"])
	assert.NotContains(t, toolsField, "items")

	nestedItems := mustAs[map[string]any](t, mustAs[map[string]any](t, mustAs[map[string]any](t, props["nested"])["properties"])["items"])
	assert.Equal(t, "array", nestedItems["type"])
	assert.NotContains(t, nestedItems, "items")
}

func TestPrepareGemini_DropsEmptyStringEnumEntries(t *testing.T) {
	// Google 400s on empty-string enum members ("enum[i]: cannot be empty").
	// Drop them; drop the enum entirely when nothing remains.
	body := []byte(`{
		"messages": [{"role":"user","content":"hi"}],
		"tools": [{
			"name":"odd-tool",
			"input_schema":{
				"type":"object",
				"properties":{
					"operator":{"type":"string","enum":["","eq","neq","gt"]},
					"all_empty":{"type":"string","enum":["",""]},
					"normal":{"type":"string","enum":["a","b"]}
				}
			}
		}]
	}`)
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	prep, err := env.PrepareGemini(http.Header{}, translate.EmitOptions{})
	require.NoError(t, err)

	out := mustUnmarshal(t, prep.Body)
	params := mustAs[map[string]any](t, mustAs[map[string]any](t, mustAs[[]any](t, mustAs[map[string]any](t, mustAs[[]any](t, out["tools"])[0])["functionDeclarations"])[0])["parameters"])
	props := mustAs[map[string]any](t, params["properties"])

	operator := mustAs[map[string]any](t, props["operator"])
	assert.Equal(t, []any{"eq", "neq", "gt"}, operator["enum"], "empty string must be filtered out")

	allEmpty := mustAs[map[string]any](t, props["all_empty"])
	assert.NotContains(t, allEmpty, "enum", "enum with only empty strings must be dropped entirely")

	normal := mustAs[map[string]any](t, props["normal"])
	assert.Equal(t, []any{"a", "b"}, normal["enum"], "well-formed enums must pass through unchanged")
}

func TestPrepareGemini_CollapsesNullableAnyOfDates(t *testing.T) {
	// MCP date fields commonly encode optionality as anyOf[string, null]. Gemini
	// then 400s on the null branch as any_of[1].enum[0]: cannot be empty.
	body := []byte(`{
		"messages": [{"role":"user","content":"hi"}],
		"tools": [{
			"name":"update_issue",
			"input_schema":{
				"type":"object",
				"properties":{
					"due_date":{"anyOf":[{"type":"string","format":"date-time"},{"type":"null"}]},
					"start_date":{"anyOf":[{"type":"string"},{"enum":[""]}]},
					"priority":{"anyOf":[{"enum":["low","high"]},{"type":"null"}]},
					"title":{"minLength":5,"anyOf":[{"type":"string","minLength":10}]}
				}
			}
		}]
	}`)
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	prep, err := env.PrepareGemini(http.Header{}, translate.EmitOptions{})
	require.NoError(t, err)

	out := mustUnmarshal(t, prep.Body)
	params := mustAs[map[string]any](t, mustAs[map[string]any](t, mustAs[[]any](t, mustAs[map[string]any](t, mustAs[[]any](t, out["tools"])[0])["functionDeclarations"])[0])["parameters"])
	props := mustAs[map[string]any](t, params["properties"])

	due := mustAs[map[string]any](t, props["due_date"])
	assert.Equal(t, "string", due["type"])
	assert.Equal(t, "date-time", due["format"])
	assert.Equal(t, true, due["nullable"])
	assert.NotContains(t, due, "anyOf")
	assert.NotContains(t, due, "enum")

	start := mustAs[map[string]any](t, props["start_date"])
	assert.Equal(t, "string", start["type"])
	assert.Equal(t, true, start["nullable"])
	assert.NotContains(t, start, "anyOf")
	assert.NotContains(t, start, "enum")

	priority := mustAs[map[string]any](t, props["priority"])
	assert.Equal(t, "string", priority["type"])
	assert.Equal(t, true, priority["nullable"])
	assert.Equal(t, []any{"low", "high"}, priority["enum"])
	assert.NotContains(t, priority, "anyOf")

	title := mustAs[map[string]any](t, props["title"])
	assert.Equal(t, "string", title["type"])
	assert.InDelta(t, float64(10), title["minLength"], 1e-9)
	assert.NotContains(t, title, "anyOf")
}

func TestPrepareGemini_PreservesNumericAnyOfEnum(t *testing.T) {
	body := []byte(`{
		"messages": [{"role":"user","content":"hi"}],
		"tools": [{
			"name":"set_priority",
			"input_schema":{
				"type":"object",
				"properties":{
					"priority":{"anyOf":[{"enum":[0,1,2]},{"type":"null"}]}
				}
			}
		}]
	}`)
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	prep, err := env.PrepareGemini(http.Header{}, translate.EmitOptions{})
	require.NoError(t, err)

	out := mustUnmarshal(t, prep.Body)
	params := mustAs[map[string]any](t, mustAs[map[string]any](t, mustAs[[]any](t, mustAs[map[string]any](t, mustAs[[]any](t, out["tools"])[0])["functionDeclarations"])[0])["parameters"])
	priority := mustAs[map[string]any](t, mustAs[map[string]any](t, params["properties"])["priority"])
	assert.Equal(t, true, priority["nullable"])
	assert.NotContains(t, priority, "enum", "non-string enums still drop, but the branch itself must survive")
	assert.NotContains(t, priority, "type", "dropping a numeric enum must not invent type:string")
}

func TestPrepareGemini_KeepsTypeArrayAnyOfBranch(t *testing.T) {
	body := []byte(`{
		"messages": [{"role":"user","content":"hi"}],
		"tools": [{
			"name":"update_issue",
			"input_schema":{
				"type":"object",
				"properties":{
					"due_date":{"anyOf":[{"type":["string","null"],"format":"date-time"}]}
				}
			}
		}]
	}`)
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	prep, err := env.PrepareGemini(http.Header{}, translate.EmitOptions{})
	require.NoError(t, err)

	out := mustUnmarshal(t, prep.Body)
	params := mustAs[map[string]any](t, mustAs[map[string]any](t, mustAs[[]any](t, mustAs[map[string]any](t, mustAs[[]any](t, out["tools"])[0])["functionDeclarations"])[0])["parameters"])
	due := mustAs[map[string]any](t, mustAs[map[string]any](t, params["properties"])["due_date"])
	assert.Equal(t, "string", due["type"])
	assert.Equal(t, "date-time", due["format"])
	assert.Equal(t, true, due["nullable"])
}

func TestPrepareGemini_DropsNonStringEnums(t *testing.T) {
	// Gemini types every enum member as TYPE_STRING and 400s on anything else.
	// Dropping is lossless: toolcheck enforces against the original schema.
	body := []byte(`{
		"messages": [{"role":"user","content":"hi"}],
		"tools": [{
			"name":"lookback",
			"input_schema":{
				"type":"object",
				"properties":{
					"untyped_numeric":{"enum":[7,14,30]},
					"typed_numeric":{"type":"integer","enum":[7,14,30]},
					"mixed":{"enum":["all",30]},
					"boolean":{"type":"boolean","enum":[true,false]},
					"nullable":{"type":"string","nullable":true,"enum":["a",null]},
					"format_enum":{"type":"string","format":"enum","enum":[1,2]},
					"strings":{"type":"string","enum":["day","week"]}
				}
			}
		}]
	}`)
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	prep, err := env.PrepareGemini(http.Header{}, translate.EmitOptions{})
	require.NoError(t, err, "an unrepresentable enum must not fail the whole tool")

	out := mustUnmarshal(t, prep.Body)
	params := mustAs[map[string]any](t, mustAs[map[string]any](t, mustAs[[]any](t, mustAs[map[string]any](t, mustAs[[]any](t, out["tools"])[0])["functionDeclarations"])[0])["parameters"])
	props := mustAs[map[string]any](t, params["properties"])

	for _, name := range []string{"untyped_numeric", "typed_numeric", "mixed", "boolean", "nullable", "format_enum"} {
		assert.NotContains(t, props[name], "enum", "%s: non-string enum must be dropped", name)
	}

	// Only the enum is dropped — the rest of each property survives.
	assert.Equal(t, "integer", mustAs[map[string]any](t, props["typed_numeric"])["type"])
	assert.Equal(t, "boolean", mustAs[map[string]any](t, props["boolean"])["type"])

	// format:"enum" is meaningless once the enum is gone, so it goes too.
	assert.NotContains(t, props["format_enum"], "format")

	assert.Equal(t, []any{"day", "week"}, mustAs[map[string]any](t, props["strings"])["enum"],
		"all-string enums must pass through unchanged")
}

func TestPrepareGemini_UserDefinedPropertyNamedProperties(t *testing.T) {
	// A user-defined property named "properties" must not be mistaken for the
	// JSON Schema "properties" keyword. Its value schema must still be
	// filtered — and additionalProperties within it silently dropped, not
	// treated as a rejection (see TestPrepareGemini_StripsJSONSchemaFieldsGoogleRejects).
	body := []byte(`{
		"messages": [{"role":"user","content":"hi"}],
		"tools": [{
			"name":"track_event",
			"input_schema":{
				"type":"object",
				"properties":{
					"eventName":{"type":"string"},
					"properties":{
						"type":"object",
						"additionalProperties":true,
						"description":"Additional properties"
					}
				}
			}
		}]
	}`)
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	prep, err := env.PrepareGemini(http.Header{}, translate.EmitOptions{})
	require.NoError(t, err)

	out := mustUnmarshal(t, prep.Body)
	decls := mustAs[[]any](t, mustAs[map[string]any](t, mustAs[[]any](t, out["tools"])[0])["functionDeclarations"])
	params := mustAs[map[string]any](t, mustAs[map[string]any](t, decls[0])["parameters"])
	properties := mustAs[map[string]any](t, params["properties"])

	nested, ok := properties["properties"].(map[string]any)
	require.True(t, ok, `the user-defined "properties" key must survive as an object schema`)
	assert.Equal(t, "object", nested["type"])
	assert.Equal(t, "Additional properties", nested["description"])
	assert.NotContains(t, nested, "additionalProperties")
}

func TestPrepareGemini_GeminiFormatSanitizesTools(t *testing.T) {
	// The same-format (FormatGemini) path must also sanitize tool schemas —
	// additionalProperties dropped silently, not rejected (see
	// TestPrepareGemini_StripsJSONSchemaFieldsGoogleRejects).
	body := []byte(`{
		"model": "gemini-3.1-pro-preview",
		"stream": false,
		"contents": [{"role":"user","parts":[{"text":"hi"}]}],
		"tools": [{
			"functionDeclarations": [{
				"name":"test",
				"parameters":{
					"type":"object",
					"additionalProperties":false,
					"properties":{"x":{"type":"string"}}
				}
			}]
		}]
	}`)
	env, err := translate.ParseGemini(body)
	require.NoError(t, err)
	prep, err := env.PrepareGemini(http.Header{}, translate.EmitOptions{})
	require.NoError(t, err)

	out := mustUnmarshal(t, prep.Body)
	decls := mustAs[[]any](t, mustAs[map[string]any](t, mustAs[[]any](t, out["tools"])[0])["functionDeclarations"])
	params := mustAs[map[string]any](t, mustAs[map[string]any](t, decls[0])["parameters"])
	assert.NotContains(t, params, "additionalProperties")
	assert.Equal(t, "object", params["type"])
}

func TestPrepareGemini_GeminiFormatInlinesSchemaRefs(t *testing.T) {
	// Same-format Gemini path must inline $ref/$defs before sanitization,
	// otherwise Gemini's allowlist strips them silently.
	body := []byte(`{
		"model": "gemini-3.1-pro-preview",
		"contents": [{"role":"user","parts":[{"text":"hi"}]}],
		"tools": [{
			"functionDeclarations": [{
				"name":"test",
				"parameters":{
					"type":"object",
					"properties":{
						"item": {"$ref": "#/$defs/Item"}
					},
					"$defs": {
						"Item": {"type":"object","properties":{"name":{"type":"string"}}}
					}
				}
			}]
		}]
	}`)
	env, err := translate.ParseGemini(body)
	require.NoError(t, err)
	prep, err := env.PrepareGemini(http.Header{}, translate.EmitOptions{})
	require.NoError(t, err)

	out := mustUnmarshal(t, prep.Body)
	params := mustAs[map[string]any](t, mustAs[map[string]any](t, mustAs[[]any](t, mustAs[map[string]any](t, mustAs[[]any](t, out["tools"])[0])["functionDeclarations"])[0])["parameters"])
	item := mustAs[map[string]any](t, mustAs[map[string]any](t, params["properties"])["item"])
	assert.NotContains(t, item, "$ref", "$ref must be inlined, not left as pointer")
	assert.Equal(t, "object", item["type"], "inlined item should have type:object")
	assert.NotContains(t, params, "$defs", "$defs must be removed after inlining")
}

func TestPrepareGemini_ConvertsBoolPropertySchemas(t *testing.T) {
	// JSON Schema allows boolean schemas as property values (true = any type).
	// Gemini's proto Schema rejects them. They must be converted to empty Schema.
	body := []byte(`{
		"messages": [{"role":"user","content":"hi"}],
		"tools": [{
			"name":"create_dashboard",
			"input_schema":{
				"type":"object",
				"properties":{
					"queryData":true,
					"softMax":true,
					"realProp":{"type":"string"}
				}
			}
		}]
	}`)
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	prep, err := env.PrepareGemini(http.Header{}, translate.EmitOptions{})
	require.NoError(t, err)

	out := mustUnmarshal(t, prep.Body)
	params := mustAs[map[string]any](t, mustAs[map[string]any](t, mustAs[[]any](t, mustAs[map[string]any](t, mustAs[[]any](t, out["tools"])[0])["functionDeclarations"])[0])["parameters"])
	props := mustAs[map[string]any](t, params["properties"])

	// Boolean true → empty Schema {}
	qd, ok := props["queryData"].(map[string]any)
	require.True(t, ok, "queryData=true should become empty schema object, got %T", props["queryData"])
	assert.NotNil(t, qd)

	sm, ok := props["softMax"].(map[string]any)
	require.True(t, ok, "softMax=true should become empty schema object, got %T", props["softMax"])
	assert.NotNil(t, sm)

	// Real property must survive
	rp, ok := props["realProp"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "string", rp["type"])
}

func TestPrepareGemini_ToolAllOfAnnotationRegression(t *testing.T) {
	// Regression (prod shape: SendMessage.to): allOf branches with differing
	// descriptions must widen (annotation merge), not fail the whole request.
	// Uses a non-CC-internal tool name so the claudecode filter keeps it.
	body := []byte(`{
		"messages": [{"role":"user","content":"hi"}],
		"tools": [{
			"name":"NotifyTeammate",
			"input_schema":{
				"type":"object",
				"properties":{
					"to":{
						"allOf":[
							{"type":"string","description":"Teammate recipient"},
							{"type":"string","description":"Recipient: teammate name"}
						]
					},
					"message":{"type":"string"}
				},
				"required":["to","message"]
			}
		}]
	}`)
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	prep, err := env.PrepareGemini(http.Header{}, translate.EmitOptions{TargetModel: "gemini-3.1-pro-preview"})
	require.NoError(t, err)

	out := mustUnmarshal(t, prep.Body)
	decls := mustAs[[]any](t, mustAs[map[string]any](t, mustAs[[]any](t, out["tools"])[0])["functionDeclarations"])
	params := mustAs[map[string]any](t, mustAs[map[string]any](t, decls[0])["parameters"])
	props := mustAs[map[string]any](t, params["properties"])

	to := mustAs[map[string]any](t, props["to"])
	assert.Equal(t, "string", to["type"])
	assert.Equal(t, "Teammate recipient", to["description"], "earlier branch's annotation wins")
	assert.ElementsMatch(t, []any{"message", "to"}, params["required"])
}

func TestPrepareGemini_UnrepresentableToolSchemaDegradesToNoParameters(t *testing.T) {
	// oneOf has no widening path — per-tool backstop keeps healthy tools alive.
	body := []byte(`{
		"messages": [{"role":"user","content":"hi"}],
		"tools": [
			{"name":"healthy","input_schema":{"type":"object","properties":{"x":{"type":"string"}}}},
			{"name":"degraded","input_schema":{"oneOf":[{"type":"string"},{"type":"number"}]}}
		]
	}`)
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	prep, err := env.PrepareGemini(http.Header{}, translate.EmitOptions{TargetModel: "gemini-3.1-pro-preview"})
	require.NoError(t, err)

	out := mustUnmarshal(t, prep.Body)
	decls := mustAs[[]any](t, mustAs[map[string]any](t, mustAs[[]any](t, out["tools"])[0])["functionDeclarations"])
	require.Len(t, decls, 2)

	byName := make(map[string]map[string]any, len(decls))
	for _, d := range decls {
		decl := mustAs[map[string]any](t, d)
		byName[mustAs[string](t, decl["name"])] = decl
	}

	healthy, ok := byName["healthy"]
	require.True(t, ok)
	assert.Contains(t, healthy, "parameters")

	degraded, ok := byName["degraded"]
	require.True(t, ok)
	assert.NotContains(t, degraded, "parameters", "unrepresentable schema must degrade to no parameters, not fail the request")
}

func TestPrepareGemini_DegradedDuplicateDeclarationsStillConflict(t *testing.T) {
	// Two tools with the same name are a client bug regardless of whether one
	// happened to degrade to no parameters; dedupe must keep rejecting them.
	body := []byte(`{
		"messages": [{"role":"user","content":"hi"}],
		"tools": [
			{"name":"dup","input_schema":{"type":"object"}},
			{"name":"dup","input_schema":{"oneOf":[{"type":"string"},{"type":"number"}]}}
		]
	}`)
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	_, err = env.PrepareGemini(http.Header{}, translate.EmitOptions{TargetModel: "gemini-3.1-pro-preview"})
	require.ErrorIs(t, err, translate.ErrGeminiToolDeclarationConflict)
}

func TestIsIntrinsicallyIncompatible(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "wrapped gemini schema incompatible",
			err:  fmt.Errorf("prepare gemini: %w", translate.ErrGeminiSchemaIncompatible),
			want: true,
		},
		{
			name: "wrapped reasoning incompatible",
			err:  fmt.Errorf("prepare gemini: %w", translate.ErrReasoningIncompatible),
			want: true,
		},
		{
			name: "wrapped gemini unsigned tool history",
			err:  fmt.Errorf("prepare gemini: %w", translate.ErrGeminiUnsignedToolHistory),
			want: true,
		},
		{
			name: "unrelated error",
			err:  errors.New("other"),
			want: false,
		},
		{
			name: "tool declaration conflict is a client bug, not intrinsic incompatibility",
			err:  translate.ErrGeminiToolDeclarationConflict,
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, translate.IsIntrinsicallyIncompatible(tt.err))
		})
	}
}
