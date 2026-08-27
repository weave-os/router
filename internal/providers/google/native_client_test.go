package google_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"workweave/router/internal/providers"
	"workweave/router/internal/providers/google"
	"workweave/router/internal/proxy"
	"workweave/router/internal/router"
	"workweave/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNativeClient_GenerateContentURLAndAuth(t *testing.T) {
	var (
		gotPath  string
		gotKey   string
		gotBody  []byte
		gotQuery string
	)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKey = r.Header.Get("x-goog-api-key")
		gotQuery = r.URL.RawQuery
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"ok"}]}}]}`))
	}))
	defer upstream.Close()

	c := google.NewNativeClient("test-key", upstream.URL)
	rec := httptest.NewRecorder()
	clientReq := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))

	prep := providers.PreparedRequest{
		Body:    []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`),
		Headers: make(http.Header),
	}
	err := c.Proxy(context.Background(), router.Decision{Model: "gemini-3.1-flash-lite-preview"}, prep, rec, clientReq)
	require.NoError(t, err)

	assert.Equal(t, "/v1beta/models/gemini-3.1-flash-lite-preview:generateContent", gotPath)
	assert.Equal(t, "test-key", gotKey)
	assert.Empty(t, gotQuery, "non-streaming requests must not carry alt=sse")
	var bodyMap map[string]any
	require.NoError(t, json.Unmarshal(gotBody, &bodyMap))
	assert.NotNil(t, bodyMap["contents"])
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestNativeClient_StreamingHintFlipsToStreamGenerateContent(t *testing.T) {
	var gotPath, gotQuery string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`data: {"candidates":[{"content":{"parts":[{"text":"x"}]}}]}` + "\n\n"))
	}))
	defer upstream.Close()

	c := google.NewNativeClient("k", upstream.URL)
	rec := httptest.NewRecorder()
	prep := providers.PreparedRequest{Body: []byte(`{"contents":[]}`), Headers: make(http.Header)}
	prep.Headers.Set(translate.GeminiStreamHintHeader, "true")
	err := c.Proxy(context.Background(), router.Decision{Model: "gemini-x"},
		prep, rec, httptest.NewRequest(http.MethodPost, "/v1/x", strings.NewReader("")))
	require.NoError(t, err)

	assert.Equal(t, "/v1beta/models/gemini-x:streamGenerateContent", gotPath)
	assert.Equal(t, "alt=sse", gotQuery)
}

// Unary :generateContent headers arrive only after the full generation; success
// with the streaming guard shorter than the server delay proves the unary client was selected.
func TestNativeClient_UnaryUsesGenerationScaleHeaderGuard(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"ok"}]}}]}`))
	}))
	defer upstream.Close()

	c := google.NewNativeClientWithHeaderTimeouts("k", upstream.URL, 50*time.Millisecond, 2*time.Second)
	rec := httptest.NewRecorder()
	prep := providers.PreparedRequest{Body: []byte(`{"contents":[]}`), Headers: make(http.Header)}
	err := c.Proxy(context.Background(), router.Decision{Model: "gemini-x"}, prep, rec,
		httptest.NewRequest(http.MethodPost, "/v1/x", strings.NewReader("")))

	require.NoError(t, err, "unary call slower than the streaming guard must still succeed")
	assert.Equal(t, http.StatusOK, rec.Code)
}

// Converse: streaming must keep the liveness guard even when the unary guard is far larger —
// a stream that stalls before headers must still fail fast.
func TestNativeClient_StreamingKeepsLivenessHeaderGuard(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	c := google.NewNativeClientWithHeaderTimeouts("k", upstream.URL, 50*time.Millisecond, 2*time.Second)
	rec := httptest.NewRecorder()
	prep := providers.PreparedRequest{Body: []byte(`{"contents":[]}`), Headers: make(http.Header)}
	prep.Headers.Set(translate.GeminiStreamHintHeader, "true")
	err := c.Proxy(context.Background(), router.Decision{Model: "gemini-x"}, prep, rec,
		httptest.NewRequest(http.MethodPost, "/v1/x", strings.NewReader("")))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "timeout awaiting response headers")
}

func TestNativeClient_StreamHintHeaderStrippedFromUpstream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Synthetic hint header is an internal signal; must not reach Gemini.
		assert.Empty(t, r.Header.Get(translate.GeminiStreamHintHeader))
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	c := google.NewNativeClient("k", upstream.URL)
	rec := httptest.NewRecorder()
	prep := providers.PreparedRequest{Body: []byte(`{}`), Headers: make(http.Header)}
	prep.Headers.Set(translate.GeminiStreamHintHeader, "true")
	_ = c.Proxy(context.Background(), router.Decision{Model: "g"}, prep, rec,
		httptest.NewRequest(http.MethodPost, "/v1/x", strings.NewReader("")))
}

func TestNativeClient_BYOKCredentialsOverrideDeploymentKey(t *testing.T) {
	var gotKey string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("x-goog-api-key")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	c := google.NewNativeClient("deployment-key", upstream.URL)
	ctx := context.WithValue(context.Background(),
		proxy.CredentialsContextKey{},
		&proxy.Credentials{APIKey: []byte("byok-key")})

	rec := httptest.NewRecorder()
	prep := providers.PreparedRequest{Body: []byte(`{}`), Headers: make(http.Header)}
	_ = c.Proxy(ctx, router.Decision{Model: "g"}, prep, rec,
		httptest.NewRequest(http.MethodPost, "/v1/x", strings.NewReader("")))
	assert.Equal(t, "byok-key", gotKey,
		"BYOK credentials on context must take precedence over the deployment-level key")
}

func TestNativeClient_DefaultBaseURL(t *testing.T) {
	c := google.NewNativeClient("k", "")
	assert.Equal(t, "https://generativelanguage.googleapis.com", google.NativeBaseURL)
	_ = c
}

func TestNativeClient_BuffersNon2xxBeforeTouchingDownstream(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusBadGateway} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("X-Upstream-Request", "redacted-id")
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"error":{"message":"retry later"}}`))
			}))
			defer upstream.Close()

			client := google.NewNativeClient("k", upstream.URL)
			rec := httptest.NewRecorder()
			err := client.Proxy(context.Background(), router.Decision{Model: "gemini-x"},
				providers.PreparedRequest{Body: []byte(`{}`), Headers: make(http.Header)}, rec,
				httptest.NewRequest(http.MethodPost, "/v1/x", nil))

			var upstreamErr *providers.UpstreamErrorResponse
			require.True(t, errors.As(err, &upstreamErr))
			assert.Equal(t, status, upstreamErr.Status)
			assert.Equal(t, "redacted-id", upstreamErr.Headers.Get("X-Upstream-Request"))
			assert.Equal(t, 0, rec.Body.Len(), "retry classification must happen before downstream commitment")
			assert.Empty(t, rec.Header().Get("X-Upstream-Request"))
			assert.Equal(t, providers.IsRetryableStatus(status), providers.IsRetryable(err))
		})
	}
}
