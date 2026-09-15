package otel_test

import (
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/observability/otel"
	"weave-os/router/internal/providers"
)

func TestUsageExtractor_AnthropicNonStreaming(t *testing.T) {
	rec := httptest.NewRecorder()
	ext := otel.NewUsageExtractor(rec, "anthropic")

	body := `{"id":"msg_123","type":"message","role":"assistant","content":[{"type":"text","text":"Hello!"}],"model":"claude-sonnet-4-5","stop_reason":"end_turn","usage":{"input_tokens":42,"output_tokens":17}}`
	_, err := ext.Write([]byte(body))
	require.NoError(t, err)

	in, out := ext.Tokens()
	assert.Equal(t, 42, in)
	assert.Equal(t, 17, out)
}

func TestUsageExtractor_AnthropicStreaming(t *testing.T) {
	rec := httptest.NewRecorder()
	ext := otel.NewUsageExtractor(rec, "anthropic")

	events := []string{
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-sonnet-4-5\",\"usage\":{\"input_tokens\":100,\"output_tokens\":0}}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hi\"}}\n\n",
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":25}}\n\n",
	}

	for _, e := range events {
		_, err := ext.Write([]byte(e))
		require.NoError(t, err)
	}

	in, out := ext.Tokens()
	assert.Equal(t, 100, in)
	assert.Equal(t, 25, out)
}

func TestUsageExtractor_OpenAINonStreaming(t *testing.T) {
	rec := httptest.NewRecorder()
	ext := otel.NewUsageExtractor(rec, "openai")

	body := `{"id":"chatcmpl-1","object":"chat.completion","choices":[{"message":{"role":"assistant","content":"Hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":15,"completion_tokens":8,"total_tokens":23}}`
	_, err := ext.Write([]byte(body))
	require.NoError(t, err)

	in, out := ext.Tokens()
	assert.Equal(t, 15, in)
	assert.Equal(t, 8, out)
}

func TestUsageExtractor_OpenAIStreaming(t *testing.T) {
	rec := httptest.NewRecorder()
	ext := otel.NewUsageExtractor(rec, "openai")

	events := []string{
		"data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\n",
		"data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"choices\":[],\"usage\":{\"prompt_tokens\":20,\"completion_tokens\":12}}\n\n",
		"data: [DONE]\n\n",
	}

	for _, e := range events {
		_, err := ext.Write([]byte(e))
		require.NoError(t, err)
	}

	in, out := ext.Tokens()
	assert.Equal(t, 20, in)
	assert.Equal(t, 12, out)
}

func TestUsageExtractor_OpenAIResponsesStreaming(t *testing.T) {
	// The Codex (ChatGPT) subscription backend streams the Responses API: usage
	// lands on the terminal response.completed event, nested under response.usage
	// with input_tokens/output_tokens (+ input_tokens_details.cached_tokens),
	// not the chat-completions prompt_tokens shape. Billing the 5% subscription
	// fee depends on this parse.
	rec := httptest.NewRecorder()
	ext := otel.NewUsageExtractor(rec, "openai")

	events := []string{
		"event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\"}}\n\n",
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"usage\":{\"input_tokens\":120,\"output_tokens\":34,\"input_tokens_details\":{\"cached_tokens\":40}}}}\n\n",
	}
	for _, e := range events {
		_, err := ext.Write([]byte(e))
		require.NoError(t, err)
	}

	in, out := ext.Tokens()
	assert.Equal(t, 120, in)
	assert.Equal(t, 34, out)
	cacheCreation, cacheRead := ext.CacheTokens()
	assert.Equal(t, 0, cacheCreation)
	assert.Equal(t, 40, cacheRead, "Responses cached_tokens must map to cache-read for accurate subscription billing")
}

