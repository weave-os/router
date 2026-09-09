package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/observability/otel"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy/usage"
	"weave-os/router/internal/router"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Characterization/parity suite for the two independently built
// subscription-failover implementations: Anthropic (ProxyMessages +
// usage_bypass.go) and Codex (ProxyOpenAIChatCompletion + codex_failover.go).
// Every stage is asserted with the provider as an explicit table axis so a
// later unification behind one provider policy shows any divergence as a
// failing row rather than as a silent behavior change.
//
// Rows that would pin a known defect assert the CORRECT behavior and are
// skipped with the bug id from the audit; they turn green when that bug is
// fixed rather than having to be rewritten.

const (
	parityAnthropicToken  = "sk-ant-oat01-parity-subscription-token"
	parityAnthropicBYOK   = "sk-ant-api-parity-byok"
	parityAnthropicModel  = "claude-sonnet-4-6"
	parityCodexToken      = "eyJhbGciOiJIUzI1NiJ9.parity-codex-jwt.signature"
	parityCodexAccountID  = "acct_parity_codex"
	parityCodexBYOK       = "sk-oai-parity-byok"
	parityCodexModel      = "gpt-5.6-sol"
	parityUncoveredOpenAI = "gpt-5.4-nano"
	parityInstallationID  = "22222222-2222-2222-2222-222222222222"
)

// parityIngress describes one ingress' subscription-failover surface: the
// credential it recognizes, the predicates that gate the rescue, and how to
// drive the ingress end to end.
type parityIngress struct {
	name     string
	provider string
	// model is covered by the subscription; uncoveredModel is not (Codex
	// gates on model coverage, Anthropic does not — D2).
	model          string
	uncoveredModel string
	token          string
	byokKey        string

	// subCtx carries the caller's subscription as the auth middleware would.
	subCtx func() context.Context
	// servedOnSub is the ingress' "is this turn on a subscription?" predicate.
	servedOnSub func(context.Context) bool
	// fallbackAvailable is the paid-fallback probe.
	fallbackAvailable func(*Service, context.Context) bool
	// exhausted is the pre-dispatch suppression predicate.
	exhausted func(*Service, context.Context, http.Header) bool
	// suppress is the provider-scoped suppression key.
	suppress func(context.Context) context.Context
	// tokenRejected is the "the OAuth credential itself is bad" classifier.
	tokenRejected func(error) bool

	// requestBody builds an ingress-native request body.
	requestBody func(stream bool) string
	// upstreamOK is an upstream success payload the ingress' translator accepts.
	upstreamOK func(stream bool) string
	// call drives the ingress.
	call func(*Service, context.Context, []byte, http.ResponseWriter, *http.Request) error
	path string
}

func parityAnthropicIngress() parityIngress {
	return parityIngress{
		name:           "anthropic",
		provider:       providers.ProviderAnthropic,
		model:          parityAnthropicModel,
		uncoveredModel: "claude-haiku-4-5",
		token:          parityAnthropicToken,
		byokKey:        parityAnthropicBYOK,
		subCtx: func() context.Context {
			ctx := context.WithValue(context.Background(), AnthropicSubscriptionContextKey{}, parityAnthropicToken)
			return context.WithValue(ctx, InstallationIDContextKey{}, parityInstallationID)
		},
		servedOnSub:       servedOnSubscription,
		fallbackAvailable: (*Service).anthropicFallbackKeyAvailable,
		exhausted:         (*Service).claudeSubscriptionExhausted,
		suppress:          withSuppressedClaudeSubscription,
		tokenRejected:     anthropicOAuthCredentialRejected,
		// A tool-bearing main-loop turn: a bare one-liner is hard-pinned as a
		// classifier turn, which short-circuits the routing stages under test.
		requestBody: func(stream bool) string {
			return `{"model":"` + parityAnthropicModel + `","max_tokens":4096,"stream":` + boolLit(stream) +
				`,"tools":[{"name":"Bash","description":"run a command","input_schema":{"type":"object","properties":{}}}]` +
				`,"messages":[{"role":"user","content":"investigate the failing dispatch and report back"}]}`
		},
		upstreamOK: func(stream bool) string {
			if stream {
				return "event: message_start\n" +
					`data: {"type":"message_start","message":{"usage":{"input_tokens":5,"output_tokens":1}}}` + "\n\n" +
					"event: message_delta\n" +
					`data: {"type":"message_delta","usage":{"output_tokens":2}}` + "\n\n"
			}
			return `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"hi"}],` +
				`"model":"` + parityAnthropicModel + `","stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":2}}`
		},
		call: (*Service).ProxyMessages,
		path: "/v1/messages",
	}
}

