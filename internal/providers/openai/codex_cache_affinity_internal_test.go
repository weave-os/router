package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProxyCodexSubscriptionUsesPreparedConversationAffinity(t *testing.T) {
	const affinity = "00112233445566778899aabbccddeeff"
	for _, tc := range []struct {
		name         string
		subscription bool
		key          string
		expected     string
	}{
		{"subscription", true, affinity, affinity},
		{"paid", false, affinity, "stale-affinity"},
		{"missing affinity", true, "", "stale-affinity"},
		{"oversized affinity", true, strings.Repeat("a", 129), "stale-affinity"},
		{"unsafe affinity", true, "bad\r\nheader", "stale-affinity"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var received []string
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if serveSyntheticCodexQuota(w, r) {
					return
				}
				received = append(received, r.Header.Get("Session-Id"))
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\"}\n\n")
			}))
			defer upstream.Close()
			client := NewClient("synthetic-paid-key", upstream.URL)
			client.SetCodexBaseURL(upstream.URL)
			ctx := context.Background()
			if tc.subscription {
				ctx = codexCtx("synthetic-subscription-token", "synthetic-account")
			}
			body, err := json.Marshal(map[string]any{"model": "gpt-6.1-sol", "input": "synthetic", "stream": true, "prompt_cache_key": tc.key})
			require.NoError(t, err)
			prep := providers.PreparedRequest{
				Endpoint: providers.EndpointResponses,
				Body:     body,
				Headers:  http.Header{"Session-Id": []string{"stale-affinity"}},
			}
			for i := 0; i < 2; i++ {
				err := client.Proxy(ctx, router.Decision{Model: "gpt-6.1-sol", Provider: providers.ProviderOpenAI}, prep, httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
				require.NoError(t, err)
			}
			assert.Equal(t, []string{tc.expected, tc.expected}, received)
		})
	}
}
