package google_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/google"
	"weave-os/router/internal/router"
	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const preservedGeminiBody = `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"thinkingConfig":{"includeThoughts":true}},"unknownNativeField":{"kept":true}}`

func TestNativeClient_PreserveNativeKeepsInboundActionAndQuery(t *testing.T) {
	var gotPath, gotQuery, gotKey, gotAccept string
	var gotBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotKey = r.Header.Get("x-goog-api-key")
		gotAccept = r.Header.Get("Accept")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"x\"}]}}]}\n\n"))
	}))
	defer upstream.Close()

	c := google.NewNativeClient("deployment-key", upstream.URL)
	rec := httptest.NewRecorder()
	inbound := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-3-pro-preview:streamGenerateContent?alt=sse&%24fields=candidates", strings.NewReader(""))
	prep := providers.PreparedRequest{Body: []byte(preservedGeminiBody), Headers: make(http.Header), PreserveNative: true}

	err := c.Proxy(context.Background(), router.Decision{Model: "gemini-3-pro-preview", Provider: providers.ProviderGoogle}, prep, rec, inbound)
	require.NoError(t, err)

	assert.Equal(t, "/v1beta/models/gemini-3-pro-preview:streamGenerateContent", gotPath)
	query, parseErr := url.ParseQuery(gotQuery)
	require.NoError(t, parseErr)
	assert.Equal(t, "sse", query.Get("alt"))
	assert.Equal(t, "candidates", query.Get("$fields"), "native query parameters beyond alt=sse must be preserved")
	assert.Equal(t, "deployment-key", gotKey)
	assert.Equal(t, "text/event-stream", gotAccept, "a preserved streaming action still negotiates SSE")
	assert.JSONEq(t, preservedGeminiBody, string(gotBody))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestNativeClient_PreserveNativeNonStreamingKeepsGenerateContentWithoutQuery(t *testing.T) {
	var gotPath, gotQuery string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"candidates":[]}`))
	}))
	defer upstream.Close()

	c := google.NewNativeClient("k", upstream.URL)
	prep := providers.PreparedRequest{Body: []byte(preservedGeminiBody), Headers: make(http.Header), PreserveNative: true}
	// The routed stream hint would normally force streamGenerateContent; the
	// caller's own non-streaming action wins under PreserveNative.
	prep.Headers.Set(translate.GeminiStreamHintHeader, "true")
	err := c.Proxy(context.Background(), router.Decision{Model: "gemini-3-pro-preview"}, prep, httptest.NewRecorder(),
		httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-3-pro-preview:generateContent", strings.NewReader("")))
	require.NoError(t, err)

	assert.Equal(t, "/v1beta/models/gemini-3-pro-preview:generateContent", gotPath)
	assert.Empty(t, gotQuery, "no query is invented for a non-streaming native request")
}

func TestNativeClient_PreserveNativeDropsRouterKeyFromQuery(t *testing.T) {
	var gotQuery string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"candidates":[]}`))
	}))
	defer upstream.Close()

	c := google.NewNativeClient("deployment-key", upstream.URL)
	prep := providers.PreparedRequest{Body: []byte(preservedGeminiBody), Headers: make(http.Header), PreserveNative: true}
	err := c.Proxy(context.Background(), router.Decision{Model: "gemini-3-pro-preview"}, prep, httptest.NewRecorder(),
		httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-3-pro-preview:generateContent?key=rk_live_secret&alt=json", strings.NewReader("")))
	require.NoError(t, err)

	query, parseErr := url.ParseQuery(gotQuery)
	require.NoError(t, parseErr)
	assert.Empty(t, query.Get("key"), "a router-issued key must never be forwarded to Google")
	assert.Equal(t, "json", query.Get("alt"), "other native parameters survive the scrub")
}

func TestNativeClient_RoutedDispatchStillRebuildsStreamQuery(t *testing.T) {
	var gotPath, gotQuery string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	c := google.NewNativeClient("k", upstream.URL)
	prep := providers.PreparedRequest{Body: []byte(`{"contents":[]}`), Headers: make(http.Header)}
	prep.Headers.Set(translate.GeminiStreamHintHeader, "true")
	err := c.Proxy(context.Background(), router.Decision{Model: "gemini-x"}, prep, httptest.NewRecorder(),
		httptest.NewRequest(http.MethodPost, "/v1/messages?ignored=1", strings.NewReader("")))
	require.NoError(t, err)

	assert.Equal(t, "/v1beta/models/gemini-x:streamGenerateContent", gotPath)
	assert.Equal(t, "alt=sse", gotQuery, "translated dispatch ignores the inbound query and rebuilds alt=sse")
}