func parityCodexIngress() parityIngress {
	return parityIngress{
		name:           "codex",
		provider:       providers.ProviderOpenAI,
		model:          parityCodexModel,
		uncoveredModel: parityUncoveredOpenAI,
		token:          parityCodexToken,
		byokKey:        parityCodexBYOK,
		subCtx: func() context.Context {
			ctx := context.WithValue(context.Background(), OpenAISubscriptionContextKey{}, parityCodexToken)
			ctx = context.WithValue(ctx, OpenAIAccountIDContextKey{}, parityCodexAccountID)
			return context.WithValue(ctx, InstallationIDContextKey{}, parityInstallationID)
		},
		servedOnSub:       servedOnCodexSubscription,
		fallbackAvailable: (*Service).openaiFallbackKeyAvailable,
		exhausted:         (*Service).codexSubscriptionExhausted,
		suppress:          withSuppressedCodexSubscription,
		tokenRejected:     codexOAuthCredentialRejected,
		requestBody: func(stream bool) string {
			return `{"model":"` + parityCodexModel + `","max_tokens":4096,"stream":` + boolLit(stream) +
				`,"tools":[{"type":"function","function":{"name":"bash","description":"run a command","parameters":{"type":"object","properties":{}}}}]` +
				`,"messages":[{"role":"user","content":"investigate the failing dispatch and report back"}]}`
		},
		upstreamOK: func(bool) string {
			return "data: " + `{"type":"response.output_text.delta","output_index":0,"delta":"hi"}` + "\n\n" +
				"data: " + `{"type":"response.completed","response":{"id":"resp_1","status":"completed","usage":{"input_tokens":5,"output_tokens":2}}}` + "\n\n"
		},
		call: (*Service).ProxyOpenAIChatCompletion,
		path: "/v1/chat/completions",
	}
}

func boolLit(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func parityIngresses() []parityIngress {
	return []parityIngress{parityAnthropicIngress(), parityCodexIngress()}
}

// byokCtx adds a BYOK key for the ingress' provider.
func (in parityIngress) byokCtx(ctx context.Context) context.Context {
	return context.WithValue(ctx, ExternalAPIKeysContextKey{}, []*auth.ExternalAPIKey{
		{Provider: in.provider, Plaintext: []byte(in.byokKey)},
	})
}

// resolved resolves credentials for the ingress' covered model, as the ingress
// does before dispatching.
func (in parityIngress) resolved(ctx context.Context) context.Context {
	return resolveAndInjectCredentials(ctx, in.provider, in.model, http.Header{})
}

// deploymentKeyedService builds a Service with the ingress' provider wired to a
// deployment key — the paid fallback a rescue spends.
func (in parityIngress) deploymentKeyedService() *Service {
	return &Service{deploymentKeyedProviders: map[string]struct{}{in.provider: {}}}
}

// parityObserver seeds a usage observer that already read the token's plan spent.
func parityObserver(token string) *usage.Observer {
	now := time.Unix(1_700_000_000, 0)
	o := usage.NewObserver([]byte("parity-salt"), time.Hour, func() time.Time { return now })
	o.Record(o.Key([]byte(token)), usage.Snapshot{Secondary: usage.Window{UsedPercent: 1, WindowMinutes: 10080}})
	return o
}

// TestSubscriptionFailoverParity_Eligibility pins the three predicates that
// decide whether a turn is a subscription-failover candidate at all, for both
// ingresses side by side: subscription-served, paid-fallback availability, and
// subscription-only mode.
func TestSubscriptionFailoverParity_Eligibility(t *testing.T) {
	for _, in := range parityIngresses() {
		t.Run(in.name, func(t *testing.T) {
			t.Run("resolved subscription is subscription-served", func(t *testing.T) {
				assert.True(t, in.servedOnSub(in.resolved(in.subCtx())),
					"a resolved subscription credential must report the turn as subscription-served")
			})

			t.Run("a paid key alone is not subscription-served", func(t *testing.T) {
				ctx := in.resolved(in.byokCtx(context.Background()))
				require.NotNil(t, CredentialsFromContext(ctx), "the BYOK key must resolve")
				assert.False(t, in.servedOnSub(ctx),
					"a BYOK/paid key must never be mistaken for the caller's subscription")
			})

			t.Run("no fallback key: not eligible", func(t *testing.T) {
				assert.False(t, in.fallbackAvailable(&Service{}, in.subCtx()),
					"with no BYOK and no deployment key there is nothing to fail over to")
			})

			t.Run("BYOK key is a fallback", func(t *testing.T) {
				assert.True(t, in.fallbackAvailable(&Service{}, in.byokCtx(in.subCtx())),
					"a per-request BYOK key can serve the turn the subscription cannot")
			})

			t.Run("deployment key is a fallback", func(t *testing.T) {
				assert.True(t, in.fallbackAvailable(in.deploymentKeyedService(), in.subCtx()),
					"the deployment's own provider key can serve the turn the subscription cannot")
			})

			t.Run("another provider's key is not a fallback", func(t *testing.T) {
				other := providers.ProviderOpenRouter
				svc := &Service{deploymentKeyedProviders: map[string]struct{}{other: {}}}
				assert.False(t, in.fallbackAvailable(svc, in.subCtx()),
					"the fallback probe is provider-scoped; a key for another provider cannot serve this turn")
			})
		})
	}
}

// TestSubscriptionFailoverParity_SubscriptionShape pins the credential shapes
// each ingress accepts as a subscription. The account-id requirement is
// provider-essential (D1): the ChatGPT backend rejects the bearer without it.
func TestSubscriptionFailoverParity_SubscriptionShape(t *testing.T) {
	t.Run("anthropic: OAuth alone is enough", func(t *testing.T) {
		in := parityAnthropicIngress()
		ctx := in.resolved(in.subCtx())
		creds := CredentialsFromContext(ctx)
		require.NotNil(t, creds)
		assert.True(t, creds.OAuth)
		assert.Empty(t, creds.AccountID, "the Claude subscription carries no account id")
		assert.True(t, servedOnSubscription(ctx))
	})

	t.Run("codex: OAuth without an account id is not Codex-served", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), InstallationIDContextKey{}, parityInstallationID)
		ctx = context.WithValue(ctx, AnthropicSubscriptionContextKey{}, parityAnthropicToken)
		ctx = resolveAndInjectCredentials(ctx, providers.ProviderAnthropic, parityAnthropicModel, http.Header{})
		assert.True(t, servedOnSubscription(ctx))
		assert.False(t, servedOnCodexSubscription(ctx),
			"an OAuth credential with no ChatGPT account id must not gate the Codex rescue (D1)")
	})

	t.Run("codex: OAuth plus account id is Codex-served", func(t *testing.T) {
		in := parityCodexIngress()
		ctx := in.resolved(in.subCtx())
		creds := CredentialsFromContext(ctx)
		require.NotNil(t, creds)
		assert.True(t, creds.OAuth)
		assert.Equal(t, []byte(parityCodexAccountID), creds.AccountID)
		assert.True(t, servedOnCodexSubscription(ctx))
	})
}

