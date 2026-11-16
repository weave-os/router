package proxy_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/openai"
	"weave-os/router/internal/proxy"

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

			err := proxy.NewCodexImages(svc).ForwardWithCallerChatGPTLogin(context.Background(), headers, operation, []byte(`{"model":"gpt-image-2","prompt":"a red square"}`), rec)

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

	err := proxy.NewCodexImages(svc).ForwardWithCallerChatGPTLogin(context.Background(), headers, proxy.CodexImageGenerations, []byte(`{}`), rec)

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

	err := proxy.NewCodexImages(svc).ForwardWithCallerChatGPTLogin(context.Background(), headers, proxy.CodexImageGenerations, []byte(`{"model":"gpt-image-2"}`), httptest.NewRecorder())

	var quotaErr *providers.UpstreamErrorResponse
	require.ErrorAs(t, err, &quotaErr)
	assert.Equal(t, http.StatusTooManyRequests, quotaErr.Status)
}

func TestCodexImageRequest_RequiresCallerChatGPTLogin(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("image requests without the caller's ChatGPT login must not reach any upstream")
	}))
	defer upstream.Close()
	client := openai.NewClient("deployment-key", upstream.URL)
	client.SetCodexBaseURL(upstream.URL)
	svc := proxy.NewService(&fakeRouter{}, map[string]providers.Client{providers.ProviderOpenAI: client}, nil, nil, false, providers.ProviderOpenAI, "gpt-5.6-sol", nil)
	for name, headers := range map[string]http.Header{
		"no credentials":        {},
		"token without account": {"Authorization": {"Bearer subscription-token"}},
		"api key":               {"Authorization": {"Bearer sk-synthetic"}},
	} {
		t.Run(name, func(t *testing.T) {
			err := proxy.NewCodexImages(svc).ForwardWithCallerChatGPTLogin(context.Background(), headers, proxy.CodexImageGenerations, []byte(`{}`), httptest.NewRecorder())
			require.ErrorIs(t, err, proxy.ErrCodexImagesRequireChatGPT)
		})
	}
}

func TestCodexImageRequest_RejectsUnknownOperation(t *testing.T) {
	client := openai.NewClient("deployment-key", "https://api.openai.com")
	svc := proxy.NewService(&fakeRouter{}, map[string]providers.Client{providers.ProviderOpenAI: client}, nil, nil, false, providers.ProviderOpenAI, "gpt-5.6-sol", nil)
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer subscription-token")
	headers.Set("ChatGPT-Account-ID", "account-123")
	err := proxy.NewCodexImages(svc).ForwardWithCallerChatGPTLogin(context.Background(), headers, "variations", []byte(`{}`), httptest.NewRecorder())
	require.Error(t, err)
}
