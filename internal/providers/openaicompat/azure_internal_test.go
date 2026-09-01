package openaicompat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hostRewriter sends requests for any host to a test server, recording the paths
// so a test can assert where an Azure-hostnamed base URL actually lands.
type hostRewriter struct {
	target *url.URL
	paths  []string
}

func (rt *hostRewriter) RoundTrip(r *http.Request) (*http.Response, error) {
	rt.paths = append(rt.paths, r.URL.Path)
	out := r.Clone(r.Context())
	out.URL.Scheme, out.URL.Host = rt.target.Scheme, rt.target.Host
	return http.DefaultTransport.RoundTrip(out)
}

// An Azure resource serves the OpenAI surface only under /openai/v1, so a
// customer endpoint stored the OpenAI way must not 404 model discovery.
func TestListModels_AzureEndpointStoredWithOpenAIStyleV1(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/openai/v1/models" {
			http.Error(w, `{"error":{"code":"404","message":"Resource not found"}}`, http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-5.6-luna"}]}`))
	}))
	defer srv.Close()
	target, err := url.Parse(srv.URL)
	require.NoError(t, err)

	rt := &hostRewriter{target: target}
	c := NewGatewayClient("k", "https://zllama-dev.openai.azure.com/v1")
	c.http = &http.Client{Transport: rt}

	ids, err := c.ListModels(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"gpt-5.6-luna"}, ids)
	assert.Equal(t, []string{"/openai/v1/models"}, rt.paths)
}
