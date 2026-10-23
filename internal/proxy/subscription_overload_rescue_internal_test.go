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

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/subscriptions"
)

const overloadRescuerModel = "claude-opus-5"

const overloadRescuerMessageSSE = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-opus-5\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":5,\"output_tokens\":0}}}\n\n" +
	"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
	"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"served by sibling\"}}\n\n" +
	"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
	"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":3}}\n\n" +
	"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

// planClient marks a fake upstream as able to carry a subscription OAuth
// bearer, as the real OpenAI and Anthropic clients do.
type planClient struct{ providers.Client }

func (planClient) SupportsSubscriptions() bool { return true }

// anthropicServingClient streams one completed native Messages turn.
type anthropicServingClient struct{ calls int }

func (c *anthropicServingClient) Proxy(_ context.Context, _ router.Decision, _ providers.PreparedRequest, w http.ResponseWriter, _ *http.Request) error {
	c.calls++
	w.Header().Set("Content-Type", "text/event-stream")
	_, err := io.WriteString(w, overloadRescuerMessageSSE)
	return err
}

func (c *anthropicServingClient) Passthrough(context.Context, providers.PreparedRequest, http.ResponseWriter, *http.Request) error {
	return providers.ErrNotImplemented
}

func codexOverloadedErr() *providers.UpstreamErrorResponse {
	return &providers.UpstreamErrorResponse{
		Status: http.StatusBadGateway,
		Body:   []byte(`{"error":{"type":"service_unavailable_error","code":"server_is_overloaded","message":"Our servers are currently overloaded. Please try again later."}}`),
	}
}

// codexOverloadService routes a Codex-plan turn to an OpenAI model whose only
// client is overloaded, with an Anthropic sibling in the scored pool.
func codexOverloadService(primary, sibling providers.Client, withSibling bool) *Service {
	decision := router.Decision{Provider: providers.ProviderOpenAI, Model: codexCoveredModel, Reason: "hmm:authoritative model=" + codexCoveredModel}
	if withSibling {
		decision.Metadata = &router.RoutingMetadata{
			CandidateModels:    []string{codexCoveredModel, overloadRescuerModel},
			CandidateProviders: map[string]string{overloadRescuerModel: providers.ProviderAnthropic},
		}
	}
	svc := NewService(
		staticRouter{decision: decision},
		map[string]providers.Client{providers.ProviderOpenAI: planClient{primary}, providers.ProviderAnthropic: planClient{sibling}},
		nil, false, nil, nil, false, providers.ProviderOpenAI, codexCoveredModel, nil,
	).WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderOpenAI: {}, providers.ProviderAnthropic: {}}).
		WithRetrySleep(func(context.Context, time.Duration) error { return nil }).
		WithSiblingFailover(true)
	return svc
}

// A Codex-plan turn whose model is overloaded upstream must reach the next
// scored candidate: the plan is a funding source, not a vendor-bound key.
func TestCodexPlanOverload_RescuedBySiblingCandidate(t *testing.T) {
	primary := &failingClient{err: codexOverloadedErr()}
	sibling := &anthropicServingClient{}
	svc := codexOverloadService(primary, sibling, true)

	body := `{"model":"auto","stream":true,"messages":[{"role":"user","content":"read main.go"}]}`
	rec := httptest.NewRecorder()
	ctx := context.WithValue(rescuedFailureCtx(), ClientIdentityContextKey{}, ClientIdentity{ClientApp: ClientAppCodex})
	ctx = WithManagedSubscriptionUsage(ctx)
	err := svc.ProxyOpenAIChatCompletion(ctx, []byte(body), rec, codexSubHTTPRequest("/v1/chat/completions", body))
	require.NoError(t, err, "the sibling must serve the turn the overloaded plan model could not")
	assert.GreaterOrEqual(t, primary.calls, 1)
	assert.Equal(t, 1, sibling.calls, "exactly one sibling dispatch serves the turn")
	assert.Contains(t, rec.Body.String(), "data:", "the rescued turn streams to the client")
}

