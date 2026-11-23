package credentials

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"weave-os/router/internal/requestcontext"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUnifiedLimitHeadersFrom_NoCaptureInstalled(t *testing.T) {
	// No capture holder installed → must return nil, not panic.
	assert.Nil(t, UnifiedLimitHeadersFrom(context.Background()))
}

func TestCaptureUnifiedLimitHeaders_RecordsOnSubscriptionCredential(t *testing.T) {
	ctx := WithUnifiedLimitCapture(context.Background())
	ctx = context.WithValue(ctx, requestcontext.CredentialsContextKey{}, &requestcontext.Credentials{
		APIKey: []byte("sk-ant-oat01-live"), Source: requestcontext.SourceSubscription, OAuth: true,
	})

	resp := http.Header{}
	resp.Set("anthropic-ratelimit-unified-status", "allowed")
	resp.Set("anthropic-ratelimit-unified-overage-status", "rejected")
	resp.Set("content-type", "application/json") // must NOT leak into the captured set

	CaptureUnifiedLimitHeaders(ctx, resp)

	got := UnifiedLimitHeadersFrom(ctx)
	require.NotNil(t, got)
	assert.Equal(t, "allowed", got["anthropic-ratelimit-unified-status"])
	assert.Equal(t, "rejected", got["anthropic-ratelimit-unified-overage-status"])
	if _, ok := got["content-type"]; ok {
		t.Errorf("non-unified header leaked into capture: %v", got)
	}
}

func TestCaptureUnifiedLimitHeaders_SkipsNonOAuthCredential(t *testing.T) {
	// BYOK/deployment-key call must not be recorded — headers describe a different account's quota.
	ctx := WithUnifiedLimitCapture(context.Background())
	ctx = context.WithValue(ctx, requestcontext.CredentialsContextKey{}, &requestcontext.Credentials{
		APIKey: []byte("sk-ant-api-deployment"), Source: requestcontext.SourceBYOK, OAuth: false,
	})

	resp := http.Header{}
	resp.Set("anthropic-ratelimit-unified-status", "allowed")
	CaptureUnifiedLimitHeaders(ctx, resp)

	assert.Nil(t, UnifiedLimitHeadersFrom(ctx), "non-OAuth credential must not be captured")
}

func TestCaptureUnifiedLimitHeaders_NoCredentialsResolved(t *testing.T) {
	ctx := WithUnifiedLimitCapture(context.Background())
	resp := http.Header{}
	resp.Set("anthropic-ratelimit-unified-status", "allowed")
	CaptureUnifiedLimitHeaders(ctx, resp) // no CredentialsContextKey set at all

	assert.Nil(t, UnifiedLimitHeadersFrom(ctx))
}

func TestCaptureUnifiedLimitHeaders_NoUnifiedHeadersPresent(t *testing.T) {
	// Subscription response with no unified headers must leave capture nil, not empty-but-non-nil.
	ctx := WithUnifiedLimitCapture(context.Background())
	ctx = context.WithValue(ctx, requestcontext.CredentialsContextKey{}, &requestcontext.Credentials{
		APIKey: []byte("sk-ant-oat01-live"), Source: requestcontext.SourceSubscription, OAuth: true,
	})
	CaptureUnifiedLimitHeaders(ctx, http.Header{"Content-Type": {"application/json"}})

	assert.Nil(t, UnifiedLimitHeadersFrom(ctx))
}

func TestUnifiedLimitHeadersJSON(t *testing.T) {
	t.Run("nothing captured -> nil (NULL column)", func(t *testing.T) {
		ctx := WithUnifiedLimitCapture(context.Background())
		assert.Nil(t, UnifiedLimitHeadersJSON(ctx))
	})

	t.Run("captured set round-trips through JSON", func(t *testing.T) {
		ctx := WithUnifiedLimitCapture(context.Background())
		ctx = context.WithValue(ctx, requestcontext.CredentialsContextKey{}, &requestcontext.Credentials{
			APIKey: []byte("sk-ant-oat01-live"), Source: requestcontext.SourceSubscription, OAuth: true,
		})
		resp := http.Header{}
		resp.Set("anthropic-ratelimit-unified-status", "allowed")
		resp.Set("anthropic-ratelimit-unified-5h-utilization", "0.42")
		CaptureUnifiedLimitHeaders(ctx, resp)

		b := UnifiedLimitHeadersJSON(ctx)
		require.NotNil(t, b)
		var got map[string]string
		require.NoError(t, json.Unmarshal(b, &got))
		assert.Equal(t, "allowed", got["anthropic-ratelimit-unified-status"])
		assert.Equal(t, "0.42", got["anthropic-ratelimit-unified-5h-utilization"])
	})
}

func TestCaptureUnifiedLimitHeaders_RecordsCodexHeadersOnCodexSubscription(t *testing.T) {
	ctx := WithUnifiedLimitCapture(context.Background())
	ctx = context.WithValue(ctx, requestcontext.CredentialsContextKey{}, &requestcontext.Credentials{
		APIKey: []byte("eyJhbGciOi.codex.jwt"), Source: requestcontext.SourceCodexSubscription, OAuth: true,
	})

	resp := http.Header{}
	resp.Set("x-codex-primary-used-percent", "42")
	resp.Set("x-codex-secondary-used-percent", "7")
	resp.Set("x-codex-parent-thread-id", "thread_123")
	CaptureUnifiedLimitHeaders(ctx, resp)

	assert.Equal(t, map[string]string{
		"x-codex-primary-used-percent":   "42",
		"x-codex-secondary-used-percent": "7",
	}, CodexLimitHeadersFrom(ctx))
	assert.Nil(t, UnifiedLimitHeadersFrom(ctx))

	b := CodexLimitHeadersJSON(ctx)
	require.NotNil(t, b)
	var got map[string]string
	require.NoError(t, json.Unmarshal(b, &got))
	assert.Equal(t, "42", got["x-codex-primary-used-percent"])
}

func TestCaptureUnifiedLimitHeaders_LastAttemptReplacesOtherFamily(t *testing.T) {
	ctx := WithUnifiedLimitCapture(context.Background())
	claudeCtx := context.WithValue(ctx, requestcontext.CredentialsContextKey{}, &requestcontext.Credentials{
		APIKey: []byte("sk-ant-oat01-live"), Source: requestcontext.SourceSubscription, OAuth: true,
	})
	claudeResp := http.Header{}
	claudeResp.Set("anthropic-ratelimit-unified-status", "rejected")
	CaptureUnifiedLimitHeaders(claudeCtx, claudeResp)

	codexCtx := context.WithValue(ctx, requestcontext.CredentialsContextKey{}, &requestcontext.Credentials{
		APIKey: []byte("eyJhbGciOi.codex.jwt"), Source: requestcontext.SourceCodexSubscription, OAuth: true,
	})
	codexResp := http.Header{}
	codexResp.Set("x-codex-primary-used-percent", "10")
	CaptureUnifiedLimitHeaders(codexCtx, codexResp)

	assert.Nil(t, UnifiedLimitHeadersJSON(ctx), "the Codex attempt served the turn, so the Claude reading must not persist")
	assert.Equal(t, "10", CodexLimitHeadersFrom(ctx)["x-codex-primary-used-percent"])
}
