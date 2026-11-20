package proxy_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/proxy/usage"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/hmm"

	"github.com/stretchr/testify/assert"
)

const laneAdmissionCodexToken = "eyJhbGciOiJub25lIn0.eyJzdWIiOiJ0ZXN0In0.signature"

func codexBypassFixture(t *testing.T, threshold float64, snapshot usage.Snapshot) (*proxy.Service, *fakeRouter, context.Context) {
	t.Helper()
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: "gpt-5.6-terra"}}
	p := &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion","choices":[]}`))
	}}
	obs := usage.NewObserver([]byte("salt"), 10*time.Minute, time.Now)
	obs.Record(obs.Key([]byte(laneAdmissionCodexToken)), snapshot)
	svc := proxy.WithStrategyPorts(proxy.NewService(fr, map[string]providers.Client{providers.ProviderOpenAI: p}, nil, nil, false, providers.ProviderOpenAI, "gpt-5.6-terra", nil), hmm.ServingPorts()).
		WithUsageObserver(obs)
	ctx := context.WithValue(context.Background(), proxy.OpenAISubscriptionContextKey{}, laneAdmissionCodexToken)
	ctx = context.WithValue(ctx, proxy.OpenAIAccountIDContextKey{}, "account-1")
	ctx = context.WithValue(ctx, proxy.InstallationUsageBypassContextKey{}, proxy.UsageBypassConfig{Enabled: true, Threshold: &threshold})
	return svc, fr, ctx
}

func proxyCodexChat(t *testing.T, svc *proxy.Service, ctx context.Context, model string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/v1/chat/completions", strings.NewReader(""))
	_ = svc.ProxyOpenAIChatCompletion(ctx, []byte(`{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`), rec, req)
	return rec
}

func TestUsageBypassSkipsACodexSubscriptionOnOverage(t *testing.T) {
	svc, fr, ctx := codexBypassFixture(t, 0.80, usage.Snapshot{
		Primary:      usage.Window{UsedPercent: 0.20, WindowMinutes: 300},
		OverageInUse: true,
	})

	rec := proxyCodexChat(t, svc, ctx, "gpt-5.6-sol")

	assert.Equal(t, 1, fr.routeCalls, "a Codex plan drawing billable overage must not be bypassed onto")
	assert.NotEqual(t, "usage_bypass", rec.Header().Get(proxy.HeaderRouterDecision))
}

func TestUsageBypassSkipsACodexSubscriptionThatIsSpent(t *testing.T) {
	svc, fr, ctx := codexBypassFixture(t, 1, usage.Snapshot{
		Primary: usage.Window{UsedPercent: 0.9995, WindowMinutes: 300},
	})

	rec := proxyCodexChat(t, svc, ctx, "gpt-5.6-sol")

	assert.Equal(t, 1, fr.routeCalls, "a spent Codex plan rejects the token, so the bypass must not engage")
	assert.NotEqual(t, "usage_bypass", rec.Header().Get(proxy.HeaderRouterDecision))
}

func TestUsageBypassRoutesAnOpenAIModelTheCodexPlanDoesNotCover(t *testing.T) {
	svc, fr, ctx := codexBypassFixture(t, 0.80, usage.Snapshot{
		Primary: usage.Window{UsedPercent: 0.20, WindowMinutes: 300},
	})

	rec := proxyCodexChat(t, svc, ctx, "gpt-5.5")

	assert.Equal(t, 1, fr.routeCalls, "a Codex subscription only covers its plan's models; others must route")
	assert.NotEqual(t, "usage_bypass", rec.Header().Get(proxy.HeaderRouterDecision))
}
