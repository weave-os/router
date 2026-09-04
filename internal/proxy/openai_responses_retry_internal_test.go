package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// staticRouter answers every routing call with one decision.
type staticRouter struct{ decision router.Decision }

func (s staticRouter) Route(context.Context, router.Request) (router.Decision, error) {
	return s.decision, nil
}

// responsesRetryClient fails its first dispatch retryably and answers the next
// one with a complete non-streaming Responses body, recording each endpoint.
type responsesRetryClient struct {
	endpoints []providers.Endpoint
}

func (c *responsesRetryClient) Proxy(_ context.Context, _ router.Decision, prep providers.PreparedRequest, w http.ResponseWriter, _ *http.Request) error {
	c.endpoints = append(c.endpoints, prep.Endpoint)
	if len(c.endpoints) == 1 {
		return &providers.UpstreamErrorResponse{
			Status: http.StatusServiceUnavailable,
			Body:   []byte(`{"error":{"message":"overloaded"}}`),
		}
	}
	w.Header().Set("Content-Type", "text/event-stream")
	for _, frame := range []string{
		`{"type":"response.output_text.delta","output_index":0,"delta":"served after retry"}`,
		`{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"served after retry"}]}],"usage":{"input_tokens":12,"output_tokens":3}}}`,
	} {
		_, _ = io.WriteString(w, "data: "+frame+"\n\n")
	}
	return nil
}

func (c *responsesRetryClient) Passthrough(context.Context, providers.PreparedRequest, http.ResponseWriter, *http.Request) error {
	return providers.ErrNotImplemented
}

// A single-binding OpenAI model retries in place on a retryable fault, and the
// retry must stay on /v1/responses: silently re-sending a reasoning + tools turn
// to chat/completions is the 400 this migration exists to remove.
func TestProxyOpenAIChatCompletion_SameBindingRetryStaysOnResponses(t *testing.T) {
	client := &responsesRetryClient{}
	svc := NewService(
		staticRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: "gpt-5.6-luna", Reason: "test"}},
		map[string]providers.Client{providers.ProviderOpenAI: client},
		nil, false, nil, nil, false, providers.ProviderOpenAI, "gpt-5.6-sol", nil,
	)
	svc.retrySleep = noopSleep

	body := `{"model":"auto","stream":false,"max_tokens":256,"messages":[{"role":"user","content":"read main.go"}],"tools":[{"type":"function","function":{"name":"read_file","parameters":{"type":"object"}}}],"reasoning_effort":"medium"}`
	rec := httptest.NewRecorder()
	require.NoError(t, svc.ProxyOpenAIChatCompletion(context.Background(), []byte(body), rec,
		httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))))

	assert.Equal(t, []providers.Endpoint{providers.EndpointResponses, providers.EndpointResponses}, client.endpoints)
	assert.NotContains(t, rec.Body.String(), "overloaded", "the failed pre-commit attempt must not reach the client")
	assert.Equal(t, "served after retry", gjson.GetBytes(rec.Body.Bytes(), "choices.0.message.content").String())
}

// alwaysFailingClient answers every dispatch with the same retryable fault.
type alwaysFailingClient struct{ calls int }

func (c *alwaysFailingClient) Proxy(context.Context, router.Decision, providers.PreparedRequest, http.ResponseWriter, *http.Request) error {
	c.calls++
	return &providers.UpstreamErrorResponse{Status: http.StatusTooManyRequests, Body: []byte(`{"type":"error","error":{"type":"overloaded_error"}}`)}
}

func (c *alwaysFailingClient) Passthrough(context.Context, providers.PreparedRequest, http.ResponseWriter, *http.Request) error {
	return providers.ErrNotImplemented
}

// An Anthropic-ingress turn whose Anthropic binding is exhausted is rescued by a
// same-cluster OpenAI candidate — the rescue must be rebuilt for the candidate's
// family, which now means /v1/responses, and reach the client as Anthropic SSE.
func TestProxyMessages_SiblingFailoverOntoOpenAIUsesResponses(t *testing.T) {
	failing := &alwaysFailingClient{}
	rescue := &responsesRetryClient{}
	decision := router.Decision{
		Provider: providers.ProviderAnthropic,
		Model:    "claude-opus-5",
		Reason:   "test",
		Metadata: &router.RoutingMetadata{
			CandidateModels:    []string{"claude-opus-5", "gpt-5.6-luna"},
			CandidateProviders: map[string]string{"gpt-5.6-luna": providers.ProviderOpenAI},
		},
	}
	svc := NewService(
		staticRouter{decision: decision},
		map[string]providers.Client{
			providers.ProviderAnthropic: failing,
			providers.ProviderOpenAI:    rescue,
		},
		nil, false, nil, nil, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil,
	).WithDeploymentKeyedProviders(map[string]struct{}{
		providers.ProviderAnthropic: {},
		providers.ProviderOpenAI:    {},
	})
	svc.retrySleep = noopSleep

	body := `{"model":"claude-opus-5","max_tokens":1024,"stream":true,"messages":[{"role":"user","content":"read main.go"}],"tools":[{"name":"read_file","input_schema":{"type":"object","properties":{"path":{"type":"string"}}}}],"thinking":{"type":"adaptive"}}`
	rec := httptest.NewRecorder()
	require.NoError(t, svc.ProxyMessages(context.Background(), []byte(body), rec,
		httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))))

	assert.Positive(t, failing.calls, "the routed Anthropic binding must be tried first")
	require.Len(t, rescue.endpoints, 2, "the rescue retries in place on its own binding")
	assert.Equal(t, providers.EndpointResponses, rescue.endpoints[1],
		"a sibling rescue onto direct OpenAI must dispatch on /v1/responses")
	out := rec.Body.String()
	assert.Contains(t, out, "event: content_block_delta", "the Anthropic client still gets Anthropic SSE")
	assert.Contains(t, out, "served after retry")
}
