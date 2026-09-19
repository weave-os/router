package openaicompat

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type bodyCapturingHostRewriter struct {
	target *url.URL
	body   []byte
}

func (rt *bodyCapturingHostRewriter) RoundTrip(r *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	rt.body = body
	out := r.Clone(r.Context())
	out.URL.Scheme, out.URL.Host = rt.target.Scheme, rt.target.Host
	out.Body = io.NopCloser(bytes.NewReader(body))
	return http.DefaultTransport.RoundTrip(out)
}

func TestProxy_SnowflakeCortexGrokResponsesOmitsReasoningSummary(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1"}`))
	}))
	defer srv.Close()
	target, err := url.Parse(srv.URL)
	require.NoError(t, err)

	tests := []struct {
		name        string
		baseURL     string
		model       string
		wantSummary bool
	}{
		{
			name:    "snowflake grok",
			baseURL: "https://acme.snowflakecomputing.com/api/v2/cortex",
			model:   "grok-4.6",
		},
		{
			name:        "native xai grok",
			baseURL:     XAIBaseURL,
			model:       "grok-4.6",
			wantSummary: true,
		},
		{
			name:        "snowflake non-grok",
			baseURL:     "https://acme.snowflakecomputing.com/api/v2/cortex",
			model:       "gpt-5.6-luna",
			wantSummary: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rt := &bodyCapturingHostRewriter{target: target}
			httpClient := &http.Client{Transport: rt}
			c := NewGatewayClient("tok", tt.baseURL)
			c.http = httpClient
			c.grokHTTP = httpClient

			prep := providers.PreparedRequest{
				Body:     []byte(`{"model":"` + tt.model + `","input":"hi","reasoning":{"effort":"high","summary":"detailed"}}`),
				Endpoint: providers.EndpointResponses,
				Headers:  make(http.Header),
			}
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			rec := httptest.NewRecorder()

			require.NoError(t, c.Proxy(context.Background(), router.Decision{Model: tt.model}, prep, rec, req))
			assert.Equal(t, tt.wantSummary, gjson.GetBytes(rt.body, "reasoning.summary").Exists())
			assert.Equal(t, "high", gjson.GetBytes(rt.body, "reasoning.effort").String())
		})
	}
}
