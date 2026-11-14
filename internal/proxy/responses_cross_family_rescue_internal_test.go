package proxy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/anthropic"
	"weave-os/router/internal/providers/openai"
	"weave-os/router/internal/subscriptions"
)

func TestResponsesCodexRejectionRescuesToAnthropic(t *testing.T) {
	for _, toolCall := range []bool{false, true} {
		for _, truncated := range []bool{false, true} {
			t.Run(fmt.Sprintf("tool=%t/truncated=%t", toolCall, truncated), func(t *testing.T) {
				fixture := &codexRescueUpstream{toolCall: toolCall, truncated: truncated}
				upstream := httptest.NewServer(fixture)
				defer upstream.Close()
				svc, ctx := newCodexRescueService(upstream.URL)
				body := `{"model":"auto","stream":true,"input":[{"role":"user","content":"synthetic task"}],"tools":[{"type":"function","name":"exec","parameters":{"type":"object","properties":{"cmd":{"type":"string"}},"required":["cmd"]}}]}`
				rec := httptest.NewRecorder()
				err := svc.ProxyOpenAIResponses(ctx, []byte(body), rec, httptest.NewRequest(http.MethodPost, routePathResponses, strings.NewReader(body)))
				assert.Equal(t, 1, fixture.preflights)
				assert.Equal(t, 1, fixture.claudeAttempts)
				assert.Zero(t, fixture.codexAttempts, "a rejected preflight must prevent GPT inference")
				assertCodexRescueOutput(t, rec, err, toolCall, truncated)
			})
		}
	}
}

type codexRescueUpstream struct {
	toolCall, truncated                       bool
	preflights, claudeAttempts, codexAttempts int
}

func (u *codexRescueUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(r.URL.Path, "/wham/usage") {
		u.preflights++
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if !strings.HasSuffix(r.URL.Path, "/messages") {
		u.codexAttempts++
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	u.claudeAttempts++
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"synthetic\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\""+stateClaudeModel+"\",\"content\":[],\"usage\":{\"input_tokens\":11,\"output_tokens\":0}}}\n\n")
	stopReason := "end_turn"
	if u.toolCall {
		stopReason = "tool_use"
		_, _ = io.WriteString(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"call_synthetic\",\"name\":\"exec\",\"input\":{}}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"cmd\\\":\\\"pwd\\\"}\"}}\n\n")
	} else {
		_, _ = io.WriteString(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"synthetic answer\"}}\n\n")
	}
	if !u.truncated {
		_, _ = io.WriteString(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\""+stopReason+"\"},\"usage\":{\"output_tokens\":7}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}
}

func newCodexRescueService(upstreamURL string) (*Service, context.Context) {
	codexClient := openai.NewClient("synthetic-paid-key", upstreamURL)
	codexClient.SetCodexBaseURL(upstreamURL)
	svc := NewService(stateModelRouter{}, map[string]providers.Client{
		providers.ProviderAnthropic: anthropic.NewClient("synthetic-paid-key", upstreamURL),
		providers.ProviderOpenAI:    codexClient,
	}, nil, nil, false, "", "", nil).
		WithManagedSubscriptions(&stateModelLeaser{activeProviders: map[subscriptions.Provider]bool{subscriptions.ProviderClaude: true}})
	ctx := context.WithValue(managedSubscriptionTestContext(), ManagedSubscriptionProvidersContextKey{}, map[auth.SubscriptionProvider]struct{}{auth.SubscriptionProviderClaude: {}, auth.SubscriptionProviderCodex: {}})
	ctx = context.WithValue(ctx, InstallationSubscriptionModelsWhenActiveContextKey{}, []string{stateCodexModel})
	ctx = context.WithValue(ctx, InstallationSubscriptionModelsWhenInactiveContextKey{}, []string{})
	ctx = context.WithValue(ctx, OpenAISubscriptionContextKey{}, codexTestToken)
	ctx = context.WithValue(ctx, OpenAIAccountIDContextKey{}, codexTestAccountID)
	ctx = context.WithValue(ctx, ClientIdentityContextKey{}, ClientIdentity{ClientApp: ClientAppCodex})
	return svc, ctx
}

func assertCodexRescueOutput(t *testing.T, rec *httptest.ResponseRecorder, err error, toolCall, truncated bool) {
	t.Helper()
	if truncated {
		require.Error(t, err)
		assert.NotContains(t, rec.Body.String(), `"type":"response.completed"`)
		return
	}
	require.NoError(t, err)
	assert.Contains(t, rec.Body.String(), `"type":"response.completed"`)
	assert.NotContains(t, rec.Body.String(), `"choices":`)
	if toolCall {
		assert.Contains(t, rec.Body.String(), `"type":"function_call"`)
		assert.Contains(t, rec.Body.String(), `"name":"exec"`)
	} else {
		assert.Contains(t, rec.Body.String(), "synthetic answer")
	}
	for _, frame := range strings.Split(rec.Body.String(), "\n\n") {
		for _, line := range strings.Split(frame, "\n") {
			if strings.HasPrefix(line, "data: ") {
				assert.True(t, gjson.Get(strings.TrimPrefix(line, "data: "), "type").Exists(), "every data frame must speak Responses")
			}
		}
	}
}
