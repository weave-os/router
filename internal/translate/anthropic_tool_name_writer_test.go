package translate_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/translate"
)

type progressArmerRecorder struct {
	*httptest.ResponseRecorder
	outputMark     func()
	reasoningMark  func()
	outputArmed    bool
	reasoningArmed bool
}

func (r *progressArmerRecorder) ArmOutputProgress(mark func()) bool {
	r.outputMark = mark
	return r.outputArmed
}

func (r *progressArmerRecorder) ArmReasoningProgress(mark func()) bool {
	r.reasoningMark = mark
	return r.reasoningArmed
}

var (
	_ providers.OutputProgressArmer    = (*progressArmerRecorder)(nil)
	_ providers.ReasoningProgressArmer = (*progressArmerRecorder)(nil)
)

func TestAnthropicToolNameWriterForwardsProgressArming(t *testing.T) {
	inner := &progressArmerRecorder{
		ResponseRecorder: httptest.NewRecorder(),
		outputArmed:      true,
		reasoningArmed:   true,
	}
	writer := translate.NewAnthropicToolNameWriter(inner, nil)
	outputMark := func() {}
	reasoningMark := func() {}

	require.True(t, writer.ArmOutputProgress(outputMark))
	require.NotNil(t, inner.outputMark)
	inner.outputMark()
	outputMark()
	require.True(t, writer.ArmReasoningProgress(reasoningMark))
	require.NotNil(t, inner.reasoningMark)
	inner.reasoningMark()
	reasoningMark()
}

func TestAnthropicToolNameWriterProgressArmingFallsBackWhenUnsupported(t *testing.T) {
	writer := translate.NewAnthropicToolNameWriter(httptest.NewRecorder(), nil)
	require.False(t, writer.ArmOutputProgress(func() {}))
	require.False(t, writer.ArmReasoningProgress(func() {}))
}

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
			var restoredToolName string
			for _, line := range strings.Split(sink.Body.String(), "\n") {
				if strings.HasPrefix(line, "data: ") && gjson.Get(line[6:], "content_block.type").String() == "tool_use" {
					restoredToolName = gjson.Get(line[6:], "content_block.name").String()
				}
			}
			require.Equal(t, name, restoredToolName, "split %d", split)
			require.Contains(t, sink.Body.String(), "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
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
			require.Equal(t, "nosniff", sink.Header().Get("X-Content-Type-Options"))
		}
	}
}

func TestAnthropicToolNameWriterEscapesPassthroughJSON(t *testing.T) {
	const body = `{"error":{"message":"</script><script>alert(1)</script>"}}`
	sink := httptest.NewRecorder()
	writer := translate.NewAnthropicToolNameWriter(sink, nil)
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Content-Length", fmt.Sprint(len(body)))
	writer.WriteHeader(http.StatusBadRequest)
	written, err := writer.Write([]byte(body))
	require.NoError(t, err)
	require.Equal(t, len(body), written)
	require.Equal(t, "</script><script>alert(1)</script>", gjson.Get(sink.Body.String(), "error.message").String())
	require.NotContains(t, sink.Body.String(), "<script>")
	require.Equal(t, "nosniff", sink.Header().Get("X-Content-Type-Options"))
	require.Empty(t, sink.Header().Get("Content-Length"))
}

func TestAnthropicToolNameWriterParsesJSONMediaTypeCaseInsensitively(t *testing.T) {
	for _, contentType := range []string{"Application/JSON", "application/vnd.example+json"} {
		t.Run(contentType, func(t *testing.T) {
			const body = `{"message":"<script>"}`
			sink := httptest.NewRecorder()
			writer := translate.NewAnthropicToolNameWriter(sink, nil)
			writer.Header().Set("Content-Type", contentType)
			_, err := writer.Write([]byte(body))
			require.NoError(t, err)
			require.NotContains(t, sink.Body.String(), "<script>")
			require.Equal(t, "<script>", gjson.Get(sink.Body.String(), "message").String())
		})
	}
}

func TestAnthropicToolNameWriterJSONEscapesRestoredHTMLCharacters(t *testing.T) {
	const originalName = "</script><script>alert(1)</script>"
	response := `{"content":[{"type":"tool_use","name":"wire_alias","input":{}}]}`
	sink := httptest.NewRecorder()
	writer := translate.NewAnthropicToolNameWriter(sink, map[string]string{"wire_alias": originalName})
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusOK)
	_, err := writer.Write([]byte(response))
	require.NoError(t, err)
	require.NoError(t, writer.Finalize())
	require.NotContains(t, sink.Body.String(), "<script>")
	require.Equal(t, originalName, gjson.Get(sink.Body.String(), "content.0.name").String())
	require.Equal(t, "nosniff", sink.Header().Get("X-Content-Type-Options"))
}

func TestAnthropicToolNameWriterPreservesLargeNumbers(t *testing.T) {
	const response = `{"content":[{"type":"tool_use","name":"wire_alias","input":{"large":9007199254740993}}]}`
	sink := httptest.NewRecorder()
	writer := translate.NewAnthropicToolNameWriter(sink, map[string]string{"wire_alias": "client_name"})
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusOK)
	_, err := writer.Write([]byte(response))
	require.NoError(t, err)
	require.NoError(t, writer.Finalize())
	require.Equal(t, int64(9007199254740993), gjson.Get(sink.Body.String(), "content.0.input.large").Int())
}

func TestAnthropicToolNameWriterClearsContentLengthWithoutJSONContentType(t *testing.T) {
	const response = `{"content":[{"type":"tool_use","name":"wire_alias","input":{}}]}`
	sink := httptest.NewRecorder()
	writer := translate.NewAnthropicToolNameWriter(sink, map[string]string{"wire_alias": "a much longer client tool name"})
	writer.Header().Set("Content-Length", fmt.Sprint(len(response)))
	writer.WriteHeader(http.StatusOK)
	_, err := writer.Write([]byte(response))
	require.NoError(t, err)
	require.NoError(t, writer.Finalize())
	require.Empty(t, sink.Header().Get("Content-Length"))
	require.Equal(t, "a much longer client tool name", gjson.Get(sink.Body.String(), "content.0.name").String())
}

func TestAnthropicToolNameWriterRejectsTrailingJSON(t *testing.T) {
	response := `{"content":[{"type":"tool_use","name":"wire_alias","input":{}}]} {"unexpected":true}`
	sink := httptest.NewRecorder()
	writer := translate.NewAnthropicToolNameWriter(sink, map[string]string{"wire_alias": "client_name"})
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusOK)
	_, err := writer.Write([]byte(response))
	require.NoError(t, err)
	err = writer.Finalize()
	require.ErrorContains(t, err, "trailing JSON value")
}
