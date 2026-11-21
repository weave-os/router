package proxy

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"weave-os/router/internal/ingress"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/policy"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestShouldEnableExtendedContext gates the 1M-context beta on request size:
// ordinary turns stay on the standard window; a large request trips the beta
// well before the ÷5 estimate's undercount could let it reach the 200K wall.
func TestShouldEnableExtendedContext(t *testing.T) {
	assert.False(t, shouldEnableExtendedContext(20_000, 8_000), "small turn must not opt into the 1M window")
	assert.False(t, shouldEnableExtendedContext(extendedContextTriggerTokens-8_000, 8_000), "exactly at the trigger is not over it")
	assert.True(t, shouldEnableExtendedContext(extendedContextTriggerTokens, 8_000), "estimate above the trigger turns the beta on")
	// A ~250K-real-token request estimates well above the trigger even with the
	// ÷5 undercount, so the beta is enabled before it can 400 on the 200K default.
	assert.True(t, shouldEnableExtendedContext(180_000, 8_000), "near-200K request opts into 1M")
}

func TestIsUpstreamContextOverflow_ProviderShapes(t *testing.T) {
	overflow := func(status int, body string) error {
		return &providers.UpstreamErrorResponse{Status: status, Body: []byte(body)}
	}
	for name, err := range map[string]error{
		"anthropic": overflow(400, `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 1050000 tokens > 1000000 maximum"}}`),
		"openai":    overflow(400, `{"error":{"message":"Your input exceeds the context window of this model.","type":"invalid_request_error","code":"context_length_exceeded"}}`),
		"gemini":    overflow(400, `{"error":{"code":400,"message":"The input token count (1200000) exceeds the maximum number of tokens allowed (1048576).","status":"INVALID_ARGUMENT"}}`),
		"vllm":      overflow(400, `{"object":"error","message":"This model's maximum context length is 131072 tokens. However, you requested 140000 tokens."}`),
		"wrapped":   fmt.Errorf("attempt 2: %w", overflow(400, `{"error":{"code":"context_length_exceeded"}}`)),
	} {
		assert.True(t, isUpstreamContextOverflow(err), name)
		assert.True(t, isContextOverflow(err), name)
	}
	for name, err := range map[string]error{
		"rate limit status": overflow(429, `{"error":{"code":"context_length_exceeded"}}`),
		"rate limit body":   overflow(400, `{"error":{"message":"rate limit: too many tokens per minute; maximum context length unaffected"}}`),
		"server error":      overflow(500, `{"error":{"message":"prompt is too long"}}`),
		"other 400":         overflow(400, `{"error":{"message":"tools.0.name: invalid"}}`),
		"not buffered":      errors.New("prompt is too long"),
	} {
		assert.False(t, isUpstreamContextOverflow(err), name)
	}
	assert.True(t, isContextOverflow(fmt.Errorf("router: %w", policy.ErrContextWindowExceeded)))
}

func TestClassifyDispatchError_ContextOverflowIsNative(t *testing.T) {
	for name, err := range map[string]error{
		"router":   fmt.Errorf("wrapped: %w", policy.ErrContextWindowExceeded),
		"upstream": &providers.UpstreamErrorResponse{Status: 400, Body: []byte(`{"error":{"code":"context_length_exceeded"}}`)},
	} {
		cls, ok := ClassifyDispatchError(err)
		require.True(t, ok, name)
		assert.Equal(t, ingress.DispatchErrorContextWindowExceeded, cls.Kind, name)
		assert.Equal(t, http.StatusBadRequest, cls.Status, name)
		assert.True(t, strings.HasPrefix(cls.Message, "prompt is too long"), "Claude Code and opencode key compaction off this wording")
		assert.True(t, cls.Kind.IsClientError(), name)
		assert.Equal(t, "context_length_exceeded", OpenAIErrorCode(cls.Kind))
	}
	assert.Empty(t, OpenAIErrorCode(ingress.DispatchErrorUpstreamStatus))
}

func TestFlushHelpersLeaveOverflowToHandler(t *testing.T) {
	overflow := &providers.UpstreamErrorResponse{Status: 400, Body: []byte(`{"error":{"code":"context_length_exceeded"}}`)}
	for name, flush := range map[string]func(http.ResponseWriter, error){
		"buffered":  flushBufferedIfPresent,
		"anthropic": flushUpstreamErrorAsAnthropic,
	} {
		rec := httptest.NewRecorder()
		flush(rec, overflow)
		assert.Zero(t, rec.Body.Len(), "%s: the handler renders the client-native overflow", name)
		assert.False(t, rec.Flushed, name)

		rec = httptest.NewRecorder()
		flush(rec, &providers.UpstreamErrorResponse{Status: 400, Body: []byte(`{"error":{"message":"bad tool"}}`)})
		assert.Equal(t, http.StatusBadRequest, rec.Code, "%s: other upstream errors still flush", name)
		assert.Contains(t, rec.Body.String(), "bad tool")
	}
}

func TestSSEErrorEventsCarryNativeOverflow(t *testing.T) {
	overflow := &providers.UpstreamErrorResponse{Status: 400, Body: []byte(`{"error":{"message":"maximum context length is 131072 tokens"}}`)}

	rec := httptest.NewRecorder()
	_ = emitAnthropicSSEErrorEvent(rec, overflow)
	assert.Contains(t, rec.Body.String(), `"type":"invalid_request_error"`)
	assert.Contains(t, rec.Body.String(), `prompt is too long`)

	rec = httptest.NewRecorder()
	_ = emitOpenAISSEErrorEvent(rec, overflow)
	assert.Contains(t, rec.Body.String(), `"code":"context_length_exceeded"`)
	assert.Contains(t, rec.Body.String(), `prompt is too long`)
}
