package proxy_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"
)

const codexSearchModel = "gpt-6-luna"

func codexSearchContext() context.Context {
	ctx := context.WithValue(context.Background(), proxy.OpenAISubscriptionContextKey{}, "synthetic-codex-bearer")
	ctx = context.WithValue(ctx, proxy.OpenAIAccountIDContextKey{}, "synthetic-codex-account")
	return context.WithValue(ctx, proxy.InstallationIDContextKey{}, "3f4c1c2e-2b4d-4c3a-9f6d-0d1a2b3c4d5e")
}

func TestProxyMessages_CodexSubscriptionExecutesNativeWebSearch(t *testing.T) {
	for _, tc := range []struct {
		stream bool
		prompt string
	}{
		{false, "You are an assistant for performing a web search tool use"},
		{true, "You are an assistant for performing a web search tool use"},
		{false, "You run a web search for another Claude model, which is waiting on the result and can search again. Search once, then reply with a brief summary of the results."},
		{true, "You run a web search for another Claude model, which is waiting on the result and can search again. Search once, then reply with a brief summary of the results."},
	} {
		stream := tc.stream
		t.Run(fmt.Sprint(stream)+"/"+tc.prompt, func(t *testing.T) {
			p := &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: "+`{"type":"response.output_item.done","output_index":0,"item":{"type":"web_search_call","status":"completed","action":{"type":"search","sources":[{"type":"url","url":"https://docs.example/guide"}]}}}`+"\n\n")
				_, _ = io.WriteString(w, "data: "+`{"type":"response.output_item.done","output_index":1,"item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"The guide explains the feature.","annotations":[{"type":"url_citation","title":"Feature guide","url":"https://docs.example/guide"}]}]}}`+"\n\n")
				_, _ = io.WriteString(w, "data: "+`{"type":"response.completed","response":{"id":"resp_search","status":"completed","model":"gpt-6-luna","output":[],"usage":{"input_tokens":120,"output_tokens":22,"input_tokens_details":{"cached_tokens":40}}}}`+"\n\n")
			}}
			store := newFakePinStore()
			store.pin = sessionpin.Pin{Provider: providers.ProviderAnthropic, Model: "claude-opus-4-6", Reason: translate.ReasonUserForceModel, PinnedUntil: time.Now().Add(time.Hour)}
			store.hasPin = true
			svc := proxy.NewService(&fakeRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: codexSearchModel}}, map[string]providers.Client{providers.ProviderOpenAI: p, providers.ProviderAnthropic: &fakeProvider{proxyErr: errors.New("native Anthropic must not override an explicit Codex search")}}, nil, false, nil, store, false, providers.ProviderOpenAI, codexSearchModel, nil)
			ctx := codexSearchContext()
			ctx = context.WithValue(ctx, proxy.ClientIdentityContextKey{}, proxy.ClientIdentity{ClientApp: proxy.ClientAppClaudeCode, SessionID: "parent-session"})
			ctx = billing.WithSubscriptionOnly(ctx, billing.SubscriptionOnlyCreditsDepleted)
			body := []byte(fmt.Sprintf(`{"model":"%s","stream":%t,"max_tokens":1024,"system":%q,"tools":[{"type":"web_search_20250305","name":"web_search","allowed_domains":["docs.example"]}],"messages":[{"role":"user","content":"Perform a web search for the query: feature guide"}]}`, codexSearchModel, stream, tc.prompt))
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			req.Header.Set(proxy.ForceModelHeader, "opus")
			require.NoError(t, svc.ProxyMessages(ctx, body, rec, req))
			store.mu.Lock()
			assert.Zero(t, store.getCalls, "isolated helpers must not read parent or helper pins")
			assert.Empty(t, store.upserts, "isolated helpers must not write pins")
			assert.Empty(t, store.usages, "isolated helpers must not update session history")
			store.mu.Unlock()
			assert.Equal(t, "opus", req.Header.Get(proxy.ForceModelHeader), "isolated clone must not mutate the caller request")
			require.Len(t, p.proxyBodies, 1)
			assert.Equal(t, providers.EndpointResponses, p.proxyEndpoints[0])
			require.NotNil(t, p.proxyCreds[0])
			assert.Equal(t, requestcontext.SourceCodexSubscription, p.proxyCreds[0].Source)
			assert.Equal(t, "web_search", gjson.GetBytes(p.proxyBodies[0], "tools.0.type").String())
			assert.Equal(t, "docs.example", gjson.GetBytes(p.proxyBodies[0], "tools.0.filters.allowed_domains.0").String())
			assert.Equal(t, codexSearchModel, rec.Header().Get(proxy.HeaderRouterModel))
			assert.Contains(t, rec.Body.String(), "web_search_tool_result")
			assert.Contains(t, rec.Body.String(), "https://docs.example/guide")
			assert.Contains(t, rec.Body.String(), "The guide explains the feature.")
			if !stream {
				assert.Equal(t, int64(80), gjson.GetBytes(rec.Body.Bytes(), "usage.input_tokens").Int())
				assert.Equal(t, int64(22), gjson.GetBytes(rec.Body.Bytes(), "usage.output_tokens").Int())
				assert.Equal(t, int64(40), gjson.GetBytes(rec.Body.Bytes(), "usage.cache_read_input_tokens").Int())
			}
		})
	}
}

