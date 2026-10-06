package proxy

import (
	"bytes"
	"context"
	"github.com/stretchr/testify/require"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/openai"
	"weave-os/router/internal/router"
	"weave-os/router/internal/subscriptions"
)

func TestVerificationSubscriptionTimeoutUsesAuthorizedAPI(t *testing.T) {
	var bearers []string
	var bearersMu sync.Mutex
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		bearer := r.Header.Get("Authorization")
		bearersMu.Lock()
		bearers = append(bearers, bearer)
		bearersMu.Unlock()
		if bearer == "Bearer timeout-seat" {
			<-r.Context().Done()
			return
		}
		if bearer != "Bearer synthetic-api-key" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":\"api answer\"}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"synthetic\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":11,\"output_tokens\":7}}}\n\n")
	}))
	defer upstream.Close()
	client := &includedOnlySyntheticClient{Client: openai.NewClient("synthetic-api-key", upstream.URL)}
	client.SetCodexBaseURL(upstream.URL)
	leaser := &scriptedSubscriptionLeaser{leases: []subscriptions.Lease{{AccountID: "timeout-account", AccessToken: "timeout-seat", ProviderAccount: "timeout-provider"}}}
	svc := NewService(staticRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: codexCoveredModel, Reason: "test"}}, map[string]providers.Client{providers.ProviderOpenAI: client}, nil, false, nil, nil, false, providers.ProviderOpenAI, codexCoveredModel, nil).WithManagedSubscriptions(leaser).WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderOpenAI: {}})
	body := []byte(`{"model":"auto","stream":true,"messages":[{"role":"user","content":"synthetic timeout"}],"tools":[{"type":"function","function":{"name":"read_file","parameters":{"type":"object"}}}]}`)
	ctx := context.WithValue(managedSubscriptionContext(auth.SubscriptionProviderCodex), InstallationIDContextKey{}, "11111111-1111-1111-1111-111111111111")
	rec := httptest.NewRecorder()
	started := time.Now()
	err := svc.ProxyOpenAIChatCompletion(ctx, body, rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(body))))
	require.NoError(t, err)
	elapsed := time.Since(started)
	require.GreaterOrEqual(t, elapsed, 9*time.Second)
	require.Less(t, elapsed, 15*time.Second, "API fallback must follow the ten-second rotation budget promptly")
	bearersMu.Lock()
	require.Equal(t, []string{"Bearer timeout-seat", "Bearer synthetic-api-key"}, bearers)
	bearersMu.Unlock()
	require.Contains(t, rec.Body.String(), "api answer")
	winner := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage)
	require.False(t, winner.Served, "API fallback must not be billed as subscription")
}

func TestVerificationCommittedSubscriptionStreamNeverReplayed(t *testing.T) {
	var bearers []string
	var bearersMu sync.Mutex
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bearersMu.Lock()
		bearers = append(bearers, r.Header.Get("Authorization"))
		bearersMu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":\"committed answer\"}\n\n")
		w.(http.Flusher).Flush()
		_, _ = io.WriteString(w, "data: {\"type\":\"response.failed\",\"response\":{\"id\":\"synthetic\",\"status\":\"failed\",\"error\":{\"code\":\"usage_limit_reached\",\"message\":\"synthetic exhaustion\"}}}\n\n")
	}))
	defer upstream.Close()
	client := &includedOnlySyntheticClient{Client: openai.NewClient("synthetic-api-key", upstream.URL)}
	client.SetCodexBaseURL(upstream.URL)
	leaser := &scriptedSubscriptionLeaser{leases: []subscriptions.Lease{{AccountID: "first-account", AccessToken: "first-seat", ProviderAccount: "first-provider"}, {AccountID: "second-account", AccessToken: "second-seat", ProviderAccount: "second-provider"}}}
	svc := NewService(staticRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: codexCoveredModel, Reason: "test"}}, map[string]providers.Client{providers.ProviderOpenAI: client}, nil, false, nil, nil, false, providers.ProviderOpenAI, codexCoveredModel, nil).WithManagedSubscriptions(leaser).WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderOpenAI: {}})
	body := []byte(`{"model":"auto","stream":true,"messages":[{"role":"user","content":"synthetic stream"}],"tools":[{"type":"function","function":{"name":"read_file","parameters":{"type":"object"}}}]}`)
	ctx := context.WithValue(managedSubscriptionContext(auth.SubscriptionProviderCodex), InstallationIDContextKey{}, "11111111-1111-1111-1111-111111111111")
	rec := httptest.NewRecorder()
	err := svc.ProxyOpenAIChatCompletion(ctx, body, rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(body))))
	require.Error(t, err)
	bearersMu.Lock()
	require.Equal(t, []string{"Bearer first-seat"}, bearers)
	bearersMu.Unlock()
	require.Contains(t, rec.Body.String(), "committed answer")
	require.NotContains(t, rec.Body.String(), "[DONE]")
}

