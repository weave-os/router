package proxy_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/sse"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const seedMarkupModel = "xiaomi/mimo-v2.5-pro"

func seedMarkupChatBody(stream bool) []byte {
	streamField := "false"
	if stream {
		streamField = "true"
	}
	tool := func(name string) string {
		return `{"type":"function","function":{"name":"` + name + `","parameters":{"type":"object","properties":{"command":{"type":"string"},"path":{"type":"string"}}}}}`
	}
	return []byte(`{"model":"` + seedMarkupModel + `","stream":` + streamField + `,"messages":[{"role":"user","content":"say hello"}],"tools":[` +
		strings.Join([]string{tool("Shell"), tool("Glob"), tool("Grep"), tool("Read"), tool("StrReplace"), tool("Write")}, ",") + `]}`)
}

func seedContentFrame(content string) string {
	return `data: {"id":"gen-1","object":"chat.completion.chunk","model":"` + seedMarkupModel + `","choices":[{"index":0,"delta":{"role":"assistant","content":` + quoteJSON(content) + `},"finish_reason":null}]}` + "\n\n"
}

func quoteJSON(s string) string {
	quoted, _ := json.Marshal(s)
	return string(quoted)
}

func seedStreamFrames(contentSplits ...string) []string {
	frames := make([]string, 0, len(contentSplits)+2)
	for _, part := range contentSplits {
		frames = append(frames, seedContentFrame(part))
	}
	return append(frames,
		`data: {"id":"gen-1","object":"chat.completion.chunk","model":"`+seedMarkupModel+`","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":40,"completion_tokens":20}}`+"\n\n",
		"data: [DONE]\n\n")
}

type chatStreamResult struct {
	content      string
	finishReason string
	calls        map[int]*chatStreamCall
}

type chatStreamCall struct {
	id, name, arguments string
}

func collectChatStream(t *testing.T, body string) chatStreamResult {
	t.Helper()
	out := chatStreamResult{calls: map[int]*chatStreamCall{}}
	var scanner sse.Scanner
	buf := []byte(body)
	for {
		event, n := scanner.Next(buf)
		if n == 0 {
			break
		}
		buf = buf[n:]
		_, data := sse.ParseEvent(event)
		if len(data) == 0 || strings.TrimSpace(string(data)) == "[DONE]" {
			continue
		}
		require.True(t, gjson.ValidBytes(data), "invalid chunk JSON: %s", data)
		choice := gjson.GetBytes(data, "choices.0")
		out.content += choice.Get("delta.content").String()
		if fr := choice.Get("finish_reason").String(); fr != "" {
			out.finishReason = fr
		}
		choice.Get("delta.tool_calls").ForEach(func(_, tc gjson.Result) bool {
			idx := int(tc.Get("index").Int())
			call := out.calls[idx]
			if call == nil {
				call = &chatStreamCall{}
				out.calls[idx] = call
			}
			if id := tc.Get("id").String(); id != "" {
				call.id = id
			}
			if name := tc.Get("function.name").String(); name != "" {
				call.name = name
			}
			call.arguments += tc.Get("function.arguments").String()
			return true
		})
	}
	return out
}

func proxySeedChat(t *testing.T, body []byte, frames []string) *httptest.ResponseRecorder {
	t.Helper()
	provider := scriptedStream(frames)
	svc := nativeSurfaceService(provider, providers.ProviderOpenRouter, seedMarkupModel)
	rec := httptest.NewRecorder()
	require.NoError(t, svc.ProxyOpenAIChatCompletion(authedCtx(uuid.NewString()), body, rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)))
	return rec
}

// An upstream that streams its tool call as Seed-style text markup (with the
// malformed `</seed>tool_call>` closer) must reach a chat-completions client
// as a structured tool call, or the client renders the markup and the agent
// loop stops.
func TestProxyOpenAIChatCompletion_SeedToolCallMarkupBecomesToolCall(t *testing.T) {
	rec := proxySeedChat(t, seedMarkupChatBody(true), seedStreamFrames(
		"<seed:to",
		"ol_call><function_name>Sh",
		"ell</function_name><argu",
		`ment>{"command": "echo hello"}</arg`,
		"ument></seed>tool",
		"_call>",
	))

	got := collectChatStream(t, rec.Body.String())
	assert.NotContains(t, got.content, "<seed", "no raw markup may reach the client")
	assert.NotContains(t, got.content, "function_name")
	assert.Equal(t, "tool_calls", got.finishReason)
	require.Len(t, got.calls, 1)
	assert.Equal(t, "Shell", got.calls[0].name)
	assert.NotEmpty(t, got.calls[0].id)
	assert.JSONEq(t, `{"command": "echo hello"}`, got.calls[0].arguments)
}

func TestProxyOpenAIChatCompletion_SeedToolCallMarkupMultipleCalls(t *testing.T) {
	rec := proxySeedChat(t, seedMarkupChatBody(true), seedStreamFrames(
		"\n<seed:tool_call><function_name>Read</function_name><argument>{\"path\": \"a.go\"}</argument></seed:tool_call>\n",
		"<seed:tool_call><function_name>Shell</function_name><argument>{\"command\": \"ls\"}</argument></seed>tool_call>",
	))

	got := collectChatStream(t, rec.Body.String())
	assert.NotContains(t, got.content, "seed")
	assert.Equal(t, "tool_calls", got.finishReason)
	require.Len(t, got.calls, 2)
	assert.Equal(t, "Read", got.calls[0].name)
	assert.JSONEq(t, `{"path": "a.go"}`, got.calls[0].arguments)
	assert.Equal(t, "Shell", got.calls[1].name)
	assert.JSONEq(t, `{"command": "ls"}`, got.calls[1].arguments)
	assert.NotEqual(t, got.calls[0].id, got.calls[1].id)
}

