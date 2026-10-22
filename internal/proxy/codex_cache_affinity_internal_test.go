package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/openai"
	"weave-os/router/internal/router"
	"weave-os/router/internal/subscriptions"
)

func TestCodexSubscriptionIngressKeepsCacheAffinityAcrossTurns(t *testing.T) {
	const session = "01a12cac-1d76-7841-99e5-1df54a84a631"
	fixtures := []struct {
		name  string
		app   string
		turns []string
		run   func(*Service, context.Context, []byte, http.ResponseWriter, *http.Request) error
	}{
		{"messages", ClientAppClaudeCode, []string{
			`{"model":"auto","max_tokens":1024,"stream":true,"messages":[{"role":"user","content":"synthetic first turn"}]}`,
			`{"model":"auto","max_tokens":1024,"stream":true,"messages":[{"role":"user","content":"synthetic first turn"},{"role":"assistant","content":"synthetic answer"},{"role":"user","content":"synthetic second turn"}]}`,
		}, (*Service).ProxyMessages},
		{"chat", ClientAppCodex, []string{
			`{"model":"auto","stream":true,"messages":[{"role":"user","content":"synthetic first turn"}]}`,
			`{"model":"auto","stream":true,"messages":[{"role":"user","content":"synthetic first turn"},{"role":"assistant","content":"synthetic answer"},{"role":"user","content":"synthetic second turn"}]}`,
		}, (*Service).ProxyOpenAIChatCompletion},
		{"responses", ClientAppCodex, []string{
			`{"model":"auto","stream":true,"prompt_cache_key":"` + session + `","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"synthetic first turn"}]}]}`,
			`{"model":"auto","stream":true,"prompt_cache_key":"` + session + `","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"synthetic first turn"}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"synthetic answer"}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"synthetic second turn"}]}]}`,
		}, (*Service).ProxyOpenAIResponses},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			var mu sync.Mutex
			var sessionIDs, bodyKeys []string
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if serveSyntheticCodexQuota(w, r) {
					return
				}
				body, _ := io.ReadAll(r.Body)
				mu.Lock()
				sessionIDs = append(sessionIDs, r.Header.Get("Session-Id"))
				bodyKeys = append(bodyKeys, gjson.GetBytes(body, "prompt_cache_key").String())
				mu.Unlock()
				w.Header().Set("Content-Type", "text/event-stream")
				for _, frame := range []string{
					`{"type":"response.output_text.delta","output_index":0,"delta":"synthetic answer"}`,
					`{"type":"response.completed","response":{"id":"synthetic","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"synthetic answer"}]}],"usage":{"input_tokens":1,"output_tokens":1}}}`,
				} {
					_, _ = io.WriteString(w, "data: "+frame+"\n\n")
				}
			}))
			defer upstream.Close()
			client := openai.NewClient("", upstream.URL)
			client.SetCodexBaseURL(upstream.URL)
			lease := subscriptions.Lease{AccountID: "10000000-0000-4000-8000-000000000001", OwnerID: "10000000-0000-4000-8000-000000000002", Tier: auth.SubscriptionTierShared, AccessToken: "synthetic-included-token", ProviderAccount: "synthetic-provider-account"}
			svc := NewService(staticRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: codexCoveredModel, Reason: "test"}}, map[string]providers.Client{providers.ProviderOpenAI: client}, nil, false, nil, newRowBackedPinStore(), false, providers.ProviderOpenAI, codexCoveredModel, nil).
				WithManagedSubscriptions(&scriptedSubscriptionLeaser{leases: []subscriptions.Lease{lease}, repeatLast: true})
			for _, body := range fixture.turns {
				ctx := context.WithValue(managedSubscriptionContext(auth.SubscriptionProviderCodex), InstallationIDContextKey{}, "11111111-1111-1111-1111-111111111111")
				ctx = context.WithValue(ctx, ClientIdentityContextKey{}, ClientIdentity{ClientApp: fixture.app, SessionID: session})
				req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
				require.NoError(t, fixture.run(svc, ctx, []byte(body), httptest.NewRecorder(), req))
			}
			mu.Lock()
			defer mu.Unlock()
			require.Len(t, sessionIDs, len(fixture.turns))
			require.NotEmpty(t, sessionIDs[0], "Codex OAuth caches on Session-Id, not the body hint alone")
			for i := range sessionIDs {
				require.Equal(t, sessionIDs[0], sessionIDs[i], "cache affinity must survive across turns")
				require.Equal(t, bodyKeys[i], sessionIDs[i])
			}
		})
	}
}