func TestProxyMessages_CodexSearchNeverFallsBackToAPIKeys(t *testing.T) {
	p := &fakeProvider{proxyErr: &providers.UpstreamStatusError{Status: http.StatusTooManyRequests}}
	store := newFakePinStore()
	store.hasPin = true
	store.pin = sessionpin.Pin{Provider: providers.ProviderAnthropic, Model: "claude-opus-4-6", Reason: translate.ReasonUserForceModel, PinnedUntil: time.Now().Add(time.Hour)}
	svc := proxy.NewService(&fakeRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: codexSearchModel}}, map[string]providers.Client{providers.ProviderOpenAI: p}, nil, false, nil, store, false, providers.ProviderOpenAI, codexSearchModel, nil).
		WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderOpenAI: {}})
	ctx := context.WithValue(codexSearchContext(), proxy.ClientIdentityContextKey{}, proxy.ClientIdentity{ClientApp: proxy.ClientAppClaudeCode, SessionID: "parent-session"})
	body := []byte(fmt.Sprintf(`{"model":"%s","max_tokens":1024,"system":"You are an assistant for performing a web search tool use","tools":[{"type":"web_search_20250305","name":"web_search"}],"messages":[{"role":"user","content":"Perform a web search for the query: a public query"}]}`, codexSearchModel))
	rec := httptest.NewRecorder()
	err := svc.ProxyMessages(ctx, body, rec, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
	require.Error(t, err)
	require.NotEmpty(t, p.proxyCreds)
	for _, creds := range p.proxyCreds {
		require.NotNil(t, creds)
		assert.True(t, creds.OAuth)
		assert.Equal(t, requestcontext.SourceCodexSubscription, creds.Source)
	}
	assert.NotContains(t, rec.Body.String(), "web_search_tool_result")
	store.mu.Lock()
	defer store.mu.Unlock()
	assert.Zero(t, store.getCalls)
	assert.Empty(t, store.upserts)
	assert.Empty(t, store.usages)
	assert.Zero(t, store.incrementCalls)
	assert.Zero(t, store.resetCalls)
	assert.Zero(t, store.overloadIncrementCalls)
	assert.Zero(t, store.overloadResetCalls)
}

func TestProxyMessages_CodexSearchPreservesTypedRateLimit(t *testing.T) {
	p := &fakeProvider{proxyErr: &providers.UpstreamErrorResponse{Status: http.StatusTooManyRequests, Headers: http.Header{"Retry-After": []string{"17"}}, Body: []byte(`{"error":{"type":"rate_limit_error","message":"search capacity is busy"}}`)}}
	svc := proxy.NewService(&fakeRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: codexSearchModel}}, map[string]providers.Client{providers.ProviderOpenAI: p}, nil, false, nil, nil, false, providers.ProviderOpenAI, codexSearchModel, nil)
	body := []byte(fmt.Sprintf(`{"model":"%s","max_tokens":1024,"system":"You are an assistant for performing a web search tool use","tools":[{"type":"web_search_20250305","name":"web_search"}],"messages":[{"role":"user","content":"Perform a web search for the query: a public query"}]}`, codexSearchModel))
	rec := httptest.NewRecorder()
	err := svc.ProxyMessages(codexSearchContext(), body, rec, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
	require.Error(t, err)
	var responseErr *providers.UpstreamErrorResponse
	var statusErr *providers.UpstreamStatusError
	if errors.As(err, &responseErr) {
		assert.Equal(t, http.StatusTooManyRequests, responseErr.Status)
	} else {
		require.ErrorAs(t, err, &statusErr)
		assert.Equal(t, http.StatusTooManyRequests, statusErr.Status)
	}
	assert.Equal(t, "17", rec.Header().Get("Retry-After"))
	assert.NotContains(t, rec.Body.String(), "web_search_tool_result")
}
