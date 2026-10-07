package translate_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"weave-os/router/internal/translate"
)

// fakeUsageSink records the last RecordUsage / RecordCacheUsage calls and
// latches RecordOutputLimitReached.
type fakeUsageSink struct {
	input              int
	output             int
	cacheCreation      int
	cacheRead          int
	reasoning          int
	outputLimitReached bool
	cacheCreation1h    int
	cacheSplitReported bool
	speed              string
	inferenceGeo       string
}

func (f *fakeUsageSink) RecordUsage(input, output int) {
	f.input = input
	f.output = output
}

func (f *fakeUsageSink) RecordCacheUsage(creation, read int) {
	f.cacheCreation = creation
	f.cacheRead = read
}

func (f *fakeUsageSink) RecordReasoningUsage(reasoning int) {
	f.reasoning = reasoning
}

func (f *fakeUsageSink) RecordUsageModifiers(cacheCreation1h int, cacheSplitReported bool, speed, inferenceGeo string) {
	f.cacheCreation1h = cacheCreation1h
	f.cacheSplitReported = cacheSplitReported
	f.speed = speed
	f.inferenceGeo = inferenceGeo
}

func (f *fakeUsageSink) RecordOutputLimitReached() {
	f.outputLimitReached = true
}

// Catches typos in the message_start cache_creation_input_tokens /
// cache_read_input_tokens JSON paths.
func TestSSETranslator_ForwardsAnthropicCacheTokens(t *testing.T) {
	rec := httptest.NewRecorder()
	sink := &fakeUsageSink{}
	translator := translate.NewSSETranslator(rec, "claude-sonnet-4-5", sink)

	translator.Header().Set("Content-Type", "text/event-stream")
	translator.WriteHeader(http.StatusOK)

	event := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-sonnet-4-5\",\"usage\":{\"input_tokens\":150,\"output_tokens\":0,\"cache_creation_input_tokens\":512,\"cache_read_input_tokens\":2048}}}\n\n"
	_, err := translator.Write([]byte(event))
	require.NoError(t, err)

	assert.Equal(t, 150, sink.input)
	assert.Equal(t, 512, sink.cacheCreation)
	assert.Equal(t, 2048, sink.cacheRead)
}

// Catches typos in the prompt_tokens_details.cached_tokens nested path that
// gjson would otherwise silently return 0 for.
func TestAnthropicSSETranslator_ForwardsOpenAICachedTokens(t *testing.T) {
	rec := httptest.NewRecorder()
	sink := &fakeUsageSink{}
	translator := translate.NewAnthropicSSETranslator(rec, "gpt-4o", sink)

	translator.Header().Set("Content-Type", "text/event-stream")
	translator.WriteHeader(http.StatusOK)

	events := []string{
		"data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hi\"},\"finish_reason\":null}]}\n\n",
		"data: {\"id\":\"chatcmpl-1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":80,\"completion_tokens\":12,\"prompt_tokens_details\":{\"cached_tokens\":64}}}\n\n",
		"data: [DONE]\n\n",
	}
	for _, e := range events {
		_, err := translator.Write([]byte(e))
		require.NoError(t, err)
	}

	assert.Equal(t, 80, sink.input)
	assert.Equal(t, 12, sink.output)
	assert.Equal(t, 0, sink.cacheCreation)
	assert.Equal(t, 64, sink.cacheRead)
}

// Cross-format upstreams only learn real input_tokens at stream end, but
// Claude Code's subagent counter reads message_start.usage.input_tokens.
// Without WithEstimatedInputTokens it shows zero for every subagent turn.
func TestAnthropicSSETranslator_MessageStartCarriesEstimatedInputTokens(t *testing.T) {
	rec := httptest.NewRecorder()
	translator := translate.NewAnthropicSSETranslator(rec, "gpt-4o", nil).
		WithEstimatedInputTokens(1234)

	translator.Header().Set("Content-Type", "text/event-stream")
	translator.WriteHeader(http.StatusOK)

	events := []string{
		"data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hi\"},\"finish_reason\":null}]}\n\n",
		"data: [DONE]\n\n",
	}
	for _, e := range events {
		_, err := translator.Write([]byte(e))
		require.NoError(t, err)
	}

	body := rec.Body.String()
	startIdx := strings.Index(body, "event: message_start")
	deltaIdx := strings.Index(body, "event: content_block_delta")
	require.GreaterOrEqual(t, startIdx, 0, "message_start must be emitted")
	require.GreaterOrEqual(t, deltaIdx, startIdx, "message_start must precede the first delta")
	startSegment := body[startIdx:deltaIdx]
	assert.Contains(t, startSegment, `"usage":{"input_tokens":1234,"output_tokens":0}`)
}

