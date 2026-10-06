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

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/anthropic"
	"weave-os/router/internal/proxy/usage"
	"weave-os/router/internal/router"
	"weave-os/router/internal/subscriptions"
)

func TestClaudeBypassDepletedCreditsRotatesLinkedAccounts(t *testing.T) {
	for _, classifier := range []bool{false, true} {
		for _, available := range []bool{false, true} {
			name := "usage-bypass"
			model := "claude-sonnet-4-6"
			maxTokens := 1024
			if classifier {
				name = "classifier"
				model = "claude-haiku-4-5"
				maxTokens = 5
			}
			if available {
				name += "/linked-capacity"
			} else {
				name += "/pool-exhausted"
			}
			t.Run(name, func(t *testing.T) {
				var tokens []string
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					token := r.Header.Get("Authorization")
					tokens = append(tokens, token)
					if token != "Bearer sk-ant-oat01-linked-healthy" {
						w.Header().Set("anthropic-ratelimit-unified-5h-status", "rejected")
						w.Header().Set("anthropic-ratelimit-unified-5h-reset", "4102444800")
						w.WriteHeader(http.StatusTooManyRequests)
						_, _ = io.WriteString(w, `{"type":"error","error":{"type":"rate_limit_error","message":"quota exhausted"}}`)
						return
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"id":"synthetic","type":"message","role":"assistant","model":"`+model+`","content":[{"type":"text","text":"linked answer"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":2}}`)
				}))
				defer upstream.Close()
				pool := &scriptedSubscriptionLeaser{}
				if available {
					pool.leases = []subscriptions.Lease{{AccountID: "spent", AccessToken: "sk-ant-oat01-linked-spent"}, {AccountID: "healthy", AccessToken: "sk-ant-oat01-linked-healthy"}}
				}
				svc := NewService(staticRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: codexCoveredModel, Reason: "test"}}, map[string]providers.Client{providers.ProviderAnthropic: anthropic.NewClient("synthetic-paid-key", upstream.URL)}, nil, false, nil, nil, false, providers.ProviderAnthropic, model, nil).
					WithManagedSubscriptions(pool).WithUsageObserver(usage.NewObserver([]byte("salt"), time.Minute, time.Now)).WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAnthropic: {}})
				ctx := context.WithValue(managedSubscriptionContext(auth.SubscriptionProviderClaude), AnthropicSubscriptionContextKey{}, "sk-ant-oat01-direct-spent")
				ctx = billing.WithSubscriptionOnly(ctx, billing.SubscriptionOnlyCreditsDepleted)
				if !classifier {
					ctx = context.WithValue(ctx, InstallationUsageBypassContextKey{}, UsageBypassConfig{Enabled: true})
				}
				body := `{"model":"` + model + `","max_tokens":` + strconv.Itoa(maxTokens) + `,"messages":[{"role":"user","content":"hello"}]}`
				rec := httptest.NewRecorder()
				err := svc.ProxyMessages(ctx, []byte(body), rec, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body)))
				if available {
					require.NoError(t, err)
					require.Contains(t, rec.Body.String(), "linked answer")
					require.Equal(t, []string{"Bearer sk-ant-oat01-direct-spent", "Bearer sk-ant-oat01-linked-spent", "Bearer sk-ant-oat01-linked-healthy"}, tokens)
					require.Equal(t, model, rec.Header().Get(HeaderRouterModel))
					wantReason := reasonUsageBypass
					if classifier {
						wantReason = reasonClassifierPassthrough
					}
					require.Equal(t, wantReason, rec.Header().Get(HeaderRouterDecision))
					require.True(t, managedSubscriptionServed(ctx))
				} else {
					require.Error(t, err)
					require.Equal(t, []string{"Bearer sk-ant-oat01-direct-spent"}, tokens)
					require.NotContains(t, rec.Body.String(), "linked answer")
				}
			})
		}
	}
}
