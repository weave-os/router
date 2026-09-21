package proxy_test

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"weave-os/router/internal/feedback"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/cache"
	"weave-os/router/internal/subscriptions"
	"weave-os/router/internal/subscriptions/entitlement"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// embeddingFixture returns a deterministic L2-normalized vector keyed by seed.
func embeddingFixture(seed float32) []float32 {
	v := []float32{seed, 1, 0, 0, 0, 0, 0, 0}
	var sum float32
	for _, x := range v {
		sum += x * x
	}
	if sum == 0 {
		return v
	}
	norm := float32(math.Sqrt(float64(sum)))
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = x / norm
	}
	return out
}

// anthropicBody returns a minimal valid Anthropic Messages body. Includes a
// stub tool so the request stays classified as MainLoop — without it the
// turntype detector would fingerprint it as Classifier (small max_tokens,
// no tools, short message list) and hard-pin past the semantic cache.
func anthropicBody(prompt string, stream bool) []byte {
	streamLit := "false"
	if stream {
		streamLit = "true"
	}
	return []byte(`{
		"model":"claude-opus-4-7",
		"max_tokens":256,
		"stream":` + streamLit + `,
		"tools":[{"name":"noop","description":"placeholder","input_schema":{"type":"object"}}],
		"messages":[{"role":"user","content":"` + prompt + `"}]
	}`)
}

// decisionWithEmbedding builds a routing decision with metadata needed for cache eligibility.
func decisionWithEmbedding(emb []float32, clusterIDs []int) router.Decision {
	return router.Decision{
		Provider: "anthropic",
		Model:    "claude-haiku-4-5",
		Reason:   "test",
		Metadata: &router.RoutingMetadata{
			Embedding:  emb,
			ClusterIDs: clusterIDs,
		},
	}
}

// proxyContextWithExternalID wires the per-tenant ID; without it the cache is bypassed.
func proxyContextWithExternalID(t *testing.T, externalID string) context.Context {
	t.Helper()
	ctx := context.Background()
	if externalID != "" {
		ctx = context.WithValue(ctx, proxy.ExternalIDContextKey{}, externalID)
	}
	return ctx
}

func cacheServingContext(t *testing.T, externalID, subject, profile, revision string) context.Context {
	t.Helper()
	ctx := proxyContextWithExternalID(t, externalID)
	ctx = context.WithValue(ctx, proxy.APIKeyIDContextKey{}, "key-"+subject)
	return requestcontext.WithServingIdentity(ctx, requestcontext.ServingIdentity{
		CredentialIdentity: subject,
		ProfileKey:         profile,
		ProfileRevision:    revision,
	})
}

type productAwareCacheRouter struct {
	embedding []float32
}

func (r *productAwareCacheRouter) Route(_ context.Context, req router.Request) (router.Decision, error) {
	decision := decisionWithEmbedding(r.embedding, []int{0})
	if req.ProductEligibility.Restricts() {
		decision.Model = "deepseek/deepseek-v4-pro"
		decision.Provider = providers.ProviderFireworks
	}
	return decision, nil
}