func TestUsageExtractor_OpenAIResponsesCacheWriteTokens(t *testing.T) {
	rec := httptest.NewRecorder()
	ext := otel.NewUsageExtractor(rec, "openai")

	events := []string{
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"usage\":{\"input_tokens\":1200,\"output_tokens\":34,\"input_tokens_details\":{\"cached_tokens\":800,\"cache_write_tokens\":256}}}}\n\n",
	}
	for _, e := range events {
		_, err := ext.Write([]byte(e))
		require.NoError(t, err)
	}

	cacheCreation, cacheRead := ext.CacheTokens()
	assert.Equal(t, 256, cacheCreation, "GPT-5.6 cache_write_tokens must map to cache-creation for 1.25x billing")
	assert.Equal(t, 800, cacheRead)
}

func TestUsageExtractor_OpenAIResponsesNonStreaming(t *testing.T) {
	rec := httptest.NewRecorder()
	ext := otel.NewUsageExtractor(rec, "openai")

	body := `{"id":"resp_2","object":"response","usage":{"input_tokens":50,"output_tokens":9,"input_tokens_details":{"cached_tokens":0}}}`
	_, err := ext.Write([]byte(body))
	require.NoError(t, err)

	in, out := ext.Tokens()
	assert.Equal(t, 50, in)
	assert.Equal(t, 9, out)
}

func TestUsageExtractor_GoogleStreaming(t *testing.T) {
	rec := httptest.NewRecorder()
	ext := otel.NewUsageExtractor(rec, "google")

	events := []string{
		"data: {\"id\":\"chatcmpl-g\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"content\":\"Yo\"}}]}\n\n",
		"data: {\"id\":\"chatcmpl-g\",\"object\":\"chat.completion.chunk\",\"choices\":[],\"usage\":{\"prompt_tokens\":30,\"completion_tokens\":5}}\n\n",
		"data: [DONE]\n\n",
	}

	for _, e := range events {
		_, err := ext.Write([]byte(e))
		require.NoError(t, err)
	}

	in, out := ext.Tokens()
	assert.Equal(t, 30, in)
	assert.Equal(t, 5, out)
}

func TestUsageExtractor_NoUsageReturnsZero(t *testing.T) {
	rec := httptest.NewRecorder()
	ext := otel.NewUsageExtractor(rec, "anthropic")

	body := `{"id":"msg_1","type":"message","content":[]}`
	_, err := ext.Write([]byte(body))
	require.NoError(t, err)

	in, out := ext.Tokens()
	assert.Equal(t, 0, in)
	assert.Equal(t, 0, out)

	cacheCreation, cacheRead := ext.CacheTokens()
	assert.Equal(t, 0, cacheCreation)
	assert.Equal(t, 0, cacheRead)
}

func TestUsageExtractor_AnthropicCacheTokens_NonStreaming(t *testing.T) {
	rec := httptest.NewRecorder()
	ext := otel.NewUsageExtractor(rec, "anthropic")

	body := `{"id":"msg_456","type":"message","role":"assistant","content":[{"type":"text","text":"OK"}],"model":"claude-sonnet-4-5","stop_reason":"end_turn","usage":{"input_tokens":42,"output_tokens":17,"cache_creation_input_tokens":256,"cache_read_input_tokens":1024}}`
	_, err := ext.Write([]byte(body))
	require.NoError(t, err)

	in, out := ext.Tokens()
	assert.Equal(t, 42, in)
	assert.Equal(t, 17, out)

	cacheCreation, cacheRead := ext.CacheTokens()
	assert.Equal(t, 256, cacheCreation)
	assert.Equal(t, 1024, cacheRead)
}

