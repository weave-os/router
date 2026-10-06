package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/subscriptions"
	"weave-os/router/internal/translate"
)

// outOfRosterCodexModel is an OpenAI catalog model that is not part of the
// native automatic Codex roster but is explicitly approved for a
// subscription-first funding attempt.
const outOfRosterCodexModel = "gpt-6-astra"

// fundingAttempt records how one upstream dispatch was funded and which model
// it carried.
type fundingAttempt struct {
	oauth    bool
	model    string
	endpoint providers.Endpoint
}

// scriptedFundingClient answers OAuth-funded attempts with subscriptionErr
// (nil = serve) and API-funded attempts with a completed response.
type scriptedFundingClient struct {
	subscriptionErr error
	attempts        []fundingAttempt
}

// Synthetic provider never bills subscription extra usage.
func (*scriptedFundingClient) IncludedOnlySubscriptions() bool { return true }

func (c *scriptedFundingClient) Proxy(ctx context.Context, decision router.Decision, prep providers.PreparedRequest, w http.ResponseWriter, _ *http.Request) error {
	creds := CredentialsFromContext(ctx)
	oauth := creds != nil && creds.OAuth
	model := gjson.GetBytes(prep.Body, "model").String()
	if model == "" {
		model = decision.Model
	}
	c.attempts = append(c.attempts, fundingAttempt{oauth: oauth, model: model, endpoint: prep.Endpoint})
	if oauth && c.subscriptionErr != nil {
		return c.subscriptionErr
	}
	if prep.Endpoint == providers.EndpointChatCompletions {
		_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl_1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"served\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		return nil
	}
	w.Header().Set("Content-Type", "text/event-stream")
	for _, frame := range []string{
		`{"type":"response.output_text.delta","output_index":0,"delta":"served"}`,
		`{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"served"}]}],"usage":{"input_tokens":12,"output_tokens":3}}}`,
	} {
		_, _ = io.WriteString(w, "data: "+frame+"\n\n")
	}
	return nil
}

func (c *scriptedFundingClient) Passthrough(context.Context, providers.PreparedRequest, http.ResponseWriter, *http.Request) error {
	return providers.ErrNotImplemented
}

func outOfRosterFundingService(client providers.Client, reason string, repo billing.Repo) *Service {
	svc := NewService(
		staticRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: outOfRosterCodexModel, Reason: reason}},
		map[string]providers.Client{providers.ProviderOpenAI: client},
		nil, false, nil, nil, false, providers.ProviderOpenAI, outOfRosterCodexModel, nil,
	).WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderOpenAI: {}}).
		WithBillingService(billing.NewService(repo))
	svc.retrySleep = noopSleep
	return svc
}

func outOfRosterBillingCtx(ctx context.Context) context.Context {
	return context.WithValue(ctx, ExternalIDContextKey{}, "org-synthetic")
}

