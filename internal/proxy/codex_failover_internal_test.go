package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy/usage"
	"weave-os/router/internal/router"
)

const (
	codexTestToken     = "eyJhbGciOiJIUzI1NiJ9.codex-subscription-jwt.signature"
	codexTestAccountID = "acct_test_codex"
	codexCoveredModel  = "gpt-5.6-sol"
)

// codexSubscriptionTestCtx returns a ctx carrying the Codex subscription token +
// account id as the auth middleware stashes them, plus an installation id so
// resolveAndInjectCredentials takes the router-keyed subscription-first branch.
func codexSubscriptionTestCtx() context.Context {
	ctx := context.WithValue(context.Background(), OpenAISubscriptionContextKey{}, codexTestToken)
	ctx = context.WithValue(ctx, OpenAIAccountIDContextKey{}, codexTestAccountID)
	return context.WithValue(ctx, InstallationIDContextKey{}, "11111111-1111-1111-1111-111111111111")
}

// TestCodexFailover_EligibilityAndSuppression covers the load-bearing predicates
// of the Codex subscription failover: a Codex-served OpenAI turn is detected as
// such, a deployment OpenAI key counts as a fallback, and suppressing the
// subscription flips credential resolution off the spent ChatGPT token.
func TestCodexFailover_EligibilityAndSuppression(t *testing.T) {
	ctx := resolveAndInjectCredentials(codexSubscriptionTestCtx(), providers.ProviderOpenAI, codexCoveredModel, http.Header{})
	require.True(t, servedOnCodexSubscription(ctx),
		"a resolved ChatGPT OAuth token + account id must report servedOnCodexSubscription")

	t.Run("no fallback key: not eligible", func(t *testing.T) {
		s := &Service{} // no deployment OpenAI key, no BYOK
		assert.False(t, s.openaiFallbackKeyAvailable(ctx),
			"without a Weave/BYOK OpenAI key there is nothing to fail over to")
	})

	t.Run("deployment OpenAI key present: eligible", func(t *testing.T) {
		s := &Service{deploymentKeyedProviders: map[string]struct{}{providers.ProviderOpenAI: {}}}
		assert.True(t, s.openaiFallbackKeyAvailable(ctx),
			"a deployment OpenAI key is a valid failover target for a spent ChatGPT plan")
	})

	t.Run("suppression flips resolution off the subscription", func(t *testing.T) {
		suppressed := withSuppressedCodexSubscription(codexSubscriptionTestCtx())
		suppressed = resolveAndInjectCredentials(suppressed, providers.ProviderOpenAI, codexCoveredModel, http.Header{})
		assert.False(t, servedOnCodexSubscription(suppressed),
			"the retry must not resolve back onto the spent ChatGPT token")
		assert.False(t, servedOnSubscription(suppressed),
			"a suppressed turn is paid from Weave credits, so it must not bill at the subscription rate")
	})

	t.Run("suppression is Codex-scoped", func(t *testing.T) {
		ctx := context.WithValue(codexSubscriptionTestCtx(), AnthropicSubscriptionContextKey{}, "sk-ant-oat01-test-subscription-token")
		suppressed := resolveAndInjectCredentials(withSuppressedCodexSubscription(ctx), providers.ProviderAnthropic, "claude-opus-4-8", http.Header{})
		assert.True(t, servedOnSubscription(suppressed),
			"suppressing the Codex token must leave a Claude subscription on the same request untouched")
	})
}