func TestUsageExtractor_AnthropicCacheTokens_Streaming(t *testing.T) {
	rec := httptest.NewRecorder()
	ext := otel.NewUsageExtractor(rec, "anthropic")

	// cache tokens arrive in message_start; subsequent message_delta carries
	// only output_tokens and must not clobber the cache values.
	events := []string{
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-sonnet-4-5\",\"usage\":{\"input_tokens\":100,\"output_tokens\":0,\"cache_creation_input_tokens\":300,\"cache_read_input_tokens\":900}}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hi\"}}\n\n",
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":25}}\n\n",
	}

	for _, e := range events {
		_, err := ext.Write([]byte(e))
		require.NoError(t, err)
	}

	in, out := ext.Tokens()
	assert.Equal(t, 100, in)
	assert.Equal(t, 25, out)

	cacheCreation, cacheRead := ext.CacheTokens()
	assert.Equal(t, 300, cacheCreation)
	assert.Equal(t, 900, cacheRead)
}

func TestUsageExtractor_OpenAICacheTokens_NonStreaming(t *testing.T) {
	rec := httptest.NewRecorder()
	ext := otel.NewUsageExtractor(rec, "openai")

	// OpenAI exposes cached prompt tokens via prompt_tokens_details.cached_tokens.
	// GPT-5.4 and earlier have no write field, so cache_creation stays at 0.
	body := `{"id":"chatcmpl-2","object":"chat.completion","choices":[{"message":{"role":"assistant","content":"Hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":15,"completion_tokens":8,"total_tokens":23,"prompt_tokens_details":{"cached_tokens":42}}}`
	_, err := ext.Write([]byte(body))
	require.NoError(t, err)

	in, out := ext.Tokens()
	assert.Equal(t, 15, in)
	assert.Equal(t, 8, out)

	cacheCreation, cacheRead := ext.CacheTokens()
	assert.Equal(t, 0, cacheCreation)
	assert.Equal(t, 42, cacheRead)
}

func TestUsageExtractor_OpenAIChatCompletionsCacheWriteTokens(t *testing.T) {
	rec := httptest.NewRecorder()
	ext := otel.NewUsageExtractor(rec, "openai")

	body := `{"id":"chatcmpl-2","object":"chat.completion","choices":[{"message":{"role":"assistant","content":"Hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":80,"completion_tokens":8,"prompt_tokens_details":{"cached_tokens":42,"cache_write_tokens":16}}}`
	_, err := ext.Write([]byte(body))
	require.NoError(t, err)

	cacheCreation, cacheRead := ext.CacheTokens()
	assert.Equal(t, 16, cacheCreation)
	assert.Equal(t, 42, cacheRead)
}

func TestUsageExtractor_OpenAICacheTokens_Streaming(t *testing.T) {
	rec := httptest.NewRecorder()
	ext := otel.NewUsageExtractor(rec, "openai")

	events := []string{
		"data: {\"id\":\"chatcmpl-2\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\n",
		"data: {\"id\":\"chatcmpl-2\",\"object\":\"chat.completion.chunk\",\"choices\":[],\"usage\":{\"prompt_tokens\":20,\"completion_tokens\":12,\"prompt_tokens_details\":{\"cached_tokens\":7}}}\n\n",
		"data: [DONE]\n\n",
	}

	for _, e := range events {
		_, err := ext.Write([]byte(e))
		require.NoError(t, err)
	}

	in, out := ext.Tokens()
	assert.Equal(t, 20, in)
	assert.Equal(t, 12, out)

	cacheCreation, cacheRead := ext.CacheTokens()
	assert.Equal(t, 0, cacheCreation)
	assert.Equal(t, 7, cacheRead)
}

func TestUsageExtractor_GoogleNativeCacheTokens_NonStreaming(t *testing.T) {
	rec := httptest.NewRecorder()
	ext := otel.NewUsageExtractor(rec, "google")

	// Native Gemini :generateContent body — no "usage" field, only "usageMetadata"
	// with cachedContentTokenCount. Without the native-shape branch the
	// extractor returned all zeros and Gemini caching was invisible end-to-end.
	body := `{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1200,"candidatesTokenCount":4,"totalTokenCount":1204,"cachedContentTokenCount":1024}}`
	_, err := ext.Write([]byte(body))
	require.NoError(t, err)

	in, out := ext.Tokens()
	assert.Equal(t, 1200, in)
	assert.Equal(t, 4, out)

	cacheCreation, cacheRead := ext.CacheTokens()
	assert.Equal(t, 0, cacheCreation)
	assert.Equal(t, 1024, cacheRead)
}

