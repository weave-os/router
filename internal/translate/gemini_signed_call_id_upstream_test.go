package translate_test

import (
	"encoding/base64"
	"net/http"
	"strings"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

var signedUpstreamSig = strings.Repeat("synthetic-gemini-signature-", 24)

var signedUpstreamCallID = "call_abc__thought__" + base64.RawURLEncoding.EncodeToString([]byte(signedUpstreamSig))

var signedUpstreamCodexTurn = `{"model":"client-model","tools":[{"type":"function","name":"Read","parameters":{"type":"object"}}],"input":[` +
	`{"type":"message","role":"user","content":[{"type":"input_text","text":"Continue."}]},` +
	`{"type":"function_call","call_id":"` + signedUpstreamCallID + `","name":"Read","arguments":"{\"file_path\":\"/tmp/a.go\"}"},` +
	`{"type":"function_call_output","call_id":"` + signedUpstreamCallID + `","output":"package a"}]}`

var signedUpstreamAnthropicTurn = `{"model":"client-model","max_tokens":1024,"messages":[` +
	`{"role":"user","content":"Continue."},` +
	`{"role":"assistant","content":[{"type":"tool_use","id":"` + signedUpstreamCallID + `","name":"Read","input":{"file_path":"/tmp/a.go"}}]},` +
	`{"role":"user","content":[{"type":"tool_result","tool_use_id":"` + signedUpstreamCallID + `","content":"package a"}]}]}`

func signedUpstreamChatEnvelope(t *testing.T) *translate.RequestEnvelope {
	t.Helper()
	conv, err := translate.ConvertResponsesToChatCompletions([]byte(signedUpstreamCodexTurn))
	require.NoError(t, err)
	env, err := translate.ParseOpenAI(conv.Body)
	require.NoError(t, err)
	return env
}

func signedUpstreamAnthropicEnvelope(t *testing.T) *translate.RequestEnvelope {
	t.Helper()
	env, err := translate.ParseAnthropic([]byte(signedUpstreamAnthropicTurn))
	require.NoError(t, err)
	return env
}

func requireShortPairedCallID(t *testing.T, calls, results []gjson.Result) {
	t.Helper()
	require.Len(t, calls, 1)
	require.Len(t, results, 1)
	assert.LessOrEqual(t, len(calls[0].String()), 64, "an OpenAI upstream rejects call ids longer than 64 characters")
	assert.NotContains(t, calls[0].String(), "__thought__")
	assert.Equal(t, calls[0].String(), results[0].String(), "the call and its result must keep matching ids")
}

func requireShortChatCallIDs(t *testing.T, body []byte) {
	t.Helper()
	requireShortPairedCallID(t,
		gjson.GetBytes(body, `messages.#(role=="assistant").tool_calls.#.id`).Array(),
		gjson.GetBytes(body, `messages.#(role=="tool")#.tool_call_id`).Array())
}

func requireShortResponsesCallIDs(t *testing.T, body []byte) {
	t.Helper()
	requireShortPairedCallID(t,
		gjson.GetBytes(body, `input.#(type=="function_call")#.call_id`).Array(),
		gjson.GetBytes(body, `input.#(type=="function_call_output")#.call_id`).Array())
}

func TestGeminiSignedCallIDsAreShortenedForOpenAIUpstreams(t *testing.T) {
	require.Greater(t, len(signedUpstreamCallID), 64)
	envelopes := map[string]func(*testing.T) *translate.RequestEnvelope{
		"codex":  signedUpstreamChatEnvelope,
		"claude": signedUpstreamAnthropicEnvelope,
	}
	for source, envelope := range envelopes {
		for _, provider := range []string{providers.ProviderOpenAI, providers.ProviderOpenAIGateway, providers.ProviderFireworks, providers.ProviderOpenRouter} {
			t.Run(source+"/chat/"+provider, func(t *testing.T) {
				prep, err := envelope(t).PrepareOpenAI(http.Header{}, translate.EmitOptions{TargetModel: "gpt-4.1", TargetProvider: provider})
				require.NoError(t, err)
				requireShortChatCallIDs(t, prep.Body)
			})
		}
		t.Run(source+"/responses", func(t *testing.T) {
			prep, err := envelope(t).PrepareOpenAIResponses(http.Header{}, translate.EmitOptions{TargetModel: "gpt-4.1", TargetProvider: providers.ProviderOpenAI})
			require.NoError(t, err)
			requireShortResponsesCallIDs(t, prep.Body)
		})
		t.Run(source+"/gemini", func(t *testing.T) {
			prep, err := envelope(t).PrepareGemini(http.Header{}, translate.EmitOptions{TargetModel: "gemini-3.1-pro-preview", TargetProvider: providers.ProviderGoogle})
			require.NoError(t, err)
			assert.Equal(t, signedUpstreamSig, gjson.GetBytes(prep.Body, `contents.#(role=="model").parts.0.thoughtSignature`).String(), "a Gemini upstream still gets the signature")
		})
	}
	t.Run("codex/native-responses", func(t *testing.T) {
		out, err := translate.ClampResponsesInputCallIDs([]byte(signedUpstreamCodexTurn))
		require.NoError(t, err)
		requireShortResponsesCallIDs(t, out)
	})
}