func TestVerificationCommittedFailureAcrossIngress(t *testing.T) {
	fixtures := []struct {
		name, path, body string
		run              func(*Service, context.Context, []byte, http.ResponseWriter, *http.Request) error
	}{
		{"messages", "/v1/messages", `{"model":"auto","stream":true,"max_tokens":100,"messages":[{"role":"user","content":"synthetic"}],"tools":[{"name":"read_file","input_schema":{"type":"object"}}]}`, (*Service).ProxyMessages},
		{"responses", "/v1/responses", `{"model":"auto","stream":true,"input":"synthetic","tools":[{"type":"function","name":"read_file","parameters":{"type":"object"}}]}`, (*Service).ProxyOpenAIResponses},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			var bearers []string
			var bearersMu sync.Mutex
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				bearersMu.Lock()
				bearers = append(bearers, r.Header.Get("Authorization"))
				bearersMu.Unlock()
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":\"committed answer\"}\n\n")
				w.(http.Flusher).Flush()
				_, _ = io.WriteString(w, "data: {\"type\":\"response.failed\",\"response\":{\"id\":\"synthetic\",\"status\":\"failed\",\"error\":{\"code\":\"usage_limit_reached\",\"message\":\"synthetic exhaustion\"}}}\n\n")
			}))
			defer server.Close()
			client := &includedOnlySyntheticClient{Client: openai.NewClient("synthetic-api-key", server.URL)}
			client.SetCodexBaseURL(server.URL)
			leaser := &scriptedSubscriptionLeaser{leases: []subscriptions.Lease{{AccountID: "first-account", AccessToken: "first-seat", ProviderAccount: "first-provider"}, {AccountID: "second-account", AccessToken: "second-seat"}}}
			svc := NewService(staticRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: codexCoveredModel, Reason: "test"}}, map[string]providers.Client{providers.ProviderOpenAI: client}, nil, false, nil, nil, false, providers.ProviderOpenAI, codexCoveredModel, nil).WithManagedSubscriptions(leaser).WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderOpenAI: {}})
			ctx := context.WithValue(managedSubscriptionContext(auth.SubscriptionProviderCodex), InstallationIDContextKey{}, "11111111-1111-1111-1111-111111111111")
			rec := httptest.NewRecorder()
			err := fixture.run(svc, ctx, []byte(fixture.body), rec, httptest.NewRequest(http.MethodPost, fixture.path, strings.NewReader(fixture.body)))
			require.Error(t, err)
			bearersMu.Lock()
			require.Equal(t, []string{"Bearer first-seat"}, bearers)
			bearersMu.Unlock()
			require.Contains(t, rec.Body.String(), "committed answer")
			require.False(t, ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage).Served, "failed committed stream cannot count as successful subscription winner")
		})
	}
}

