package translate_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"weave-os/router/internal/translate"
)

func TestAnthropicToolNameRoundTrip(t *testing.T) {
	name := strings.Repeat("x", 65)
	body := []byte(fmt.Sprintf(`{"model":"claude-sonnet-5-5","max_tokens":1024,"tools":[{"name":%q,"input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"hello"}]}`, name))
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	prep, err := env.PrepareAnthropic(nil, translate.EmitOptions{TargetModel: "claude-sonnet-5-5"})
	require.NoError(t, err)
	alias := gjson.GetBytes(prep.Body, "tools.0.name").String()
	require.NotEqual(t, name, alias)

	t.Run("JSON", func(t *testing.T) {
		response := fmt.Sprintf(`{"content":[{"type":"tool_use","id":"call_1","name":%q,"input":{"type":"tool_reference","name":%q}},{"type":"text","text":%q}],"stop_reason":"tool_use"}`, alias, alias, alias)
		sink := httptest.NewRecorder()
		writer := translate.NewAnthropicToolNameWriter(sink, prep.ResponseToolNames)
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("Content-Length", fmt.Sprint(len(response)))
		writer.WriteHeader(http.StatusOK)
		for _, b := range []byte(response) {
			_, err = writer.Write([]byte{b})
			require.NoError(t, err)
		}
		require.NoError(t, writer.Finalize())
		require.Equal(t, name, gjson.Get(sink.Body.String(), "content.0.name").String())
		require.Equal(t, "call_1", gjson.Get(sink.Body.String(), "content.0.id").String())
		require.Equal(t, alias, gjson.Get(sink.Body.String(), "content.0.input.name").String())
		require.Equal(t, alias, gjson.Get(sink.Body.String(), "content.1.text").String())
		require.Empty(t, sink.Header().Get("Content-Length"))
	})

	t.Run("SSE every split", func(t *testing.T) {
		stream := fmt.Sprintf("event: content_block_start\r\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"call_1\",\"name\":%q,\"input\":{}}}\r\n\r\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n", alias)
		expected := strings.Replace(stream, fmt.Sprintf("\"name\":%q", alias), fmt.Sprintf("\"name\":%q", name), 1)
		for split := 0; split <= len(stream); split++ {
			sink := httptest.NewRecorder()
			writer := translate.NewAnthropicToolNameWriter(sink, prep.ResponseToolNames)
			writer.Header().Set("Content-Type", "text/event-stream")
			writer.WriteHeader(http.StatusOK)
			_, err = writer.Write([]byte(stream[:split]))
			require.NoError(t, err)
			_, err = writer.Write([]byte(stream[split:]))
			require.NoError(t, err)
			require.NoError(t, writer.Finalize())
			require.Equal(t, expected, sink.Body.String(), "split %d", split)
		}
	})
}

func TestAnthropicToolNameWriterPreservesUnchangedResponses(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusBadRequest} {
		for _, names := range []map[string]string{nil, {"wire_name": "client_name"}} {
			sink := httptest.NewRecorder()
			writer := translate.NewAnthropicToolNameWriter(sink, names)
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(status)
			body := `{"content":[{"type":"text","text":"wire_name"}],"error":{"message":"wire_name"}}`
			_, err := writer.Write([]byte(body[:12]))
			require.NoError(t, err)
			_, err = writer.Write([]byte(body[12:]))
			require.NoError(t, err)
			require.NoError(t, writer.Finalize())
			require.Equal(t, body, sink.Body.String())
			require.Equal(t, status, sink.Code)
		}
	}
}