// TestSubscriptionFailoverParity_ModelCoverage pins D2: the Codex subscription
// covers only the Codex model family, and an uncovered model never resolves to
// the subscription token; the Claude subscription has no resolution-time
// coverage gate.
func TestSubscriptionFailoverParity_ModelCoverage(t *testing.T) {
	t.Run("codex uncovered model resolves to the paid key", func(t *testing.T) {
		in := parityCodexIngress()
		ctx := resolveAndInjectCredentials(in.byokCtx(in.subCtx()), in.provider, in.uncoveredModel, http.Header{})
		creds := CredentialsFromContext(ctx)
		require.NotNil(t, creds)
		assert.Equal(t, []byte(parityCodexBYOK), creds.APIKey,
			"a model outside the Codex family must serve on infrastructure credentials")
		assert.False(t, servedOnCodexSubscription(ctx))
	})

	t.Run("anthropic has no coverage gate", func(t *testing.T) {
		in := parityAnthropicIngress()
		ctx := resolveAndInjectCredentials(in.byokCtx(in.subCtx()), in.provider, in.uncoveredModel, http.Header{})
		require.NotNil(t, CredentialsFromContext(ctx))
		assert.True(t, servedOnSubscription(ctx),
			"any Anthropic model the caller can reach serves on the Claude subscription (D2)")
	})
}

// TestSubscriptionFailoverParity_PreDispatchSuppression pins the observer-driven
// pre-dispatch predicate on both ingresses: it needs an observer, a present
// token, an exhausted reading, AND a paid key — the last condition is what stops
// suppression from stranding a turn with no credential.
func TestSubscriptionFailoverParity_PreDispatchSuppression(t *testing.T) {
	for _, in := range parityIngresses() {
		t.Run(in.name, func(t *testing.T) {
			withObserver := func(svc *Service) *Service {
				svc.usageObserver = parityObserver(in.token)
				return svc
			}

			t.Run("observer + token + exhausted reading + fallback key: suppress", func(t *testing.T) {
				assert.True(t, in.exhausted(withObserver(in.deploymentKeyedService()), in.subCtx(), http.Header{}),
					"a plan the observer already read spent must be suppressed before it is dispatched again")
			})

			t.Run("no observer: never suppress", func(t *testing.T) {
				assert.False(t, in.exhausted(in.deploymentKeyedService(), in.subCtx(), http.Header{}),
					"without an observer nothing is known about the plan window")
			})

			t.Run("no fallback key: never suppress", func(t *testing.T) {
				assert.False(t, in.exhausted(withObserver(&Service{}), in.subCtx(), http.Header{}),
					"suppressing with no paid key would leave the turn with no credential at all")
			})

			t.Run("token absent from the request: never suppress", func(t *testing.T) {
				assert.False(t, in.exhausted(withObserver(in.deploymentKeyedService()), context.Background(), http.Header{}),
					"the reading is keyed by the caller's own token; another caller's turn must be unaffected")
			})

			t.Run("observer reads slack: never suppress", func(t *testing.T) {
				svc := in.deploymentKeyedService()
				now := time.Unix(1_700_000_000, 0)
				o := usage.NewObserver([]byte("parity-salt"), time.Hour, func() time.Time { return now })
				o.Record(o.Key([]byte(in.token)), usage.Snapshot{Primary: usage.Window{UsedPercent: 0.2, WindowMinutes: 300}})
				svc.usageObserver = o
				assert.False(t, in.exhausted(svc, in.subCtx(), http.Header{}),
					"a plan with quota left must keep serving on the subscription")
			})

			t.Run("suppression still leaves the turn a credential", func(t *testing.T) {
				// BYOK present: resolution must land on the paid key.
				byok := resolveAndInjectCredentials(in.suppress(in.byokCtx(in.subCtx())), in.provider, in.model, http.Header{})
				creds := CredentialsFromContext(byok)
				require.NotNil(t, creds, "suppression must fall through to the BYOK key, not strand the turn")
				assert.Equal(t, []byte(in.byokKey), creds.APIKey)
				assert.False(t, in.servedOnSub(byok), "the rescued turn bills at full cost, not the subscription rate")

				// Deployment key only: no credential is set, which is how the
				// provider adapter is told to use the deployment key.
				deployment := resolveAndInjectCredentials(in.suppress(in.subCtx()), in.provider, in.model, http.Header{})
				assert.Nil(t, CredentialsFromContext(deployment),
					"with no BYOK the suppressed turn resolves to no credential so the deployment key serves it")

				svc := in.deploymentKeyedService()
				bindings := svc.resolveBindingsForDispatch(deployment, router.Decision{Provider: in.provider, Model: in.model})
				assert.NotEmpty(t, bindings, "the suppressed turn must still have a binding to dispatch on")
			})
		})
	}
}