func TestService_Cache_HitShortCircuitsProvider(t *testing.T) {
	emb := embeddingFixture(1)
	provider := &fakeProvider{
		proxyResponse: func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"first","content":"hi"}`))
		},
	}
	fr := &fakeRouter{decision: decisionWithEmbedding(emb, []int{0, 1, 2, 3})}
	c := cache.New(cache.DefaultConfig())
	svc := proxy.NewService(fr, map[string]providers.Client{providers.ProviderAnthropic: provider}, nil, false, c, nil, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil)

	ctx := proxyContextWithExternalID(t, "tenant-1")
	body := anthropicBody("ping", false)

	rec1 := httptest.NewRecorder()
	httpReq1 := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	require.NoError(t, svc.ProxyMessages(ctx, body, rec1, httpReq1))
	require.Len(t, provider.proxyBodies, 1, "first call must hit the provider")

	rec2 := httptest.NewRecorder()
	httpReq2 := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	require.NoError(t, svc.ProxyMessages(ctx, body, rec2, httpReq2))
	assert.Len(t, provider.proxyBodies, 1, "cache hit must not invoke provider a second time")

	assert.Equal(t, `{"id":"first","content":"hi"}`, rec2.Body.String())
	assert.Equal(t, proxy.RouterCacheHit, rec2.Header().Get(proxy.HeaderRouterCache))
}

func TestService_Cache_ProviderFallbackUsesInitialProvenance(t *testing.T) {
	emb := embeddingFixture(24)
	primary := &fakeProvider{proxyErr: &providers.UpstreamErrorResponse{Status: http.StatusServiceUnavailable}}
	fallback := &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl_1",
			"object":"chat.completion",
			"choices":[{"index":0,"message":{"role":"assistant","content":"fallback"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}
		}`))
	}}
	decision := decisionWithEmbedding(emb, []int{0})
	decision.Model = "deepseek/deepseek-v4-pro"
	decision.Provider = providers.ProviderTogether
	svc := proxy.NewService(
		&fakeRouter{decision: decision},
		map[string]providers.Client{
			providers.ProviderTogether:  primary,
			providers.ProviderFireworks: fallback,
		},
		nil, false, cache.New(cache.DefaultConfig()), nil, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil,
	).WithDeploymentKeyedProviders(map[string]struct{}{
		providers.ProviderTogether:  {},
		providers.ProviderFireworks: {},
	})
	ctx := cacheServingContext(t, "installation-1", "subject-a", "profile-a", "revision-1")
	body := []byte(`{
		"model":"gpt-5.6-sol",
		"stream":false,
		"tools":[{"type":"function","function":{"name":"noop","description":"placeholder","parameters":{"type":"object"}}}],
		"messages":[{"role":"user","content":"same fallback request"}]
	}`)

	first := httptest.NewRecorder()
	require.NoError(t, svc.ProxyOpenAIChatCompletion(ctx, body, first, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(""))))
	require.NotEmpty(t, primary.proxyBodies)
	require.Len(t, fallback.proxyBodies, 1)
	primaryCalls := len(primary.proxyBodies)

	second := httptest.NewRecorder()
	require.NoError(t, svc.ProxyOpenAIChatCompletion(ctx, body, second, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(""))))

	assert.Len(t, primary.proxyBodies, primaryCalls, "repeat must not retry the initial provider")
	assert.Len(t, fallback.proxyBodies, 1, "repeat must replay the fallback-served response")
	assert.Equal(t, proxy.RouterCacheHit, second.Header().Get(proxy.HeaderRouterCache))
	assert.Equal(t, first.Body.String(), second.Body.String())
}

func TestService_Cache_UnrestrictedClosedResponseDoesNotReplayToMax(t *testing.T) {
	emb := embeddingFixture(21)
	closed := &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
		_, _ = w.Write([]byte(`{"model":"closed"}`))
	}}
	open := &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
		_, _ = w.Write([]byte(`{"model":"open"}`))
	}}
	svc := proxy.NewService(
		&productAwareCacheRouter{embedding: emb},
		map[string]providers.Client{
			providers.ProviderAnthropic: closed,
			providers.ProviderFireworks: open,
		},
		nil, false, cache.New(cache.DefaultConfig()), nil, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil,
	)
	ctx := cacheServingContext(t, "installation-1", "subject-a", "profile-a", "revision-1")
	body := anthropicBody("same semantic request", false)

	unrestricted := httptest.NewRecorder()
	require.NoError(t, svc.ProxyMessages(ctx, body, unrestricted, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))))
	max := httptest.NewRecorder()
	require.NoError(t, svc.ProxyMessages(entitlement.WithProductScope(ctx, entitlement.PlanMax), body, max, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))))

	assert.Equal(t, `{"model":"closed"}`, unrestricted.Body.String())
	assert.Contains(t, max.Body.String(), `"model":"deepseek/deepseek-v4-pro"`)
	assert.Len(t, closed.proxyBodies, 1)
	assert.Len(t, open.proxyBodies, 1, "Max must dispatch its eligible model instead of replaying a closed-model response")
	assert.Empty(t, max.Header().Get(proxy.HeaderRouterCache))
}

