package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type codexTitleRouter struct {
	routeCalls int
}

func (r *codexTitleRouter) Route(context.Context, router.Request) (router.Decision, error) {
	r.routeCalls++
	return router.Decision{Provider: providers.ProviderOpenAI, Model: "gpt-5.6-sol", Reason: "scored"}, nil
}

type codexTitleProvider struct {
	endpoints []providers.Endpoint
}

func (p *codexTitleProvider) Proxy(_ context.Context, _ router.Decision, prep providers.PreparedRequest, w http.ResponseWriter, _ *http.Request) error {
	p.endpoints = append(p.endpoints, prep.Endpoint)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","output":[]}`))
	return nil
}

func (p *codexTitleProvider) Passthrough(context.Context, providers.PreparedRequest, http.ResponseWriter, *http.Request) error {
	return providers.ErrNotImplemented
}

func TestCodexResponsesTitleGenerationHardPinsWithoutScoring(t *testing.T) {
	routerSpy := &codexTitleRouter{}
	provider := &codexTitleProvider{}
	svc := NewService(
		routerSpy,
		map[string]providers.Client{providers.ProviderOpenAI: provider},
		nil, false, nil, nil, false,
		providers.ProviderOpenAI, "gpt-5.6-luna", nil,
	)
	body := []byte(`{
		"model":"gpt-5.6-sol",
		"stream":true,
		"tools":[{"type":"function","name":"shell","parameters":{"type":"object"}}],
		"text":{"format":{"type":"json_schema","schema":{
			"type":"object",
			"properties":{"title":{"type":"string"}},
			"required":["title"],
			"additionalProperties":false
		}}},
		"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Generate a concise task title."}]}]
	}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	ctx := context.WithValue(context.Background(), ClientIdentityContextKey{}, ClientIdentity{ClientApp: ClientAppCodex})

	require.NoError(t, svc.ProxyOpenAIResponses(ctx, body, rec, req))
	require.Len(t, provider.endpoints, 1)
	require.Zero(t, routerSpy.routeCalls, "Codex title generation must use the hard-pin path")
	assert.NotContains(t, rec.Body.String(), "Weave Router", "hard-pinned title responses must not carry a routing marker")
}

func TestCodexResponsesTitlePromptHardPinsWithoutScoring(t *testing.T) {
	routerSpy := &codexTitleRouter{}
	provider := &codexTitleProvider{}
	svc := NewService(
		routerSpy,
		map[string]providers.Client{providers.ProviderOpenAI: provider},
		nil, false, nil, nil, false,
		providers.ProviderOpenAI, "gpt-5.6-luna", nil,
	)
	body := []byte(`{
		"model":"gpt-5.6-sol",
		"parallel_tool_calls":true,
		"store":false,
		"stream":true,
		"text":{"verbosity":"low"},
		"tool_choice":"auto",
		"input":[
			{"type":"message","role":"developer","content":[{"type":"input_text","text":"Base instructions."}]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"Respond directly to the user's prompt.\n\nYou are generating a short conversation title. Keep it concise."}]}
		]
	}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	ctx := context.WithValue(context.Background(), ClientIdentityContextKey{}, ClientIdentity{ClientApp: ClientAppCodex})

	require.NoError(t, svc.ProxyOpenAIResponses(ctx, body, rec, req))
	require.Len(t, provider.endpoints, 1)
	require.Zero(t, routerSpy.routeCalls, "Codex title generation must use the hard-pin path")
	assert.NotContains(t, rec.Body.String(), "Weave Router", "hard-pinned title responses must not carry a routing marker")
}

func TestCodexResponsesTitlePromptAfterHarnessContextHardPins(t *testing.T) {
	routerSpy := &codexTitleRouter{}
	provider := &codexTitleProvider{}
	svc := NewService(
		routerSpy,
		map[string]providers.Client{providers.ProviderOpenAI: provider},
		nil, false, nil, nil, false,
		providers.ProviderOpenAI, "gpt-5.6-luna", nil,
	)
	body := []byte(`{
		"model":"gpt-5.6-sol",
		"stream":true,
		"tools":[{"type":"function","name":"shell","parameters":{"type":"object"}}],
		"text":{"verbosity":"low"},
		"input":[
			{"type":"message","role":"developer","content":[{"type":"input_text","text":"Base instructions."}]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"<recommended_plugins>\nHere is a list of plugins that are available but not installed.\n</recommended_plugins>"}]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context>\n  <current_date>2026-09-12</current_date>\n</environment_context>"}]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"Respond directly to the user's prompt. Do not run shell commands.\n\nYou are generating a short conversation title.\n\nReturn only the title."}]}
		]
	}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	ctx := context.WithValue(context.Background(), ClientIdentityContextKey{}, ClientIdentity{ClientApp: ClientAppCodex})

	require.NoError(t, svc.ProxyOpenAIResponses(ctx, body, rec, req))
	require.Len(t, provider.endpoints, 1)
	require.Zero(t, routerSpy.routeCalls, "Codex title generation must use the hard-pin path")
	assert.NotContains(t, rec.Body.String(), "Weave Router", "hard-pinned title responses must not carry a routing marker")
}

func TestCodexResponsesTitlePromptWithAssistantHistoryUsesScorer(t *testing.T) {
	routerSpy := &codexTitleRouter{}
	provider := &codexTitleProvider{}
	svc := NewService(
		routerSpy,
		map[string]providers.Client{providers.ProviderOpenAI: provider},
		nil, false, nil, nil, false,
		providers.ProviderOpenAI, "gpt-5.6-luna", nil,
	)
	body := []byte(`{
		"model":"gpt-5.6-sol",
		"stream":true,
		"tools":[{"type":"function","name":"shell","parameters":{"type":"object"}}],
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"Fix the failing test."}]},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"On it."}]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"Respond directly to the user's prompt.\n\nYou are generating a short conversation title."}]}
		]
	}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	ctx := context.WithValue(context.Background(), ClientIdentityContextKey{}, ClientIdentity{ClientApp: ClientAppCodex})

	require.NoError(t, svc.ProxyOpenAIResponses(ctx, body, rec, req))
	require.Len(t, provider.endpoints, 1)
	assert.Equal(t, 1, routerSpy.routeCalls, "an in-conversation title quote must stay on the scorer")
}

func TestResponsesTitlePromptWithoutCodexIdentityUsesScorer(t *testing.T) {
	routerSpy := &codexTitleRouter{}
	provider := &codexTitleProvider{}
	svc := NewService(
		routerSpy,
		map[string]providers.Client{providers.ProviderOpenAI: provider},
		nil, false, nil, nil, false,
		providers.ProviderOpenAI, "gpt-5.6-luna", nil,
	)
	body := []byte(`{
		"model":"gpt-5.6-sol",
		"stream":true,
		"text":{"verbosity":"low"},
		"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Respond directly to the user's prompt. You are generating a short conversation title."}]}]
	}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	require.NoError(t, svc.ProxyOpenAIResponses(context.Background(), body, rec, req))
	require.Len(t, provider.endpoints, 1)
	assert.Equal(t, 1, routerSpy.routeCalls, "prompt matching must remain gated to Codex ingress")
}