// TestSubscriptionFailoverParity_SuppressionIsProviderScoped pins that each
// ingress' suppression key only disables its own provider's subscription: a
// spent Claude plan must not knock out a healthy Codex plan on the same turn,
// and vice versa.
func TestSubscriptionFailoverParity_SuppressionIsProviderScoped(t *testing.T) {
	bothSubs := func() context.Context {
		ctx := context.WithValue(context.Background(), InstallationIDContextKey{}, parityInstallationID)
		ctx = context.WithValue(ctx, AnthropicSubscriptionContextKey{}, parityAnthropicToken)
		ctx = context.WithValue(ctx, OpenAISubscriptionContextKey{}, parityCodexToken)
		return context.WithValue(ctx, OpenAIAccountIDContextKey{}, parityCodexAccountID)
	}

	t.Run("suppressing Claude leaves Codex intact", func(t *testing.T) {
		ctx := withSuppressedClaudeSubscription(bothSubs())
		assert.True(t, claudeSubscriptionSuppressed(ctx))
		assert.False(t, codexSubscriptionSuppressed(ctx))

		anthropic := resolveAndInjectCredentials(ctx, providers.ProviderAnthropic, parityAnthropicModel, http.Header{})
		assert.Nil(t, CredentialsFromContext(anthropic), "the suppressed Claude subscription must not resolve")

		codex := resolveAndInjectCredentials(ctx, providers.ProviderOpenAI, parityCodexModel, http.Header{})
		creds := CredentialsFromContext(codex)
		require.NotNil(t, creds, "the untouched Codex subscription must still resolve")
		assert.True(t, servedOnCodexSubscription(codex))
	})

	t.Run("suppressing Codex leaves Claude intact", func(t *testing.T) {
		ctx := withSuppressedCodexSubscription(bothSubs())
		assert.True(t, codexSubscriptionSuppressed(ctx))
		assert.False(t, claudeSubscriptionSuppressed(ctx))

		codex := resolveAndInjectCredentials(ctx, providers.ProviderOpenAI, parityCodexModel, http.Header{})
		assert.Nil(t, CredentialsFromContext(codex), "the suppressed Codex subscription must not resolve")

		anthropic := resolveAndInjectCredentials(ctx, providers.ProviderAnthropic, parityAnthropicModel, http.Header{})
		creds := CredentialsFromContext(anthropic)
		require.NotNil(t, creds, "the untouched Claude subscription must still resolve")
		assert.True(t, servedOnSubscription(anthropic))
	})
}

// upstreamErr builds a buffered upstream error, the shape both classifiers read.
func upstreamErr(status int, body string) error {
	return &providers.UpstreamErrorResponse{Status: status, Body: []byte(body)}
}