func TestProxyOpenAIChatCompletion_SeedToolCallMarkupFallsBackToText(t *testing.T) {
	undeclared := "<seed:tool_call><function_name>Delete</function_name><argument>{\"path\": \"a.go\"}</argument></seed:tool_call>"
	invalidJSON := "<seed:tool_call><function_name>Shell</function_name><argument>{\"command\": </argument></seed:tool_call>"
	midProse := "Run this: <seed:tool_call><function_name>Shell</function_name><argument>{\"command\": \"ls\"}</argument></seed:tool_call>"
	cases := map[string]struct {
		body    []byte
		content string
	}{
		"undeclared tool": {seedMarkupChatBody(true), undeclared},
		"invalid json":    {seedMarkupChatBody(true), invalidJSON},
		"not leading":     {seedMarkupChatBody(true), midProse},
		"tools disabled": {
			[]byte(strings.Replace(string(seedMarkupChatBody(true)), `"tools":`, `"tool_choice":"none","tools":`, 1)),
			"<seed:tool_call><function_name>Shell</function_name><argument>{\"command\": \"ls\"}</argument></seed:tool_call>",
		},
		"no tools declared": {
			[]byte(`{"model":"` + seedMarkupModel + `","stream":true,"messages":[{"role":"user","content":"hi"}]}`),
			"<seed:tool_call><function_name>Shell</function_name><argument>{\"command\": \"ls\"}</argument></seed:tool_call>",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			half := len(tc.content) / 2
			rec := proxySeedChat(t, tc.body, seedStreamFrames(tc.content[:half], tc.content[half:]))
			got := collectChatStream(t, rec.Body.String())
			assert.True(t, strings.HasSuffix(got.content, tc.content), "content must pass through unchanged, got %q", got.content)
			assert.Equal(t, "stop", got.finishReason)
			assert.Empty(t, got.calls)
		})
	}
}

func TestProxyOpenAIChatCompletion_SeedToolCallMarkupNonStreaming(t *testing.T) {
	content := `<seed:tool_call><function_name>Shell</function_name><argument>{"command": "echo hello"}</argument></seed>tool_call>`
	provider := &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"gen-1","object":"chat.completion","model":"` + seedMarkupModel + `","choices":[{"index":0,"message":{"role":"assistant","content":` + quoteJSON(content) + `},"finish_reason":"stop"}],"usage":{"prompt_tokens":40,"completion_tokens":20}}`))
	}}
	svc := nativeSurfaceService(provider, providers.ProviderOpenRouter, seedMarkupModel)
	rec := httptest.NewRecorder()
	require.NoError(t, svc.ProxyOpenAIChatCompletion(authedCtx(uuid.NewString()), seedMarkupChatBody(false), rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)))

	body := rec.Body.Bytes()
	require.True(t, gjson.ValidBytes(body), "%s", body)
	message := gjson.GetBytes(body, "choices.0.message")
	assert.Equal(t, "tool_calls", gjson.GetBytes(body, "choices.0.finish_reason").String())
	assert.NotContains(t, message.Get("content").String(), "seed")
	require.Len(t, message.Get("tool_calls").Array(), 1)
	assert.Equal(t, "Shell", message.Get("tool_calls.0.function.name").String())
	assert.Equal(t, "function", message.Get("tool_calls.0.type").String())
	assert.NotEmpty(t, message.Get("tool_calls.0.id").String())
	assert.JSONEq(t, `{"command": "echo hello"}`, message.Get("tool_calls.0.function.arguments").String())
}

func seedMarkupMessagesBody(stream bool) []byte {
	streamField := "false"
	if stream {
		streamField = "true"
	}
	return []byte(`{"model":"` + seedMarkupModel + `","max_tokens":1024,"stream":` + streamField + `,"messages":[{"role":"user","content":"say hello"}],"tools":[` +
		`{"name":"Bash","input_schema":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}},` +
		`{"name":"Read","input_schema":{"type":"object","properties":{"file_path":{"type":"string"}},"required":["file_path"]}}]}`)
}

// The same upstream reached from an Anthropic Messages client must surface the
// markup as a tool_use block rather than text (and not as a recovery nudge).
func TestProxyMessages_SeedToolCallMarkupBecomesToolUse(t *testing.T) {
	provider := scriptedStream(seedStreamFrames(
		"<seed:tool_call><function_na",
		"me>Bash</function_name><argument>{\"command\": \"echo hel",
		"lo\"}</argument></seed>tool_call>",
	))
	svc := makeProxyService(
		router.Decision{Provider: providers.ProviderOpenRouter, Model: seedMarkupModel, Reason: "test"},
		map[string]providers.Client{providers.ProviderOpenRouter: provider},
	)
	body := seedMarkupMessagesBody(true)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(string(body)))
	require.NoError(t, svc.ProxyMessages(authedCtx(uuid.NewString()), body, rec, req))

	out := rec.Body.String()
	assert.NotContains(t, out, "seed:tool_call", "no raw markup may reach the client")
	assert.NotContains(t, out, "router recovery nudge")
	assert.Contains(t, out, `"type":"tool_use"`)
	assert.Contains(t, out, `"name":"Bash"`)
	assert.Contains(t, out, `"stop_reason":"tool_use"`)
	assert.Contains(t, out, `echo hello`)
}
