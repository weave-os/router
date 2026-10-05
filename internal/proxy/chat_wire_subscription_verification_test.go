package proxy

import (
	"context"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/openai"
	"weave-os/router/internal/router"
	"weave-os/router/internal/subscriptions"
)

func TestVerificationUnrepresentableChatUsesAPIOnly(t *testing.T) {
	var bearers, paths []string
	var sent []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bearers = append(bearers, r.Header.Get("Authorization"))
		paths = append(paths, r.URL.Path)
		sent, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"synthetic\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"chat answer\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"synthetic\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":7}}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	client := &includedOnlySyntheticClient{Client: openai.NewClient("synthetic-api-key", server.URL)}
	client.SetCodexBaseURL(server.URL)
	leaser := &scriptedSubscriptionLeaser{leases: []subscriptions.Lease{{AccountID: "unsafe-wire-account", AccessToken: "unsafe-wire-seat"}}}
	svc := NewService(staticRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: codexCoveredModel, Reason: "test"}}, map[string]providers.Client{providers.ProviderOpenAI: client}, nil, false, nil, nil, false, providers.ProviderOpenAI, codexCoveredModel, nil).WithManagedSubscriptions(leaser).WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderOpenAI: {}})
	ctx := context.WithValue(managedSubscriptionContext(auth.SubscriptionProviderCodex), InstallationIDContextKey{}, "11111111-1111-1111-1111-111111111111")
	body := `{"model":"auto","n":2,"logprobs":true,"stream":true,"messages":[{"role":"user","content":"synthetic"}],"tools":[{"type":"function","function":{"name":"read_file","parameters":{"type":"object"}}}]}`
	rec := httptest.NewRecorder()
	require.NoError(t, svc.ProxyOpenAIChatCompletion(ctx, []byte(body), rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))))
	require.Equal(t, []string{"Bearer synthetic-api-key"}, bearers)
	require.Equal(t, []string{"/v1/chat/completions"}, paths)
	require.Equal(t, int64(2), gjson.GetBytes(sent, "n").Int())
	require.True(t, gjson.GetBytes(sent, "logprobs").Bool())
	require.Contains(t, rec.Body.String(), "chat answer")
	require.False(t, servedOnSubscription(ctx))
}