// TestSubscriptionFailoverParity_ErrorClassification pins, per provider, which
// upstream failure counts as "token rejected", which as generic-retryable, and
// which stays terminal. The rescue fires on (retryable OR token-rejected), so
// the two columns together are the rescue trigger.
func TestSubscriptionFailoverParity_ErrorClassification(t *testing.T) {
	type row struct {
		name string
		err  error
		// wantRejected/wantRetryable per ingress name.
		wantRejected  map[string]bool
		wantRetryable bool
		// bug names the audit defect when the expectation below is the
		// CORRECT behavior rather than today's behavior.
		bug map[string]string
	}

	rows := []row{
		{
			name:          "401 authentication_error is a rejected token on both",
			err:           upstreamErr(http.StatusUnauthorized, `{"error":{"type":"authentication_error","message":"expired"}}`),
			wantRejected:  map[string]bool{"anthropic": true, "codex": true},
			wantRetryable: false,
		},
		{
			name:          "403 permission_error is a rejected token on both",
			err:           upstreamErr(http.StatusForbidden, `{"error":{"type":"permission_error","message":"OAuth not allowed"}}`),
			wantRejected:  map[string]bool{"anthropic": true, "codex": true},
			wantRetryable: false,
		},
		{
			name: "403 that is not a credential problem stays terminal",
			err:  upstreamErr(http.StatusForbidden, `{"error":{"type":"invalid_request_error","message":"model not allowed for this account"}}`),
			// Spending a paid key on a request that fails identically is
			// strictly worse than surfacing the 403.
			wantRejected:  map[string]bool{"anthropic": false, "codex": false},
			wantRetryable: false,
			bug:           map[string]string{"codex": "B5: codexOAuthCredentialRejected accepts any 401/403, so a policy/entitlement 403 spends the paid key"},
		},
		{
			name:          "429 plan throttle is generic-retryable, not a rejected token",
			err:           upstreamErr(http.StatusTooManyRequests, `{"error":{"type":"rate_limit_error","message":"slow down"}}`),
			wantRejected:  map[string]bool{"anthropic": false, "codex": false},
			wantRetryable: true,
		},
		{
			name:          "503 overload is generic-retryable on both",
			err:           upstreamErr(http.StatusServiceUnavailable, `{"error":{"type":"overloaded_error"}}`),
			wantRejected:  map[string]bool{"anthropic": false, "codex": false},
			wantRetryable: true,
		},
		{
			name:          "400 stays terminal on both",
			err:           upstreamErr(http.StatusBadRequest, `{"error":{"type":"invalid_request_error"}}`),
			wantRejected:  map[string]bool{"anthropic": false, "codex": false},
			wantRetryable: false,
		},
		{
			name:          "an unbuffered transport error is retryable but never a rejected token",
			err:           &providers.UpstreamStatusError{Status: http.StatusUnauthorized},
			wantRejected:  map[string]bool{"anthropic": false, "codex": false},
			wantRetryable: false,
		},
	}

	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.wantRetryable, providers.IsRetryable(tc.err),
				"the generic-retryable arm of the rescue trigger is shared by both ingresses")
			for _, in := range parityIngresses() {
				t.Run(in.name, func(t *testing.T) {
					if bug := tc.bug[in.name]; bug != "" {
						t.Skip(bug)
					}
					assert.Equal(t, tc.wantRejected[in.name], in.tokenRejected(tc.err))
				})
			}
		})
	}
}

// TestSubscriptionFailoverParity_PlanSpentSignal pins D4: "the plan is spent" is
// an error-body signal on Codex and a header-only signal on Anthropic.
func TestSubscriptionFailoverParity_PlanSpentSignal(t *testing.T) {
	resetAt := time.Now().Add(6 * 24 * time.Hour).Truncate(time.Second)

	t.Run("codex reads usage_limit_reached and its reset instant from the body", func(t *testing.T) {
		got, spent := codexQuotaExhaustion(upstreamErr(http.StatusTooManyRequests,
			`{"error":{"type":"usage_limit_reached","resets_at":`+strconv.FormatInt(resetAt.Unix(), 10)+`}}`))
		require.True(t, spent)
		assert.Equal(t, resetAt.UTC(), got)
	})

	t.Run("codex reads insufficient_quota without a reset instant", func(t *testing.T) {
		got, spent := codexQuotaExhaustion(upstreamErr(http.StatusTooManyRequests, `{"error":{"type":"insufficient_quota"}}`))
		require.True(t, spent)
		assert.True(t, got.IsZero(), "no resets_at means the window length must fall back to the plan default")
	})

	t.Run("codex treats a plain throttle as not-spent", func(t *testing.T) {
		_, spent := codexQuotaExhaustion(upstreamErr(http.StatusTooManyRequests, `{"error":{"type":"rate_limit_error"}}`))
		assert.False(t, spent, "a transient throttle must not mark the plan spent for the rest of its window")
	})

	t.Run("anthropic learns spent-ness from an error body", func(t *testing.T) {
		t.Skip("B4: Anthropic has no error-path exhaustion signal; a 429 without unified headers teaches the observer nothing")
	})
}

