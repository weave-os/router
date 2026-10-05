package openai_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/openai"
	"weave-os/router/internal/router"

	"github.com/stretchr/testify/require"
)

func TestProxy_NonStreamingResponsesContextOverflowPreservesBadRequest(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"failed","error":{"code":"context_length_exceeded","message":"prompt is too long"},"output":[]}`))
	}))
	defer upstream.Close()

	client := openai.NewClient("synthetic-api-key", upstream.URL)
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(""))
	prepared := providers.PreparedRequest{
		Body:     []byte(`{"model":"gpt-5","input":"synthetic prompt"}`),
		Endpoint: providers.EndpointResponses,
	}

	err := client.Proxy(context.Background(), router.Decision{Provider: providers.ProviderOpenAI, Model: "gpt-5"}, prepared, httptest.NewRecorder(), request)

	var upstreamError *providers.UpstreamErrorResponse
	require.ErrorAs(t, err, &upstreamError)
	require.Equal(t, http.StatusBadRequest, upstreamError.Status)
	require.Contains(t, string(upstreamError.Body), `"code":"context_length_exceeded"`)
}
