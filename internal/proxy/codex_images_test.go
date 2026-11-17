package proxy_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/openai"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/subscriptions"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const allowedCodexQuota = `{"plan_type":"pro","rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":10,"reset_at":4102444800,"limit_window_seconds":18000}}}`

func newCodexImagesUpstream(t *testing.T, quota string, images http.HandlerFunc) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/wham/usage", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(quota)) })
	mux.HandleFunc("/codex/images/", images)
	upstream := httptest.NewServer(mux)
	t.Cleanup(upstream.Close)
	return upstream
}

func TestCodexImageRequest_ForwardsWithCallerOAuth(t *testing.T) {
	for _, operation := range []string{proxy.CodexImageGenerations, proxy.CodexImageEdits} {
		t.Run(operation, func(t *testing.T) {
			upstream := newCodexImagesUpstream(t, allowedCodexQuota, func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodPost, r.Method)
				assert.Equal(t, "/codex/images/"+operation, r.URL.Path)
				assert.Equal(t, "Bearer subscription-token", r.Header.Get("Authorization"))
				assert.Equal(t, "account-123", r.Header.Get("ChatGPT-Account-ID"))
				assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
				assert.Equal(t, "turn-1", r.Header.Get("X-Codex-Image-Turn-Id"))
				assert.Empty(t, r.Header.Get("X-Weave-Router-Key"))
				body, _ := io.ReadAll(r.Body)
				assert.JSONEq(t, `{"model":"gpt-image-2","prompt":"a red square"}`, string(body))
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"created":1,"data":[{"b64_json":"aW1n"}]}`))
			})
			client := openai.NewClient("deployment-key", "https://api.openai.com")
			client.SetCodexBaseURL(upstream.URL + "/codex")
			svc := proxy.NewService(&fakeRouter{}, map[string]providers.Client{providers.ProviderOpenAI: client}, nil, nil, false, providers.ProviderOpenAI, "gpt-5.6-sol", nil)
			headers := make(http.Header)
			headers.Set("Authorization", "Bearer subscription-token")
			headers.Set("ChatGPT-Account-ID", "account-123")
			headers.Set("Content-Type", "application/json")
			headers.Set("X-Codex-Image-Turn-Id", "turn-1")
			headers.Set("X-Weave-Router-Key", "rk-synthetic")
			rec := httptest.NewRecorder()

			err := proxy.NewCodexImages(svc).ForwardWithCallerCredential(context.Background(), headers, operation, []byte(`{"model":"gpt-image-2","prompt":"a red square"}`), rec)

			require.NoError(t, err)
			assert.Equal(t, http.StatusOK, rec.Code)
			assert.JSONEq(t, `{"created":1,"data":[{"b64_json":"aW1n"}]}`, rec.Body.String())
		})
	}
}

func TestCodexImageRequest_RelaysUpstreamError(t *testing.T) {
	upstream := newCodexImagesUpstream(t, allowedCodexQuota, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"usage limit"}}`))
	})
	client := openai.NewClient("deployment-key", "https://api.openai.com")
	client.SetCodexBaseURL(upstream.URL + "/codex")
	svc := proxy.NewService(&fakeRouter{}, map[string]providers.Client{providers.ProviderOpenAI: client}, nil, nil, false, providers.ProviderOpenAI, "gpt-5.6-sol", nil)
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer subscription-token")
	headers.Set("ChatGPT-Account-ID", "account-123")
	rec := httptest.NewRecorder()

	err := proxy.NewCodexImages(svc).ForwardWithCallerCredential(context.Background(), headers, proxy.CodexImageGenerations, []byte(`{}`), rec)

	var statusErr *providers.UpstreamStatusError
	require.ErrorAs(t, err, &statusErr)
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.Contains(t, rec.Body.String(), "usage limit")
}

