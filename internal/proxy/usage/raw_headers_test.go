package usage_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/proxy/usage"
)

func TestRawAnthropicUnifiedHeaders(t *testing.T) {
	t.Run("captures every unified-prefixed header, case-insensitively", func(t *testing.T) {
		h := http.Header{}
		h.Set("Anthropic-Ratelimit-Unified-Status", "allowed")
		h.Set("anthropic-ratelimit-unified-overage-status", "rejected")
		h.Set("Content-Type", "application/json")
		h.Set("Request-Id", "req_123")

		got := usage.RawAnthropicUnifiedHeaders(h)
		assert.Equal(t, "allowed", got["anthropic-ratelimit-unified-status"])
		assert.Equal(t, "rejected", got["anthropic-ratelimit-unified-overage-status"])
		assert.Len(t, got, 2, "only unified-prefixed headers should be captured")
	})

	t.Run("no unified headers -> nil, not empty map", func(t *testing.T) {
		h := http.Header{}
		h.Set("Content-Type", "application/json")
		assert.Nil(t, usage.RawAnthropicUnifiedHeaders(h))
	})

	t.Run("empty header set -> nil", func(t *testing.T) {
		assert.Nil(t, usage.RawAnthropicUnifiedHeaders(http.Header{}))
	})
}

func TestRawCodexLimitHeaders(t *testing.T) {
	t.Run("captures quota-window and credit headers, case-insensitively", func(t *testing.T) {
		h := http.Header{}
		h.Set("X-Codex-Primary-Used-Percent", "42")
		h.Set("x-codex-primary-reset-at", "1793566800")
		h.Set("x-codex-secondary-window-minutes", "10080")
		h.Set("x-codex-credits-has-credits", "false")
		h.Set("Content-Type", "application/json")

		got := usage.RawCodexLimitHeaders(h)
		assert.Equal(t, map[string]string{
			"x-codex-primary-used-percent":     "42",
			"x-codex-primary-reset-at":         "1793566800",
			"x-codex-secondary-window-minutes": "10080",
			"x-codex-credits-has-credits":      "false",
		}, got)
	})

	t.Run("conversation identifiers are not limit headers", func(t *testing.T) {
		h := http.Header{}
		h.Set("x-codex-parent-thread-id", "thread_123")
		h.Set("x-codex-window-id", "window_456")
		assert.Nil(t, usage.RawCodexLimitHeaders(h))
	})

	t.Run("anthropic headers are not codex headers", func(t *testing.T) {
		h := http.Header{}
		h.Set("anthropic-ratelimit-unified-status", "allowed")
		assert.Nil(t, usage.RawCodexLimitHeaders(h))
	})
}