func TestVerificationCommittedCRLFFailureAcrossIngress(t *testing.T) {
	fixtures := []struct {
		name, path, body string
		run              func(*Service, context.Context, []byte, http.ResponseWriter, *http.Request) error
	}{
		{"messages", "/v1/messages", `{"model":"auto","stream":true,"max_tokens":100,"messages":[{"role":"user","content":"synthetic"}],"tools":[{"name":"read_file","input_schema":{"type":"object"}}]}`, (*Service).ProxyMessages},
		{"responses", "/v1/responses", `{"model":"auto","stream":true,"input":"synthetic","tools":[{"type":"function","name":"read_file","parameters":{"type":"object"}}]}`, (*Service).ProxyOpenAIResponses},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			var bearers []string
			var bearersMu sync.Mutex
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				bearersMu.Lock()
				bearers = append(bearers, r.Header.Get("Authorization"))
				bearersMu.Unlock()
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":\"committed answer\"}\r\n\r\n")
				w.(http.Flusher).Flush()
				_, _ = io.WriteString(w, "data: {\"type\":\"response.failed\",\"response\":{\"id\":\"synthetic\",\"status\":\"failed\",\"error\":{\"code\":\"usage_limit_reached\",\"message\":\"synthetic exhaustion\"}}}\r\n\r\n")
			}))
			defer server.Close()
			client := &includedOnlySyntheticClient{Client: openai.NewClient("synthetic-api-key", server.URL)}
			client.SetCodexBaseURL(server.URL)
			leaser := &scriptedSubscriptionLeaser{leases: []subscriptions.Lease{{AccountID: "first-account", AccessToken: "first-seat", ProviderAccount: "first-provider"}, {AccountID: "second-account", AccessToken: "second-seat"}}}
			svc := NewService(staticRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: codexCoveredModel, Reason: "test"}}, map[string]providers.Client{providers.ProviderOpenAI: client}, nil, false, nil, nil, false, providers.ProviderOpenAI, codexCoveredModel, nil).WithManagedSubscriptions(leaser).WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderOpenAI: {}})
			ctx := context.WithValue(managedSubscriptionContext(auth.SubscriptionProviderCodex), InstallationIDContextKey{}, "11111111-1111-1111-1111-111111111111")
			rec := httptest.NewRecorder()
			err := fixture.run(svc, ctx, []byte(fixture.body), rec, httptest.NewRequest(http.MethodPost, fixture.path, strings.NewReader(fixture.body)))
			require.Error(t, err)
			bearersMu.Lock()
			require.Equal(t, []string{"Bearer first-seat"}, bearers)
			bearersMu.Unlock()
			require.Contains(t, rec.Body.String(), "committed answer")
			require.False(t, ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage).Served, "failed committed stream cannot count as successful subscription winner")
		})
	}
}

