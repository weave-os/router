package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/openai"
	"weave-os/router/internal/proxy/usage"
	"weave-os/router/internal/router"
	"weave-os/router/internal/subscriptions"
)

// Only this synthetic provider fixture enforces no extra-usage serving.
// Production adapters intentionally do not implement this proof.
type includedOnlySyntheticClient struct{ *openai.Client }

func (*includedOnlySyntheticClient) IncludedOnlySubscriptions() bool { return true }

// This exercises ingress, translation, provider HTTP dispatch, and response
// translation. An exhausted request-local token must not strand linked capacity
// merely because the requester has no API credential.
func TestVerificationSafeTransportDirectExhaustionUsesLinkedWithoutAPI(t *testing.T) {
	var bearers, accountIDs, paths []string
	var sent [][]byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bearers = append(bearers, r.Header.Get("Authorization"))
		accountIDs = append(accountIDs, r.Header.Get("ChatGPT-Account-ID"))
		paths = append(paths, r.URL.Path)
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		sent = append(sent, body)
		if r.Header.Get("Authorization") == "Bearer "+codexTestToken {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"type":"usage_limit_reached"}}`)
			return
		}
		if r.Header.Get("Authorization") != "Bearer linked-token" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":\"linked answer\"}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"synthetic-response\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":11,\"output_tokens\":7}}}\n\n")
	}))
	defer upstream.Close()
	client := &includedOnlySyntheticClient{Client: openai.NewClient("", upstream.URL)}
	client.SetCodexBaseURL(upstream.URL)
	leaser := &scriptedSubscriptionLeaser{leases: []subscriptions.Lease{{AccountID: "linked-account", AccessToken: "linked-token", ProviderAccount: "linked-provider-account"}}}
	svc := NewService(staticRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: codexCoveredModel, Reason: "test"}}, map[string]providers.Client{providers.ProviderOpenAI: client}, nil, false, nil, nil, false, providers.ProviderOpenAI, codexCoveredModel, nil).WithManagedSubscriptions(leaser)
	body := []byte(`{"model":"auto","stream":true,"messages":[{"role":"user","content":"inspect synthetic input"}],"tools":[{"type":"function","function":{"name":"read_file","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}}],"reasoning_effort":"medium"}`)
	ctx := context.WithValue(codexSubscriptionTestCtx(), APIKeyIDContextKey{}, "synthetic-key")
	ctx = context.WithValue(ctx, ManagedSubscriptionProvidersContextKey{}, map[auth.SubscriptionProvider]struct{}{auth.SubscriptionProviderCodex: {}})
	ctx = WithManagedSubscriptionUsage(ctx)
	recorder := httptest.NewRecorder()
	err := svc.ProxyOpenAIChatCompletion(ctx, body, recorder, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(body))))
	require.NoError(t, err)
	require.Equal(t, []string{"Bearer " + codexTestToken, "Bearer linked-token"}, bearers)
	require.Equal(t, []string{codexTestAccountID, "linked-provider-account"}, accountIDs)
	require.Equal(t, []string{"/responses", "/responses"}, paths)
	require.Contains(t, recorder.Body.String(), "linked answer")
	require.True(t, servedOnSubscription(ctx), "winning subscription must be cost-neutral")
	require.Equal(t, "read_file", gjson.GetBytes(sent[1], "tools.0.name").String())
	require.Equal(t, "medium", gjson.GetBytes(sent[1], "reasoning.effort").String())
}

func TestVerificationSafeTransportManagedWinnerQuotaObservedFromHTTP(t *testing.T) {
	var winnerBearer string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		winnerBearer = r.Header.Get("Authorization")
		if r.Header.Get("Authorization") != "Bearer quota-token" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Codex-Primary-Used-Percent", "82")
		w.Header().Set("X-Codex-Primary-Window-Minutes", "300")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":\"quota answer\"}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"synthetic-response\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":11,\"output_tokens\":7}}}\n\n")
	}))
	defer upstream.Close()
	client := &includedOnlySyntheticClient{Client: openai.NewClient("", upstream.URL)}
	client.SetCodexBaseURL(upstream.URL)
	leaser := &scriptedSubscriptionLeaser{leases: []subscriptions.Lease{{AccountID: "quota-account", AccessToken: "quota-token", ProviderAccount: "quota-provider-account"}}}
	observer := usage.NewObserver([]byte("synthetic-salt"), time.Hour, time.Now)
	svc := NewService(staticRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: codexCoveredModel, Reason: "test"}}, map[string]providers.Client{providers.ProviderOpenAI: client}, nil, false, nil, nil, false, providers.ProviderOpenAI, codexCoveredModel, nil).WithManagedSubscriptions(leaser).WithUsageObserver(observer)
	body := []byte(`{"model":"auto","stream":true,"messages":[{"role":"user","content":"inspect synthetic quota"}],"tools":[{"type":"function","function":{"name":"read_file","parameters":{"type":"object"}}}]}`)
	ctx := context.WithValue(managedSubscriptionContext(auth.SubscriptionProviderCodex), InstallationIDContextKey{}, "11111111-1111-1111-1111-111111111111")
	recorder := httptest.NewRecorder()
	err := svc.ProxyOpenAIChatCompletion(ctx, body, recorder, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(body))))
	require.NoError(t, err)
	require.Equal(t, "Bearer quota-token", winnerBearer)
	require.Contains(t, recorder.Body.String(), "quota answer")
	snapshot, observed := observer.Snapshot(observer.Key([]byte("quota-token")))
	require.True(t, observed, "quota must be recorded for the actual leased credential")
	require.InDelta(t, .82, snapshot.Primary.UsedPercent, .0001)
}