// TestCodexQuotaExhaustion_Classification pins which upstream envelopes count as
// "the caller's plan is spent". Only these mark the plan exhausted for later
// turns; an unrelated 429 must not suppress a healthy subscription.
func TestCodexQuotaExhaustion_Classification(t *testing.T) {
	resetAt := time.Date(2026, 9, 9, 22, 39, 49, 0, time.UTC)

	t.Run("usage_limit_reached carries the reset instant", func(t *testing.T) {
		err := &providers.UpstreamErrorResponse{
			Status: http.StatusTooManyRequests,
			Body: []byte(`{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached",` +
				`"plan_type":"team","resets_at":` + strconv.FormatInt(resetAt.Unix(), 10) + `}}`),
		}
		got, exhausted := codexQuotaExhaustion(err)
		require.True(t, exhausted, "usage_limit_reached is the ChatGPT plan-window rejection")
		assert.Equal(t, resetAt, got, "the upstream-reported reset bounds how long the plan stays suppressed")
	})

	t.Run("insufficient_quota with no reset", func(t *testing.T) {
		err := &providers.UpstreamErrorResponse{
			Status: http.StatusTooManyRequests,
			Body:   []byte(`{"error":{"type":"insufficient_quota","message":"Your workspace is out of credits."}}`),
		}
		got, exhausted := codexQuotaExhaustion(err)
		require.True(t, exhausted, "an out-of-credits workspace can't serve the turn either")
		assert.True(t, got.IsZero(), "no resets_at means no known reset instant")
	})

	t.Run("unrelated rate limit is not exhaustion", func(t *testing.T) {
		err := &providers.UpstreamErrorResponse{
			Status: http.StatusTooManyRequests,
			Body:   []byte(`{"error":{"type":"rate_limit_exceeded","message":"Slow down"}}`),
		}
		_, exhausted := codexQuotaExhaustion(err)
		assert.False(t, exhausted,
			"a transient per-minute throttle must not mark the plan spent for the rest of the window")
	})

	t.Run("non-upstream error is not exhaustion", func(t *testing.T) {
		_, exhausted := codexQuotaExhaustion(context.DeadlineExceeded)
		assert.False(t, exhausted, "a transport error says nothing about the plan's quota")
	})
}

// TestCodexOAuthCredentialRejected covers the second retry trigger: a rejected
// or expired ChatGPT token, which the Weave key can still serve.
func TestCodexOAuthCredentialRejected(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		assert.True(t, codexOAuthCredentialRejected(&providers.UpstreamErrorResponse{Status: status}),
			"a %d from the Codex backend means the OAuth token itself was refused", status)
	}
	assert.False(t, codexOAuthCredentialRejected(&providers.UpstreamErrorResponse{Status: http.StatusBadRequest}),
		"a 400 is a request-shape problem the Weave key would hit identically")
	assert.False(t, codexOAuthCredentialRejected(context.DeadlineExceeded),
		"only buffered upstream envelopes classify as credential rejections")
}

// TestCodexQuotaExhaustion_RecordedForLaterTurns: the quota rejection carries no
// x-codex-* headers, so without recording it the observer keeps reading the plan
// as having slack and every later turn re-buys the same rejected round-trip.
func TestCodexQuotaExhaustion_RecordedForLaterTurns(t *testing.T) {
	svc := &Service{
		usageObserver:            usage.NewObserver([]byte("test-salt"), time.Hour, time.Now),
		deploymentKeyedProviders: map[string]struct{}{providers.ProviderOpenAI: {}},
	}
	ctx := codexSubscriptionTestCtx()
	headers := http.Header{}

	require.False(t, svc.codexSubscriptionExhausted(ctx, headers),
		"an unobserved plan must be tried, not pre-emptively skipped")

	svc.recordCodexQuotaExhaustion(ctx, headers, &providers.UpstreamErrorResponse{
		Status: http.StatusTooManyRequests,
		Body: []byte(`{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached",` +
			`"plan_type":"team","resets_at":` + strconv.FormatInt(time.Now().Add(3*time.Hour).Unix(), 10) + `}}`),
	})

	assert.True(t, svc.codexSubscriptionExhausted(ctx, headers),
		"after the plan reports itself spent, later turns must suppress it pre-dispatch")
}

// TestCodexExhaustionWindow_OutlivesWeeklyReset: freshFor never retains past
// WindowMinutes, so a weekly limit recorded with the 5h default would expire
// hours before the plan actually refills.
func TestCodexExhaustionWindow_OutlivesWeeklyReset(t *testing.T) {
	now := time.Now()

	weekly := codexExhaustionWindow(now.Add(6*24*time.Hour), now)
	assert.Greater(t, time.Duration(weekly.WindowMinutes)*time.Minute, 6*24*time.Hour-time.Minute,
		"a weekly reset must be retained until it actually resets")

	rolling := codexExhaustionWindow(now.Add(2*time.Hour), now)
	assert.Equal(t, codexQuotaWindowMinutes, rolling.WindowMinutes,
		"a reset inside the rolling window keeps the default length; freshFor clamps to ResetAt")
	assert.Equal(t, codexQuotaWindowMinutes, codexExhaustionWindow(time.Time{}, now).WindowMinutes,
		"a body naming no reset falls back to the rolling window")
}

