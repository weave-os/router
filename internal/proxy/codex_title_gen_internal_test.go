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
