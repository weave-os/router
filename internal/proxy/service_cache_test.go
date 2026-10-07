package proxy_test

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tidwall/sjson"
	"weave-os/router/internal/feedback"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/cache"
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

// anthropicBody uses an ordinary text prompt with a MainLoop output budget.
func anthropicBody(prompt string, stream bool) []byte {
	streamLit := "false"
	if stream {
		streamLit = "true"
	}
	return []byte(`{
		"model":"claude-opus-4-7",
		"max_tokens":4096,
		"stream":` + streamLit + `,
		"messages":[{"role":"user","content":"` + prompt + `"}]
	}`)
}

// decisionWithEmbedding builds a routing decision with metadata needed for cache eligibility.
func decisionWithEmbedding(emb []float32, clusterIDs []int) router.Decision {
	return router.Decision{
		Provider: providers.ProviderAnthropic,
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
	ctx := context.WithValue(context.Background(), proxy.APIKeyIDContextKey{}, "cache-test-key")
	if externalID != "" {
		ctx = context.WithValue(ctx, proxy.ExternalIDContextKey{}, externalID)
	}
	return ctx
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
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderAnthropic, Model: "claude-haiku-4-5", Reason: "heuristic"}}
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

func TestService_Cache_VerifiedProvenanceChangesDispatch(t *testing.T) {
	for _, surface := range []struct {
		name   string
		body   []byte
		invoke func(*proxy.Service, context.Context, []byte, http.ResponseWriter, *http.Request) error
	}{
		{"messages", anthropicBody("ordinary prompt", false), (*proxy.Service).ProxyMessages},
		{"chat", []byte(`{"model":"auto","max_tokens":4096,"messages":[{"role":"user","content":"ordinary prompt"}]}`), (*proxy.Service).ProxyOpenAIChatCompletion},
	} {
		for name, change := range map[string]func(context.Context, *fakeRouter) context.Context{
			"credential subject": func(ctx context.Context, _ *fakeRouter) context.Context {
				return requestcontext.WithServingIdentity(ctx, requestcontext.ServingIdentity{CredentialIdentity: "other-subject", Plan: string(entitlement.PlanBoost), ProfileKey: "profile", ProfileRevision: "revision"})
			},
			"product": func(ctx context.Context, _ *fakeRouter) context.Context {
				return requestcontext.WithServingIdentity(ctx, requestcontext.ServingIdentity{CredentialIdentity: "subject", ProfileKey: "profile", ProfileRevision: "revision"})
			},
			"profile": func(ctx context.Context, _ *fakeRouter) context.Context {
				return requestcontext.WithServingIdentity(ctx, requestcontext.ServingIdentity{CredentialIdentity: "subject", Plan: string(entitlement.PlanBoost), ProfileKey: "other-profile", ProfileRevision: "revision"})
			},
			"revision": func(ctx context.Context, _ *fakeRouter) context.Context {
				return requestcontext.WithServingIdentity(ctx, requestcontext.ServingIdentity{CredentialIdentity: "subject", Plan: string(entitlement.PlanBoost), ProfileKey: "profile", ProfileRevision: "other-revision"})
			},
			"model": func(ctx context.Context, router *fakeRouter) context.Context {
				router.decision.Model = "claude-sonnet-4-6"
				return ctx
			},
			"provider": func(ctx context.Context, router *fakeRouter) context.Context {
				router.decision.Provider = providers.ProviderAnthropicGateway
				return ctx
			},
			"missing identity": func(ctx context.Context, _ *fakeRouter) context.Context {
				return requestcontext.WithServingIdentity(ctx, requestcontext.ServingIdentity{Plan: string(entitlement.PlanBoost), ProfileKey: "profile", ProfileRevision: "revision"})
			},
		} {
			t.Run(surface.name+"/"+name, func(t *testing.T) {
				provider := &fakeProvider{proxyResponse: cacheUpstreamReply("provider response")}
				fr := &fakeRouter{decision: decisionWithEmbedding(embeddingFixture(1), []int{0})}
				svc := proxy.NewService(fr, map[string]providers.Client{providers.ProviderAnthropic: provider, providers.ProviderAnthropicGateway: provider}, nil, false, cache.New(cache.DefaultConfig()), nil, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil)
				ctx := requestcontext.WithServingIdentity(proxyContextWithExternalID(t, "tenant"), requestcontext.ServingIdentity{CredentialIdentity: "subject", Plan: string(entitlement.PlanBoost), ProfileKey: "profile", ProfileRevision: "revision"})
				invoke := func(ctx context.Context) *httptest.ResponseRecorder {
					rec := httptest.NewRecorder()
					require.NoError(t, surface.invoke(svc, ctx, surface.body, rec, httptest.NewRequest(http.MethodPost, "/test", nil)))
					return rec
				}
				first := invoke(ctx)
				require.Empty(t, first.Header().Get(proxy.HeaderRouterCache))
				require.Len(t, provider.proxyBodies, 1)
				second := invoke(ctx)
				require.Equal(t, proxy.RouterCacheHit, second.Header().Get(proxy.HeaderRouterCache))
				require.Len(t, provider.proxyBodies, 1)
				assert.Equal(t, fr.decision.Model, second.Header().Get(proxy.HeaderRouterModel))
				assert.Equal(t, fr.decision.Provider, second.Header().Get(proxy.HeaderRouterProvider))
				changed := invoke(change(ctx, fr))
				assert.Empty(t, changed.Header().Get(proxy.HeaderRouterCache))
				assert.Len(t, provider.proxyBodies, 2)
			})
		}
	}
}