// TestSubscriptionFailoverParity_ObserverWrites pins what each ingress writes to
// the usage observer when the upstream says the plan is spent, and the retention
// of that reading.
func TestSubscriptionFailoverParity_ObserverWrites(t *testing.T) {
	t.Run("codex records the rejection so later turns suppress pre-dispatch", func(t *testing.T) {
		in := parityCodexIngress()
		svc := in.deploymentKeyedService()
		svc.usageObserver = usage.NewObserver([]byte("parity-salt"), time.Hour, time.Now)
		ctx := in.subCtx()

		require.False(t, in.exhausted(svc, ctx, http.Header{}), "nothing is known before the rejection")
		svc.recordCodexQuotaExhaustion(ctx, http.Header{}, upstreamErr(http.StatusTooManyRequests,
			`{"error":{"type":"usage_limit_reached","resets_at":`+strconv.FormatInt(time.Now().Add(2*time.Hour).Unix(), 10)+`}}`))
		assert.True(t, in.exhausted(svc, ctx, http.Header{}),
			"the next turn must suppress the spent plan instead of buying another rejected round-trip")
	})

	t.Run("codex records nothing for a non-quota failure", func(t *testing.T) {
		in := parityCodexIngress()
		svc := in.deploymentKeyedService()
		svc.usageObserver = usage.NewObserver([]byte("parity-salt"), time.Hour, time.Now)
		svc.recordCodexQuotaExhaustion(in.subCtx(), http.Header{}, upstreamErr(http.StatusServiceUnavailable, `{"error":{"type":"overloaded_error"}}`))
		assert.False(t, in.exhausted(svc, in.subCtx(), http.Header{}),
			"a transient overload must not suppress the subscription for the rest of the window")
	})

	t.Run("codex window outlives a reset that is days away", func(t *testing.T) {
		now := time.Unix(1_700_000_000, 0).UTC()
		weekly := codexExhaustionWindow(now.Add(6*24*time.Hour), now)
		assert.Greater(t, weekly.WindowMinutes, codexQuotaWindowMinutes,
			"a weekly reset recorded in a 5-hour window would expire early and re-buy the rejection (B2)")
		assert.Equal(t, now.Add(6*24*time.Hour), weekly.ResetAt)

		short := codexExhaustionWindow(time.Time{}, now)
		assert.Equal(t, codexQuotaWindowMinutes, short.WindowMinutes,
			"with no upstream reset the plan default window applies")
		assert.True(t, usage.Snapshot{Primary: short}.Exhausted())
	})

	t.Run("anthropic writes the same rejection to the observer", func(t *testing.T) {
		t.Skip("B4/D11: the Anthropic ingress never records exhaustion from the error path; only response headers teach the observer")
	})
}

// parityUpstream answers each dispatch according to the credential it carries,
// not to a call index: the subscription attempt and the paid rescue are scripted
// independently, so the assertions stay meaningful regardless of how many
// same-binding retries the dispatcher interposes between them.
type parityUpstream struct {
	// subErr fails every dispatch that carries the caller's subscription.
	subErr error
	// paidErr fails every dispatch that serves on a paid key.
	paidErr error
	// okBody is written by a dispatch scripted to succeed.
	okBody string
	// preErrBody is written before returning an error: provider bytes on the
	// wire commit the attempt.
	preErrBody string

	subDispatches  int
	paidDispatches int
}

func (p *parityUpstream) Proxy(ctx context.Context, _ router.Decision, _ providers.PreparedRequest, w http.ResponseWriter, _ *http.Request) error {
	// No credential in context means the provider adapter falls back to the
	// deployment key, which is a paid dispatch.
	creds := CredentialsFromContext(ctx)
	onSubscription := creds != nil && creds.OAuth
	err := p.paidErr
	if onSubscription {
		p.subDispatches++
		err = p.subErr
	} else {
		p.paidDispatches++
	}
	if err != nil {
		if p.preErrBody != "" {
			_, _ = io.WriteString(w, p.preErrBody)
		}
		return err
	}
	_, _ = io.WriteString(w, p.okBody)
	return nil
}

func (p *parityUpstream) Passthrough(context.Context, providers.PreparedRequest, http.ResponseWriter, *http.Request) error {
	return providers.ErrNotImplemented
}

// parityService wires the ingress' provider to upstream with a deployment key,
// which is the paid fallback the rescue spends.
func (in parityIngress) parityService(upstream providers.Client) *Service {
	svc := NewService(
		staticRouter{decision: router.Decision{Provider: in.provider, Model: in.model, Reason: "test"}},
		map[string]providers.Client{in.provider: upstream},
		nil, false, nil, nil, false, in.provider, in.model, nil,
	).WithDeploymentKeyedProviders(map[string]struct{}{in.provider: {}})
	svc.retrySleep = noopSleep
	return svc
}

func (in parityIngress) request(t *testing.T, stream bool) (*httptest.ResponseRecorder, *http.Request, []byte) {
	t.Helper()
	body := in.requestBody(stream)
	return httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, in.path, strings.NewReader(body)), []byte(body)
}