func TestService_Cache_EffectiveProfileAndRevisionIsolation(t *testing.T) {
	emb := embeddingFixture(22)
	provider := &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
		_, _ = w.Write([]byte(`{"id":"profile"}`))
	}}
	svc := proxy.NewService(
		&fakeRouter{decision: decisionWithEmbedding(emb, []int{0})},
		map[string]providers.Client{providers.ProviderAnthropic: provider},
		nil, false, cache.New(cache.DefaultConfig()), nil, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil,
	)
	body := anthropicBody("same semantic request", false)
	contexts := []context.Context{
		cacheServingContext(t, "installation-1", "subject-a", "profile-a", "revision-1"),
		cacheServingContext(t, "installation-1", "subject-a", "profile-b", "revision-1"),
		cacheServingContext(t, "installation-1", "subject-a", "profile-b", "revision-2"),
		cacheServingContext(t, "installation-1", "subject-a", "profile-b", "revision-2"),
	}

	for _, ctx := range contexts {
		rec := httptest.NewRecorder()
		require.NoError(t, svc.ProxyMessages(ctx, body, rec, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))))
	}

	assert.Len(t, provider.proxyBodies, 3, "profile and revision changes must miss while an equivalent repeat hits")
}

func TestService_Cache_CredentialSubjectIsolation(t *testing.T) {
	emb := embeddingFixture(23)
	provider := &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
		_, _ = w.Write([]byte(`{"id":"subject"}`))
	}}
	svc := proxy.NewService(
		&fakeRouter{decision: decisionWithEmbedding(emb, []int{0})},
		map[string]providers.Client{providers.ProviderAnthropic: provider},
		nil, false, cache.New(cache.DefaultConfig()), nil, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil,
	)
	body := anthropicBody("same semantic request", false)
	contexts := []context.Context{
		cacheServingContext(t, "installation-1", "subject-a", "profile-a", "revision-1"),
		cacheServingContext(t, "installation-1", "subject-b", "profile-a", "revision-1"),
		cacheServingContext(t, "installation-1", "subject-b", "profile-a", "revision-1"),
	}

	for _, ctx := range contexts {
		rec := httptest.NewRecorder()
		require.NoError(t, svc.ProxyMessages(ctx, body, rec, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))))
	}

	assert.Len(t, provider.proxyBodies, 2, "subjects must miss across identities while an equivalent repeat hits")
}

func TestService_Cache_SubscriptionStatePreferencesBypass(t *testing.T) {
	emb := embeddingFixture(11)
	provider := &fakeProvider{
		proxyResponse: func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"conditional"}`))
		},
	}
	fr := &fakeRouter{decision: decisionWithEmbedding(emb, []int{0, 1})}
	c := cache.New(cache.DefaultConfig())
	svc := proxy.NewService(fr, map[string]providers.Client{providers.ProviderAnthropic: provider}, nil, false, c, nil, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil)

	ctx := proxyContextWithExternalID(t, "tenant-conditional")
	ctx = context.WithValue(ctx, proxy.SubscriptionStatePreferredModelsContextKey{}, []string{"claude-haiku-4-5"})
	body := anthropicBody("conditional cache", false)

	rec1 := httptest.NewRecorder()
	require.NoError(t, svc.ProxyMessages(ctx, body, rec1, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))))
	rec2 := httptest.NewRecorder()
	require.NoError(t, svc.ProxyMessages(ctx, body, rec2, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))))

	assert.Len(t, provider.proxyBodies, 2, "subscription-state preferences must not replay a cached response")
	assert.Empty(t, rec2.Header().Get(proxy.HeaderRouterCache))
}

func TestService_Cache_EmptySubscriptionStatePreferencesAllowCache(t *testing.T) {
	emb := embeddingFixture(12)
	provider := &fakeProvider{
		proxyResponse: func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"conditional-empty"}`))
		},
	}
	fr := &fakeRouter{decision: decisionWithEmbedding(emb, []int{0, 1})}
	c := cache.New(cache.DefaultConfig())
	svc := proxy.NewService(fr, map[string]providers.Client{providers.ProviderAnthropic: provider}, nil, false, c, nil, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil)

	ctx := proxyContextWithExternalID(t, "tenant-conditional-empty")
	ctx = context.WithValue(ctx, proxy.SubscriptionStatePreferredModelsContextKey{}, []string{})
	body := anthropicBody("conditional empty cache", false)

	rec1 := httptest.NewRecorder()
	require.NoError(t, svc.ProxyMessages(ctx, body, rec1, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))))
	rec2 := httptest.NewRecorder()
	require.NoError(t, svc.ProxyMessages(ctx, body, rec2, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))))

	assert.Len(t, provider.proxyBodies, 1, "an empty preference list does not alter routing and can reuse the cache")
	assert.Equal(t, proxy.RouterCacheHit, rec2.Header().Get(proxy.HeaderRouterCache))
}