func TestUsageExtractor_GoogleNativeThoughtsTokensCountAsOutput(t *testing.T) {
	rec := httptest.NewRecorder()
	ext := otel.NewUsageExtractor(rec, "google")

	body := `{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":7,"thoughtsTokenCount":250,"totalTokenCount":357}}`
	_, err := ext.Write([]byte(body))
	require.NoError(t, err)

	in, out := ext.Tokens()
	assert.Equal(t, 100, in)
	assert.Equal(t, 257, out, "thoughtsTokenCount is billed as output")
}

func TestUsageExtractor_AnthropicGatewayStreaming(t *testing.T) {
	// Gateway providers use the native path (no translator RecordUsage call),
	// so the extractor's sniffing is the only usage source.
	rec := httptest.NewRecorder()
	ext := otel.NewUsageExtractor(rec, "anthropic_gateway")

	events := []string{
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-opus-5\",\"usage\":{\"input_tokens\":100,\"output_tokens\":0,\"cache_read_input_tokens\":900}}}\n\n",
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":25}}\n\n",
	}
	for _, e := range events {
		_, err := ext.Write([]byte(e))
		require.NoError(t, err)
	}

	in, out := ext.Tokens()
	assert.Equal(t, 100, in)
	assert.Equal(t, 25, out)

	_, cacheRead := ext.CacheTokens()
	assert.Equal(t, 900, cacheRead)
}

func TestUsageExtractor_OpenAIGatewayStreaming(t *testing.T) {
	rec := httptest.NewRecorder()
	ext := otel.NewUsageExtractor(rec, "openai_gateway")

	events := []string{
		"data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\n",
		"data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"choices\":[],\"usage\":{\"prompt_tokens\":20,\"completion_tokens\":12}}\n\n",
		"data: [DONE]\n\n",
	}
	for _, e := range events {
		_, err := ext.Write([]byte(e))
		require.NoError(t, err)
	}

	in, out := ext.Tokens()
	assert.Equal(t, 20, in)
	assert.Equal(t, 12, out)
}

func TestUsageExtractor_RecordedUsageSurvivesUsagelessChunk(t *testing.T) {
	// Some OpenAI-compat upstreams keep emitting a null/empty usage object
	// after the terminal chunk; that must not wipe the counts already seen.
	rec := httptest.NewRecorder()
	ext := otel.NewUsageExtractor(rec, "openai_gateway")

	events := []string{
		"data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"choices\":[],\"usage\":{\"prompt_tokens\":20,\"completion_tokens\":12}}\n\n",
		"data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"choices\":[],\"usage\":{}}\n\n",
	}
	for _, e := range events {
		_, err := ext.Write([]byte(e))
		require.NoError(t, err)
	}

	in, out := ext.Tokens()
	assert.Equal(t, 20, in)
	assert.Equal(t, 12, out)
}

func TestUsageExtractor_RecordCacheUsage_NilReceiver(t *testing.T) {
	var ext *otel.UsageExtractor
	creation, read := ext.CacheTokens()
	assert.Equal(t, 0, creation)
	assert.Equal(t, 0, read)
}