// TestSubscriptionFailoverParity_RescueDispatch pins the rescue itself on both
// ingresses: a subscription-served turn whose subscription fails pre-commit is
// re-dispatched once, on the paid key, for the same model.
func TestSubscriptionFailoverParity_RescueDispatch(t *testing.T) {
	triggers := []struct {
		name string
		err  error
	}{
		{name: "retryable fault", err: upstreamErr(http.StatusServiceUnavailable, `{"error":{"type":"overloaded_error"}}`)},
		{name: "rejected token", err: upstreamErr(http.StatusUnauthorized, `{"error":{"type":"authentication_error"}}`)},
	}
	for _, in := range parityIngresses() {
		for _, trigger := range triggers {
			t.Run(in.name+"/"+trigger.name, func(t *testing.T) {
				upstream := &parityUpstream{subErr: trigger.err, okBody: in.upstreamOK(false)}
				svc := in.parityService(upstream)
				rec, req, body := in.request(t, false)

				require.NoError(t, in.call(svc, in.subCtx(), body, rec, req))

				assert.Positive(t, upstream.subDispatches, "the turn must first be attempted on the caller's subscription")
				assert.Equal(t, 1, upstream.paidDispatches,
					"a subscription failure the client cannot see yet must be rescued once on the paid key")
			})
		}

		t.Run(in.name+"/terminal error is not rescued", func(t *testing.T) {
			upstream := &parityUpstream{
				subErr: upstreamErr(http.StatusBadRequest, `{"error":{"type":"invalid_request_error"}}`),
				okBody: in.upstreamOK(false),
			}
			svc := in.parityService(upstream)
			rec, req, body := in.request(t, false)

			require.Error(t, in.call(svc, in.subCtx(), body, rec, req))
			assert.Zero(t, upstream.paidDispatches,
				"a request the upstream refuses on its merits would fail identically on the paid key")
		})

		t.Run(in.name+"/no paid key: no rescue", func(t *testing.T) {
			upstream := &parityUpstream{
				subErr: upstreamErr(http.StatusServiceUnavailable, `{"error":{"type":"overloaded_error"}}`),
				okBody: in.upstreamOK(false),
			}
			svc := NewService(
				staticRouter{decision: router.Decision{Provider: in.provider, Model: in.model, Reason: "test"}},
				map[string]providers.Client{in.provider: upstream},
				nil, false, nil, nil, false, in.provider, in.model, nil,
			)
			svc.retrySleep = noopSleep
			rec, req, body := in.request(t, false)

			require.Error(t, in.call(svc, in.subCtx(), body, rec, req))
			assert.Zero(t, upstream.paidDispatches,
				"with no paid key there is nothing to rescue onto; the upstream error must surface raw")
		})

		t.Run(in.name+"/subscription-only mode forbids the paid rescue", func(t *testing.T) {
			upstream := &parityUpstream{
				subErr: upstreamErr(http.StatusServiceUnavailable, `{"error":{"type":"overloaded_error"}}`),
				okBody: in.upstreamOK(false),
			}
			svc := in.parityService(upstream)
			rec, req, body := in.request(t, false)

			require.Error(t, in.call(svc, billing.WithSubscriptionOnly(in.subCtx()), body, rec, req))
			assert.Zero(t, upstream.paidDispatches,
				"subscription-only mode exists to forbid exactly this paid spend")
		})
	}
}

// TestSubscriptionFailoverParity_BlindExperimentPassthrough pins D9: the Codex
// rescue is gated out for a blind-experiment passthrough turn, the Anthropic one
// is not. A rescue changes the credential, not the model, so the two ingresses
// disagree about whether that invalidates an experiment arm.
func TestSubscriptionFailoverParity_BlindExperimentPassthrough(t *testing.T) {
	wantPaid := map[string]int{"anthropic": 1, "codex": 0}
	for _, in := range parityIngresses() {
		t.Run(in.name, func(t *testing.T) {
			upstream := &parityUpstream{
				subErr: upstreamErr(http.StatusServiceUnavailable, `{"error":{"type":"overloaded_error"}}`),
				okBody: in.upstreamOK(false),
			}
			svc := in.parityService(upstream)
			ctx := context.WithValue(in.subCtx(), auth.BlindExperimentContextKey{}, auth.BlindExperimentState{
				Active:              true,
				Arm:                 auth.BlindExperimentArmPassthrough,
				AssignmentSource:    auth.BlindExperimentAssignmentAutomatic,
				CanonicalSubjectKey: "account-parity",
			})
			rec, req, body := in.request(t, false)

			_ = in.call(svc, ctx, body, rec, req)
			assert.Equal(t, wantPaid[in.name], upstream.paidDispatches,
				"blind-experiment passthrough gates the Codex rescue and not the Anthropic one (D9)")
		})
	}
}