// TestCodexQuotaExhaustion_SuppressionLastsUntilThePlanResets: the recorded
// window is what the observer retains the reading for, so a weekly reset must
// keep later turns off the spent plan well past the 5h rolling window, while a
// body naming no reset still lets the plan be retried after it.
func TestCodexQuotaExhaustion_SuppressionLastsUntilThePlanResets(t *testing.T) {
	spentPlan := func(resetsAt string) *providers.UpstreamErrorResponse {
		return &providers.UpstreamErrorResponse{
			Status: http.StatusTooManyRequests,
			Body: []byte(`{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached"` +
				resetsAt + `}}`),
		}
	}

	for _, tc := range []struct {
		name             string
		resetsAt         string
		stillSuppressed  bool
		suppressionAfter time.Duration
	}{
		{
			name:             "weekly reset outlives the rolling window",
			resetsAt:         `,"resets_at":` + strconv.FormatInt(time.Now().Add(6*24*time.Hour).Unix(), 10),
			stillSuppressed:  true,
			suppressionAfter: 6 * time.Hour,
		},
		{
			name:             "no reset falls back to the rolling window",
			stillSuppressed:  false,
			suppressionAfter: 6 * time.Hour,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clock := time.Now()
			svc := &Service{
				usageObserver:            usage.NewObserver([]byte("test-salt"), time.Minute, func() time.Time { return clock }),
				deploymentKeyedProviders: map[string]struct{}{providers.ProviderOpenAI: {}},
			}
			ctx, headers := codexSubscriptionTestCtx(), http.Header{}

			svc.recordCodexQuotaExhaustion(ctx, headers, spentPlan(tc.resetsAt))
			require.True(t, svc.codexSubscriptionExhausted(ctx, headers),
				"the plan the upstream just refused must be suppressed immediately")

			clock = clock.Add(tc.suppressionAfter)
			assert.Equal(t, tc.stillSuppressed, svc.codexSubscriptionExhausted(ctx, headers),
				"suppression must last exactly as long as the upstream's own reset warrants")
		})
	}
}

// codexQuotaClient answers every dispatch with the ChatGPT plan-spent envelope,
// recording whether each attempt carried the caller's OAuth subscription.
type codexQuotaClient struct{ oauthPerCall []bool }

// leakProbe appears only in the raw upstream envelope, so any renderer that
// writes the buffered body verbatim is visible in the client's bytes.
const (
	leakProbe  = "raw_envelope_leak_probe"
	leakHeader = "X-Weave-Test-Upstream-Envelope"
)

func (c *codexQuotaClient) Proxy(ctx context.Context, _ router.Decision, _ providers.PreparedRequest, _ http.ResponseWriter, _ *http.Request) error {
	creds := CredentialsFromContext(ctx)
	c.oauthPerCall = append(c.oauthPerCall, creds != nil && creds.OAuth)
	return &providers.UpstreamErrorResponse{
		Status:  http.StatusTooManyRequests,
		Headers: http.Header{leakHeader: []string{"1"}},
		Body: []byte(`{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached",` +
			`"` + leakProbe + `":true}}`),
	}
}

func (c *codexQuotaClient) Passthrough(context.Context, providers.PreparedRequest, http.ResponseWriter, *http.Request) error {
	return providers.ErrNotImplemented
}

// codexQuotaService wires a Codex-covered OpenAI route whose only client always
// reports the plan spent, with a deployment OpenAI key so the rescue is viable.
func codexQuotaService(client providers.Client) *Service {
	svc := NewService(
		staticRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: codexCoveredModel, Reason: "test"}},
		map[string]providers.Client{providers.ProviderOpenAI: client},
		nil, false, nil, nil, false, providers.ProviderOpenAI, codexCoveredModel, nil,
	).WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderOpenAI: {}})
	svc.retrySleep = noopSleep
	return svc
}