func TestService_Cache_PlanAwareRoutingBypasses(t *testing.T) {
	emb := embeddingFixture(13)
	provider := &fakeProvider{
		proxyResponse: func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"plan-aware"}`))
		},
	}
	fr := &fakeRouter{decision: decisionWithEmbedding(emb, []int{0, 1})}
	c := cache.New(cache.DefaultConfig())
	svc := proxy.NewService(fr, map[string]providers.Client{providers.ProviderAnthropic: provider}, nil, false, c, nil, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil)

	ctx := proxyContextWithExternalID(t, "tenant-plan-aware")
	ctx = flags.WithOverrides(ctx, flags.Overrides{Bools: map[flags.Key]bool{flags.KeySubscriptionPlanAwareRouting: true}})
	ctx = context.WithValue(ctx, proxy.ManagedSubscriptionPlanStatesContextKey{}, map[subscriptions.Provider]proxy.SubscriptionPlanState{
		subscriptions.ProviderClaude: proxy.SubscriptionPlanStateExhausted,
		subscriptions.ProviderCodex:  proxy.SubscriptionPlanStateActive,
	})
	body := anthropicBody("plan-aware cache", false)

	rec1 := httptest.NewRecorder()
	require.NoError(t, svc.ProxyMessages(ctx, body, rec1, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))))
	rec2 := httptest.NewRecorder()
	require.NoError(t, svc.ProxyMessages(ctx, body, rec2, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))))

	assert.Len(t, provider.proxyBodies, 2, "plan-aware eligibility must not replay a response cached under another plan state")
	assert.Empty(t, rec2.Header().Get(proxy.HeaderRouterCache))
}

func TestService_Cache_StreamingBypasses(t *testing.T) {
	emb := embeddingFixture(2)
	provider := &fakeProvider{
		proxyResponse: func(w http.ResponseWriter) { _, _ = w.Write([]byte("event: stream-payload\n")) },
	}
	fr := &fakeRouter{decision: decisionWithEmbedding(emb, []int{0})}
	c := cache.New(cache.DefaultConfig())
	svc := proxy.NewService(fr, map[string]providers.Client{providers.ProviderAnthropic: provider}, nil, false, c, nil, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil)

	ctx := proxyContextWithExternalID(t, "tenant-1")
	body := anthropicBody("streaming please", true)

	rec1 := httptest.NewRecorder()
	httpReq1 := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	require.NoError(t, svc.ProxyMessages(ctx, body, rec1, httpReq1))

	rec2 := httptest.NewRecorder()
	httpReq2 := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	require.NoError(t, svc.ProxyMessages(ctx, body, rec2, httpReq2))

	assert.Len(t, provider.proxyBodies, 2, "streaming requests must always hit the provider — no caching")
	assert.Empty(t, rec2.Header().Get(proxy.HeaderRouterCache), "streaming responses carry no x-router-cache marker")
}

func TestService_Cache_HeuristicDecisionBypasses(t *testing.T) {
	provider := &fakeProvider{
		proxyResponse: func(w http.ResponseWriter) { _, _ = w.Write([]byte(`{"id":"x"}`)) },
	}
	// Decision with no Metadata — what the heuristic router produces.
	fr := &fakeRouter{decision: router.Decision{Provider: "anthropic", Model: "claude-haiku-4-5", Reason: "heuristic"}}
	c := cache.New(cache.DefaultConfig())
	svc := proxy.NewService(fr, map[string]providers.Client{providers.ProviderAnthropic: provider}, nil, false, c, nil, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil)

	ctx := proxyContextWithExternalID(t, "tenant-1")
	body := anthropicBody("ask", false)

	rec1 := httptest.NewRecorder()
	httpReq1 := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	require.NoError(t, svc.ProxyMessages(ctx, body, rec1, httpReq1))

	rec2 := httptest.NewRecorder()
	httpReq2 := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	require.NoError(t, svc.ProxyMessages(ctx, body, rec2, httpReq2))

	assert.Len(t, provider.proxyBodies, 2, "decisions without RoutingMetadata must not be cached")
}

func TestService_Cache_MissingExternalIDBypasses(t *testing.T) {
	emb := embeddingFixture(3)
	provider := &fakeProvider{
		proxyResponse: func(w http.ResponseWriter) { _, _ = w.Write([]byte(`{"id":"y"}`)) },
	}
	fr := &fakeRouter{decision: decisionWithEmbedding(emb, []int{0})}
	c := cache.New(cache.DefaultConfig())
	svc := proxy.NewService(fr, map[string]providers.Client{providers.ProviderAnthropic: provider}, nil, false, c, nil, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil)

	body := anthropicBody("ask", false)

	// No externalID → cache bypassed (per-tenant scope is the only isolation).
	ctx := context.Background()
	rec1 := httptest.NewRecorder()
	httpReq1 := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	require.NoError(t, svc.ProxyMessages(ctx, body, rec1, httpReq1))

	rec2 := httptest.NewRecorder()
	httpReq2 := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	require.NoError(t, svc.ProxyMessages(ctx, body, rec2, httpReq2))

	assert.Len(t, provider.proxyBodies, 2, "without externalID the cache must not store or replay")
}

// TestService_Cache_HitOmitsFeedbackLink guards two properties of the
// semantic-cache hit path: (1) the miss response carries a feedback link, and
// (2) the cache hit carries none — a cache hit writes no telemetry row, so its
// feedback page would have no routing context, and replaying the cached
// request's link would attribute a new client's rating to the wrong request_id.
func TestService_Cache_HitOmitsFeedbackLink(t *testing.T) {
	emb := embeddingFixture(7)
	provider := &fakeProvider{
		proxyResponse: func(w http.ResponseWriter) {
			// Echo a feedback header into the stored body to prove it is not
			// replayed from cache on the hit.
			w.Header().Set(proxy.HeaderRouterFeedbackURL, "https://router.example.com/f/STALE")
			_, _ = w.Write([]byte(`{"id":"cached","content":"hi"}`))
		},
	}
	fr := &fakeRouter{decision: decisionWithEmbedding(emb, []int{0, 1, 2, 3})}
	c := cache.New(cache.DefaultConfig())
	signer := feedback.NewSigner("cache-secret", time.Hour)
	svc := proxy.NewService(fr, map[string]providers.Client{providers.ProviderAnthropic: provider}, nil, false, c, nil, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil).
		WithFeedback(nil, signer, "https://router.example.com")

	ctx := context.WithValue(proxyContextWithExternalID(t, "tenant-1"), proxy.InstallationIDContextKey{}, uuid.New().String())
	body := anthropicBody("ping", false)

	rec1 := httptest.NewRecorder()
	require.NoError(t, svc.ProxyMessages(ctx, body, rec1, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))))
	require.NotEmpty(t, rec1.Header().Get(proxy.HeaderRouterFeedbackURL), "miss path must emit a feedback link")

	rec2 := httptest.NewRecorder()
	require.NoError(t, svc.ProxyMessages(ctx, body, rec2, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))))
	require.Len(t, provider.proxyBodies, 1, "second call must be a cache hit")
	require.Equal(t, proxy.RouterCacheHit, rec2.Header().Get(proxy.HeaderRouterCache))
	assert.Empty(t, rec2.Header().Get(proxy.HeaderRouterFeedbackURL), "cache hit must not emit a feedback link (no telemetry to back it, and never replay the cached one)")
}

func TestService_Cache_DisabledByNilCache(t *testing.T) {
	emb := embeddingFixture(4)
	provider := &fakeProvider{
		proxyResponse: func(w http.ResponseWriter) { _, _ = w.Write([]byte(`{"id":"z"}`)) },
	}
	fr := &fakeRouter{decision: decisionWithEmbedding(emb, []int{0})}
	// nil cache equivalent to ROUTER_SEMANTIC_CACHE_ENABLED=false.
	svc := proxy.NewService(fr, map[string]providers.Client{providers.ProviderAnthropic: provider}, nil, false, nil, nil, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil)

	ctx := proxyContextWithExternalID(t, "tenant-1")
	body := anthropicBody("ask", false)

	rec1 := httptest.NewRecorder()
	httpReq1 := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	require.NoError(t, svc.ProxyMessages(ctx, body, rec1, httpReq1))

	rec2 := httptest.NewRecorder()
	httpReq2 := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	require.NoError(t, svc.ProxyMessages(ctx, body, rec2, httpReq2))

	assert.Len(t, provider.proxyBodies, 2, "nil cache must be a transparent passthrough")
	assert.Empty(t, rec2.Header().Get(proxy.HeaderRouterCache))
}