// Gemini implicit caching is the only signal we have that caching works on
// the Gemini path, so cachedContentTokenCount must reach the usage sink.
func TestGeminiSSETranslator_ForwardsCachedContentTokenCount(t *testing.T) {
	rec := httptest.NewRecorder()
	sink := &fakeUsageSink{}
	translator := translate.NewGeminiToOpenAISSETranslator(rec, "gemini-3.1-flash-lite-preview", sink)

	translator.Header().Set("Content-Type", "text/event-stream")
	translator.WriteHeader(http.StatusOK)

	chunks := []string{
		`data: {"candidates":[{"content":{"parts":[{"text":"ok"}]}}]}` + "\n\n",
		`data: {"candidates":[{"content":{"parts":[]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":2048,"candidatesTokenCount":4,"totalTokenCount":2052,"cachedContentTokenCount":1900}}` + "\n\n",
	}
	for _, c := range chunks {
		_, err := translator.Write([]byte(c))
		require.NoError(t, err)
	}
	require.NoError(t, translator.Finalize())

	assert.Equal(t, 2048, sink.input)
	assert.Equal(t, 4, sink.output)
	assert.Equal(t, 0, sink.cacheCreation, "Gemini reports only cache reads, not creation")
	assert.Equal(t, 1900, sink.cacheRead)

	// Must carry prompt_tokens_details.cached_tokens for the downstream
	// AnthropicSSETranslator to pick up (stream.go:604).
	body := rec.Body.String()
	assert.Contains(t, body, `"prompt_tokens_details":{"cached_tokens":1900}`)
}

// Same field, non-streaming path (Gemini :generateContent returned as a single
// JSON body rather than SSE). Same propagation requirement.
func TestGeminiSSETranslator_NonStreamingForwardsCachedContentTokenCount(t *testing.T) {
	rec := httptest.NewRecorder()
	sink := &fakeUsageSink{}
	translator := translate.NewGeminiToOpenAISSETranslator(rec, "gemini-3.1-flash-lite-preview", sink)

	translator.Header().Set("Content-Type", "application/json")
	translator.WriteHeader(http.StatusOK)

	body := `{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1500,"candidatesTokenCount":3,"totalTokenCount":1503,"cachedContentTokenCount":1200}}`
	_, err := translator.Write([]byte(body))
	require.NoError(t, err)
	require.NoError(t, translator.Finalize())

	assert.Equal(t, 1500, sink.input)
	assert.Equal(t, 3, sink.output)
	assert.Equal(t, 1200, sink.cacheRead)
}

// Anthropic splits token counts across two events: message_start carries
// input_tokens, message_delta carries only output_tokens. Without persisting
// input_tokens from message_start, the final chunk's prompt_tokens is always 0.
func TestSSETranslator_FinalChunkCarriesPromptTokensFromMessageStart(t *testing.T) {
	rec := httptest.NewRecorder()
	sink := &fakeUsageSink{}
	translator := translate.NewSSETranslator(rec, "claude-haiku-4-5", sink)

	translator.Header().Set("Content-Type", "text/event-stream")
	translator.WriteHeader(http.StatusOK)

	events := []string{
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-haiku-4-5\",\"usage\":{\"input_tokens\":42,\"output_tokens\":0}}}\n\n",
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n",
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n",
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":17}}\n\n",
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
	}
	for _, e := range events {
		_, err := translator.Write([]byte(e))
		require.NoError(t, err)
	}

	body := rec.Body.String()

	// Final chunk carries finish_reason; OpenAI SDK clients read it for cost attribution.
	chunks := strings.Split(body, "\n\n")
	var finalChunk string
	for _, chunk := range chunks {
		if strings.Contains(chunk, `"finish_reason":"stop"`) && strings.Contains(chunk, `"usage"`) {
			finalChunk = chunk
		}
	}
	require.NotEmpty(t, finalChunk, "expected a final chunk with finish_reason:stop and usage")

	assert.Contains(t, finalChunk, `"prompt_tokens":42`, "final chunk must carry input tokens from message_start")
	assert.Contains(t, finalChunk, `"completion_tokens":17`)
	assert.Contains(t, finalChunk, `"total_tokens":59`)
}

