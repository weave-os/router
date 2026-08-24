package anthropic_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"workweave/router/internal/providers"
	"workweave/router/internal/providers/anthropic"
	"workweave/router/internal/router"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// waferCapture records what a fake Wafer Messages upstream saw.
type waferCapture struct {
	path string
	auth string
	zdR  string
	body map[string]any
}

func fakeWaferMessages(t *testing.T, got *waferCapture) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.path = r.URL.Path
		got.auth = r.Header.Get("Authorization")
		got.zdR = r.Header.Get("Wafer-ZDR")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got.body)
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_wafer"}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestWaferMessages_SendsBearerAuthZDRAndRewritesModel covers the full Wafer
// Anthropic-spec contract: bearer auth, the mandatory Wafer-ZDR header, and
// the router-slug → Wafer model-ID rewrite, all on the /v1/messages path.
func TestWaferMessages_SendsBearerAuthZDRAndRewritesModel(t *testing.T) {
	var got waferCapture
	upstream := fakeWaferMessages(t, &got)

	c := anthropic.NewClient("wfr_…", upstream.URL, anthropic.WithAuthScheme(anthropic.AuthBearer)).
		WithDefaultHeaders(http.Header{"Wafer-ZDR": []string{"required"}}).
		WithModelIDMap(map[string]string{"z-ai/glm-5.2": "GLM-5.2"})
	rec := httptest.NewRecorder()
	clientReq := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	prep := providers.PreparedRequest{
		Body:    []byte(`{"model":"z-ai/glm-5.2","messages":[{"role":"user","content":"hi"}]}`),
		Headers: make(http.Header),
	}

	require.NoError(t, c.Proxy(context.Background(), router.Decision{Model: "z-ai/glm-5.2"}, prep, rec, clientReq))

	assert.Equal(t, "/v1/messages", got.path)
	assert.Equal(t, "Bearer wfr_…", got.auth)
	assert.Equal(t, "required", got.zdR)
	assert.Equal(t, "GLM-5.2", got.body["model"], "router slug must be rewritten to Wafer's model ID")
}

// TestWaferMessages_PreferredModelNotMappedForwardsVerbatim: an unmapped model
// passes through unchanged, matching the openaicompat rewrite semantics.
func TestWaferMessages_PreferredModelNotMappedForwardsVerbatim(t *testing.T) {
	var got waferCapture
	upstream := fakeWaferMessages(t, &got)

	c := anthropic.NewClient("wfr_…", upstream.URL, anthropic.WithAuthScheme(anthropic.AuthBearer)).
		WithModelIDMap(map[string]string{"z-ai/glm-5.2": "GLM-5.2"})
	rec := httptest.NewRecorder()
	clientReq := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	prep := providers.PreparedRequest{
		Body:    []byte(`{"model":"some-other","messages":[{"role":"user","content":"hi"}]}`),
		Headers: make(http.Header),
	}

	require.NoError(t, c.Proxy(context.Background(), router.Decision{Model: "some-other"}, prep, rec, clientReq))
	assert.Equal(t, "some-other", got.body["model"])
}