func TestCodexImageRequest_EnforcesIncludedQuotaGate(t *testing.T) {
	exhausted := `{"rate_limit":{"allowed":false,"limit_reached":true,"primary_window":{"used_percent":100}},"credits":{"has_credits":true,"balance":"5"}}`
	upstream := newCodexImagesUpstream(t, exhausted, func(http.ResponseWriter, *http.Request) {
		t.Error("image requests must not reach upstream once included Codex quota is exhausted")
	})
	client := openai.NewClient("deployment-key", "https://api.openai.com")
	client.SetCodexBaseURL(upstream.URL + "/codex")
	svc := proxy.NewService(&fakeRouter{}, map[string]providers.Client{providers.ProviderOpenAI: client}, nil, nil, false, providers.ProviderOpenAI, "gpt-5.6-sol", nil)
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer subscription-token")
	headers.Set("ChatGPT-Account-ID", "account-123")

	err := proxy.NewCodexImages(svc).ForwardWithCallerCredential(context.Background(), headers, proxy.CodexImageGenerations, []byte(`{"model":"gpt-image-2"}`), httptest.NewRecorder())

	var quotaErr *providers.UpstreamErrorResponse
	require.ErrorAs(t, err, &quotaErr)
	assert.Equal(t, http.StatusTooManyRequests, quotaErr.Status)
}

func TestCodexImageRequest_ForwardsCallerAPIKeyToOpenAI(t *testing.T) {
	cases := map[string]struct {
		headers http.Header
		ctx     context.Context
		wantKey string
	}{
		"client API key": {headers: http.Header{"Authorization": {"Bearer sk-synthetic-client"}}, ctx: context.Background(), wantKey: "sk-synthetic-client"},
		"saved BYOK key": {headers: http.Header{}, ctx: context.WithValue(context.Background(), proxy.ExternalAPIKeysContextKey{}, []*auth.ExternalAPIKey{{Provider: providers.ProviderOpenAI, Plaintext: []byte("sk-synthetic-byok")}}), wantKey: "sk-synthetic-byok"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("/v1/images/edits", func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "Bearer "+tc.wantKey, r.Header.Get("Authorization"))
				assert.Empty(t, r.Header.Get("ChatGPT-Account-ID"))
				_, _ = w.Write([]byte(`{"created":1,"data":[]}`))
			})
			mux.HandleFunc("/", func(_ http.ResponseWriter, r *http.Request) {
				t.Errorf("API-key image requests must only reach the OpenAI images API, got %s", r.URL.Path)
			})
			upstream := httptest.NewServer(mux)
			defer upstream.Close()
			client := openai.NewClient("deployment-key", upstream.URL)
			client.SetCodexBaseURL(upstream.URL + "/codex")
			svc := proxy.NewService(&fakeRouter{}, map[string]providers.Client{providers.ProviderOpenAI: client}, nil, nil, false, providers.ProviderOpenAI, "gpt-5.6-sol", nil)
			rec := httptest.NewRecorder()

			err := proxy.NewCodexImages(svc).ForwardWithCallerCredential(tc.ctx, tc.headers, proxy.CodexImageEdits, []byte(`{"model":"gpt-image-2"}`), rec)

			require.NoError(t, err)
			assert.Equal(t, http.StatusOK, rec.Code)
		})
	}
}

