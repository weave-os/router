package proxy_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
)

func TestDependencyFallbackStopsAfterFirstFailedSessionRead(t *testing.T) {
	store := newFakePinStore()
	store.getErr = errors.New("database connection refused")
	policy := &fakeRouter{decision: router.Decision{Provider: providers.ProviderAnthropic, Model: "claude-haiku-4-5"}}
	provider := &fakeProvider{proxyResponse: func(w http.ResponseWriter) { _, _ = w.Write([]byte(`{"content":[{"type":"text","text":"original"}]}`)) }}
	service := proxy.NewService(policy, map[string]providers.Client{providers.ProviderAnthropic: provider}, nil, false, nil, store, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil).
		WithDependencyFailOpen(requestcontext.NewDependencyHealth(), requestcontext.DefaultPreparationLimits())
	rec := httptest.NewRecorder()
	require.NoError(t, service.ProxyMessages(authedCtx("00000000-0000-0000-0000-000000000001"), []byte(pinTestBody), rec, httptest.NewRequest(http.MethodPost, "/v1/messages", nil)))
	assert.JSONEq(t, `{"content":[{"type":"text","text":"original"}]}`, rec.Body.String())
	assert.Equal(t, "claude-opus-4-7", rec.Header().Get(proxy.HeaderRouterModel))
	assert.Equal(t, "database_unavailable", rec.Header().Get(proxy.HeaderRouterFailOpenReason))
	assert.Equal(t, 1, store.getCalls)
	assert.Zero(t, policy.routeCalls)
	assert.Empty(t, store.upserts)
	assert.Empty(t, store.usages)
}

type stalledSessionStore struct {
	*fakePinStore
}

func (s *stalledSessionStore) Get(ctx context.Context, _ [sessionpin.SessionKeyLen]byte, _ string) (sessionpin.Pin, bool, error) {
	s.getCalls++
	<-ctx.Done()
	return sessionpin.Pin{}, false, ctx.Err()
}

func TestDependencyFallbackBoundsBlackholedSessionRead(t *testing.T) {
	store := &stalledSessionStore{fakePinStore: newFakePinStore()}
	provider := &liveContextProvider{fakeProvider: fakeProvider{proxyResponse: func(w http.ResponseWriter) { _, _ = w.Write([]byte(`{"content":[]}`)) }}}
	limits := requestcontext.DefaultPreparationLimits()
	limits.Database = 50 * time.Millisecond
	limits.DatabaseCall = 20 * time.Millisecond
	service := proxy.NewService(&fakeRouter{}, map[string]providers.Client{providers.ProviderAnthropic: provider}, nil, false, nil, store, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil).
		WithDependencyFailOpen(requestcontext.NewDependencyHealth(), limits)
	started := time.Now()
	rec := httptest.NewRecorder()
	require.NoError(t, service.ProxyMessages(authedCtx("00000000-0000-0000-0000-000000000001"), []byte(pinTestBody), rec, httptest.NewRequest(http.MethodPost, "/v1/messages", nil)))
	assert.LessOrEqual(t, time.Since(started), 250*time.Millisecond)
	assert.JSONEq(t, `{"content":[]}`, rec.Body.String())
	assert.Equal(t, 1, store.getCalls)
	require.Len(t, provider.proxyBodies, 1)
}

func TestDependencyFallbackRetainsExplicitDenials(t *testing.T) {
	oversized := `{"model":"claude-haiku-4-5","messages":[{"role":"user","content":"` + strings.Repeat("original history ", 200000) + `"}]}`
	for _, tt := range []struct {
		name      string
		body      string
		configure func(context.Context, *http.Request) context.Context
	}{
		{name: "invalid override", body: pinTestBody, configure: func(ctx context.Context, request *http.Request) context.Context {
			request.Header.Set("X-Weave-Force-Model", "not-a-real-model")
			return ctx
		}},
		{name: "tenant allowlist", body: pinTestBody, configure: func(ctx context.Context, _ *http.Request) context.Context {
			return context.WithValue(ctx, proxy.InstallationAllowedModelsContextKey{}, []string{"claude-haiku-4-5"})
		}},
		{name: "context window", body: oversized, configure: func(ctx context.Context, _ *http.Request) context.Context { return ctx }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			provider := &fakeProvider{}
			service := proxy.NewService(&fakeRouter{err: errors.New("policy unavailable")}, map[string]providers.Client{providers.ProviderAnthropic: provider}, nil, false, nil, nil, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil).
				WithAvailableModels(map[string]struct{}{"claude-opus-4-7": {}, "claude-haiku-4-5": {}}).
				WithDependencyFailOpen(requestcontext.NewDependencyHealth(), requestcontext.DefaultPreparationLimits())
			request := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			ctx := tt.configure(authedCtx("00000000-0000-0000-0000-000000000001"), request)
			rec := httptest.NewRecorder()
			err := service.ProxyMessages(ctx, []byte(tt.body), rec, request)
			classification, ok := proxy.ClassifyDispatchError(err)
			require.True(t, ok)
			assert.Equal(t, http.StatusBadRequest, classification.Status)
			assert.Empty(t, provider.proxyBodies)
			assert.Empty(t, rec.Body.String())
		})
	}
}

type postCommitFailureProvider struct{ fakeProvider }

func (p *postCommitFailureProvider) Proxy(ctx context.Context, decision router.Decision, prepared providers.PreparedRequest, w http.ResponseWriter, r *http.Request) error {
	if err := p.fakeProvider.Proxy(ctx, decision, prepared, w, r); err != nil {
		return err
	}
	return requestcontext.PreparationFrom(ctx).Fail(requestcontext.DependencyDatabase, errors.New("late bookkeeping failure"))
}

func TestDependencyFallbackNeverReplaysCommittedOutput(t *testing.T) {
	fixture := readFixture(t, "anthropic/basic_text.upstream.sse")
	provider := &postCommitFailureProvider{fakeProvider: fakeProvider{proxyResponse: func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write(fixture)
	}}}
	service := proxy.NewService(&fakeRouter{decision: router.Decision{Provider: providers.ProviderAnthropic, Model: "claude-haiku-4-5"}}, map[string]providers.Client{providers.ProviderAnthropic: provider}, nil, false, nil, nil, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil).
		WithDependencyFailOpen(requestcontext.NewDependencyHealth(), requestcontext.DefaultPreparationLimits())
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	request.Header.Set(routingMarkerHeader, "off")
	rec := httptest.NewRecorder()
	_ = service.ProxyMessages(context.Background(), []byte(`{"model":"claude-opus-4-8","stream":true,"messages":[{"role":"user","content":"hi"}]}`), rec, request)
	require.Len(t, provider.proxyBodies, 1)
	assert.Contains(t, rec.Body.String(), "message_start")
	assert.Empty(t, rec.Header().Get(proxy.HeaderRouterFailOpenReason))
	assert.Equal(t, "claude-haiku-4-5", rec.Header().Get(proxy.HeaderRouterModel))
}