// Without a scored sibling the overload still surfaces, so the rescue never
// invents a candidate the policy did not rank.
func TestCodexPlanOverload_NoSiblingSurfacesUpstreamError(t *testing.T) {
	primary := &failingClient{err: codexOverloadedErr()}
	sibling := &anthropicServingClient{}
	svc := codexOverloadService(primary, sibling, false)

	body := `{"model":"auto","stream":true,"messages":[{"role":"user","content":"read main.go"}]}`
	rec := httptest.NewRecorder()
	ctx := context.WithValue(rescuedFailureCtx(), ClientIdentityContextKey{}, ClientIdentity{ClientApp: ClientAppCodex})
	ctx = WithManagedSubscriptionUsage(ctx)
	err := svc.ProxyOpenAIChatCompletion(ctx, []byte(body), rec, codexSubHTTPRequest("/v1/chat/completions", body))
	require.Error(t, err)
	assert.Equal(t, 0, sibling.calls)
	assert.True(t, strings.Contains(rec.Body.String(), "server_is_overloaded") || strings.Contains(rec.Body.String(), "overloaded"),
		"the upstream capacity error must reach the client when nothing can rescue it")
}

const codexInStreamOverloadFailure = `{"type":"response.failed","sequence_number":2,"response":{"id":"resp_overloaded","object":"response","status":"failed","output":[],"error":{"code":"server_is_overloaded","message":"Our servers are currently overloaded. Please try again later."}}}`

// codexInStreamOverloadClient answers like the ChatGPT backend under load: an
// HTTP 200 stream that fails before any output.
type codexInStreamOverloadClient struct{ calls int }

func (c *codexInStreamOverloadClient) Proxy(_ context.Context, _ router.Decision, _ providers.PreparedRequest, w http.ResponseWriter, _ *http.Request) error {
	c.calls++
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "event: response.created\ndata: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{\"id\":\"resp_overloaded\",\"object\":\"response\",\"status\":\"in_progress\",\"output\":[]}}\n\n")
	_, _ = io.WriteString(w, "event: response.in_progress\ndata: {\"type\":\"response.in_progress\",\"sequence_number\":1,\"response\":{\"id\":\"resp_overloaded\",\"object\":\"response\",\"status\":\"in_progress\",\"output\":[]}}\n\n")
	_, _ = io.WriteString(w, "event: response.failed\ndata: "+codexInStreamOverloadFailure+"\n\n")
	return &providers.UpstreamErrorResponse{Status: http.StatusBadGateway, Body: []byte(codexInStreamOverloadFailure)}
}

func (c *codexInStreamOverloadClient) Passthrough(context.Context, providers.PreparedRequest, http.ResponseWriter, *http.Request) error {
	return providers.ErrNotImplemented
}

func proxyCodexResponsesOverload(t *testing.T, withSibling bool) (*codexInStreamOverloadClient, *anthropicServingClient, *httptest.ResponseRecorder, error) {
	t.Helper()
	primary := &codexInStreamOverloadClient{}
	sibling := &anthropicServingClient{}
	svc := codexOverloadService(primary, sibling, withSibling)
	body := `{"model":"auto","stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"read main.go"}]}]}`
	rec := httptest.NewRecorder()
	ctx := context.WithValue(rescuedFailureCtx(), ClientIdentityContextKey{}, ClientIdentity{ClientApp: ClientAppCodex})
	ctx = WithManagedSubscriptionUsage(ctx)
	err := svc.ProxyOpenAIResponses(ctx, []byte(body), rec, codexSubHTTPRequest("/v1/responses", body))
	return primary, sibling, rec, err
}

// The ChatGPT backend reports overload inside an HTTP 200 Responses stream.
// Its pre-output lifecycle frames must not commit the turn, or every rescue is
// skipped and Codex sees the overload instead of the next scored candidate.
func TestCodexPlanInStreamOverload_RescuedBySiblingCandidate(t *testing.T) {
	primary, sibling, rec, err := proxyCodexResponsesOverload(t, true)
	require.NoError(t, err, "the sibling must serve the turn the overloaded plan model failed before output")
	assert.GreaterOrEqual(t, primary.calls, 1)
	assert.Equal(t, 1, sibling.calls, "exactly one sibling dispatch serves the turn")
	out := rec.Body.String()
	assert.Contains(t, out, "served by sibling")
	assert.NotContains(t, out, "resp_overloaded", "no frame of the failed attempt reaches the client")
	assert.NotContains(t, out, "response.failed")
	assert.Contains(t, out, "event: response.completed", "Codex needs a Responses terminal for the rescued turn")
	assert.NotContains(t, out, "chat.completion.chunk", "the sibling's Chat Completions frames are translated to Responses")
	assert.NotContains(t, out, "[DONE]")
}