func TestCodexImageRequest_RequiresCallerOwnCredential(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("image requests without the caller's own credential must not reach any upstream")
	}))
	defer upstream.Close()
	client := openai.NewClient("deployment-key", upstream.URL)
	client.SetCodexBaseURL(upstream.URL)
	svc := proxy.NewService(&fakeRouter{}, map[string]providers.Client{providers.ProviderOpenAI: client}, nil, nil, false, providers.ProviderOpenAI, "gpt-5.6-sol", nil)
	gatewayCtx := context.WithValue(context.Background(), proxy.ExternalAPIKeysContextKey{}, []*auth.ExternalAPIKey{{Provider: providers.ProviderOpenAIGateway, Plaintext: []byte("synthetic-gateway"), BaseURL: "https://gateway.example"}})
	cases := map[string]struct {
		ctx     context.Context
		headers http.Header
		want    error
	}{
		"no credentials":  {context.Background(), http.Header{}, proxy.ErrCodexImagesRequireOwnCredential},
		"router key only": {context.Background(), http.Header{"X-Weave-Router-Key": {"rk-synthetic"}}, proxy.ErrCodexImagesRequireOwnCredential},
		"gateway tenancy": {gatewayCtx, http.Header{"Authorization": {"Bearer subscription-token"}, "Chatgpt-Account-Id": {"account-123"}}, proxy.ErrCodexImagesUnavailableWithGateway},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := proxy.NewCodexImages(svc).ForwardWithCallerCredential(tc.ctx, tc.headers, proxy.CodexImageGenerations, []byte(`{}`), httptest.NewRecorder())
			require.ErrorIs(t, err, tc.want)
		})
	}
}

func TestCodexImageRequest_RejectsUnknownOperation(t *testing.T) {
	client := openai.NewClient("deployment-key", "https://api.openai.com")
	svc := proxy.NewService(&fakeRouter{}, map[string]providers.Client{providers.ProviderOpenAI: client}, nil, nil, false, providers.ProviderOpenAI, "gpt-5.6-sol", nil)
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer subscription-token")
	headers.Set("ChatGPT-Account-ID", "account-123")
	err := proxy.NewCodexImages(svc).ForwardWithCallerCredential(context.Background(), headers, "variations", []byte(`{}`), httptest.NewRecorder())
	require.Error(t, err)
}

type linkedCodexSeat struct {
	cooled      []string
	cooledUntil time.Time
}

func (l *linkedCodexSeat) Lease(context.Context, auth.SubscriptionOwner, subscriptions.Provider, string) (subscriptions.Lease, bool, error) {
	return subscriptions.Lease{AccountID: "seat-1", OwnerID: "owner-1", AccessToken: "linked-token", ProviderAccount: "linked-account", State: auth.SubscriptionAccountStateActive}, true, nil
}

func (l *linkedCodexSeat) Cooldown(_ context.Context, _ auth.SubscriptionOwner, _ subscriptions.Provider, accountID string, until time.Time) error {
	l.cooled = append(l.cooled, accountID)
	l.cooledUntil = until
	return nil
}

func (l *linkedCodexSeat) Disable(context.Context, auth.SubscriptionOwner, subscriptions.Provider, string) error {
	return nil
}

type codexImageTelemetry struct {
	proxy.TelemetryRepository
	rows chan proxy.InsertTelemetryParams
}

func (t *codexImageTelemetry) InsertRequestTelemetry(_ context.Context, p proxy.InsertTelemetryParams) error {
	t.rows <- p
	return nil
}

