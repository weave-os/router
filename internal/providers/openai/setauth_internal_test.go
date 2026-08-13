package openai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestSetAuth_PassthroughRejectsRouterKey guards the fix for PR #159's
// agentic-security review. The router auth middleware accepts both
// `X-Weave-Router-Key: rk_...` and `Authorization: Bearer rk_...`, so a
// passthrough that blindly forwarded the inbound Authorization could relay
// a router credential to api.openai.com when no BYOK/deployment key was
// configured. We strip Bearer tokens with the router prefix before
// forwarding; everything else flows through unchanged.
func TestSetAuth_PassthroughRejectsRouterKey(t *testing.T) {
	c := &Client{apiKey: ""} // passthrough mode

	t.Run("router-key Bearer is not forwarded", func(t *testing.T) {
		inbound := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		inbound.Header.Set("Authorization", "Bearer rk_abc123")
		upstream := httptest.NewRequest(http.MethodPost, "https://api.openai.com/v1/chat/completions", nil)

		c.setAuth(context.Background(), upstream, inbound)

		assert.Empty(t, upstream.Header.Get("Authorization"),
			"router-prefixed Bearer must be dropped — relaying it to api.openai.com is a credential leak across trust boundaries")
	})

	t.Run("genuine OpenAI Bearer flows through", func(t *testing.T) {
		inbound := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		inbound.Header.Set("Authorization", "Bearer sk-proj-real")
		upstream := httptest.NewRequest(http.MethodPost, "https://api.openai.com/v1/chat/completions", nil)

		c.setAuth(context.Background(), upstream, inbound)

		assert.Equal(t, "Bearer sk-proj-real", upstream.Header.Get("Authorization"),
			"genuine OpenAI API keys must reach api.openai.com untouched")
	})

	t.Run("ChatGPT OAuth is never forwarded to public OpenAI", func(t *testing.T) {
		inbound := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		inbound.Header.Set("Authorization", "Bearer eyJhbGciOi.codex.jwt")
		inbound.Header.Set("ChatGPT-Account-ID", "acct-123")
		upstream := httptest.NewRequest(http.MethodPost, "https://api.openai.com/v1/responses", nil)

		c.setAuth(context.Background(), upstream, inbound)

		assert.Empty(t, upstream.Header.Get("Authorization"),
			"a suppressed ChatGPT bearer must never fall through to api.openai.com")
	})

	t.Run("genuine OpenAI Bearer with stray account id still flows through", func(t *testing.T) {
		inbound := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		inbound.Header.Set("Authorization", "Bearer sk-proj-real")
		inbound.Header.Set("ChatGPT-Account-ID", "acct-stray")
		upstream := httptest.NewRequest(http.MethodPost, "https://api.openai.com/v1/responses", nil)

		c.setAuth(context.Background(), upstream, inbound)

		assert.Equal(t, "Bearer sk-proj-real", upstream.Header.Get("Authorization"),
			"a non-OAuth OpenAI client key must be unaffected by Codex-specific hardening")
	})

	t.Run("non-Bearer authorization flows through", func(t *testing.T) {
		// Some legacy / non-standard clients send just the token. We can't
		// know whether it's a router key without the prefix check, so we
		// fall through — upstream will 401 on invalid creds, which is the
		// correct failure mode.
		inbound := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		inbound.Header.Set("Authorization", "sk-proj-bare")
		upstream := httptest.NewRequest(http.MethodPost, "https://api.openai.com/v1/chat/completions", nil)

		c.setAuth(context.Background(), upstream, inbound)

		assert.Equal(t, "sk-proj-bare", upstream.Header.Get("Authorization"))
	})

	t.Run("empty authorization is a no-op", func(t *testing.T) {
		inbound := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		upstream := httptest.NewRequest(http.MethodPost, "https://api.openai.com/v1/chat/completions", nil)

		c.setAuth(context.Background(), upstream, inbound)

		assert.Empty(t, upstream.Header.Get("Authorization"))
	})
}