func TestCodexPlanInStreamOverload_NoSiblingSurfacesUpstreamFailure(t *testing.T) {
	_, sibling, rec, err := proxyCodexResponsesOverload(t, false)
	require.Error(t, err)
	assert.Equal(t, 0, sibling.calls)
	assert.Equal(t, 1, strings.Count(rec.Body.String(), "event: response.failed"),
		"the unrescued failure ends the stream exactly once, so Codex does not see a truncated response")
}

// A plan 429 or timeout is not capacity evidence: the walk the plan credential
// opened stays shut and the error surfaces, so no paid sibling is bought while
// included quota may be unused.
func TestCodexPlanThrottle_DoesNotBuyPaidSibling(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "429", err: &providers.UpstreamErrorResponse{Status: http.StatusTooManyRequests, Body: []byte(`{"error":{"type":"rate_limit_exceeded","message":"slow down"}}`)}},
		{name: "transport fault", err: providers.ErrUpstreamEmptyCompletion},
		{name: "router-minted empty completion", err: emptyCompletion502()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			primary := &failingClient{err: tc.err}
			sibling := &anthropicServingClient{}
			svc := codexOverloadService(primary, sibling, true)

			body := `{"model":"auto","stream":true,"messages":[{"role":"user","content":"read main.go"}]}`
			rec := httptest.NewRecorder()
			ctx := context.WithValue(rescuedFailureCtx(), ClientIdentityContextKey{}, ClientIdentity{ClientApp: ClientAppCodex})
			ctx = WithManagedSubscriptionUsage(ctx)
			err := svc.ProxyOpenAIChatCompletion(ctx, []byte(body), rec, codexSubHTTPRequest("/v1/chat/completions", body))
			require.Error(t, err)
			assert.Equal(t, 0, sibling.calls, "no paid sibling without a plan 5xx")
		})
	}
}

// The clear is keyed on the bearer's own vendor: after one failed Anthropic
// sibling the walk's decision is already Anthropic, and the next Anthropic
// candidate must still not inherit the Codex token.
func TestRescueCredentialContext_ClearsForeignOAuthBearer(t *testing.T) {
	codex := &Credentials{APIKey: []byte("tok"), AccountID: []byte("acct"), Source: credSourceCodexSubscription, OAuth: true}
	ctx := context.WithValue(context.Background(), CredentialsContextKey{}, codex)
	assert.Nil(t, CredentialsFromContext(rescueCredentialContext(ctx, providers.ProviderAnthropic)))
	assert.Nil(t, CredentialsFromContext(rescueCredentialContext(ctx, providers.ProviderFireworks)))
	assert.Equal(t, codex, CredentialsFromContext(rescueCredentialContext(ctx, providers.ProviderOpenAI)))

	claude := &Credentials{APIKey: []byte("sk-ant-oat01-tok"), Source: credSourceSubscription, OAuth: true}
	ctx = context.WithValue(context.Background(), CredentialsContextKey{}, claude)
	assert.Nil(t, CredentialsFromContext(rescueCredentialContext(ctx, providers.ProviderOpenAI)))
	assert.Equal(t, claude, CredentialsFromContext(rescueCredentialContext(ctx, providers.ProviderAnthropic)))

	byok := &Credentials{APIKey: []byte("sk-byok"), Source: credSourceBYOK}
	ctx = context.WithValue(context.Background(), CredentialsContextKey{}, byok)
	assert.Equal(t, byok, CredentialsFromContext(rescueCredentialContext(ctx, providers.ProviderAnthropic)), "only OAuth bearers are cleared")
}