func TestUsageExtractor_AnthropicResponse(t *testing.T) {
	anthropicToolTurn := []string{
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":100}}}\n\n",
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n",
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":\"Read\"}}\n\n",
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":2,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_2\",\"name\":\"Grep\"}}\n\n",
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":40}}\n\n",
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
	}

	tests := []struct {
		name              string
		provider          string
		writes            []string
		wantStopReason    string
		wantToolUseBlocks int
		wantObserved      bool
	}{
		{
			name:              "streaming tool turn",
			provider:          "anthropic",
			writes:            anthropicToolTurn,
			wantStopReason:    "tool_use",
			wantToolUseBlocks: 2,
			wantObserved:      true,
		},
		{
			name:              "gateway family dispatch parses identically",
			provider:          "anthropic_gateway",
			writes:            anthropicToolTurn,
			wantStopReason:    "tool_use",
			wantToolUseBlocks: 2,
			wantObserved:      true,
		},
		{
			name:              "stream cut before message_delta is unobserved",
			provider:          "anthropic",
			writes:            anthropicToolTurn[:4],
			wantStopReason:    "",
			wantToolUseBlocks: 2,
			wantObserved:      false,
		},
		{
			name:     "non-streaming body",
			provider: "anthropic",
			writes: []string{
				"{\"type\":\"message\",\"content\":[{\"type\":\"text\",\"text\":\"ok\"},{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":\"Read\"}]," +
					"\"stop_reason\":\"tool_use\",\"usage\":{\"input_tokens\":10,\"output_tokens\":5}}",
			},
			wantStopReason:    "tool_use",
			wantToolUseBlocks: 1,
			wantObserved:      true,
		},
		{
			name:     "end_turn with no tool blocks is a measured zero",
			provider: "anthropic",
			writes: []string{
				"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":10}}}\n\n",
				"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":4}}\n\n",
			},
			wantStopReason:    "end_turn",
			wantToolUseBlocks: 0,
			wantObserved:      true,
		},
		{
			name:     "openai family leaves the accessor unobserved",
			provider: "openai",
			writes: []string{
				"data: {\"id\":\"chatcmpl-1\",\"choices\":[{\"delta\":{\"content\":\"Hi\"},\"finish_reason\":\"tool_calls\"}]}\n\n",
				"data: {\"id\":\"chatcmpl-1\",\"choices\":[],\"usage\":{\"prompt_tokens\":20,\"completion_tokens\":12}}\n\n",
				"data: [DONE]\n\n",
			},
			wantStopReason:    "",
			wantToolUseBlocks: 0,
			wantObserved:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ext := otel.NewUsageExtractor(httptest.NewRecorder(), tt.provider)
			for _, w := range tt.writes {
				_, err := ext.Write([]byte(w))
				require.NoError(t, err)
			}

			stopReason, toolUseBlocks, observed := ext.AnthropicResponse()
			assert.Equal(t, tt.wantStopReason, stopReason)
			assert.Equal(t, tt.wantToolUseBlocks, toolUseBlocks)
			assert.Equal(t, tt.wantObserved, observed)
		})
	}
}

