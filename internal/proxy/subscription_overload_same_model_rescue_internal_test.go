package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
)

// planThenPaidClient overloads the ChatGPT-plan attempt and serves the retry
// that arrives on the deployment key, recording which credential each call used.
type planThenPaidClient struct {
	oauthPerCall []bool
	models       []string
}

func (*planThenPaidClient) SupportsSubscriptions() bool { return true }

func (c *planThenPaidClient) Proxy(ctx context.Context, decision router.Decision, _ providers.PreparedRequest, w http.ResponseWriter, _ *http.Request) error {
	creds := CredentialsFromContext(ctx)
	oauth := creds != nil && creds.OAuth
	c.oauthPerCall = append(c.oauthPerCall, oauth)
	c.models = append(c.models, decision.Model)
	if oauth {
		return &providers.UpstreamErrorResponse{
			Status: http.StatusBadGateway,
			Body:   []byte(`{"error":{"type":"service_unavailable_error","code":"server_is_overloaded","message":"Our servers are currently overloaded. Please try again later."}}`),
		}
	}
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":\"served on the Weave key\"}\n\n")
	_, err := io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"paid\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n")
	return err
}

func (c *planThenPaidClient) Passthrough(context.Context, providers.PreparedRequest, http.ResponseWriter, *http.Request) error {
	return providers.ErrNotImplemented
}

// A provider 5xx on a ChatGPT-plan turn is "the plan cannot serve this turn",
// not proof of unused quota: the same model must be retried on the deployment
// key instead of the overload reaching the client. Regression: #1711 blocked
// this retry behind quota-exhaustion evidence and Codex users saw "model is at
// capacity" for every plan 5xx until #1784.
func TestCodexPlanOverload_RetriesSameModelOnWeaveKey(t *testing.T) {
	client := &planThenPaidClient{}
	svc := codexQuotaService(client)

	body := `{"model":"auto","stream":true,"messages":[{"role":"user","content":"read main.go"}]}`
	rec := httptest.NewRecorder()
	ctx := context.WithValue(codexSubscriptionTestCtx(), ClientIdentityContextKey{}, ClientIdentity{ClientApp: ClientAppCodex})
	ctx = WithManagedSubscriptionUsage(ctx)
	err := svc.ProxyOpenAIChatCompletion(ctx, []byte(body), rec, codexSubHTTPRequest("/v1/chat/completions", body))

	require.NoError(t, err)
	assert.Equal(t, []bool{true, false}, client.oauthPerCall, "plan attempt first, then the deployment key")
	assert.Equal(t, []string{codexCoveredModel, codexCoveredModel}, client.models, "the paid retry must keep the model the plan attempt failed on")
	assert.Contains(t, rec.Body.String(), "served on the Weave key")
	assert.NotContains(t, rec.Body.String(), "server_is_overloaded")
}
