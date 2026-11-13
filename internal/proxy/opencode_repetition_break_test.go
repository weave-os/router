package proxy_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const repeatedOpenCodeAnswer = "Authentication uses one signed session cookie with middleware validation and per-field authorization directives."

func TestService_ProxyOpenAIResponses_BreaksRepeatedOpenCodeAnswers(t *testing.T) {
	provider := &fakeProvider{}
	svc := makeProxyService(
		router.Decision{Provider: providers.ProviderXAI, Model: "grok-4.6", Reason: "test"},
		map[string]providers.Client{providers.ProviderXAI: provider},
	).WithTextRepetitionBreak(true)

	body := []byte(`{
		"model":"auto",
		"stream":true,
		"input":[
			{"role":"user","content":[{"type":"input_text","text":"How is auth implemented?"}]},
			{"role":"assistant","content":[{"type":"output_text","text":"` + repeatedOpenCodeAnswer + `"}]},
			{"role":"assistant","content":[{"type":"output_text","text":"` + repeatedOpenCodeAnswer + `"}]},
			{"role":"assistant","content":[{"type":"output_text","text":"` + repeatedOpenCodeAnswer + `"}]}
		]
	}`)
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(string(body)))
	response := httptest.NewRecorder()
	ctx := context.WithValue(context.Background(), proxy.ClientIdentityContextKey{}, proxy.ClientIdentity{ClientApp: proxy.ClientAppOpencode})

	require.NoError(t, svc.ProxyOpenAIResponses(ctx, body, response, request))
	assert.Empty(t, provider.proxyBodies)
	assert.Contains(t, response.Body.String(), "repetition loop detected")
	assert.Contains(t, response.Body.String(), `"type":"response.completed"`)
}