func TestUsageExtractor_OpenAIChatResponse(t *testing.T) {
	// The same tool call arrives as several fragments under one index, so the
	// count must follow indices rather than frames.
	chatToolTurn := []string{
		"data: {\"id\":\"chatcmpl-1\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"}}]}\n\n",
		"data: {\"id\":\"chatcmpl-1\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"function\":{\"name\":\"read\",\"arguments\":\"\"}}]}}]}\n\n",
		"data: {\"id\":\"chatcmpl-1\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{\\\"path\\\"\"}}]}}]}\n\n",
		"data: {\"id\":\"chatcmpl-1\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":1,\"id\":\"call_2\",\"function\":{\"name\":\"grep\",\"arguments\":\"{}\"}}]}}]}\n\n",
		"data: {\"id\":\"chatcmpl-1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n",
		"data: {\"id\":\"chatcmpl-1\",\"choices\":[],\"usage\":{\"prompt_tokens\":20,\"completion_tokens\":12}}\n\n",
		"data: [DONE]\n\n",
	}

	tests := []struct {
		name             string
		provider         string
		writes           []string
		wantFinishReason string
		wantToolCalls    int
		wantObserved     bool
	}{
		{
			name:             "streaming tool turn counts each call once",
			provider:         "openai",
			writes:           chatToolTurn,
			wantFinishReason: "tool_calls",
			wantToolCalls:    2,
			wantObserved:     true,
		},
		{
			name:     "streaming text turn is a measured zero",
			provider: "openai",
			writes: []string{
				"data: {\"id\":\"chatcmpl-1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\n",
				"data: {\"id\":\"chatcmpl-1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n",
				"data: [DONE]\n\n",
			},
			wantFinishReason: "stop",
			wantToolCalls:    0,
			wantObserved:     true,
		},
		{
			name:             "stream cut before finish_reason is unobserved",
			provider:         "openai",
			writes:           chatToolTurn[:4],
			wantFinishReason: "",
			wantToolCalls:    0,
			wantObserved:     false,
		},
		{
			name:             "openai-compat family dispatch parses identically",
			provider:         "openrouter",
			writes:           chatToolTurn,
			wantFinishReason: "tool_calls",
			wantToolCalls:    2,
			wantObserved:     true,
		},
		{
			name:     "non-streaming body",
			provider: "openai",
			writes: []string{
				"{\"id\":\"chatcmpl-1\",\"object\":\"chat.completion\",\"choices\":[{\"index\":0,\"finish_reason\":\"tool_calls\"," +
					"\"message\":{\"role\":\"assistant\",\"tool_calls\":[{\"id\":\"call_1\",\"function\":{\"name\":\"read\",\"arguments\":\"{}\"}}]}}]," +
					"\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5}}",
			},
			wantFinishReason: "tool_calls",
			wantToolCalls:    1,
			wantObserved:     true,
		},
		{
			name:     "responses frames carry no choices and stay unobserved",
			provider: "openai",
			writes: []string{
				"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n",
				"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":9,\"output_tokens\":3}}}\n\n",
			},
			wantFinishReason: "",
			wantToolCalls:    0,
			wantObserved:     false,
		},
		{
			name:     "anthropic family leaves the accessor unobserved",
			provider: "anthropic",
			writes: []string{
				"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":4}}\n\n",
			},
			wantFinishReason: "",
			wantToolCalls:    0,
			wantObserved:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ext := otel.NewUsageExtractor(httptest.NewRecorder(), tt.provider)
			for _, w := range tt.writes {
				_, err := ext.Write([]byte(w))
				require.NoError(t, err)
			}

			finishReason, toolCalls, observed := ext.OpenAIChatResponse()
			assert.Equal(t, tt.wantFinishReason, finishReason)
			assert.Equal(t, tt.wantToolCalls, toolCalls)
			assert.Equal(t, tt.wantObserved, observed)
		})
	}
}

func TestUsageExtractor_OpenAIChatResponse_NilReceiver(t *testing.T) {
	var ext *otel.UsageExtractor
	finishReason, toolCalls, observed := ext.OpenAIChatResponse()
	assert.Equal(t, "", finishReason)
	assert.Equal(t, 0, toolCalls)
	assert.False(t, observed)
}

func TestUsageExtractor_AnthropicResponse_NilReceiver(t *testing.T) {
	var ext *otel.UsageExtractor
	stopReason, toolUseBlocks, observed := ext.AnthropicResponse()
	assert.Equal(t, "", stopReason)
	assert.Equal(t, 0, toolUseBlocks)
	assert.False(t, observed)
}

