package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/cache"
	"weave-os/router/internal/router/policy"
)

func TestCacheTenantKey(t *testing.T) {
	instID := uuid.New()

	t.Run("external ID takes priority when present", func(t *testing.T) {
		assert.Equal(t, "org_12345", cacheTenantKey(instID, "org_12345"))
	})

	t.Run("installation ID used for self-hosted when external ID is empty", func(t *testing.T) {
		assert.Equal(t, instID.String(), cacheTenantKey(instID, ""))
	})

	t.Run("empty string when both are absent", func(t *testing.T) {
		assert.Empty(t, cacheTenantKey(uuid.Nil, ""))
	})
}

func TestSemanticCache_SelfHostedInstallationHit(t *testing.T) {
	instID := uuid.New()
	embedding := []float32{1, 0}

	for _, tc := range []struct {
		name   string
		format cache.Format
		body   string
		invoke func(*Service, context.Context, []byte, http.ResponseWriter, *http.Request) error
	}{
		{
			name:   "messages_anthropic",
			format: cache.FormatAnthropic,
			body:   `{"model":"claude-opus-4-8","max_tokens":4096,"messages":[{"role":"user","content":"explain semantic caching"}]}`,
			invoke: (*Service).ProxyMessages,
		},
		{
			name:   "chat_openai",
			format: cache.FormatOpenAI,
			body:   `{"model":"gpt-5","messages":[{"role":"user","content":"explain semantic caching"}]}`,
			invoke: (*Service).ProxyOpenAIChatCompletion,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			semanticCache := cache.New(cache.DefaultConfig())
			cachedBody := `{"cached_response":true,"format":"` + string(tc.format) + `"}`
			semanticCache.Store(
				instID.String(),
				tc.format,
				embedding,
				1,
				cache.CachedResponse{StatusCode: http.StatusOK, Body: []byte(cachedBody)},
				"",
				0,
			)

			// Router returns matching metadata with cluster 1 and embedding
			decision := router.Decision{
				Provider: providers.ProviderAnthropic,
				Model:    "claude-haiku-4-5",
				Metadata: &router.RoutingMetadata{
					Strategy:   string(router.StrategyHMMEmbedding),
					Embedding:  embedding,
					ClusterIDs: []int{1},
				},
			}
			routerMock := &authoritativeTestRouter{decision: decision}

			svc := NewService(routerMock, nil, nil, false, semanticCache, newStubPinStore(), false, providers.ProviderAnthropic, "claude-opus-4-8", nil).
				WithPolicyStrategy(policy.StrategySpec{
					Strategy: router.StrategyHMMEmbedding,
					Router:   routerMock,
					Capabilities: policy.Capabilities{
						SchemaVersion: policy.SchemaVersionV1,
					},
				})

			// Context has InstallationIDContextKey but NO ExternalIDContextKey (self-hosted mode)
			ctx := context.Background()
			ctx = context.WithValue(ctx, InstallationIDContextKey{}, instID.String())
			ctx = context.WithValue(ctx, APIKeyIDContextKey{}, "test-api-key")

			recorder := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

			err := tc.invoke(svc, ctx, []byte(tc.body), recorder, req)
			require.NoError(t, err, "self-hosted request must successfully hit semantic cache without provider dispatch")
			assert.Equal(t, http.StatusOK, recorder.Code)
			assert.JSONEq(t, cachedBody, recorder.Body.String())

			// A different installation ID must miss the cache and attempt provider dispatch (failing with ErrProviderNotConfigured)
			otherCtx := context.Background()
			otherCtx = context.WithValue(otherCtx, InstallationIDContextKey{}, uuid.NewString())
			otherCtx = context.WithValue(otherCtx, APIKeyIDContextKey{}, "test-api-key")

			otherRecorder := httptest.NewRecorder()
			otherErr := tc.invoke(svc, otherCtx, []byte(tc.body), otherRecorder, req)
			require.ErrorIs(t, otherErr, ErrProviderNotConfigured, "different installation ID must miss cache and attempt provider dispatch")
		})
	}
}
