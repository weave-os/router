package anthropic_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/anthropic"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
)

const claudeCodeIdentity = "You are Claude Code, Anthropic's official CLI for Claude."

// upstreamBodyServer captures the request body the adapter sent upstream.
func upstreamBodyServer(t *testing.T, body *[]byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		read, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		*body = read
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1"}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// proxyWithBody dispatches requestBody through the adapter and returns the body
// the fake upstream received.
func proxyWithBody(ctx context.Context, t *testing.T, requestBody string) []byte {
	t.Helper()
	var upstreamBody []byte
	srv := upstreamBodyServer(t, &upstreamBody)
	c := anthropic.NewClient("", srv.URL)
	prep := providers.PreparedRequest{Body: []byte(requestBody), Headers: make(http.Header)}
	inbound := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	require.NoError(t, c.Proxy(ctx, router.Decision{Model: "claude-opus-5"}, prep, httptest.NewRecorder(), inbound))
	return upstreamBody
}

func subscriptionContext() context.Context {
	return context.WithValue(context.Background(), proxy.CredentialsContextKey{}, &proxy.Credentials{
		APIKey: []byte("sk-ant-oat01-subscription-token"),
		OAuth:  true,
		Source: "subscription",
	})
}

func TestSubscriptionRequestWithoutSystemPromptGetsClaudeCodeIdentity(t *testing.T) {
	// Anthropic answers a subscription-authenticated turn that does not identify
	// as Claude Code with 429 regardless of remaining quota.
	body := proxyWithBody(subscriptionContext(), t, `{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}]}`)

	system := gjson.GetBytes(body, "system")
	require.True(t, system.IsArray())
	assert.Len(t, system.Array(), 1)
	assert.Equal(t, claudeCodeIdentity, system.Array()[0].Get("text").String())
	assert.Equal(t, "text", system.Array()[0].Get("type").String())
}

func TestSubscriptionRequestKeepsCallerStringSystemPromptBehindIdentity(t *testing.T) {
	body := proxyWithBody(subscriptionContext(), t, `{"model":"claude-opus-5","system":"You are a haiku bot.","messages":[{"role":"user","content":"hi"}]}`)

	blocks := gjson.GetBytes(body, "system").Array()
	require.Len(t, blocks, 2)
	assert.Equal(t, claudeCodeIdentity, blocks[0].Get("text").String())
	assert.Equal(t, "You are a haiku bot.", blocks[1].Get("text").String())
}

func TestSubscriptionRequestPrependsIdentityToCallerSystemBlocks(t *testing.T) {
	body := proxyWithBody(subscriptionContext(), t, `{"model":"claude-opus-5","system":[{"type":"text","text":"House rules.","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":"hi"}]}`)

	blocks := gjson.GetBytes(body, "system").Array()
	require.Len(t, blocks, 2)
	assert.Equal(t, claudeCodeIdentity, blocks[0].Get("text").String())
	assert.Equal(t, "House rules.", blocks[1].Get("text").String())
	assert.Equal(t, "ephemeral", blocks[1].Get("cache_control.type").String(), "caller cache breakpoints survive")
}

func TestSubscriptionRequestAlreadyIdentifyingAsClaudeCodeIsUnchanged(t *testing.T) {
	original := `{"model":"claude-opus-5","system":[{"type":"text","text":"` + claudeCodeIdentity + `"},{"type":"text","text":"CLAUDE.md says: no emojis."}],"messages":[{"role":"user","content":"hi"}]}`
	body := proxyWithBody(subscriptionContext(), t, original)

	blocks := gjson.GetBytes(body, "system").Array()
	require.Len(t, blocks, 2, "a Claude Code client's own system prompt is not duplicated")
	assert.Equal(t, claudeCodeIdentity, blocks[0].Get("text").String())
}

func TestAPIKeyRequestKeepsCallerSystemPromptUntouched(t *testing.T) {
	ctx := context.WithValue(context.Background(), proxy.CredentialsContextKey{}, &proxy.Credentials{
		APIKey: []byte("sk-ant-api-byok"),
		Source: "byok",
	})
	body := proxyWithBody(ctx, t, `{"model":"claude-opus-5","system":"You are a haiku bot.","messages":[{"role":"user","content":"hi"}]}`)

	assert.Equal(t, "You are a haiku bot.", gjson.GetBytes(body, "system").String(), "paid API-key traffic carries no Claude Code identity")
}

func TestInboundSubscriptionBearerPassthroughGetsClaudeCodeIdentity(t *testing.T) {
	// Pure passthrough: no resolved credential and no deployment key, so the
	// caller's own sk-ant-oat bearer authenticates the turn upstream.
	var upstreamBody []byte
	srv := upstreamBodyServer(t, &upstreamBody)
	c := anthropic.NewClient("", srv.URL)
	prep := providers.PreparedRequest{
		Body:    []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}]}`),
		Headers: make(http.Header),
	}
	inbound := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	inbound.Header.Set("Authorization", "Bearer sk-ant-oat01-caller-token")

	require.NoError(t, c.Proxy(context.Background(), router.Decision{Model: "claude-opus-5"}, prep, httptest.NewRecorder(), inbound))

	assert.Equal(t, claudeCodeIdentity, gjson.GetBytes(upstreamBody, "system").Array()[0].Get("text").String())
}

func TestBearerGatewayRequestKeepsCallerSystemPromptUntouched(t *testing.T) {
	// A Bearer gateway resolves an OAuth-shaped credential that is not a Claude
	// subscription token; its upstream has no Claude Code requirement.
	var upstreamBody []byte
	srv := upstreamBodyServer(t, &upstreamBody)
	c := anthropic.NewClient("gateway-token", srv.URL, anthropic.WithAuthScheme(anthropic.AuthBearer))
	prep := providers.PreparedRequest{
		Body:    []byte(`{"model":"claude-opus-5","system":"You are a haiku bot.","messages":[{"role":"user","content":"hi"}]}`),
		Headers: make(http.Header),
	}
	inbound := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))

	require.NoError(t, c.Proxy(context.Background(), router.Decision{Model: "claude-opus-5"}, prep, httptest.NewRecorder(), inbound))

	assert.Equal(t, "You are a haiku bot.", gjson.GetBytes(upstreamBody, "system").String())
}
