package proxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/catalog"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Routing-policy passthrough serves the client's model as-is, so a gateway
// alias for a model retired from routing must still be honored.
func TestRoutingPolicyPassthroughServesGatewayAliasedRetiredModel(t *testing.T) {
	retired := catalog.ModelIDClaudeOpus48.String()
	require.Equal(t, catalog.TierUnknown, catalog.TierFor(retired), "fixture needs a model retired from routing")

	gateway := &fakeClient{name: providers.ProviderAnthropicGateway, outcomes: []fakeOutcome{{
		writeBytes: []byte(`{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":1}}`),
	}}}
	spy := &blindExperimentRouterSpy{err: errors.New("scorer must not run")}
	svc := NewService(spy, map[string]providers.Client{providers.ProviderAnthropicGateway: gateway},
		nil, newStubPinStore(), false, providers.ProviderAnthropic, catalog.ModelIDClaudeHaiku45.String(), nil)

	installationID := uuid.New().String()
	authService := auth.NewService(nil, nil, nil, nil, nil, nil, time.Now).WithRoutingPolicies(routingPolicyStub{mode: auth.RoutingPolicyPassthrough}, nil)
	ctx, err := authService.WithRoutingPolicy(context.Background(), installationID)
	require.NoError(t, err)
	ctx = context.WithValue(ctx, APIKeyIDContextKey{}, "key-1")
	ctx = context.WithValue(ctx, InstallationIDContextKey{}, installationID)
	ctx = context.WithValue(ctx, ExternalAPIKeysContextKey{}, []*auth.ExternalAPIKey{{
		InstallationID: installationID,
		Provider:       providers.ProviderAnthropicGateway,
		Plaintext:      []byte("gw-token"),
		BaseURL:        "https://gateway.example.com",
		ModelAliases:   map[string]string{retired: retired, "claude-opus-5-5": "claude-opus-5-5"},
	}})
	ctx = context.WithValue(ctx, InstallationAllowedModelsContextKey{}, []string{retired, "claude-opus-5-5"})

	rec := httptest.NewRecorder()
	body := `{"model":"` + retired + `","max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`
	require.NoError(t, svc.ProxyMessages(ctx, []byte(body), rec, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))))

	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, retired, rec.Header().Get(HeaderRouterModel))
	assert.Equal(t, 1, gateway.calls)
	assert.Zero(t, spy.routeCalls)
}
