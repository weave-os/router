package proxy_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// OpenCode's Responses turns have no body fingerprint for title, sub-agent, or
// compaction work: every lifecycle request carries the same "auto" model and
// tool registry. The typed X-Weave-OpenCode-Agent header is the only signal.
const openCodeResponsesBody = `{
	"model":"auto",
	"stream":false,
	"tools":[{"type":"function","name":"bash","parameters":{"type":"object"}}],
	"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Return the test marker"}]}]
}`

const openCodeHardPinModel = "gpt-4o-mini"

func newOpenCodeTurnSvc(fr *fakeRouter, store *fakePinStore) *proxy.Service {
	openAIResp := func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"chatcmpl_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}
	anthropicResp := func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`)
	}
	return proxy.NewService(
		fr,
		map[string]providers.Client{
			providers.ProviderAnthropic: &fakeProvider{proxyResponse: anthropicResp},
			providers.ProviderOpenAI:    &fakeProvider{proxyResponse: openAIResp},
		},
		nil, false, nil, store, false,
		providers.ProviderOpenAI, openCodeHardPinModel, nil,
	)
}

const openCodeParentSession = "ses_opencode_parent"

func openCodeRequest(t *testing.T, svc *proxy.Service, agent string) *httptest.ResponseRecorder {
	t.Helper()
	return openCodeSessionRequest(t, svc, agent, openCodeParentSession)
}

func openCodeSessionRequest(t *testing.T, svc *proxy.Service, agent, sessionID string) *httptest.ResponseRecorder {
	t.Helper()
	httpReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(""))
	httpReq.Header.Set("X-App", proxy.ClientAppOpencode)
	httpReq.Header.Set("Session-Id", sessionID)
	if agent != "" {
		httpReq.Header.Set(requestcontext.OpenCodeAgentHeader, agent)
	}
	// The auth middleware stashes the parsed identity before the proxy runs.
	ctx := context.WithValue(authedCtx(uuid.New().String()), proxy.ClientIdentityContextKey{}, proxy.ClientIdentityFromHeaders(httpReq.Header))
	rec := httptest.NewRecorder()
	require.NoError(t, svc.ProxyOpenAIResponses(ctx, []byte(openCodeResponsesBody), rec, httpReq))
	return rec
}

func TestService_OpenCodeAgent_TitleHardPinsWithoutTouchingThePin(t *testing.T) {
	store := newFakePinStore()
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: "gpt-4o", Reason: "cluster"}}
	svc := newOpenCodeTurnSvc(fr, store)

	rec := openCodeRequest(t, svc, string(requestcontext.OpenCodeAgentTitle))

	assert.Equal(t, 0, fr.routeCalls, "title generation must bypass the scorer")
	assert.Equal(t, openCodeHardPinModel, rec.Header().Get(proxy.HeaderRouterModel))
	assert.Empty(t, store.upserts, "title generation must not anchor a conversation pin")

	// The real conversation turn that follows still starts from a clean slate.
	rec = openCodeRequest(t, svc, string(requestcontext.OpenCodeAgentBuild))
	assert.Equal(t, 1, fr.routeCalls)
	assert.Equal(t, "gpt-4o", rec.Header().Get(proxy.HeaderRouterModel), "the title hard-pin must not leak into the conversation")
}

func TestService_OpenCodeAgent_CompactionHardPins(t *testing.T) {
	store := newFakePinStore()
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: "gpt-4o", Reason: "cluster"}}
	svc := newOpenCodeTurnSvc(fr, store)

	rec := openCodeRequest(t, svc, string(requestcontext.OpenCodeAgentCompaction))

	assert.Equal(t, 0, fr.routeCalls, "compaction must take the hard-pin path")
	assert.Equal(t, openCodeHardPinModel, rec.Header().Get(proxy.HeaderRouterModel))
	assert.Empty(t, store.upserts, "compaction must not rewrite the conversation pin")
}

// OpenCode runs an explore sub-agent in a child session (its own Session-Id;
// see test_subagent in opencode_conformance.py), so its pin writes must land
// on the child's key, never the parent conversation's.
func TestService_OpenCodeAgent_ExploreDoesNotOverwriteParentPin(t *testing.T) {
	store := newFakePinStore()
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: "gpt-4o", Reason: "cluster"}}
	svc := newOpenCodeTurnSvc(fr, store)

	openCodeRequest(t, svc, string(requestcontext.OpenCodeAgentBuild))
	require.NotEmpty(t, store.upserts)
	parentKey := store.upserts[0].SessionKey
	parentUpserts := len(store.upserts)

	fr.decision = router.Decision{Provider: providers.ProviderAnthropic, Model: "claude-opus-4-7", Reason: "cluster"}
	rec := openCodeSessionRequest(t, svc, string(requestcontext.OpenCodeAgentExplore), "ses_opencode_child")

	assert.Equal(t, 2, fr.routeCalls, "a sub-agent dispatch is scored fresh")
	assert.Equal(t, "claude-opus-4-7", rec.Header().Get(proxy.HeaderRouterModel))
	require.Greater(t, len(store.upserts), parentUpserts, "the child session anchors its own pin")
	for _, upsert := range store.upserts[parentUpserts:] {
		assert.NotEqual(t, parentKey, upsert.SessionKey, "sub-agent turns must not overwrite the parent conversation's pin")
	}
}

func TestService_OpenCodeAgent_BuildAndUnknownRouteAsMainLoop(t *testing.T) {
	for _, agent := range []string{string(requestcontext.OpenCodeAgentBuild), "reviewer", ""} {
		t.Run("agent="+agent, func(t *testing.T) {
			store := newFakePinStore()
			fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: "gpt-4o", Reason: "cluster"}}
			svc := newOpenCodeTurnSvc(fr, store)

			rec := openCodeRequest(t, svc, agent)

			assert.Equal(t, 1, fr.routeCalls, "main-loop turns keep ordinary scoring")
			assert.Equal(t, "gpt-4o", rec.Header().Get(proxy.HeaderRouterModel))
			require.NotEmpty(t, store.upserts, "main-loop turns anchor the conversation pin")
			assert.Equal(t, "gpt-4o", store.upserts[0].Model)
		})
	}
}

func TestService_OpenCodeAgent_HeaderIgnoredForOtherClients(t *testing.T) {
	store := newFakePinStore()
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: "gpt-4o", Reason: "cluster"}}
	svc := newOpenCodeTurnSvc(fr, store)

	httpReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(""))
	httpReq.Header.Set("X-App", proxy.ClientAppCodex)
	httpReq.Header.Set(requestcontext.OpenCodeAgentHeader, string(requestcontext.OpenCodeAgentTitle))
	ctx := context.WithValue(authedCtx(uuid.New().String()), proxy.ClientIdentityContextKey{}, proxy.ClientIdentityFromHeaders(httpReq.Header))
	rec := httptest.NewRecorder()
	require.NoError(t, svc.ProxyOpenAIResponses(ctx, []byte(openCodeResponsesBody), rec, httpReq))

	assert.Equal(t, 1, fr.routeCalls, "a non-OpenCode caller cannot select the title hard-pin with the header")
	assert.Equal(t, "gpt-4o", rec.Header().Get(proxy.HeaderRouterModel))
}

// The lifecycle header is metadata: a router command carried in Responses
// tool-result history is still extracted and acted on before classification.
func TestService_OpenCodeAgent_BuildKeepsToolOutputCommandsActionable(t *testing.T) {
	const forceBody = `{
		"model":"auto",
		"input":[
			{"type":"function_call","call_id":"call-1","name":"bash","arguments":"{}"},
			{"type":"function_call_output","call_id":"call-1","output":"/force-model gpt-5"}
		]
	}`
	store := newFakePinStore()
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: "gpt-4o", Reason: "cluster"}}
	svc := newOpenCodeTurnSvc(fr, store)

	httpReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(""))
	httpReq.Header.Set("X-App", proxy.ClientAppOpencode)
	httpReq.Header.Set("Session-Id", openCodeParentSession)
	httpReq.Header.Set(requestcontext.OpenCodeAgentHeader, string(requestcontext.OpenCodeAgentBuild))
	ctx := context.WithValue(authedCtx(uuid.New().String()), proxy.ClientIdentityContextKey{}, proxy.ClientIdentityFromHeaders(httpReq.Header))
	rec := httptest.NewRecorder()
	require.NoError(t, svc.ProxyOpenAIResponses(ctx, []byte(forceBody), rec, httpReq))

	assert.Equal(t, 0, fr.routeCalls, "force-model command must bypass fresh routing")
	require.NotEmpty(t, store.upserts)
	assert.Equal(t, "gpt-5", store.upserts[0].Model)
	assert.Equal(t, "gpt-5", rec.Header().Get(proxy.HeaderRouterModel))
}
