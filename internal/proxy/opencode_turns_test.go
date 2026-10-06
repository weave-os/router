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

// OpenCode v2's native session and title request shapes are used without a
// Weave plugin or lifecycle-agent header.
const openCodeResponsesBody = `{
	"model":"auto",
	"stream":false,
	"tools":[{"type":"function","name":"bash","parameters":{"type":"object"}}],
	"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Return the test marker"}]}]
}`

const openCodeHardPinModel = "gpt-4o-mini"

func newOpenCodeTurnSvc(fr *fakeRouter, store *fakePinStore) *proxy.Service {
	responsesResp := func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"resp_1","object":"response","output":[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok"}]}]}`)
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
			providers.ProviderOpenAI:    &fakeProvider{proxyResponse: responsesResp},
		},
		nil, false, nil, store, false,
		providers.ProviderOpenAI, openCodeHardPinModel, nil,
	)
}

const openCodeParentSession = "ses_opencode_parent"

// OpenCode v2 names the parent session in a native header. Its distinct task
// prompt keeps the subagent pin off the parent conversation's.
func TestService_OpenCodeNativeSubagentSharesSessionIDButNotPin(t *testing.T) {
	const childBody = `{
	"model":"auto",
	"stream":false,
	"tools":[{"type":"function","name":"bash","parameters":{"type":"object"}}],
	"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Reply with the single word pong"}]}]
}`
	store := newFakePinStore()
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: "gpt-4o", Reason: "cluster"}}
	svc := newOpenCodeTurnSvc(fr, store)
	apiKeyID := uuid.New().String()

	send := func(body string, headers map[string]string) proxy.ClientIdentity {
		httpReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(""))
		httpReq.Header.Set("X-App", proxy.ClientAppOpencode)
		for name, value := range headers {
			httpReq.Header.Set(name, value)
		}
		identity := proxy.ClientIdentityFromHeaders(httpReq.Header)
		ctx := context.WithValue(authedCtx(apiKeyID), proxy.ClientIdentityContextKey{}, identity)
		require.NoError(t, svc.ProxyOpenAIResponses(ctx, []byte(body), httptest.NewRecorder(), httpReq))
		return identity
	}

	parent := send(openCodeResponsesBody, map[string]string{requestcontext.OpenCodeSessionHeader: openCodeParentSession})
	require.NotEmpty(t, store.upserts)
	parentKey := store.upserts[0].SessionKey
	parentUpserts := len(store.upserts)

	child := send(childBody, map[string]string{
		requestcontext.OpenCodeSessionHeader:       "ses_opencode_child",
		requestcontext.OpenCodeParentSessionHeader: openCodeParentSession,
	})

	assert.Equal(t, openCodeParentSession, parent.SessionID)
	assert.Equal(t, openCodeParentSession, child.SessionID, "a subagent reports its parent's session id")
	require.Greater(t, len(store.upserts), parentUpserts, "the subagent anchors its own pin")
	for _, upsert := range store.upserts[parentUpserts:] {
		assert.NotEqual(t, parentKey, upsert.SessionKey, "subagent turns must not overwrite the parent conversation's pin")
	}
}

// A router command carried in OpenCode's Responses tool-result history is
// extracted and acted on before classification.
func TestService_OpenCode_ToolOutputCommandsStayActionable(t *testing.T) {
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
	ctx := context.WithValue(authedCtx(uuid.New().String()), proxy.ClientIdentityContextKey{}, proxy.ClientIdentityFromHeaders(httpReq.Header))
	rec := httptest.NewRecorder()
	require.NoError(t, svc.ProxyOpenAIResponses(ctx, []byte(forceBody), rec, httpReq))

	assert.Equal(t, 0, fr.routeCalls, "force-model command must bypass fresh routing")
	require.NotEmpty(t, store.upserts)
	assert.Equal(t, "gpt-5", store.upserts[0].Model)
	assert.Equal(t, "gpt-5", rec.Header().Get(proxy.HeaderRouterModel))
}

const (
	openCodeHeaderlessMainBody = `{"model":"auto","stream":false,
		"tools":[{"type":"function","name":"bash","parameters":{"type":"object"}}],
		"input":[
			{"role":"system","content":"You are an AI agent running in OpenCode."},
			{"role":"user","content":"who are you?"}]}`
	openCodeHeaderlessTitleBody = `{"model":"auto","stream":false,"input":[
		{"role":"system","content":"You are a title generator. You output ONLY a thread title. Nothing else.\n\n<task>\nGenerate a brief title that would help the user find this conversation later."},
		{"role":"user","content":"who are you?"}]}`
)

