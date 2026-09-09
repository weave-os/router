package proxy

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy/usage"
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
