package translate

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestCommandResponseWordingDoesNotChangeOutcome(t *testing.T) {
	outcome := CommandOutcome{Version: 1, ID: "synthetic-id", Command: CommandForceModel, Disposition: CommandAccepted, Pin: CommandPin{Transition: PinApplied, Model: "synthetic-model", Provider: "synthetic"}}
	for _, format := range []Format{FormatAnthropic, FormatOpenAI, FormatOpenAIResponses} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%v/%t", format, stream), func(t *testing.T) {
				var previous string
				for _, text := range []string{"Weave Router: force-model applied: synthetic-model (synthetic).", "Entirely different acknowledgment wording."} {
					body := []byte(fmt.Sprintf(`{"model":"synthetic-model","stream":%t,"messages":[]}`, stream))
					var env *RequestEnvelope
					var err error
					if format == FormatAnthropic {
						env, err = ParseAnthropic(body)
					} else {
						env, err = ParseOpenAI(body)
					}
					require.NoError(t, err)
					rec := httptest.NewRecorder()
					var writer http.ResponseWriter = rec
					var responses *ResponsesWriter
					if format == FormatOpenAIResponses {
						responses = NewResponsesWriter(rec, "synthetic-model")
						writer = responses
					}
					require.NoError(t, WriteCommandResponse(writer, env, text, 1, outcome, format == FormatOpenAIResponses))
					if responses != nil {
						require.NoError(t, responses.Finalize())
					}
					encoded := rec.Header().Get(RouterCommandHeader)
					raw, err := base64.RawURLEncoding.DecodeString(encoded)
					require.NoError(t, err)
					var decoded CommandOutcome
					require.NoError(t, json.Unmarshal(raw, &decoded))
					assert.Equal(t, outcome, decoded)
					if previous != "" {
						assert.Equal(t, previous, encoded)
					}
					previous = encoded
					if format == FormatAnthropic {
						assert.Contains(t, rec.Body.String(), `"router_command":`)
						assert.Contains(t, rec.Body.String(), "msg_router_cmd_"+outcome.ID)
					} else if responses != nil {
						assert.Contains(t, rec.Body.String(), "[weave-router-command-v1]")
						assert.Contains(t, rec.Body.String(), encoded)
					}
				}
			})
		}
	}
}

func TestCommandReplayPreservesOrdinaryContentAndTools(t *testing.T) {
	carrier := "[weave-router-command-v1]: <weave-router:" + base64.RawURLEncoding.EncodeToString([]byte(`{"version":1,"id":"11111111-2222-4333-8444-555555555555","command":"router_session","disposition":"accepted","pin":{"transition":"unchanged"}}`)) + ">"
	for _, collection := range []string{"messages", "input"} {
		text := "Entirely changed wording.\n\n" + carrier
		body, err := json.Marshal(map[string]any{"model": "synthetic-model", collection: []any{
			map[string]any{"role": "user", "content": "ordinary user text"},
			map[string]any{"role": "assistant", "content": text},
			map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": text}, map[string]any{"type": "text", "text": "ordinary assistant text"}, map[string]any{"type": "tool_use", "id": "call_synthetic", "name": "synthetic", "input": map[string]any{}}, map[string]any{"type": "thinking", "thinking": "reasoning survives", "signature": "synthetic"}}},
		}})
		require.NoError(t, err)
		var stripped []byte
		if collection == "messages" {
			env, parseErr := ParseAnthropic(body)
			require.NoError(t, parseErr)
			require.NoError(t, env.StripCommandOutcomes())
			stripped = env.body
		} else {
			stripped, err = StripCommandOutcomesFromResponsesInput(body)
			require.NoError(t, err)
		}
		assert.NotContains(t, string(stripped), carrier)
		assert.NotContains(t, string(stripped), "changed wording")
		assert.Len(t, gjson.GetBytes(stripped, collection).Array(), 2)
		for _, preserved := range []string{"ordinary user text", "ordinary assistant text", "call_synthetic", "reasoning survives"} {
			assert.Contains(t, string(stripped), preserved)
		}
	}
}

func TestCommandReplayRemovesClaudeMetadata(t *testing.T) {
	body := []byte(`{"model":"synthetic-model","messages":[{"role":"assistant","model":"weave-router","router_command":{"version":1},"content":[{"type":"text","text":"A differently worded acknowledgment."}]},{"role":"user","content":"continue"}]}`)
	env, err := ParseAnthropic(body)
	require.NoError(t, err)
	require.NoError(t, env.StripCommandOutcomes())
	assert.False(t, strings.Contains(string(env.body), "router_command"))
	assert.Len(t, gjson.GetBytes(env.body, "messages").Array(), 1)
}

func TestCommandReplayWithoutMetadataIsByteIdentical(t *testing.T) {
	body := []byte(`{ "model": "synthetic-model", "messages": [ { "role": "assistant", "content": [ { "type": "text", "text": "ordinary prose" }, { "type": "thinking", "thinking": "reasoning" } ] } ] }`)
	env, err := ParseAnthropic(body)
	require.NoError(t, err)
	require.NoError(t, env.StripCommandOutcomes())
	assert.Equal(t, body, env.body)
}

func TestCommandReplayReferenceWithCRLF(t *testing.T) {
	outcome := CommandOutcome{Version: 1, ID: "synthetic-id", Command: CommandRouterSession, Disposition: CommandAccepted, Pin: CommandPin{Transition: PinUnchanged}}
	raw, err := json.Marshal(outcome)
	require.NoError(t, err)
	text := "A changed acknowledgment.\r\n\r\n[weave-router-command-v1]: <weave-router:" + base64.RawURLEncoding.EncodeToString(raw) + ">\r\nordinary content"
	assert.Equal(t, "ordinary content", stripCommandReference(text))
}