// UsageExtractor guards RecordUsage with if value > 0, so input tokens from
// handleMessageStart must not be overwritten by the zero re-read in
// handleMessageDelta.
func TestSSETranslator_SinkAccumulatesInputAndOutputAcrossStream(t *testing.T) {
	rec := httptest.NewRecorder()
	sink := &fakeUsageSink{}
	translator := translate.NewSSETranslator(rec, "claude-haiku-4-5", sink)

	translator.Header().Set("Content-Type", "text/event-stream")
	translator.WriteHeader(http.StatusOK)

	events := []string{
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-haiku-4-5\",\"usage\":{\"input_tokens\":42,\"output_tokens\":0}}}\n\n",
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":17}}\n\n",
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
	}
	for _, e := range events {
		_, err := translator.Write([]byte(e))
		require.NoError(t, err)
	}

	assert.Equal(t, 17, sink.output, "sink must record output tokens from message_delta")
}

func TestAnthropicSSETranslator_ForwardsOpenAICacheWriteTokens(t *testing.T) {
	rec := httptest.NewRecorder()
	sink := &fakeUsageSink{}
	translator := translate.NewAnthropicSSETranslator(rec, "gpt-5.6-sol", sink)

	translator.Header().Set("Content-Type", "text/event-stream")
	translator.WriteHeader(http.StatusOK)

	events := []string{
		"data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hi\"},\"finish_reason\":null}]}\n\n",
		"data: {\"id\":\"chatcmpl-1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":80,\"completion_tokens\":12,\"prompt_tokens_details\":{\"cached_tokens\":64,\"cache_write_tokens\":16}}}\n\n",
		"data: [DONE]\n\n",
	}
	for _, e := range events {
		_, err := translator.Write([]byte(e))
		require.NoError(t, err)
	}

	assert.Equal(t, 16, sink.cacheCreation)
	assert.Equal(t, 64, sink.cacheRead)
	assert.Contains(t, rec.Body.String(), `"cache_creation_input_tokens":16`)
	assert.Contains(t, rec.Body.String(), `"cache_read_input_tokens":64`)
}

func TestResponsesToAnthropicWriter_ForwardsCacheWriteTokens(t *testing.T) {
	const fixture = `event: response.completed
data: {"type":"response.completed","response":{"id":"r","status":"completed","model":"gpt-5.6-sol","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}],"usage":{"input_tokens":1200,"input_tokens_details":{"cached_tokens":800,"cache_write_tokens":256},"output_tokens":340}}}

`
	rec := httptest.NewRecorder()
	sink := &fakeUsageSink{}
	w := translate.NewResponsesToAnthropicWriter(rec, "gpt-5.6-sol", sink)
	require.NoError(t, w.Prelude(true))
	_, err := w.Write([]byte(fixture))
	require.NoError(t, err)
	require.NoError(t, w.Finalize())

	assert.Equal(t, 256, sink.cacheCreation)
	assert.Equal(t, 800, sink.cacheRead)
	got := w.Summary()
	assert.Equal(t, 256, got.CacheCreationTokens)
	assert.Equal(t, 800, got.CacheReadTokens)
	body := rec.Body.String()
	assert.Contains(t, body, `"cache_creation_input_tokens":256`)
	assert.Contains(t, body, `"cache_read_input_tokens":800`)
}

func TestOpenAICacheTokens_PrefersCacheWriteTokens(t *testing.T) {
	usage := gjsonMust(`{"input_tokens_details":{"cached_tokens":10,"cache_write_tokens":4,"cache_creation_tokens":99}}`)
	w, r := translate.OpenAICacheTokens(usage)
	assert.Equal(t, 4, w)
	assert.Equal(t, 10, r)
}

func TestOpenAICacheTokens_FallsBackToCacheCreationTokens(t *testing.T) {
	usage := gjsonMust(`{"prompt_tokens_details":{"cached_tokens":7,"cache_creation_tokens":3}}`)
	w, r := translate.OpenAICacheTokens(usage)
	assert.Equal(t, 3, w)
	assert.Equal(t, 7, r)
}

func gjsonMust(raw string) gjson.Result {
	return gjson.Parse(raw)
}

func TestResponsesToAnthropicWriter_NonStreamingSinkKeepsInclusiveInput(t *testing.T) {
	const fixture = `event: response.completed
data: {"type":"response.completed","response":{"id":"r","status":"completed","model":"gpt-5.6-sol","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}],"usage":{"input_tokens":1200,"input_tokens_details":{"cached_tokens":800,"cache_write_tokens":256},"output_tokens":340}}}

`
	rec := httptest.NewRecorder()
	sink := &fakeUsageSink{}
	w := translate.NewResponsesToAnthropicWriter(rec, "gpt-5.6-sol", sink)
	require.NoError(t, w.Prelude(false))
	_, err := w.Write([]byte(fixture))
	require.NoError(t, err)
	require.NoError(t, w.Finalize())

	assert.Equal(t, 1200, sink.input, "billing sink must keep OpenAI inclusive input_tokens")
	assert.Equal(t, 256, sink.cacheCreation)
	assert.Equal(t, 800, sink.cacheRead)
	assert.Contains(t, rec.Body.String(), `"input_tokens":144`)
	assert.Contains(t, rec.Body.String(), `"cache_creation_input_tokens":256`)
}

