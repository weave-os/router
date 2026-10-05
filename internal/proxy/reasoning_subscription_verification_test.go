package proxy

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"github.com/stretchr/testify/require"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/openai"
	"weave-os/router/internal/router"
	"weave-os/router/internal/subscriptions"
)

func TestVerificationOpaqueReasoningWireScope(t *testing.T) {
	for _, scenario := range []struct {
		name, providerAccount string
		rotated, modelChanged bool
		wantOpaque            bool
	}{{name: "same-principal-refreshed-token", providerAccount: "reasoning-provider", wantOpaque: true}, {name: "rotation-to-other-owner", providerAccount: "other-provider", rotated: true}, {name: "different-model", providerAccount: "reasoning-provider", modelChanged: true}} {
		t.Run(scenario.name, func(t *testing.T) {
			var sent [][]byte
			var bearers []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				payload, _ := io.ReadAll(r.Body)
				sent = append(sent, payload)
				bearers = append(bearers, r.Header.Get("Authorization"))
				if scenario.rotated && len(sent) == 1 {
					w.WriteHeader(429)
					_, _ = io.WriteString(w, `{"error":{"code":"usage_limit_reached"}}`)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":\"reasoning answer\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"synthetic\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":11,\"output_tokens\":7}}}\n\n")
			}))
			defer server.Close()
			client := &includedOnlySyntheticClient{Client: openai.NewClient("", server.URL)}
			client.SetCodexBaseURL(server.URL)
			leases := []subscriptions.Lease{{AccountID: "reasoning-account", OwnerID: "reasoning-owner", AccessToken: "refreshed-reasoning-token", ProviderAccount: scenario.providerAccount}}
			if scenario.rotated {
				leases = []subscriptions.Lease{{AccountID: "original-account", OwnerID: "original-owner", AccessToken: "original-reasoning-token", ProviderAccount: "reasoning-provider"}, {AccountID: "new-account", OwnerID: "new-owner", AccessToken: "new-owner-token", ProviderAccount: "other-provider"}}
			}
			model := codexCoveredModel
			if scenario.modelChanged {
				model = "gpt-6-sol"
			}
			svc := NewService(staticRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: model, Reason: "test"}}, map[string]providers.Client{providers.ProviderOpenAI: client}, nil, false, nil, nil, false, providers.ProviderOpenAI, model, nil).WithManagedSubscriptions(&scriptedSubscriptionLeaser{leases: leases})
			// Mint a protocol envelope for the old stable provider principal/model; token differs from current bearer.
			oldCtx := context.WithValue(context.Background(), CredentialsContextKey{}, &Credentials{APIKey: []byte("old-access-token"), AccountID: []byte("reasoning-provider"), OAuth: true, Source: credSourceCodexSubscription, PrincipalID: "chatgpt-account:reasoning-provider"})
			scope := svc.reasoningReplayScope(oldCtx, router.Decision{Provider: providers.ProviderOpenAI, Model: codexCoveredModel})
			envelope, err := json.Marshal(map[string]any{"v": 1, "provider": "openai", "id": "synthetic-reasoning-id", "enc": "opaque-prior-owner-reasoning", "scope": scope})
			require.NoError(t, err)
			signature := base64.StdEncoding.EncodeToString(envelope)
			body := `{"model":"auto","stream":true,"max_tokens":1000,"thinking":{"type":"enabled","budget_tokens":8192},"messages":[{"role":"user","content":"synthetic"},{"role":"assistant","content":[{"type":"thinking","thinking":"summary","signature":` + strconv.Quote(signature) + `},{"type":"text","text":"continue"}]}],"tools":[{"name":"read_file","input_schema":{"type":"object"}}]}`
			ctx := context.WithValue(managedSubscriptionContext(auth.SubscriptionProviderCodex), InstallationIDContextKey{}, "11111111-1111-1111-1111-111111111111")
			rec := httptest.NewRecorder()
			require.NoError(t, svc.ProxyMessages(ctx, []byte(body), rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))))
			require.Contains(t, rec.Body.String(), "reasoning answer")
			winningPayload := string(sent[len(sent)-1])
			if scenario.wantOpaque {
				require.Contains(t, winningPayload, "opaque-prior-owner-reasoning")
				require.Equal(t, []string{"Bearer refreshed-reasoning-token"}, bearers)
			} else {
				require.NotContains(t, winningPayload, "opaque-prior-owner-reasoning")
				if scenario.rotated {
					require.Contains(t, string(sent[0]), "opaque-prior-owner-reasoning")
					require.Equal(t, []string{"Bearer original-reasoning-token", "Bearer new-owner-token"}, bearers)
				}
			}
		})
	}
}