// TestCodexOutOfRosterModel_SubscriptionFirstFunding: a selected OpenAI model
// outside the native Codex roster keeps its model and provider, tries the
// caller's Codex subscription first, and only falls back to the API credential
// on a quota, credential, or explicit model-availability rejection.
func TestCodexOutOfRosterModel_SubscriptionFirstFunding(t *testing.T) {
	modelRejected := &providers.UpstreamErrorResponse{
		Status: http.StatusBadRequest,
		Body:   []byte(`{"error":{"type":"invalid_request_error","code":"model_not_found","param":"model","message":"synthetic"}}`),
	}
	quotaSpent := &providers.UpstreamErrorResponse{
		Status: http.StatusTooManyRequests,
		Body:   []byte(`{"error":{"type":"usage_limit_reached","message":"synthetic"}}`),
	}
	tokenRejected := &providers.UpstreamErrorResponse{
		Status: http.StatusUnauthorized,
		Body:   []byte(`{"error":{"type":"invalid_request_error","message":"synthetic"}}`),
	}
	shapeRejected := &providers.UpstreamErrorResponse{
		Status: http.StatusBadRequest,
		Body:   []byte(`{"error":{"type":"invalid_request_error","param":"input","message":"synthetic"}}`),
	}

	for _, selection := range []struct {
		name   string
		reason string
	}{
		{name: "user forced", reason: translate.ReasonUserForceModel},
		{name: "HMM selected", reason: "hmm"},
	} {
		for _, tc := range []struct {
			name                  string
			subscriptionEr        error
			enrollmentUnavailable bool
			wantOAuth             []bool
			wantErr               bool
			wantSubServed         bool
		}{
			{name: "subscription serves", wantOAuth: []bool{true}, wantSubServed: true},
			{name: "personal subscription survives managed enrollment outage", enrollmentUnavailable: true, wantOAuth: []bool{true}, wantSubServed: true},
			// 429 keeps the existing bounded same-binding retries before rescue.
			{name: "quota rejection falls back to API credential", subscriptionEr: quotaSpent, wantOAuth: []bool{true, false}},
			{name: "credential rejection falls back to API credential", subscriptionEr: tokenRejected, wantOAuth: []bool{true, false}},
			{name: "unsupported model falls back to API credential", subscriptionEr: modelRejected, wantOAuth: []bool{true, false}},
			{name: "request-shape rejection stays terminal", subscriptionEr: shapeRejected, wantOAuth: []bool{true}, wantErr: true},
		} {
			t.Run(selection.name+"/"+tc.name, func(t *testing.T) {
				client := &scriptedFundingClient{subscriptionErr: tc.subscriptionEr}
				repo := &auxBillingRepo{}
				svc := outOfRosterFundingService(client, selection.reason, repo)
				ctx := context.WithValue(context.Background(), ClientIdentityContextKey{}, ClientIdentity{ClientApp: ClientAppCodex})
				ctx = outOfRosterBillingCtx(ctx)
				if tc.enrollmentUnavailable {
					svc = svc.WithManagedSubscriptions(&scriptedSubscriptionLeaser{})
					ctx = context.WithValue(ctx, ManagedSubscriptionEnrollmentUnavailableContextKey{}, true)
				}
				body := `{"model":"codex-auto-review","stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"synthetic"}]}]}`
				rec := httptest.NewRecorder()
				err := svc.ProxyOpenAIResponses(ctx, []byte(body), rec, codexSubHTTPRequest("/v1/responses", body))

				if tc.wantErr {
					require.Error(t, err)
				} else {
					require.NoError(t, err)
				}
				gotOAuth := make([]bool, 0, len(client.attempts))
				for _, attempt := range client.attempts {
					gotOAuth = append(gotOAuth, attempt.oauth)
					assert.Equal(t, outOfRosterCodexModel, attempt.model, "funding fallback must never change the selected model")
				}
				assert.Equal(t, tc.wantOAuth, gotOAuth, "subscription first, then at most one API-funded attempt")
				if tc.wantErr {
					return
				}
				assert.Equal(t, outOfRosterCodexModel, rec.Header().Get(HeaderRouterModel))
				assert.Equal(t, providers.ProviderOpenAI, rec.Header().Get(HeaderRouterProvider))
				require.Eventually(t, func() bool { return len(repo.snapshot()) == 1 }, 2*time.Second, 10*time.Millisecond)
				debit := repo.snapshot()[0]
				assert.Equal(t, outOfRosterCodexModel, debit.RouterModel)
				if tc.wantSubServed {
					assert.Zero(t, debit.DeltaUsdMicros, "a subscription-served turn must not bill API cost")
				} else {
					assert.Negative(t, debit.DeltaUsdMicros, "an API-funded fallback must record API cost")
				}
			})
		}
	}
}