// codexSubHTTPRequest builds a request carrying the ChatGPT subscription the way
// the client presents it: an OAuth bearer plus the account id.
func codexSubHTTPRequest(path, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+codexTestToken)
	req.Header.Set("ChatGPT-Account-ID", codexTestAccountID)
	return req
}

// TestCodexRescue_FailedRetryRendersIntoTheLiveStream: the rescue runs whenever
// the prelude is not COMMITTED, which still allows the buffered 200 + routing
// marker to be on the wire. A failed rescue must therefore render the way the
// primary dispatch would — an SSE frame — instead of appending a JSON envelope
// to what the client is parsing as a stream.
func TestCodexRescue_FailedRetryRendersIntoTheLiveStream(t *testing.T) {
	client := &codexQuotaClient{}
	svc := codexQuotaService(client)

	body := `{"model":"auto","stream":true,"messages":[{"role":"user","content":"read main.go"}]}`
	rec := httptest.NewRecorder()
	err := svc.ProxyOpenAIChatCompletion(context.Background(), []byte(body), rec, codexSubHTTPRequest("/v1/chat/completions", body))
	require.Error(t, err)

	require.GreaterOrEqual(t, len(client.oauthPerCall), 2, "the spent plan must be rescued on the Weave key")
	assert.True(t, client.oauthPerCall[0], "the primary dispatch serves on the caller's ChatGPT plan")
	assert.False(t, client.oauthPerCall[len(client.oauthPerCall)-1], "the rescue must dispatch on the Weave key")

	out := rec.Body.String()
	require.Contains(t, out, "data: ", "the marker prelude must already be on the wire for this to be a stream")
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		require.True(t, strings.HasPrefix(line, "data: ") || strings.HasPrefix(line, ":") || strings.HasPrefix(line, "event:"),
			"a failed rescue must stay in SSE framing, got a bare line: %s", line)
	}
}

// TestCodexRescue_FailedRetryWritesNothingIntoACommittedResponsesStream: on the
// Responses ingress the prelude is released before dispatch, so the turn is
// already an open Responses stream. A failed rescue must write nothing there —
// a chat-shaped envelope is not something a Responses client can parse.
func TestCodexRescue_FailedRetryWritesNothingIntoACommittedResponsesStream(t *testing.T) {
	client := &codexQuotaClient{}
	svc := codexQuotaService(client)

	body := `{"model":"auto","stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"read main.go"}]}]}`
	rec := httptest.NewRecorder()
	ctx := context.WithValue(context.Background(), ClientIdentityContextKey{}, ClientIdentity{ClientApp: ClientAppCodex})
	err := svc.ProxyOpenAIResponses(ctx, []byte(body), rec, codexSubHTTPRequest("/v1/responses", body))
	require.Error(t, err)

	require.GreaterOrEqual(t, len(client.oauthPerCall), 2, "the spent plan must be rescued on the Weave key")
	assert.NotContains(t, rec.Body.String(), leakProbe,
		"the upstream envelope must not be written into an open Responses stream")
	assert.Empty(t, rec.Header().Get(leakHeader),
		"a failed rescue must not replay the upstream error response onto a committed Responses stream")
}

// TestCodexSubscriptionExhausted_NoFallbackKey: suppressing the only OpenAI
// credential the turn has would leave it unable to dispatch at all, which is
// strictly worse than letting the subscription answer.
func TestCodexSubscriptionExhausted_NoFallbackKey(t *testing.T) {
	svc := &Service{usageObserver: usage.NewObserver([]byte("test-salt"), time.Hour, time.Now)}
	ctx := codexSubscriptionTestCtx()

	svc.recordCodexQuotaExhaustion(ctx, http.Header{}, &providers.UpstreamErrorResponse{
		Status: http.StatusTooManyRequests,
		Body:   []byte(`{"error":{"type":"usage_limit_reached","resets_at":` + strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10) + `}}`),
	})

	assert.False(t, svc.codexSubscriptionExhausted(ctx, http.Header{}),
		"with no Weave/BYOK OpenAI key there is nothing to suppress the subscription in favor of")
}