func TestService_Cache_UnsafeRequestsCannotHitOrPopulate(t *testing.T) {
	for name, shape := range map[string]struct{ path, json string }{
		"tools":      {"tools", `[{"name":"Read","input_schema":{"type":"object"}}]`},
		"structured": {"output_config", `{"format":{"type":"json_schema","schema":{"type":"object"}}}`},
		"media":      {"messages", `[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"YWJj"}}]}]`},
	} {
		t.Run(name, func(t *testing.T) {
			provider := &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
				_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"provider response"}]}`))
			}}
			fr := &fakeRouter{decision: decisionWithEmbedding(embeddingFixture(1), []int{0})}
			svc := proxy.NewService(fr, map[string]providers.Client{providers.ProviderAnthropic: provider}, nil, false, cache.New(cache.DefaultConfig()), nil, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil)
			ctx := proxyContextWithExternalID(t, "tenant")
			plain := anthropicBody("prompt", false)
			unsafe, err := sjson.SetRawBytes(plain, shape.path, []byte(shape.json))
			require.NoError(t, err)
			invoke := func(body []byte) *httptest.ResponseRecorder {
				rec := httptest.NewRecorder()
				require.NoError(t, svc.ProxyMessages(ctx, body, rec, httptest.NewRequest(http.MethodPost, "/test", nil)))
				return rec
			}
			invoke(unsafe)
			invoke(unsafe)
			require.Len(t, provider.proxyBodies, 2, "unsafe requests cannot populate")
			invoke(plain)
			require.Len(t, provider.proxyBodies, 3)
			require.Equal(t, proxy.RouterCacheHit, invoke(plain).Header().Get(proxy.HeaderRouterCache))
			assert.Empty(t, invoke(unsafe).Header().Get(proxy.HeaderRouterCache))
			assert.Len(t, provider.proxyBodies, 4, "unsafe requests cannot hit a populated ordinary bucket")
		})
	}
}

func TestService_Cache_RequestRestrictionsBypassPopulatedCache(t *testing.T) {
	for name, restrict := range map[string]func(context.Context) context.Context{
		"allowed": func(ctx context.Context) context.Context {
			return context.WithValue(ctx, proxy.InstallationAllowedModelsContextKey{}, []string{"claude-haiku-4-5"})
		},
		"excluded": func(ctx context.Context) context.Context {
			return context.WithValue(ctx, proxy.InstallationExcludedModelsContextKey{}, []string{"claude-opus-4-7"})
		},
		"missing key": func(ctx context.Context) context.Context {
			return context.WithValue(ctx, proxy.APIKeyIDContextKey{}, "")
		},
	} {
		t.Run(name, func(t *testing.T) {
			provider := &fakeProvider{proxyResponse: func(w http.ResponseWriter) { _, _ = w.Write([]byte(`{"content":[{"type":"text","text":"response"}]}`)) }}
			fr := &fakeRouter{decision: decisionWithEmbedding(embeddingFixture(1), []int{0})}
			svc := proxy.NewService(fr, map[string]providers.Client{providers.ProviderAnthropic: provider}, nil, false, cache.New(cache.DefaultConfig()), nil, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil)
			ctx := proxyContextWithExternalID(t, "tenant")
			invoke := func(ctx context.Context) {
				rec := httptest.NewRecorder()
				require.NoError(t, svc.ProxyMessages(ctx, anthropicBody("prompt", false), rec, httptest.NewRequest(http.MethodPost, "/test", nil)))
			}
			invoke(ctx)
			invoke(ctx)
			require.Len(t, provider.proxyBodies, 1)
			invoke(restrict(ctx))
			invoke(restrict(ctx))
			assert.Len(t, provider.proxyBodies, 3)
		})
	}
}

func TestService_Cache_FallbackBodyNeverStoredUnderInitialTarget(t *testing.T) {
	for _, surface := range []struct {
		name   string
		body   []byte
		invoke func(*proxy.Service, context.Context, []byte, http.ResponseWriter, *http.Request) error
	}{
		{"messages", anthropicBody("prompt", false), (*proxy.Service).ProxyMessages},
		{"chat", []byte(`{"model":"auto","max_tokens":4096,"messages":[{"role":"user","content":"prompt"}]}`), (*proxy.Service).ProxyOpenAIChatCompletion},
	} {
		t.Run(surface.name, func(t *testing.T) {
			primary := &fakeProvider{proxyErr: &providers.UpstreamErrorResponse{Status: 503, Body: []byte(`{"error":{"message":"unavailable"}}`)}}
			fallback := &fakeProvider{proxyResponse: cacheUpstreamReply("fallback response")}
			decision := decisionWithEmbedding(embeddingFixture(1), []int{0})
			decision.Model = "claude-sonnet-4-6"
			fr := &fakeRouter{decision: decision}
			svc := proxy.NewService(fr, map[string]providers.Client{providers.ProviderAnthropic: primary, providers.ProviderAnthropicGateway: fallback}, nil, false, cache.New(cache.DefaultConfig()), nil, false, providers.ProviderAnthropic, "claude-sonnet-4-6", nil).WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAnthropic: {}, providers.ProviderAnthropicGateway: {}})
			ctx := proxyContextWithExternalID(t, "tenant")
			invoke := func() *httptest.ResponseRecorder {
				rec := httptest.NewRecorder()
				require.NoError(t, surface.invoke(svc, ctx, surface.body, rec, httptest.NewRequest(http.MethodPost, "/test", nil)))
				return rec
			}
			first := invoke()
			require.Len(t, fallback.proxyBodies, 1)
			assert.Equal(t, providers.ProviderAnthropicGateway, first.Header().Get(proxy.HeaderRouterProvider))
			assert.Contains(t, first.Body.String(), "fallback response")
			initialCalls := len(primary.proxyBodies)
			primary.proxyErr = nil
			primary.proxyResponse = cacheUpstreamReply("primary response")
			second := invoke()
			assert.Len(t, primary.proxyBodies, initialCalls+1)
			assert.Empty(t, second.Header().Get(proxy.HeaderRouterCache))
			assert.Contains(t, second.Body.String(), "primary response")
			third := invoke()
			assert.Equal(t, proxy.RouterCacheHit, third.Header().Get(proxy.HeaderRouterCache))
			assert.Equal(t, providers.ProviderAnthropic, third.Header().Get(proxy.HeaderRouterProvider))
			assert.Contains(t, third.Body.String(), "primary response")
		})
	}
}

func cacheUpstreamReply(text string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"id":"message","type":"message","role":"assistant","content":[{"type":"text","text":%q}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`, text)
	}
}
