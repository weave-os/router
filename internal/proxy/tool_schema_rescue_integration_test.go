package proxy_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"workweave/router/internal/providers"
	"workweave/router/internal/providers/anthropic"
	"workweave/router/internal/proxy"
	"workweave/router/internal/router"
	"workweave/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// untranslatableToolBody asks for an Anthropic model while declaring a tool
// whose input schema has no Gemini representation (oneOf). The router
// cost-routes it to Gemini, so PrepareGemini fails before anything is
// dispatched.
//
// The tool is deliberately NOT one of claudeCodeOnlyToolNames: those are
// stripped from cross-vendor emit before the translator runs (#986 added the
// real-world offender, SendMessage, to that list), which would make these tests
// exercise the tool filter instead of the schema translator they are about.
const untranslatableToolBody = `{"model":"claude-opus-4-8","stream":true,` +
	`"messages":[{"role":"user","content":"hi"}],` +
	`"tools":[{"name":"weave_send_message","input_schema":{"type":"object","properties":` +
	`{"to":{"oneOf":[{"type":"string"},{"type":"number"}]}}}}]}`

// TestProxyMessages_UntranslatableToolSchemaRescuesToBaseline: a tool schema
// the routed provider cannot express is a property of the route, not of the
// request — the same tool translates fine for Anthropic. Before this rescue the
// turn returned an unclassified 502 that Claude Code retried eleven times, and
// wrote no telemetry row at all.
func TestProxyMessages_UntranslatableToolSchemaRescuesToBaseline(t *testing.T) {
	var (
		mu                     sync.Mutex
		anthropicCount         int
		anthropicReceivedModel string
	)

	anthropicUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		anthropicCount++
		anthropicReceivedModel = gjson.GetBytes(body, "model").String()
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(anthropicMessageSSE))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	defer anthropicUpstream.Close()

	// A Google client is registered so the routed provider is configured: the
	// turn must fail in translation, not for a missing provider key.
	googleProv := &fakeProvider{}
	store := newFakePinStore()
	tel := newCaptureTelemetry()
	svc := proxy.NewService(
		&fakeRouter{decision: router.Decision{Provider: providers.ProviderGoogle, Model: "gemini-2.5-pro", Reason: "cluster"}},
		map[string]providers.Client{
			providers.ProviderGoogle:    googleProv,
			providers.ProviderAnthropic: anthropic.NewClient("test-anthropic-key", anthropicUpstream.URL),
		},
		nil, false, nil, store, false, providers.ProviderAnthropic, "claude-haiku-4-5", tel,
	).WithDeploymentKeyedProviders(map[string]struct{}{
		providers.ProviderGoogle:    {},
		providers.ProviderAnthropic: {},
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))

	err := svc.ProxyMessages(
		authedCtx("22222222-2222-2222-2222-222222222222"),
		[]byte(untranslatableToolBody), rec, req,
	)
	require.NoError(t, err, "an untranslatable tool schema must be rescued on the baseline, not surfaced")

	mu.Lock()
	defer mu.Unlock()
	assert.Empty(t, googleProv.proxyBodies, "the Gemini upstream must never be called: translation failed pre-dispatch")
	assert.Equal(t, 1, anthropicCount, "the baseline Anthropic upstream serves the turn")
	assert.Equal(t, "claude-opus-4-8", anthropicReceivedModel, "the rescue requests the caller's model on Anthropic")

	respBody := rec.Body.String()
	assert.Contains(t, respBody, "event: message_start", "the client sees a real stream, not an error envelope")
	assert.Contains(t, respBody, "event: message_stop")
	assert.Equal(t, providers.ProviderAnthropic, rec.Header().Get(proxy.HeaderRouterProvider),
		"the served-provider header reflects the rescue")
	assert.Equal(t, "claude-opus-4-8", rec.Header().Get(proxy.HeaderRouterModel))
}

// TestProxyMessages_UntranslatableToolSchemaWithNoBaselineIsClassified: when
// the requested model IS the Gemini model there is no baseline to rescue to, so
// the turn still fails — but as a classified 400 the client will not retry,
// rather than the bare 502 "Upstream call failed." it used to fall through to.
func TestProxyMessages_UntranslatableToolSchemaWithNoBaselineIsClassified(t *testing.T) {
	googleProv := &fakeProvider{}
	store := newFakePinStore()
	svc := proxy.NewService(
		&fakeRouter{decision: router.Decision{Provider: providers.ProviderGoogle, Model: "gemini-2.5-pro", Reason: "cluster"}},
		map[string]providers.Client{providers.ProviderGoogle: googleProv},
		nil, false, nil, store, false, providers.ProviderGoogle, "gemini-2.5-flash", nil,
	).WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderGoogle: {}})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	body := []byte(`{"model":"gemini-2.5-pro","stream":true,` +
		`"messages":[{"role":"user","content":"hi"}],` +
		`"tools":[{"name":"weave_send_message","input_schema":{"type":"object","properties":` +
		`{"to":{"oneOf":[{"type":"string"},{"type":"number"}]}}}}]}`)

	err := svc.ProxyMessages(
		authedCtx("33333333-3333-3333-3333-333333333333"),
		body, rec, req,
	)
	require.Error(t, err, "with no baseline to rescue to the turn must still fail")
	assert.ErrorIs(t, err, translate.ErrGeminiSchemaIncompatible)
	assert.Empty(t, googleProv.proxyBodies, "nothing may be dispatched")

	cls, classified := proxy.ClassifyDispatchError(err)
	require.True(t, classified, "the handler must be able to classify this instead of defaulting to 502")
	assert.Equal(t, http.StatusBadRequest, cls.Status)
	assert.False(t, cls.RetryAfter, "retrying an unrepresentable schema is futile")
}
