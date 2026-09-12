package proxy_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/openaicompat"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
)

const grokGatewayToolTurn = `{"model":"grok-4.6","stream":true,"max_tokens":1024,"messages":[{"role":"user","content":"list files"}],` +
	`"tools":[{"name":"read_file","input_schema":{"type":"object","properties":{"path":{"type":"string"}}}}]}`

const cortexReasoningSummaryRejection = `{"error":{"message":"Unsupported parameter: 'reasoning.summary' is not supported with the 'global.xai.grok-4.6' model.",` +
	`"type":"invalid_request_error","param":"reasoning.summary","code":"unsupported_parameter"}}`

// summaryStrictResponsesGateway serves Responses for every model but, like
// Snowflake Cortex fronting grok-4.6, 400s any body carrying reasoning.summary.
func summaryStrictResponsesGateway(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var (
		mu     sync.Mutex
		bodies []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, r.URL.Path+" "+string(raw))
		mu.Unlock()
		if !strings.HasSuffix(r.URL.Path, "/responses") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if gjson.GetBytes(raw, "reasoning.summary").Exists() {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(cortexReasoningSummaryRejection))
			return
		}
		writeOpenAIResponsesSSE(w)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), bodies...)
	}
}

const miniGatewayReasoningToolTurn = `{"model":"gpt-5.4-mini","stream":true,"max_tokens":1024,"reasoning_effort":"medium","messages":[{"role":"user","content":"list files"}],` +
	`"tools":[{"name":"read_file","input_schema":{"type":"object","properties":{"path":{"type":"string"}}}}]}`

func grokGatewayService(t *testing.T, baseURL string) *proxy.Service {
	t.Helper()
	return proxy.NewService(
		&fakeRouter{decision: router.Decision{Provider: providers.ProviderOpenAIGateway, Model: "grok-4.6"}},
		map[string]providers.Client{
			providers.ProviderOpenAIGateway: openaicompat.NewGatewayClient("test-key", baseURL),
		},
		nil, false, nil, nil, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil,
	).WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderOpenAIGateway: {}})
}

func requestBody(entry string) []byte {
	_, body, _ := strings.Cut(entry, " ")
	return []byte(body)
}

// A gateway that serves Responses for grok-4.6 but rejects reasoning.summary
// gets one retry without the knob — effort, encrypted reasoning, and tools
// intact — and later turns to that model skip the probe.
func TestProxyMessages_GatewayReasoningSummaryRejectionRetriesWithoutSummary(t *testing.T) {
	gateway, sent := summaryStrictResponsesGateway(t)
	svc := grokGatewayService(t, gateway.URL+"/v1")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	require.NoError(t, svc.ProxyMessages(context.Background(), []byte(grokGatewayToolTurn), rec, req))
	assert.Contains(t, rec.Body.String(), "event: message_start")
	assert.NotContains(t, rec.Body.String(), "unsupported_parameter")

	first := sent()
	require.Len(t, first, 2, "the summary 400 must trigger exactly one retry")
	for _, entry := range first {
		assert.True(t, strings.HasPrefix(entry, "/v1/responses "), "retry stays on Responses: %s", entry)
	}
	probe, retry := requestBody(first[0]), requestBody(first[1])
	assert.Equal(t, "detailed", gjson.GetBytes(probe, "reasoning.summary").String())
	assert.False(t, gjson.GetBytes(retry, "reasoning.summary").Exists())
	assert.Equal(t, "low", gjson.GetBytes(retry, "reasoning.effort").String())
	assert.Equal(t, "reasoning.encrypted_content", gjson.GetBytes(retry, "include.0").String())
	assert.Equal(t, "read_file", gjson.GetBytes(retry, "tools.0.name").String())

	rec2 := httptest.NewRecorder()
	require.NoError(t, svc.ProxyMessages(context.Background(), []byte(grokGatewayToolTurn), rec2, req))
	assert.Contains(t, rec2.Body.String(), "event: message_start")
	second := sent()
	require.Len(t, second, 3, "memoized (endpoint, model) must not pay the 400 again")
	assert.False(t, gjson.GetBytes(requestBody(second[2]), "reasoning.summary").Exists())
}

// The memo is per (endpoint, model): another reasoning model on the same
// gateway keeps sending the summary knob it supports.
func TestProxyMessages_GatewayReasoningSummaryMemoIsPerModel(t *testing.T) {
	gateway, sent := summaryStrictResponsesGateway(t)
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderOpenAIGateway, Model: "grok-4.6"}}
	svc := proxy.NewService(fr,
		map[string]providers.Client{
			providers.ProviderOpenAIGateway: openaicompat.NewGatewayClient("test-key", gateway.URL+"/v1"),
		},
		nil, false, nil, nil, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil,
	).WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderOpenAIGateway: {}})

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	require.NoError(t, svc.ProxyMessages(context.Background(), []byte(grokGatewayToolTurn), httptest.NewRecorder(), req))
	require.Len(t, sent(), 2)

	fr.decision = router.Decision{Provider: providers.ProviderOpenAIGateway, Model: "gpt-5.4-mini"}
	require.NoError(t, svc.ProxyMessages(context.Background(), []byte(miniGatewayReasoningToolTurn), httptest.NewRecorder(), req))
	all := sent()
	require.Len(t, all, 4, "gpt-5.4-mini has not been refused yet, so its first turn still probes with the summary knob")
	assert.Equal(t, "detailed", gjson.GetBytes(requestBody(all[2]), "reasoning.summary").String())
	assert.False(t, gjson.GetBytes(requestBody(all[3]), "reasoning.summary").Exists())
}
