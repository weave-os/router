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
	"weave-os/router/internal/requestcontext"
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
	return context.WithValue(context.Background(), requestcontext.CredentialsContextKey{}, &requestcontext.Credentials{
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
	ctx := context.WithValue(context.Background(), requestcontext.CredentialsContextKey{}, &requestcontext.Credentials{
		APIKey: []byte("sk-ant-api-byok"),
		Source: "byok",
	})
	body := proxyWithBody(ctx, t, `{"model":"claude-opus-5","system":"You are a haiku bot.","messages":[{"role":"user","content":"hi"}]}`)

	assert.Equal(t, "You are a haiku bot.", gjson.GetBytes(body, "system").String(), "paid API-key traffic carries no Claude Code identity")
}

func TestInboundSubscriptionBearerPassthroughIsRefused(t *testing.T) {
	var upstreamBody []byte
	srv := upstreamBodyServer(t, &upstreamBody)
	c := anthropic.NewClient("", srv.URL)
	prep := providers.PreparedRequest{
		Body:    []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}]}`),
		Headers: make(http.Header),
	}
	inbound := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	inbound.Header.Set("Authorization", "Bearer sk-ant-oat01-caller-token")

	require.Error(t, c.Proxy(context.Background(), router.Decision{Model: "claude-opus-5"}, prep, httptest.NewRecorder(), inbound))
	assert.Nil(t, upstreamBody, "an unresolved inbound subscription bearer must never reach the upstream")
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

// desktopBillingLine mirrors the billing line Claude Desktop sends as its first
// system block. Its cch and prompt id change on every request.
const desktopBillingLine = "x-anthropic-billing-header: cc_version=2.1.325.00a; cc_entrypoint=claude-desktop; cch=00000; cc_prompt_id=00000000-0000-4000-8000-000000000000; cc_turn_origin=human"

// desktopSDKIdentity is the Agent SDK variant of claudeCodeIdentity that Desktop
// sends after its billing line.
const desktopSDKIdentity = "You are Claude Code, Anthropic's official CLI for Claude, running within the Claude Agent SDK."

func TestSubscriptionDesktopLayoutKeepsBillingLineFirst(t *testing.T) {
	// Regression: the router inserted its short identity ahead of Desktop's
	// billing line, so the changing line sat inside the cached prefix and every
	// turn rewrote the whole prompt (0 cache reads, ~88k cache writes).
	request := `{"model":"claude-opus-5","system":[` +
		`{"type":"text","text":"` + desktopBillingLine + `"},` +
		`{"type":"text","text":"` + desktopSDKIdentity + `"},` +
		`{"type":"text","text":"Project rules.","cache_control":{"type":"ephemeral","ttl":"1h","scope":"global"}}],` +
		`"messages":[{"role":"user","content":"hi"}]}`
	body := proxyWithBody(subscriptionContext(), t, request)

	blocks := gjson.GetBytes(body, "system").Array()
	require.Len(t, blocks, 3, "Desktop already identifies as Claude Code; nothing may be inserted")
	assert.Equal(t, desktopBillingLine, blocks[0].Get("text").String(), "billing line must stay the first block")
	assert.Equal(t, desktopSDKIdentity, blocks[1].Get("text").String())
	assert.Equal(t, "global", blocks[2].Get("cache_control.scope").String(), "caller breakpoint with scope survives")
}

func TestSubscriptionIdentityIsInsertedAfterBillingLine(t *testing.T) {
	// A billing-line-first body without an identity gets the identity after the
	// billing line, never ahead of it.
	request := `{"model":"claude-opus-5","system":[` +
		`{"type":"text","text":"` + desktopBillingLine + `"},` +
		`{"type":"text","text":"House rules.","cache_control":{"type":"ephemeral"}}],` +
		`"messages":[{"role":"user","content":"hi"}]}`
	body := proxyWithBody(subscriptionContext(), t, request)

	blocks := gjson.GetBytes(body, "system").Array()
	require.Len(t, blocks, 3)
	assert.Equal(t, desktopBillingLine, blocks[0].Get("text").String())
	assert.Equal(t, claudeCodeIdentity, blocks[1].Get("text").String())
	assert.Equal(t, "House rules.", blocks[2].Get("text").String())
	assert.Equal(t, "ephemeral", blocks[2].Get("cache_control.type").String(), "caller breakpoint survives")
}

func TestSubscriptionCLILayoutWithIdentityFirstIsUnchanged(t *testing.T) {
	// Native Claude Code sends its identity before the billing line; the router
	// adds nothing and the bytes are left exactly as the client sent them.
	request := `{"model":"claude-opus-5","system":[` +
		`{"type":"text","text":"` + claudeCodeIdentity + `"},` +
		`{"type":"text","text":"` + desktopBillingLine + `"},` +
		`{"type":"text","text":"Project rules.","cache_control":{"type":"ephemeral","ttl":"1h"}}],` +
		`"messages":[{"role":"user","content":"hi"}]}`
	body := proxyWithBody(subscriptionContext(), t, request)

	blocks := gjson.GetBytes(body, "system").Array()
	require.Len(t, blocks, 3, "no duplicate identity")
	assert.Equal(t, claudeCodeIdentity, blocks[0].Get("text").String())
	assert.Equal(t, desktopBillingLine, blocks[1].Get("text").String())
}

func TestAPIKeyBillingLineLayoutKeepsSystemUntouched(t *testing.T) {
	// Paid API-key traffic never gets the subscription identity, whatever the
	// billing line says; the caller's blocks are forwarded as sent.
	ctx := context.WithValue(context.Background(), requestcontext.CredentialsContextKey{}, &requestcontext.Credentials{
		APIKey: []byte("sk-ant-api-byok"),
		Source: "byok",
	})
	request := `{"model":"claude-opus-5","system":[` +
		`{"type":"text","text":"` + desktopBillingLine + `"},` +
		`{"type":"text","text":"` + desktopSDKIdentity + `"}],` +
		`"messages":[{"role":"user","content":"hi"}]}`
	body := proxyWithBody(ctx, t, request)

	blocks := gjson.GetBytes(body, "system").Array()
	require.Len(t, blocks, 2)
	assert.Equal(t, desktopBillingLine, blocks[0].Get("text").String())
	assert.Equal(t, desktopSDKIdentity, blocks[1].Get("text").String())
}

func TestSubscriptionAgentSDKIdentityIsInsertedAfterBillingLine(t *testing.T) {
	// SDK-driven clients lead with the billing line and then a generic Agent SDK
	// identity that is not Claude Code's. The router adds Claude Code's identity
	// after the billing line and keeps the SDK text behind it.
	const sdkAgent = "You are a Claude agent, built on Anthropic's Claude Agent SDK."
	request := `{"model":"claude-opus-5","system":[` +
		`{"type":"text","text":"` + desktopBillingLine + `"},` +
		`{"type":"text","text":"` + sdkAgent + `"}],` +
		`"messages":[{"role":"user","content":"hi"}]}`
	body := proxyWithBody(subscriptionContext(), t, request)

	blocks := gjson.GetBytes(body, "system").Array()
	require.Len(t, blocks, 3)
	assert.Equal(t, desktopBillingLine, blocks[0].Get("text").String())
	assert.Equal(t, claudeCodeIdentity, blocks[1].Get("text").String())
	assert.Equal(t, sdkAgent, blocks[2].Get("text").String())
}