// A Responses response.failed is surfaced as a buffered 502 whatever it says;
// only a genuine server failure is capacity evidence.
func TestUpstreamServerFailure_ReadsTheFailureMeaning(t *testing.T) {
	failed := func(code, typ string) error {
		return &providers.UpstreamErrorResponse{Status: http.StatusBadGateway, Body: []byte(`{"type":"response.failed","response":{"status":"failed","error":{"code":"` + code + `","type":"` + typ + `"}},"error":{"code":"` + code + `","type":"` + typ + `"}}`)}
	}
	assert.True(t, upstreamServerFailure(failed("server_is_overloaded", "service_unavailable_error")))
	assert.True(t, upstreamServerFailure(failed("server_error", "server_error")))
	assert.True(t, upstreamServerFailure(&providers.UpstreamErrorResponse{Status: http.StatusBadGateway, Body: []byte("<html>bad gateway</html>")}))
	assert.False(t, upstreamServerFailure(failed("rate_limit_exceeded", "requests")), "an in-stream throttle is not capacity")
	assert.False(t, upstreamServerFailure(failed("", "rate_limit_error")))
	assert.False(t, upstreamServerFailure(failed("usage_limit_reached", "")), "quota belongs to the quota path")
	assert.False(t, upstreamServerFailure(&providers.UpstreamErrorResponse{Status: http.StatusTooManyRequests}))
	assert.False(t, upstreamServerFailure(providers.ErrUpstreamEmptyCompletion))
	assert.False(t, upstreamServerFailure(emptyCompletion502()), "an empty completion is the router's verdict, not provider capacity")
}

// emptyCompletion502 is the shape the translators hand the proxy for a clean
// upstream terminal with no usable output: a buffered 502 wrapping the sentinel.
func emptyCompletion502() error {
	return &providers.UpstreamErrorResponse{
		Status: http.StatusBadGateway,
		Body:   []byte(`{"error":{"type":"upstream_empty_completion","message":"Upstream returned no assistant output."}}`),
		Cause:  providers.ErrUpstreamEmptyCompletion,
	}
}

func TestSubscriptionSiblingAllowed(t *testing.T) {
	oauth := &Credentials{APIKey: []byte("tok"), AccountID: []byte("acct"), Source: credSourceCodexSubscription, OAuth: true}
	t.Run("subscription OAuth permits sibling rescue", func(t *testing.T) {
		s := &Service{}
		ctx := context.WithValue(context.Background(), CredentialsContextKey{}, oauth)
		assert.False(t, s.shouldFailover(ctx), "generic binding failover stays off for an OAuth credential")
		assert.True(t, s.subscriptionSiblingAllowed(ctx))
	})
	t.Run("BYOK credential does not", func(t *testing.T) {
		s := &Service{}
		ctx := context.WithValue(context.Background(), CredentialsContextKey{}, &Credentials{APIKey: []byte("sk-byok"), Source: credSourceBYOK})
		assert.False(t, s.subscriptionSiblingAllowed(ctx))
	})
	t.Run("BYOK keys on the request keep the generic rule", func(t *testing.T) {
		s := &Service{}
		ctx := context.WithValue(context.Background(), CredentialsContextKey{}, oauth)
		ctx = context.WithValue(ctx, ExternalAPIKeysContextKey{}, []*auth.ExternalAPIKey{{Provider: providers.ProviderMakora, Plaintext: []byte("mk")}})
		assert.False(t, s.subscriptionSiblingAllowed(ctx))
	})
	t.Run("BYOK-only deployment never rescues", func(t *testing.T) {
		s := &Service{byokOnly: true}
		ctx := context.WithValue(context.Background(), CredentialsContextKey{}, oauth)
		assert.False(t, s.subscriptionSiblingAllowed(ctx))
	})
}