// TestCodexOutOfRosterModel_ManagedPoolFundsBeforeAPICredential: without a
// personal Codex credential, an enrolled managed Codex pool is leased for the
// out-of-roster model before any API-priced attempt.
func TestCodexOutOfRosterModel_ManagedPoolFundsBeforeAPICredential(t *testing.T) {
	modelRejected := &providers.UpstreamErrorResponse{
		Status: http.StatusNotFound,
		Body:   []byte(`{"error":{"type":"invalid_request_error","code":"model_not_found","param":"model","message":"synthetic"}}`),
	}
	for _, tc := range []struct {
		name            string
		poolErr         error
		poolUnavailable bool
		wantOAuth       []bool
		wantPoolServe   bool
	}{
		{name: "pool serves", wantOAuth: []bool{true}, wantPoolServe: true},
		{name: "pool model rejection falls back to API credential", poolErr: modelRejected, wantOAuth: []bool{true, false}},
		{name: "unavailable enrollment falls back to API credential", poolUnavailable: true, wantOAuth: []bool{false}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			leaser := &scriptedSubscriptionLeaser{leases: []subscriptions.Lease{{
				AccountID: "synthetic-account", AccessToken: "synthetic-managed-token", ProviderAccount: "synthetic-chatgpt-account",
				State: auth.SubscriptionAccountStateActive,
			}}, repeatLast: true}
			client := &scriptedFundingClient{subscriptionErr: tc.poolErr}
			repo := &auxBillingRepo{}
			svc := outOfRosterFundingService(client, translate.ReasonUserForceModel, repo).WithManagedSubscriptions(leaser)

			body := `{"model":"codex-auto-review","stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"synthetic"}]}]}`
			ctx := managedSubscriptionContext(auth.SubscriptionProviderCodex)
			ctx = context.WithValue(ctx, InstallationIDContextKey{}, "33333333-3333-3333-3333-333333333333")
			ctx = context.WithValue(ctx, ClientIdentityContextKey{}, ClientIdentity{ClientApp: ClientAppCodex})
			if tc.poolUnavailable {
				ctx = context.WithValue(ctx, ManagedSubscriptionEnrollmentUnavailableContextKey{}, true)
			}
			ctx = outOfRosterBillingCtx(ctx)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))

			require.NoError(t, svc.ProxyOpenAIResponses(ctx, []byte(body), rec, req))

			gotOAuth := make([]bool, 0, len(client.attempts))
			for _, attempt := range client.attempts {
				gotOAuth = append(gotOAuth, attempt.oauth)
				assert.Equal(t, outOfRosterCodexModel, attempt.model)
			}
			assert.Equal(t, tc.wantOAuth, gotOAuth)
			if tc.poolUnavailable {
				assert.Empty(t, leaser.providers, "unavailable enrollment must go directly to permitted API funding")
			} else {
				require.NotEmpty(t, leaser.providers)
				assert.Equal(t, subscriptions.ProviderCodex, leaser.providers[0], "the out-of-roster model must lease from the Codex pool")
			}
			assert.Equal(t, tc.wantPoolServe, managedSubscriptionServed(ctx))
		})
	}
}

func TestCodexOutOfRosterModel_ChatOnlyRequestUsesAPICredential(t *testing.T) {
	client := &scriptedFundingClient{}
	svc := outOfRosterFundingService(client, translate.ReasonUserForceModel, &auxBillingRepo{})
	ctx := outOfRosterBillingCtx(codexSubscriptionTestCtx())
	body := `{"model":"gpt-6-astra","stream":true,"n":2,"messages":[{"role":"user","content":"synthetic"}]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))

	require.NoError(t, svc.ProxyOpenAIChatCompletion(ctx, []byte(body), rec, req))
	require.Len(t, client.attempts, 1)
	assert.Equal(t, providers.EndpointChatCompletions, client.attempts[0].endpoint)
	assert.False(t, client.attempts[0].oauth, "a Chat Completions request must use the API credential, not Codex OAuth")
}

func TestCodexOutOfRosterModel_ChatOnlyRequestWithoutAPICredentialRefusesBeforeDispatch(t *testing.T) {
	client := &scriptedFundingClient{}
	svc := outOfRosterFundingService(client, translate.ReasonUserForceModel, &auxBillingRepo{})
	svc.deploymentKeyedProviders = nil
	ctx := outOfRosterBillingCtx(codexSubscriptionTestCtx())
	body := `{"model":"gpt-6-astra","stream":true,"n":2,"messages":[{"role":"user","content":"synthetic"}]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))

	err := svc.ProxyOpenAIChatCompletion(ctx, []byte(body), rec, req)
	assert.ErrorIs(t, err, ErrCreditsExhaustedSubscriptionUnavailable)
	assert.Empty(t, client.attempts, "the Codex token must not be sent to Chat Completions without an API fallback")
}