func TestCodexImageRequest_ServesOnLinkedCodexSubscription(t *testing.T) {
	for name, tc := range map[string]struct {
		status     int
		wantCooled []string
	}{
		"served":    {status: http.StatusOK},
		"throttled": {status: http.StatusTooManyRequests, wantCooled: []string{"seat-1"}},
	} {
		t.Run(name, func(t *testing.T) {
			upstream := newCodexImagesUpstream(t, allowedCodexQuota, func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "Bearer linked-token", r.Header.Get("Authorization"))
				assert.Equal(t, "linked-account", r.Header.Get("ChatGPT-Account-ID"))
				w.Header().Set("Retry-After", "600")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{"created":1,"data":[]}`))
			})
			client := openai.NewClient("deployment-key", upstream.URL)
			client.SetCodexBaseURL(upstream.URL + "/codex")
			seat := &linkedCodexSeat{}
			telemetry := &codexImageTelemetry{rows: make(chan proxy.InsertTelemetryParams, 1)}
			svc := proxy.NewService(&fakeRouter{}, map[string]providers.Client{providers.ProviderOpenAI: client}, nil, nil, false, providers.ProviderOpenAI, "gpt-5.6-sol", telemetry).WithManagedSubscriptions(seat)
			ctx := context.WithValue(context.Background(), proxy.InstallationIDContextKey{}, "00000000-0000-0000-0000-000000000001")
			ctx = proxy.WithManagedSubscriptionCandidates(proxy.WithManagedSubscriptionUsage(ctx), []*auth.SubscriptionAccount{{ID: "seat-1", Provider: auth.SubscriptionProvider(subscriptions.ProviderCodex), Enabled: true}})

			err := proxy.NewCodexImages(svc).ForwardWithCallerCredential(ctx, http.Header{"X-Weave-Router-Key": {"rk-synthetic"}}, proxy.CodexImageGenerations, []byte(`{"model":"gpt-image-2"}`), httptest.NewRecorder())

			if tc.status == http.StatusOK {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			assert.Equal(t, tc.wantCooled, seat.cooled)
			if tc.wantCooled != nil {
				assert.True(t, seat.cooledUntil.After(time.Now().Add(9*time.Minute)), "cooldown must honor the upstream Retry-After")
			}
			select {
			case row := <-telemetry.rows:
				assert.Equal(t, "gpt-image-2", row.DecisionModel)
				assert.Equal(t, int32(tc.status), row.UpstreamStatusCode)
				assert.Equal(t, "codex_subscription", row.CredentialSource)
				if tc.status == http.StatusOK {
					assert.Equal(t, "seat-1", row.SubscriptionAccountID)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("linked-subscription image call was not recorded")
			}
		})
	}
}

func TestCodexImageRequest_FallsBackToSavedKeyWhenLinkedCodexQuotaIsSpent(t *testing.T) {
	exhausted := `{"rate_limit":{"allowed":false,"limit_reached":true,"primary_window":{"used_percent":100}}}`
	var gotKey string
	upstream := newCodexImagesUpstream(t, exhausted, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("a spent linked Codex seat must not reach the image backend")
	})
	mux, ok := upstream.Config.Handler.(*http.ServeMux)
	require.True(t, ok)
	mux.HandleFunc("/v1/images/generations", func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"created":1,"data":[]}`))
	})
	client := openai.NewClient("deployment-key", upstream.URL)
	client.SetCodexBaseURL(upstream.URL + "/codex")
	telemetry := &codexImageTelemetry{rows: make(chan proxy.InsertTelemetryParams, 1)}
	svc := proxy.NewService(&fakeRouter{}, map[string]providers.Client{providers.ProviderOpenAI: client}, nil, nil, false, providers.ProviderOpenAI, "gpt-5.6-sol", telemetry).WithManagedSubscriptions(&linkedCodexSeat{})
	ctx := context.WithValue(context.Background(), proxy.InstallationIDContextKey{}, "00000000-0000-0000-0000-000000000001")
	ctx = context.WithValue(ctx, proxy.ExternalAPIKeysContextKey{}, []*auth.ExternalAPIKey{{Provider: providers.ProviderOpenAI, Plaintext: []byte("sk-synthetic-byok")}})
	ctx = proxy.WithManagedSubscriptionCandidates(proxy.WithManagedSubscriptionUsage(ctx), []*auth.SubscriptionAccount{{ID: "seat-1", Provider: auth.SubscriptionProvider(subscriptions.ProviderCodex), Enabled: true}})

	err := proxy.NewCodexImages(svc).ForwardWithCallerCredential(ctx, http.Header{}, proxy.CodexImageGenerations, []byte(`{"model":"gpt-image-2"}`), httptest.NewRecorder())

	require.NoError(t, err)
	assert.Equal(t, "Bearer sk-synthetic-byok", gotKey)
	select {
	case row := <-telemetry.rows:
		t.Fatalf("a seat that never reached upstream must not record usage, got status %d", row.UpstreamStatusCode)
	case <-time.After(200 * time.Millisecond):
	}
}
