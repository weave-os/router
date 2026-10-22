package translate_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestUsageProjection_GeminiBufferedCacheDetails(t *testing.T) {
	body := []byte(`{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":7,"thoughtsTokenCount":3,"cachedContentTokenCount":80,"totalTokenCount":110}}`)
	out, err := translate.GeminiToOpenAIResponse(body, "synthetic-model")
	require.NoError(t, err)
	assert.EqualValues(t, 80, gjson.GetBytes(out, "usage.prompt_tokens_details.cached_tokens").Int())
	assert.EqualValues(t, 3, gjson.GetBytes(out, "usage.completion_tokens_details.reasoning_tokens").Int())
}

func TestUsageProjection_AnthropicCacheDetails(t *testing.T) {
	body := []byte(`{"id":"msg_synthetic","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":20,"output_tokens":10,"cache_creation_input_tokens":30,"cache_read_input_tokens":50}}`)
	out, err := translate.AnthropicToOpenAIResponse(body, "synthetic-model")
	require.NoError(t, err)
	assert.EqualValues(t, 100, gjson.GetBytes(out, "usage.prompt_tokens").Int())
	assert.EqualValues(t, 30, gjson.GetBytes(out, "usage.prompt_tokens_details.cache_write_tokens").Int())
	assert.EqualValues(t, 50, gjson.GetBytes(out, "usage.prompt_tokens_details.cached_tokens").Int())
}

func TestUsageProjection_ResponsesChatCacheWrites(t *testing.T) {
	const response = `{"id":"resp_synthetic","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":100,"output_tokens":10,"input_tokens_details":{"cached_tokens":50,"cache_write_tokens":30},"output_tokens_details":{"reasoning_tokens":3}}}`
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "buffered", true: "streaming"}[stream], func(t *testing.T) {
			rec := httptest.NewRecorder()
			w := translate.NewResponsesToOpenAIChatWriter(rec, "synthetic-model", nil)
			require.NoError(t, w.Prelude(stream))
			_, err := w.Write([]byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":" + response + "}\n\n"))
			require.NoError(t, err)
			require.NoError(t, w.Finalize())
			var usage gjson.Result
			if stream {
				for _, line := range strings.Split(rec.Body.String(), "\n") {
					if payload, ok := strings.CutPrefix(line, "data: "); ok {
						if u := gjson.Get(payload, "usage"); u.IsObject() {
							usage = u
						}
					}
				}
			} else {
				usage = gjson.GetBytes(rec.Body.Bytes(), "usage")
			}
			assert.EqualValues(t, 30, usage.Get("prompt_tokens_details.cache_write_tokens").Int())
		})
	}
}

func TestUsageProjection_ChatResponsesDetails(t *testing.T) {
	const usage = `"usage":{"prompt_tokens":100,"completion_tokens":10,"total_tokens":110,"prompt_tokens_details":{"cached_tokens":50,"cache_write_tokens":30},"completion_tokens_details":{"reasoning_tokens":3}}`
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "buffered", true: "streaming"}[stream], func(t *testing.T) {
			rec := httptest.NewRecorder()
			w := translate.NewResponsesWriter(rec, "synthetic-model")
			var body string
			if stream {
				w.Header().Set("Content-Type", "text/event-stream")
				body = "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]," + usage + "}\n\ndata: [DONE]\n\n"
			} else {
				w.Header().Set("Content-Type", "application/json")
				body = `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],` + usage + `}`
			}
			w.WriteHeader(http.StatusOK)
			_, err := w.Write([]byte(body))
			require.NoError(t, err)
			require.NoError(t, w.Finalize())
			var got gjson.Result
			if stream {
				for _, line := range strings.Split(rec.Body.String(), "\n") {
					if payload, ok := strings.CutPrefix(line, "data: "); ok && gjson.Get(payload, "type").String() == "response.completed" {
						got = gjson.Get(payload, "response.usage")
					}
				}
			} else {
				got = gjson.GetBytes(rec.Body.Bytes(), "usage")
			}
			assert.EqualValues(t, 50, got.Get("input_tokens_details.cached_tokens").Int())
			assert.EqualValues(t, 30, got.Get("input_tokens_details.cache_write_tokens").Int())
			assert.EqualValues(t, 3, got.Get("output_tokens_details.reasoning_tokens").Int())
		})
	}
}