func TestUsageExtractor_OutputLimitEvidence(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		reason   string
		output   int
		wantCap  bool
	}{
		{"anthropic small cap", providers.ProviderAnthropic, "max_tokens", 128, true},
		{"anthropic old cap", providers.ProviderAnthropic, "max_tokens", 8192, true},
		{"anthropic large cap", providers.ProviderAnthropic, "max_tokens", 64000, true},
		{"anthropic long tool handoff", providers.ProviderAnthropic, "tool_use", 32000, false},
		{"anthropic long answer", providers.ProviderAnthropic, "end_turn", 32000, false},
		{"anthropic missing terminal", providers.ProviderAnthropic, "", 16000, false},
		{"anthropic gateway cap", providers.ProviderAnthropicGateway, "max_tokens", 512, true},
		{"chat cap", providers.ProviderOpenAI, "length", 8192, true},
		{"chat long tool handoff", providers.ProviderOpenAI, "tool_calls", 32000, false},
		{"chat long answer", providers.ProviderOpenAI, "stop", 32000, false},
		{"chat missing terminal", providers.ProviderOpenAI, "", 16000, false},
		{"openrouter cap", providers.ProviderOpenRouter, "length", 512, true},
		{"gemini cap", providers.ProviderGoogle, "MAX_TOKENS", 512, true},
		{"gemini long answer", providers.ProviderGoogle, "STOP", 32000, false},
		{"gemini missing terminal", providers.ProviderGoogle, "", 16000, false},
	}
	for _, tc := range tests {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", tc.name, stream), func(t *testing.T) {
				var body string
				switch providers.FamilyFor(tc.provider) {
				case providers.FamilyAnthropic:
					if stream {
						body = fmt.Sprintf("event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":%q},\"usage\":{\"output_tokens\":%d}}\n\n", tc.reason, tc.output)
					} else {
						body = fmt.Sprintf(`{"type":"message","content":[{"type":"tool_use","id":"call_1","name":"Read","input":{}}],"stop_reason":%q,"usage":{"input_tokens":100,"output_tokens":%d}}`, tc.reason, tc.output)
					}
				case providers.FamilyOpenAICompat:
					body = fmt.Sprintf(`{"choices":[{"index":0,"message":{"content":"answer"},"delta":{"content":"answer"},"finish_reason":%q}],"usage":{"prompt_tokens":100,"completion_tokens":%d}}`, tc.reason, tc.output)
					if stream {
						body = "data: " + body + "\n\ndata: [DONE]\n\n"
					}
				case providers.FamilyGemini:
					body = fmt.Sprintf(`{"candidates":[{"content":{"parts":[{"text":"answer"}]},"finishReason":%q}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":%d}}`, tc.reason, tc.output)
					if stream {
						body = "data: " + body + "\n\n"
					}
				}
				rec := httptest.NewRecorder()
				ext := otel.NewUsageExtractor(rec, tc.provider)
				for start := 0; start < len(body); start += 7 {
					_, err := ext.Write([]byte(body[start:min(start+7, len(body))]))
					require.NoError(t, err)
				}
				assert.Equal(t, tc.wantCap, ext.OutputLimitReached())
				_, output := ext.Tokens()
				assert.Equal(t, tc.output, output)
				assert.Equal(t, body, rec.Body.String(), "observing must not change the response")
			})
		}
	}
}

func TestUsageExtractor_OutputLimitIsAttemptScoped(t *testing.T) {
	capped := otel.NewUsageExtractor(httptest.NewRecorder(), providers.ProviderOpenAI)
	_, err := capped.Write([]byte("data: {\"choices\":[{\"finish_reason\":\"length\"}]}\n\n"))
	require.NoError(t, err)
	_, err = capped.Write([]byte("data: {\"choices\":[],\"usage\":{\"completion_tokens\":128}}\n\n"))
	require.NoError(t, err)
	assert.True(t, capped.OutputLimitReached(), "usage-only tails cannot erase the terminal cap")

	winner := otel.NewUsageExtractor(httptest.NewRecorder(), providers.ProviderOpenAI)
	_, err = winner.Write([]byte("data: {\"choices\":[{\"finish_reason\":\"tool_calls\"}],\"usage\":{\"completion_tokens\":32000}}\n\n"))
	require.NoError(t, err)
	assert.False(t, winner.OutputLimitReached(), "another attempt has independent terminal evidence")
	assert.False(t, (*otel.UsageExtractor)(nil).OutputLimitReached())
}