func headerlessSender(t *testing.T, svc *proxy.Service, clientApp string) func(body string) *httptest.ResponseRecorder {
	t.Helper()
	apiKeyID := uuid.New().String()
	return func(body string) *httptest.ResponseRecorder {
		httpReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(""))
		httpReq.Header.Set("X-App", clientApp)
		ctx := context.WithValue(authedCtx(apiKeyID), proxy.ClientIdentityContextKey{}, proxy.ClientIdentityFromHeaders(httpReq.Header))
		rec := httptest.NewRecorder()
		require.NoError(t, svc.ProxyOpenAIResponses(ctx, []byte(body), rec, httpReq))
		return rec
	}
}

// Headerless OpenCode title calls derive the same session key from their
// constant system prompt. They must score independently, render no routing
// marker (OpenCode titles the session with the reply's first line), and neither
// serve nor rewrite the conversation's automatic pin.
func TestService_OpenCodeHeaderlessTitleScoresWithoutTouchingThePin(t *testing.T) {
	store := newFakePinStore()
	store.persistUpserts = true
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: "gpt-4o", Reason: "cluster"}}
	send := headerlessSender(t, newOpenCodeTurnSvc(fr, store), proxy.ClientAppOpencode)

	rec := send(openCodeHeaderlessMainBody)
	require.Len(t, store.upserts, 1)
	require.Contains(t, rec.Body.String(), "Weave Router", "main-loop turns carry the routing marker")

	for range 2 {
		getsBefore := store.getCalls
		rec := send(openCodeHeaderlessTitleBody)
		assert.Equal(t, "gpt-4o", rec.Header().Get(proxy.HeaderRouterModel), "the title uses its independently scored model")
		assert.NotContains(t, rec.Body.String(), "Weave Router", "a routing marker would become the session title")
		assert.Equal(t, 2, store.getCalls-getsBefore, "title turns read only session and legacy thread force state")
	}

	assert.Equal(t, 3, fr.routeCalls, "each title must be scored independently of the conversation pin")
	assert.Len(t, store.upserts, 1, "title generation must not anchor a pin")
}

// The title-agent prompt is only trusted from OpenCode: another client sending
// the same body keeps ordinary scoring and its session pin.
func TestService_OpenCodeTitlePromptIgnoredForOtherClients(t *testing.T) {
	store := newFakePinStore()
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: "gpt-4o", Reason: "cluster"}}
	send := headerlessSender(t, newOpenCodeTurnSvc(fr, store), proxy.ClientAppCodex)

	rec := send(openCodeHeaderlessTitleBody)

	assert.Equal(t, 1, fr.routeCalls, "a non-OpenCode caller cannot select the title hard-pin with the prompt")
	assert.Equal(t, "gpt-4o", rec.Header().Get(proxy.HeaderRouterModel))
	assert.NotEmpty(t, store.upserts, "a scored turn anchors the conversation pin")
}

// OpenCode v2 marks a subagent only by its native child-session headers; its
// body carries the full tool registry like any main-loop turn. A configured
// sub-agent override must serve it, while the parent stays scored and the same
// headers from another client select nothing.
func TestService_OpenCodeChildSessionUsesSubAgentOverride(t *testing.T) {
	const overrideModel = "gpt-5"
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: "gpt-4o", Reason: "cluster"}}
	svc := newOpenCodeTurnSvc(fr, newFakePinStore()).WithSubAgentOverride(providers.ProviderOpenAI, overrideModel)
	apiKeyID := uuid.New().String()
	send := func(clientApp string, headers map[string]string) *httptest.ResponseRecorder {
		httpReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(""))
		httpReq.Header.Set("X-App", clientApp)
		for name, value := range headers {
			httpReq.Header.Set(name, value)
		}
		ctx := context.WithValue(authedCtx(apiKeyID), proxy.ClientIdentityContextKey{}, proxy.ClientIdentityFromHeaders(httpReq.Header))
		rec := httptest.NewRecorder()
		require.NoError(t, svc.ProxyOpenAIResponses(ctx, []byte(openCodeResponsesBody), rec, httpReq))
		return rec
	}
	childHeaders := map[string]string{
		requestcontext.OpenCodeSessionHeader:       "ses_opencode_child",
		requestcontext.OpenCodeParentSessionHeader: openCodeParentSession,
	}

	parent := send(proxy.ClientAppOpencode, map[string]string{requestcontext.OpenCodeSessionHeader: openCodeParentSession})
	assert.Equal(t, "gpt-4o", parent.Header().Get(proxy.HeaderRouterModel))
	assert.Equal(t, 1, fr.routeCalls)

	child := send(proxy.ClientAppOpencode, childHeaders)
	assert.Equal(t, overrideModel, child.Header().Get(proxy.HeaderRouterModel))
	assert.Equal(t, 1, fr.routeCalls, "the sub-agent override bypasses the scorer")

	other := send(proxy.ClientAppCodex, childHeaders)
	assert.Equal(t, "gpt-4o", other.Header().Get(proxy.HeaderRouterModel))
	assert.Equal(t, 2, fr.routeCalls, "OpenCode child-session headers are ignored for other clients")
}
