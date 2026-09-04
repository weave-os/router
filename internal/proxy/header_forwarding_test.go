package proxy_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/proxy"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApplyForwardedClientHeaders(t *testing.T) {
	identity := proxy.ClientIdentity{Email: "engineer@example.com", SessionID: "session-1"}
	snowflakeCreds := &proxy.Credentials{
		ForwardedClientHeaders: []string{"X-SNOWFLAKE-APPLICATION", "X-Claude-Code-Session-Id"},
		BaggageHeader:          "X-SNOWFLAKE-BAGGAGE",
	}

	inbound := func(kv map[string]string) http.Header {
		h := http.Header{}
		for k, v := range kv {
			h.Set(k, v)
		}
		return h
	}

	t.Run("forwards nothing when the key configures nothing", func(t *testing.T) {
		upstream := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		ctx := identityCtx(&proxy.Credentials{}, identity)
		proxy.ApplyForwardedClientHeaders(ctx, upstream, inbound(map[string]string{
			"X-SNOWFLAKE-APPLICATION": "cortex-cli",
			"X-SNOWFLAKE-BAGGAGE":     "deployment=prod",
		}))
		assert.Empty(t, upstream.Header,
			"an unconfigured key must not leak the caller's correlation ids to its endpoint")
	})

	t.Run("copies configured headers verbatim", func(t *testing.T) {
		upstream := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		ctx := identityCtx(snowflakeCreds, identity)
		proxy.ApplyForwardedClientHeaders(ctx, upstream, inbound(map[string]string{
			"X-SNOWFLAKE-APPLICATION":  "cortex-cli/1.2.3",
			"X-Claude-Code-Session-Id": "abc-123",
			"X-Unrelated":              "nope",
		}))
		assert.Equal(t, "cortex-cli/1.2.3", upstream.Header.Get("X-SNOWFLAKE-APPLICATION"))
		assert.Equal(t, "abc-123", upstream.Header.Get("X-Claude-Code-Session-Id"))
		assert.Empty(t, upstream.Header.Get("X-Unrelated"),
			"only the headers the endpoint asked for may cross the hop")
	})

	t.Run("adds the resolved email to existing baggage as raw JSON", func(t *testing.T) {
		upstream := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		ctx := identityCtx(snowflakeCreds, identity)
		proxy.ApplyForwardedClientHeaders(ctx, upstream, inbound(map[string]string{
			"X-SNOWFLAKE-BAGGAGE": `{"existing-key":"existing-value"}`,
		}))
		raw := upstream.Header.Get("X-SNOWFLAKE-BAGGAGE")
		assert.NotContains(t, raw, "%22",
			"Cortex reads this bag as raw JSON; percent-encoding it would land as a literal string")
		var bag map[string]string
		require.NoError(t, json.Unmarshal([]byte(raw), &bag))
		assert.Equal(t, map[string]string{
			"existing-key": "existing-value",
			"on-behalf-of": "engineer@example.com",
		}, bag)
	})

	t.Run("emits baggage even when the caller sent none", func(t *testing.T) {
		upstream := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		ctx := identityCtx(snowflakeCreds, identity)
		proxy.ApplyForwardedClientHeaders(ctx, upstream, http.Header{})
		assert.JSONEq(t, `{"on-behalf-of":"engineer@example.com"}`, upstream.Header.Get("X-SNOWFLAKE-BAGGAGE"))
	})

	t.Run("replaces a client-supplied on-behalf-of", func(t *testing.T) {
		upstream := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		ctx := identityCtx(snowflakeCreds, identity)
		proxy.ApplyForwardedClientHeaders(ctx, upstream, inbound(map[string]string{
			"X-SNOWFLAKE-BAGGAGE": `{"on-behalf-of":"ceo@example.com","deployment":"prod"}`,
		}))
		assert.JSONEq(t, `{"deployment":"prod","on-behalf-of":"engineer@example.com"}`,
			upstream.Header.Get("X-SNOWFLAKE-BAGGAGE"),
			"the endpoint attributes spend off this bag, so a forged member must not survive")
	})

	t.Run("keeps the caller's baggage when no email resolved", func(t *testing.T) {
		upstream := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		ctx := identityCtx(snowflakeCreds, proxy.ClientIdentity{})
		proxy.ApplyForwardedClientHeaders(ctx, upstream, inbound(map[string]string{
			"X-SNOWFLAKE-BAGGAGE": `{"deployment":"prod"}`,
		}))
		assert.JSONEq(t, `{"deployment":"prod"}`, upstream.Header.Get("X-SNOWFLAKE-BAGGAGE"))
	})

	t.Run("forwards a non-JSON bag unchanged", func(t *testing.T) {
		upstream := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		ctx := identityCtx(snowflakeCreds, identity)
		proxy.ApplyForwardedClientHeaders(ctx, upstream, inbound(map[string]string{
			"X-SNOWFLAKE-BAGGAGE": "deployment=prod",
		}))
		assert.Equal(t, "deployment=prod", upstream.Header.Get("X-SNOWFLAKE-BAGGAGE"),
			"a bag we cannot parse must still reach the endpoint rather than be discarded")
	})

	t.Run("forwards a null bag unchanged", func(t *testing.T) {
		upstream := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		ctx := identityCtx(snowflakeCreds, identity)
		require.NotPanics(t, func() {
			proxy.ApplyForwardedClientHeaders(ctx, upstream, inbound(map[string]string{
				"X-SNOWFLAKE-BAGGAGE": "null",
			}))
		})
		assert.Equal(t, "null", upstream.Header.Get("X-SNOWFLAKE-BAGGAGE"))
	})

	t.Run("falls back to the ingress snapshot on router-built requests", func(t *testing.T) {
		upstream := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		ctx := proxy.WithForwardedHeaderSnapshot(
			identityCtx(snowflakeCreds, identity),
			[]*auth.ExternalAPIKey{{
				ForwardedClientHeaders: []string{"X-SNOWFLAKE-APPLICATION"},
				BaggageHeader:          "X-SNOWFLAKE-BAGGAGE",
			}},
			inbound(map[string]string{
				"X-SNOWFLAKE-APPLICATION": "cortex-cli/1.2.3",
				"X-SNOWFLAKE-BAGGAGE":     `{"deployment":"prod"}`,
				"X-Unrelated":             "nope",
			}),
		)
		// Compaction/handover summaries and Cortex web search synthesize their
		// own request, so nothing but the snapshot carries the caller's ids.
		proxy.ApplyForwardedClientHeaders(ctx, upstream, nil)
		assert.Equal(t, "cortex-cli/1.2.3", upstream.Header.Get("X-SNOWFLAKE-APPLICATION"))
		assert.JSONEq(t, `{"deployment":"prod","on-behalf-of":"engineer@example.com"}`,
			upstream.Header.Get("X-SNOWFLAKE-BAGGAGE"))
		assert.Empty(t, upstream.Header.Get("X-Unrelated"),
			"the snapshot must only capture headers a key actually forwards")
	})

	t.Run("falls back to the resolved session id when the client sent no header", func(t *testing.T) {
		upstream := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		// Claude Code builds before 2.0.x, and every non-Anthropic surface,
		// carry the session id in the body rather than the header.
		ctx := identityCtx(snowflakeCreds, identity)
		proxy.ApplyForwardedClientHeaders(ctx, upstream, http.Header{})
		assert.Equal(t, "session-1", upstream.Header.Get("X-Claude-Code-Session-Id"))
	})

	t.Run("does not invent a session id for other configured headers", func(t *testing.T) {
		upstream := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		ctx := identityCtx(snowflakeCreds, identity)
		proxy.ApplyForwardedClientHeaders(ctx, upstream, http.Header{})
		assert.Empty(t, upstream.Header.Get("X-SNOWFLAKE-APPLICATION"))
	})

	t.Run("does not overwrite auth with a blank inbound value", func(t *testing.T) {
		upstream := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		upstream.Header.Set("X-SNOWFLAKE-APPLICATION", "")
		ctx := identityCtx(snowflakeCreds, identity)
		proxy.ApplyForwardedClientHeaders(ctx, upstream, http.Header{})
		assert.Empty(t, upstream.Header.Get("X-SNOWFLAKE-APPLICATION"))
	})
}
