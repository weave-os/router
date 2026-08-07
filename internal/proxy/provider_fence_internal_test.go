package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"workweave/router/internal/auth"
	"workweave/router/internal/providers"
	"workweave/router/internal/router"
	"workweave/router/internal/router/catalog"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fencedCtx mimics what the auth middleware stashes for a fenced installation.
func fencedCtx(allowed ...string) context.Context {
	return context.WithValue(context.Background(), InstallationAllowedProvidersContextKey{}, allowed)
}

func TestAllowedProvidersForRequest(t *testing.T) {
	t.Run("no fence anywhere is unfenced", func(t *testing.T) {
		s := &Service{}
		assert.Nil(t, s.allowedProvidersForRequest(context.Background()))
	})

	// An empty stored list is the "no fence configured" state; reading it as a
	// fence of zero providers would take every existing installation offline.
	t.Run("empty installation list is unfenced", func(t *testing.T) {
		s := &Service{}
		assert.Nil(t, s.allowedProvidersForRequest(fencedCtx()))
	})

	t.Run("installation list becomes the fence", func(t *testing.T) {
		s := &Service{}
		fence := s.allowedProvidersForRequest(fencedCtx(providers.ProviderAnthropic))
		require.NotNil(t, fence)
		assert.Contains(t, fence, providers.ProviderAnthropic)
		assert.NotContains(t, fence, providers.ProviderOpenAI)
	})

	// ROUTER_ALLOWED_PROVIDERS is the deploy operator's boundary; installation
	// data must not be able to widen it, or a compromised control plane could.
	t.Run("deployment override outranks the installation list", func(t *testing.T) {
		s := (&Service{}).WithAllowedProvidersOverride([]string{providers.ProviderAnthropic})
		fence := s.allowedProvidersForRequest(fencedCtx(providers.ProviderOpenAI))
		require.NotNil(t, fence)
		assert.Contains(t, fence, providers.ProviderAnthropic)
		assert.NotContains(t, fence, providers.ProviderOpenAI,
			"the installation must not be able to add a provider the deployment fenced off")
	})

	t.Run("empty override leaves installations unfenced", func(t *testing.T) {
		s := (&Service{}).WithAllowedProvidersOverride(nil)
		assert.False(t, s.HasAllowedProvidersOverride())
		assert.Nil(t, s.allowedProvidersForRequest(context.Background()))
	})
}

// Every upstream dispatch acquires its client through s.provider, so refusing
// here is what makes the fence structural rather than routing hygiene.
func TestServiceProvider_RefusesClientOutsideFence(t *testing.T) {
	s := &Service{providers: map[string]providers.Client{
		providers.ProviderAnthropic: &fakeClient{name: providers.ProviderAnthropic},
		providers.ProviderOpenAI:    &fakeClient{name: providers.ProviderOpenAI},
	}}
	ctx := fencedCtx(providers.ProviderAnthropic)

	inside, err := s.provider(ctx, providers.ProviderAnthropic)
	require.NoError(t, err)
	assert.NotNil(t, inside)

	outside, err := s.provider(ctx, providers.ProviderOpenAI)
	assert.Nil(t, outside)
	require.ErrorIs(t, err, ErrProviderNotAllowed,
		"a wired, healthy client must still be refused when it sits outside the fence")
}

func TestExcludedProvidersForRequest_FoldsInFenceComplement(t *testing.T) {
	s := &Service{}

	t.Run("providers outside the fence read as excluded", func(t *testing.T) {
		excluded := s.excludedProvidersForRequest(fencedCtx(providers.ProviderAnthropic))
		assert.Contains(t, excluded, providers.ProviderOpenAI,
			"scorer eligibility, pins and failover all read this set — the fence must reach them")
		assert.NotContains(t, excluded, providers.ProviderAnthropic)
	})

	// Exclusions are a routing preference and the fence is a boundary; they
	// stack rather than replace each other.
	t.Run("explicit exclusions still apply inside the fence", func(t *testing.T) {
		ctx := context.WithValue(fencedCtx(providers.ProviderAnthropic, providers.ProviderOpenAI),
			InstallationExcludedProvidersContextKey{}, []string{providers.ProviderOpenAI})
		excluded := s.excludedProvidersForRequest(ctx)
		assert.Contains(t, excluded, providers.ProviderOpenAI)
		assert.NotContains(t, excluded, providers.ProviderAnthropic)
	})

	t.Run("unfenced request keeps its exclusion set untouched", func(t *testing.T) {
		assert.Empty(t, s.excludedProvidersForRequest(context.Background()))
	})
}

// A BYOK key enrolls its provider for routing; the fence has to win, or a
// customer key would be enough to route around the deploy's egress boundary.
func TestEnabledProvidersForRequest_FenceBeatsByokEnrollment(t *testing.T) {
	s := &Service{
		providers: map[string]providers.Client{
			providers.ProviderAnthropic: nil,
			providers.ProviderOpenAI:    nil,
		},
		deploymentKeyedProviders: map[string]struct{}{},
	}
	ctx := context.WithValue(fencedCtx(providers.ProviderAnthropic), ExternalAPIKeysContextKey{},
		[]*auth.ExternalAPIKey{
			{Provider: providers.ProviderAnthropic, Plaintext: []byte("sk-ant-byok")},
			{Provider: providers.ProviderOpenAI, Plaintext: []byte("sk-openai-byok")},
		})

	enabled := s.enabledProvidersForRequest(ctx, providers.ProviderAnthropic, http.Header{})

	assert.Contains(t, enabled, providers.ProviderAnthropic)
	assert.NotContains(t, enabled, providers.ProviderOpenAI,
		"a BYOK key must not enroll a provider the installation is fenced away from")
}