// TestSubscriptionFailoverParity_Telemetry pins the four signals both ingresses
// emit for a rescue, on the shared router.upstream span: whether a subscription
// paid, whether a failover happened, and whether it was the subscription rescue.
func TestSubscriptionFailoverParity_Telemetry(t *testing.T) {
	cases := []struct {
		name string
		// rescueSucceeds scripts the second dispatch.
		rescueSucceeds bool
	}{
		{name: "rescue succeeds", rescueSucceeds: true},
		{name: "rescue fails", rescueSucceeds: false},
	}
	for _, in := range parityIngresses() {
		for _, tc := range cases {
			t.Run(in.name+"/"+tc.name, func(t *testing.T) {
				fault := upstreamErr(http.StatusServiceUnavailable, `{"error":{"type":"overloaded_error"}}`)
				upstream := &parityUpstream{subErr: fault, okBody: in.upstreamOK(false)}
				if !tc.rescueSucceeds {
					upstream.paidErr = fault
				}
				svc := in.parityService(upstream)

				collector := newBypassSpanCollector(t)
				emitter, err := otel.NewEmitter(otel.EmitterConfig{
					Endpoint:      collector.srv.URL,
					Workers:       1,
					QueueSize:     100,
					BatchSize:     1,
					FlushInterval: 10 * time.Millisecond,
				})
				require.NoError(t, err)
				t.Cleanup(func() { _ = emitter.Shutdown(context.Background()) })
				svc.emitter = emitter

				buf := otel.NewBuffer(emitter)
				ctx := buf.WithContext(in.subCtx())
				rec, req, body := in.request(t, false)
				_ = in.call(svc, ctx, body, rec, req)
				buf.Flush()

				require.Eventually(t, func() bool {
					collector.mu.Lock()
					defer collector.mu.Unlock()
					return len(collector.byName["router.upstream"]) == 1
				}, 2*time.Second, 5*time.Millisecond, "the upstream span must be exported")

				collector.mu.Lock()
				sp := collector.byName["router.upstream"][0]
				collector.mu.Unlock()

				assert.Equal(t, tc.rescueSucceeds, spanBool(t, sp, "dispatch.subscription_failover"),
					"the subscription rescue is reported only when it actually served the turn")
				assert.Equal(t, tc.rescueSucceeds, spanBool(t, sp, "dispatch.failover_used"),
					"failover_used must agree with the rescue that served")
				assert.Equal(t, !tc.rescueSucceeds, spanBool(t, sp, "cost.subscription_served"),
					"billing attribution must name the credential that actually paid: the Weave key after a successful rescue, the subscription when the rescue never served")
			})
		}
	}
}

// countOccurrences counts non-overlapping occurrences of needle in s.
func countOccurrences(s, needle string) int { return strings.Count(s, needle) }

// TestSubscriptionFailoverParity_HeldErrorDelivery pins how the held upstream
// error reaches the client when both the subscription attempt and its rescue
// fail: exactly once, and through the renderer that matches what is already on
// the wire.
func TestSubscriptionFailoverParity_HeldErrorDelivery(t *testing.T) {
	const needle = "parity-upstream-failure"
	fault := upstreamErr(http.StatusServiceUnavailable, `{"error":{"type":"overloaded_error","message":"`+needle+`"}}`)

	for _, in := range parityIngresses() {
		t.Run(in.name+"/buffered response is written exactly once", func(t *testing.T) {
			upstream := &parityUpstream{subErr: fault, paidErr: fault, okBody: in.upstreamOK(false)}
			svc := in.parityService(upstream)
			rec, req, body := in.request(t, false)

			require.Error(t, in.call(svc, in.subCtx(), body, rec, req))
			require.Positive(t, upstream.paidDispatches, "the scenario under test is a failed rescue")
			assert.Equal(t, 1, countOccurrences(rec.Body.String(), needle),
				"the held upstream error must reach the client exactly once, no matter how many rescues declined")
		})

		t.Run(in.name+"/prelude-sent stream keeps the SSE framing", func(t *testing.T) {
			upstream := &parityUpstream{subErr: fault, paidErr: fault, okBody: in.upstreamOK(true)}
			svc := in.parityService(upstream)
			rec, req, body := in.request(t, true)

			require.Error(t, in.call(svc, in.subCtx(), body, rec, req))
			out := rec.Body.String()
			require.Positive(t, upstream.paidDispatches, "the scenario under test is a failed rescue")
			require.Contains(t, out, "✦ **Weave Router**",
				"the scenario under test needs the routing prelude already on the wire")
			for _, line := range strings.Split(out, "\n") {
				if !strings.Contains(line, needle) {
					continue
				}
				assert.True(t, strings.HasPrefix(line, "data: "),
					"a bare JSON error envelope corrupts a stream the client is parsing as SSE, got %q", line)
			}
			assert.Equal(t, 1, countOccurrences(out, needle),
				"the held error must reach the client exactly once")
		})

		t.Run(in.name+"/committed stream is never rescued", func(t *testing.T) {
			upstream := &parityUpstream{
				subErr:     fault,
				paidErr:    fault,
				okBody:     in.upstreamOK(true),
				preErrBody: in.upstreamOK(true),
			}
			svc := in.parityService(upstream)
			rec, req, body := in.request(t, true)

			_ = in.call(svc, in.subCtx(), body, rec, req)
			assert.Equal(t, 1, upstream.subDispatches,
				"provider output already reached the client; a retry would duplicate the turn")
			assert.Zero(t, upstream.paidDispatches,
				"a committed stream cannot be rescued onto the paid key")
		})
	}
}