func TestVerificationDebugCommittedFailureAcrossIngress(t *testing.T) {
	fixtures := []struct {
		name, path, body string
		run              func(*Service, context.Context, []byte, http.ResponseWriter, *http.Request) error
	}{
		{"messages", "/v1/messages", `{"model":"auto","stream":true,"max_tokens":100,"messages":[{"role":"user","content":"synthetic"}],"tools":[{"name":"read_file","input_schema":{"type":"object"}}]}`, (*Service).ProxyMessages},
		{"responses", "/v1/responses", `{"model":"auto","stream":true,"input":"synthetic","tools":[{"type":"function","name":"read_file","parameters":{"type":"object"}}]}`, (*Service).ProxyOpenAIResponses},
	}
	for _, newline := range []string{"\n\n", "\r\n\r\n"} {
		for _, fixture := range fixtures {
			t.Run(fixture.name+map[string]string{"\n\n": "/LF", "\r\n\r\n": "/CRLF"}[newline], func(t *testing.T) {
				var bearers []string
				var bearersMu sync.Mutex
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					bearersMu.Lock()
					bearers = append(bearers, r.Header.Get("Authorization"))
					bearersMu.Unlock()
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":\"committed answer\"}"+newline)
					w.(http.Flusher).Flush()
					_, _ = io.WriteString(w, "data: {\"type\":\"response.failed\",\"response\":{\"id\":\"synthetic\",\"status\":\"failed\",\"error\":{\"code\":\"usage_limit_reached\",\"message\":\"synthetic exhaustion\"}}}"+newline)
				}))
				defer server.Close()
				client := &includedOnlySyntheticClient{Client: openai.NewClient("synthetic-api-key", server.URL)}
				client.SetCodexBaseURL(server.URL)
				leaser := &scriptedSubscriptionLeaser{leases: []subscriptions.Lease{{AccountID: "first-account", AccessToken: "first-seat", ProviderAccount: "first-provider"}, {AccountID: "second-account", AccessToken: "second-seat"}}}
				svc := NewService(staticRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: codexCoveredModel, Reason: "test"}}, map[string]providers.Client{providers.ProviderOpenAI: client}, nil, false, nil, nil, false, providers.ProviderOpenAI, codexCoveredModel, nil).WithManagedSubscriptions(leaser).WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderOpenAI: {}})
				ctx := context.WithValue(managedSubscriptionContext(auth.SubscriptionProviderCodex), InstallationIDContextKey{}, "11111111-1111-1111-1111-111111111111")
				var debugLogs bytes.Buffer
				ctx = observability.WithLogger(ctx, slog.New(slog.NewTextHandler(&debugLogs, &slog.HandlerOptions{Level: slog.LevelDebug})))
				rec := httptest.NewRecorder()
				err := fixture.run(svc, ctx, []byte(fixture.body), rec, httptest.NewRequest(http.MethodPost, fixture.path, strings.NewReader(fixture.body)))
				require.Error(t, err)
				require.Contains(t, debugLogs.String(), "OpenAI upstream first chunk")
				bearersMu.Lock()
				require.Equal(t, []string{"Bearer first-seat"}, bearers)
				bearersMu.Unlock()
				require.Contains(t, rec.Body.String(), "committed answer")
				require.False(t, ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage).Served, "failed committed stream cannot count as successful subscription winner")
			})
		}
	}
}

func TestVerificationCommittedSubscriptionStreamOutlivesRotationBudget(t *testing.T) {
	var bearers []string
	var bearersMu sync.Mutex
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bearersMu.Lock()
		bearers = append(bearers, r.Header.Get("Authorization"))
		bearersMu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":\"early output \"}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
			return
		case <-time.After(sameBindingRetryBudget + time.Second):
		}
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":\"late output\"}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"synthetic\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":11,\"output_tokens\":7}}}\n\n")
	}))
	defer upstream.Close()
	client := &includedOnlySyntheticClient{Client: openai.NewClient("synthetic-api-key", upstream.URL)}
	client.SetCodexBaseURL(upstream.URL)
	leaser := &scriptedSubscriptionLeaser{leases: []subscriptions.Lease{{AccountID: "long-account", AccessToken: "long-seat", ProviderAccount: "long-provider"}}}
	svc := NewService(staticRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: codexCoveredModel, Reason: "test"}}, map[string]providers.Client{providers.ProviderOpenAI: client}, nil, false, nil, nil, false, providers.ProviderOpenAI, codexCoveredModel, nil).WithManagedSubscriptions(leaser).WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderOpenAI: {}})
	body := []byte(`{"model":"auto","stream":true,"messages":[{"role":"user","content":"synthetic long stream"}],"tools":[{"type":"function","function":{"name":"read_file","parameters":{"type":"object"}}}]}`)
	ctx := context.WithValue(managedSubscriptionContext(auth.SubscriptionProviderCodex), InstallationIDContextKey{}, "11111111-1111-1111-1111-111111111111")
	rec := httptest.NewRecorder()
	err := svc.ProxyOpenAIChatCompletion(ctx, body, rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(body))))
	require.NoError(t, err, "the rotation budget bounds pre-output attempts, not a committed stream")
	bearersMu.Lock()
	require.Equal(t, []string{"Bearer long-seat"}, bearers)
	bearersMu.Unlock()
	require.Contains(t, rec.Body.String(), "late output")
	require.True(t, ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage).Served)
}