// A provider 5xx on the plan is "the plan cannot serve this turn", which
// authorizes paid capacity for the turn; it is not quota exhaustion, so a 429
// never qualifies and the mark expires with its cooldown.
func TestAuthorizeSubscriptionAPI_ProviderOverloadAuthorizesPaidRescue(t *testing.T) {
	now := time.Now()
	svc := &Service{now: func() time.Time { return now }}
	ctx := WithManagedSubscriptionUsage(context.Background())
	ctx = WithManagedSubscriptionCandidates(ctx, []*auth.SubscriptionAccount{{ID: "acct-1", Provider: auth.SubscriptionProviderCodex, Enabled: true, State: auth.SubscriptionAccountStateActive}})

	denied := svc.authorizeSubscriptionAPI(ctx, subscriptions.ProviderCodex, "codex-token", "", clientAppRefusalFunding{})
	require.ErrorIs(t, denied, ErrSubscriptionPoolUnavailable, "healthy included quota must still block paid inference")

	svc.recordSubscriptionUnavailable(ctx, providers.ProviderOpenAI, codexCoveredModel, &providers.UpstreamErrorResponse{Status: http.StatusTooManyRequests, Body: []byte(`{"error":{"type":"rate_limit_exceeded"}}`)})
	require.ErrorIs(t, svc.authorizeSubscriptionAPI(ctx, subscriptions.ProviderCodex, "codex-token", "", clientAppRefusalFunding{}), ErrSubscriptionPoolUnavailable,
		"a 429 is the quota path's evidence, never capacity evidence")

	svc.recordSubscriptionUnavailable(ctx, providers.ProviderOpenAI, codexCoveredModel, codexOverloadedErr())
	require.NoError(t, svc.authorizeSubscriptionAPI(ctx, subscriptions.ProviderCodex, "codex-token", "", clientAppRefusalFunding{}),
		"a 5xx on the plan authorizes paid rescue for this turn")
	require.ErrorIs(t, svc.authorizeSubscriptionAPI(ctx, subscriptions.ProviderClaude, "", "claude-token", clientAppRefusalFunding{}), ErrSubscriptionPoolUnavailable,
		"the mark is scoped to the pool that failed")

	now = now.Add(subscriptionUnavailableCooldown + time.Second)
	require.ErrorIs(t, svc.authorizeSubscriptionAPI(ctx, subscriptions.ProviderCodex, "codex-token", "", clientAppRefusalFunding{}), ErrSubscriptionPoolUnavailable,
		"the capacity mark is bounded by its cooldown")
}

// codexReasoningThenOverloadClient answers like the ChatGPT backend overloading
// after reasoning began but before any output.
type codexReasoningThenOverloadClient struct{ calls int }

func (c *codexReasoningThenOverloadClient) Proxy(_ context.Context, _ router.Decision, _ providers.PreparedRequest, w http.ResponseWriter, _ *http.Request) error {
	c.calls++
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "event: response.created\ndata: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{\"id\":\"resp_overloaded\",\"object\":\"response\",\"status\":\"in_progress\",\"output\":[]}}\n\n")
	_, _ = io.WriteString(w, "event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"sequence_number\":1,\"output_index\":0,\"item\":{\"id\":\"rs_overloaded\",\"type\":\"reasoning\",\"summary\":[]}}\n\n")
	_, _ = io.WriteString(w, "event: response.reasoning_summary_text.delta\ndata: {\"type\":\"response.reasoning_summary_text.delta\",\"sequence_number\":2,\"item_id\":\"rs_overloaded\",\"output_index\":0,\"summary_index\":0,\"delta\":\"thinking\"}\n\n")
	_, _ = io.WriteString(w, "event: response.failed\ndata: "+codexInStreamOverloadFailure+"\n\n")
	return &providers.UpstreamErrorResponse{Status: http.StatusBadGateway, Body: []byte(codexInStreamOverloadFailure)}
}

func (c *codexReasoningThenOverloadClient) Passthrough(context.Context, providers.PreparedRequest, http.ResponseWriter, *http.Request) error {
	return nil
}

// The ChatGPT backend can overload after streaming reasoning frames. Those
// frames must not commit the turn, or the caller's own plan session sees the
// overload instead of the next scored candidate.
func TestCodexPlanOverloadAfterReasoning_RescuedBySiblingCandidate(t *testing.T) {
	primary := &codexReasoningThenOverloadClient{}
	sibling := &anthropicServingClient{}
	svc := codexOverloadService(primary, sibling, true)
	body := `{"model":"auto","stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"read main.go"}]}]}`
	rec := httptest.NewRecorder()
	ctx := context.WithValue(codexSubscriptionTestCtx(), ClientIdentityContextKey{}, ClientIdentity{ClientApp: ClientAppCodex})
	ctx = WithManagedSubscriptionUsage(ctx)
	err := svc.ProxyOpenAIResponses(ctx, []byte(body), rec, codexSubHTTPRequest("/v1/responses", body))

	require.NoError(t, err, "the sibling must serve the turn the plan model failed before output")
	assert.Greater(t, primary.calls, 1, "the overloaded binding is retried before the rescue")
	assert.Equal(t, 1, sibling.calls)
	out := rec.Body.String()
	assert.Contains(t, out, "served by sibling")
	assert.NotContains(t, out, "rs_overloaded", "no reasoning from the failed attempt reaches the client")
	assert.NotContains(t, out, "response.failed")
}