func TestResponsesToAnthropicWriter_ForwardsReasoningTokens(t *testing.T) {
	const fixture = `event: response.completed
data: {"type":"response.completed","response":{"id":"r","status":"completed","model":"gpt-6-luna","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}],"usage":{"input_tokens":1200,"output_tokens":340,"output_tokens_details":{"reasoning_tokens":300}}}}

`
	for _, stream := range []bool{true, false} {
		sink := &fakeUsageSink{}
		w := translate.NewResponsesToAnthropicWriter(httptest.NewRecorder(), "gpt-6-luna", sink)
		require.NoError(t, w.Prelude(stream))
		_, err := w.Write([]byte(fixture))
		require.NoError(t, err)
		require.NoError(t, w.Finalize())
		assert.Equal(t, 300, sink.reasoning, "stream=%v", stream)
	}
}

func TestOpenAIReasoningTokens_PrefersResponsesShapeWithoutDoubleCounting(t *testing.T) {
	usage := gjson.Parse(`{"output_tokens_details":{"reasoning_tokens":21},"completion_tokens_details":{"reasoning_tokens":7}}`)
	assert.Equal(t, 21, translate.OpenAIReasoningTokens(usage))
	assert.Equal(t, 7, translate.OpenAIReasoningTokens(gjson.Parse(`{"completion_tokens_details":{"reasoning_tokens":7}}`)))
}

func TestSSETranslator_ForwardsAnthropicUsageModifiers(t *testing.T) {
	t.Run("streaming message_start", func(t *testing.T) {
		rec := httptest.NewRecorder()
		sink := &fakeUsageSink{}
		translator := translate.NewSSETranslator(rec, "claude-opus-5", sink)
		translator.Header().Set("Content-Type", "text/event-stream")
		translator.WriteHeader(http.StatusOK)

		event := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-opus-5\",\"usage\":{\"input_tokens\":3,\"output_tokens\":0,\"cache_creation_input_tokens\":9217,\"cache_creation\":{\"ephemeral_5m_input_tokens\":17,\"ephemeral_1h_input_tokens\":9200},\"speed\":\"fast\",\"inference_geo\":\"us\"}}}\n\n"
		_, err := translator.Write([]byte(event))
		require.NoError(t, err)

		assert.Equal(t, 9200, sink.cacheCreation1h)
		assert.True(t, sink.cacheSplitReported)
		assert.Equal(t, "fast", sink.speed)
		assert.Equal(t, "us", sink.inferenceGeo)
	})
	t.Run("non-streaming body without split", func(t *testing.T) {
		rec := httptest.NewRecorder()
		sink := &fakeUsageSink{}
		translator := translate.NewSSETranslator(rec, "claude-opus-5", sink)
		translator.Header().Set("Content-Type", "application/json")
		translator.WriteHeader(http.StatusOK)
		_, err := translator.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"model":"claude-opus-5","stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":4,"cache_creation_input_tokens":322}}`))
		require.NoError(t, err)
		require.NoError(t, translator.Finalize())

		assert.Equal(t, 322, sink.cacheCreation)
		assert.False(t, sink.cacheSplitReported)
	})
}

func TestAnthropicRequestCacheTTL1h(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{name: "no breakpoints", body: `{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`, want: false},
		{name: "default 5m breakpoint", body: `{"system":[{"type":"text","text":"s","cache_control":{"type":"ephemeral"}}]}`, want: false},
		{name: "explicit 5m breakpoint", body: `{"tools":[{"name":"t","cache_control":{"type":"ephemeral","ttl":"5m"}}]}`, want: false},
		{name: "all block breakpoints 1h", body: `{"tools":[{"name":"t","cache_control":{"type":"ephemeral","ttl":"1h"}}],"system":[{"type":"text","text":"s","cache_control":{"type":"ephemeral","ttl":"1h"}}],"messages":[{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral","ttl":"1h"}}]}]}`, want: true},
		{name: "top-level automatic 1h", body: `{"cache_control":{"type":"ephemeral","ttl":"1h"},"messages":[{"role":"user","content":"hi"}]}`, want: true},
		{name: "1h prefix with 5m tail", body: `{"system":[{"type":"text","text":"s","cache_control":{"type":"ephemeral","ttl":"1h"}}],"messages":[{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral"}}]}]}`, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, translate.AnthropicRequestCacheTTL1h([]byte(tc.body)))
		})
	}
}