func TestResolveBindingsForDispatch_Fence(t *testing.T) {
	multiBinding := &Service{deploymentKeyedProviders: map[string]struct{}{
		providers.ProviderFireworks:  {},
		providers.ProviderOpenRouter: {},
	}}

	t.Run("fallback binding outside the fence is dropped", func(t *testing.T) {
		bs := multiBinding.resolveBindingsForDispatch(fencedCtx(providers.ProviderFireworks),
			router.Decision{Model: "deepseek/deepseek-v4-pro", Provider: providers.ProviderFireworks})
		require.NotEmpty(t, bs)
		for _, b := range bs {
			assert.Equal(t, providers.ProviderFireworks, b.Provider,
				"failover must not walk the request out to a provider outside the fence")
		}
	})

	t.Run("primary outside the fence yields no walk at all", func(t *testing.T) {
		bs := multiBinding.resolveBindingsForDispatch(fencedCtx(providers.ProviderAnthropic),
			router.Decision{Model: "deepseek/deepseek-v4-pro", Provider: providers.ProviderFireworks})
		assert.Empty(t, bs, "dispatch must fail closed rather than serve a fenced-off provider")
	})

	// BYOK/legacy requests take the single-attempt shortcut, which used to run
	// before any fence check — a fence that only held with failover enabled
	// would be no fence at all.
	t.Run("BYOK single-attempt path is fenced too", func(t *testing.T) {
		s := &Service{}
		ctx := context.WithValue(fencedCtx(providers.ProviderAnthropic), CredentialsContextKey{},
			&Credentials{APIKey: []byte("k"), Source: "client"})
		bs := s.resolveBindingsForDispatch(ctx,
			router.Decision{Model: "deepseek/deepseek-v4-pro", Provider: providers.ProviderFireworks})
		assert.Empty(t, bs)
	})

	t.Run("legacy single-attempt path is fenced too", func(t *testing.T) {
		s := &Service{} // deploymentKeyedProviders == nil
		bs := s.resolveBindingsForDispatch(fencedCtx(providers.ProviderAnthropic),
			router.Decision{Model: "deepseek/deepseek-v4-pro", Provider: providers.ProviderFireworks})
		assert.Empty(t, bs)
	})

	t.Run("an in-fence primary still dispatches", func(t *testing.T) {
		bs := multiBinding.resolveBindingsForDispatch(
			fencedCtx(providers.ProviderFireworks, providers.ProviderOpenRouter),
			router.Decision{Model: "deepseek/deepseek-v4-pro", Provider: providers.ProviderFireworks})
		require.GreaterOrEqual(t, len(bs), 2, "a fence covering both bindings must not disable failover")
		assert.Equal(t, providers.ProviderFireworks, bs[0].Provider)
	})
}

func TestDispatchWithFallback_FenceFailsClosed(t *testing.T) {
	t.Run("empty walk from the fence reports the fence, not a 502", func(t *testing.T) {
		s := newServiceWithProviders(t, map[string]providers.Client{
			providers.ProviderFireworks: &fakeClient{name: providers.ProviderFireworks},
		})
		rec := httptest.NewRecorder()

		_, err := s.dispatchWithFallback(fencedCtx(providers.ProviderAnthropic), failoverInputs{
			w:               rec,
			buf:             newPreludeBuffer(rec),
			initialDecision: router.Decision{Model: "deepseek/deepseek-v4-pro", Provider: providers.ProviderFireworks},
			attempt: func(context.Context, router.Decision, providers.Client) error {
				t.Fatal("no attempt may run when the fence leaves nothing to dispatch to")
				return nil
			},
		})

		require.ErrorIs(t, err, ErrProviderNotAllowed)
		cls, ok := ClassifyDispatchError(err)
		require.True(t, ok)
		assert.Equal(t, http.StatusForbidden, cls.Status,
			"the deploy is healthy — the request simply has nowhere permitted to go")
	})

	// Defense in depth: resolveBindingsForDispatch already filters these, so a
	// fenced binding reaching the walk is an upstream bug. It must still never
	// be dispatched.
	t.Run("a fenced binding in the walk is never called", func(t *testing.T) {
		forbidden := &fakeClient{name: providers.ProviderOpenRouter, outcomes: []fakeOutcome{{writeBytes: []byte("leaked")}}}
		s := newServiceWithProviders(t, map[string]providers.Client{
			providers.ProviderOpenRouter: forbidden,
		})
		rec := httptest.NewRecorder()
		buf := newPreludeBuffer(rec)
		r := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

		_, err := s.dispatchWithFallback(fencedCtx(providers.ProviderAnthropic), failoverInputs{
			w:               rec,
			buf:             buf,
			initialDecision: router.Decision{Model: "deepseek/deepseek-v4-pro", Provider: providers.ProviderOpenRouter},
			bindings:        []catalog.ProviderBinding{{Provider: providers.ProviderOpenRouter}},
			attempt: func(ctx context.Context, d router.Decision, p providers.Client) error {
				buf.Seal()
				return p.Proxy(ctx, d, providers.PreparedRequest{}, buf, r)
			},
		})

		require.ErrorIs(t, err, ErrProviderNotAllowed)
		assert.Zero(t, forbidden.calls, "the fenced provider must not receive the request")
		assert.Empty(t, rec.Body.String())
	})
}
