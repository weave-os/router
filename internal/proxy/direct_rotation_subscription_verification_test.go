package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/openai"
	"weave-os/router/internal/router"
)

// No prior quota snapshot can establish a provider-enforced no-overage mode.
// The fixture charges the first OAuth request: inspecting its response headers
// afterwards is too late. Authorized API capacity remains available.
func TestVerificationUnprovenCodexExtraUsageNeverDispatched(t *testing.T) {
	var oauthCharges, apiRequests int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer synthetic-api-key" {
			apiRequests++
		} else {
			oauthCharges++
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":\"authorized answer\"}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"synthetic-response\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":11,\"output_tokens\":7}}}\n\n")
	}))
	defer upstream.Close()
	client := openai.NewClient("synthetic-api-key", upstream.URL)
	client.SetCodexBaseURL(upstream.URL)
	svc := NewService(staticRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: codexCoveredModel, Reason: "test"}}, map[string]providers.Client{providers.ProviderOpenAI: client}, nil, false, nil, nil, false, providers.ProviderOpenAI, codexCoveredModel, nil).WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderOpenAI: {}})
	body := []byte(`{"model":"auto","stream":true,"messages":[{"role":"user","content":"inspect synthetic billing"}],"tools":[{"type":"function","function":{"name":"read_file","parameters":{"type":"object"}}}]}`)
	recorder := httptest.NewRecorder()
	err := svc.ProxyOpenAIChatCompletion(codexSubscriptionTestCtx(), body, recorder, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(body))))
	require.NoError(t, err)
	require.Zero(t, oauthCharges, "unproven included-only OAuth must be excluded before its first charge")
	require.Equal(t, 1, apiRequests)
	require.Contains(t, recorder.Body.String(), "authorized answer")
}
