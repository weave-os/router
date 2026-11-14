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
	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/anthropic"
	"weave-os/router/internal/providers/openai"
	"weave-os/router/internal/proxy/usage"
	"weave-os/router/internal/router"
	"weave-os/router/internal/subscriptions"
	"weave-os/router/internal/translate"
)

const ownClaudePlanSpentPhrase = "your own Claude plan is out of usage"

func linkedSeatNudgeCases() []struct {
	name      string
	seatState auth.SubscriptionAccountState
	leases    []subscriptions.Lease
	wantNudge bool
} {
	return []struct {
		name      string
		seatState auth.SubscriptionAccountState
		leases    []subscriptions.Lease
		wantNudge bool
	}{
		{name: "teammate seat covers the turn", seatState: auth.SubscriptionAccountStateActive, leases: []subscriptions.Lease{{AccountID: "teammate", AccessToken: "sk-ant-oat01-teammate", ProviderAccount: "teammate-provider", Tier: auth.SubscriptionTierShared}}, wantNudge: false},
		{name: "every teammate seat is spent", seatState: auth.SubscriptionAccountStateExhausted, wantNudge: true},
	}
}

func TestSpentOwnClaudePlanNudgesOnlyWithoutACoveringLinkedSeat(t *testing.T) {
	const ownToken = "sk-ant-oat01-own-spent"
	for _, tc := range linkedSeatNudgeCases() {
		t.Run(tc.name, func(t *testing.T) {
			var tokens []string
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				tokens = append(tokens, r.Header.Get("Authorization")+r.Header.Get("X-Api-Key"))
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "event: message_start\ndata: "+`{"type":"message_start","message":{"id":"synthetic","usage":{"input_tokens":3}}}`+"\n\n")
				_, _ = io.WriteString(w, "event: content_block_start\ndata: "+`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`+"\n\n")
				_, _ = io.WriteString(w, "event: content_block_delta\ndata: "+`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"answer"}}`+"\n\n")
				_, _ = io.WriteString(w, "event: content_block_stop\ndata: "+`{"type":"content_block_stop","index":0}`+"\n\n")
				_, _ = io.WriteString(w, "event: message_delta\ndata: "+`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`+"\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
			}))
			defer upstream.Close()
			observer := usage.NewObserver([]byte("salt"), time.Hour, time.Now)
			observer.Record(observer.Key([]byte(ownToken)), usage.Snapshot{Secondary: usage.Window{UsedPercent: 1.0, WindowMinutes: 10080}})
			model := "claude-sonnet-4-6"
			svc := NewService(staticRouter{decision: router.Decision{Provider: providers.ProviderAnthropic, Model: model, Reason: "test"}}, map[string]providers.Client{providers.ProviderAnthropic: anthropic.NewClient("synthetic-paid-key", upstream.URL)}, nil, nil, false, providers.ProviderAnthropic, model, nil).
				WithManagedSubscriptions(&scriptedSubscriptionLeaser{leases: tc.leases, repeatLast: true}).
				WithUsageObserver(observer).
				WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAnthropic: {}})
			resetAt := time.Now().Add(time.Hour)
			ctx := WithManagedSubscriptionCandidates(managedSubscriptionContext(auth.SubscriptionProviderClaude), []*auth.SubscriptionAccount{{ID: "teammate", Provider: auth.SubscriptionProviderClaude, Enabled: true, State: tc.seatState, CooldownUntil: &resetAt}})
			ctx = context.WithValue(ctx, AnthropicSubscriptionContextKey{}, ownToken)
			body := `{"model":"` + model + `","stream":true,"max_tokens":1024,"messages":[{"role":"user","content":"hello"}]}`
			rec := httptest.NewRecorder()
			require.NoError(t, svc.ProxyMessages(ctx, []byte(body), rec, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))))
			require.Contains(t, rec.Body.String(), "answer")
			require.NotContains(t, strings.Join(tokens, ","), ownToken, "the spent plan is not forwarded")
			if tc.wantNudge {
				require.Contains(t, rec.Body.String(), ownClaudePlanSpentPhrase)
				return
			}
			require.Equal(t, []string{"Bearer sk-ant-oat01-teammate"}, tokens)
			require.NotContains(t, rec.Body.String(), ownClaudePlanSpentPhrase, "the turn stays on a subscription")
		})
	}
}

func TestSpentOwnCodexPlanNudgesOnlyWithoutACoveringLinkedSeat(t *testing.T) {
	for _, tc := range linkedSeatNudgeCases() {
		t.Run(tc.name, func(t *testing.T) {
			var bearers []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				bearer := r.Header.Get("Authorization")
				if r.URL.Path == "/wham/usage" {
					if bearer == "Bearer "+codexTestToken {
						_, _ = io.WriteString(w, `{"plan_type":"team","rate_limit":{"allowed":false,"limit_reached":true,"primary_window":{"used_percent":100,"reset_at":`+
							strconv.FormatInt(time.Now().Add(3*time.Hour).Unix(), 10)+`}}}`)
						return
					}
					_, _ = io.WriteString(w, `{"rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":10}}}`)
					return
				}
				bearers = append(bearers, bearer)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":\"answer\"}\n\n")
				_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"synthetic\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":11,\"output_tokens\":7}}}\n\n")
			}))
			defer server.Close()
			client := openai.NewClient("weave-capacity", server.URL)
			client.SetCodexBaseURL(server.URL)
			leases := make([]subscriptions.Lease, 0, len(tc.leases))
			for _, lease := range tc.leases {
				lease.AccessToken = "pool-seat"
				leases = append(leases, lease)
			}
			svc := NewService(staticRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: codexCoveredModel, Reason: "test"}}, map[string]providers.Client{providers.ProviderOpenAI: client}, nil, nil, false, providers.ProviderOpenAI, codexCoveredModel, nil).
				WithManagedSubscriptions(&scriptedSubscriptionLeaser{leases: leases, repeatLast: true}).
				WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderOpenAI: {}}).
				WithUsageObserver(usage.NewObserver([]byte("test-salt"), time.Hour, time.Now))
			resetAt := time.Now().Add(time.Hour)
			body := `{"model":"auto","stream":true,"messages":[{"role":"user","content":"synthetic"}],"tools":[{"type":"function","function":{"name":"read_file","parameters":{"type":"object"}}}]}`
			// The first turn learns the plan is spent; the second starts from that memory.
			var responses []string
			for turn := 0; turn < 2; turn++ {
				ctx := context.WithValue(managedSubscriptionContext(auth.SubscriptionProviderCodex), InstallationIDContextKey{}, "11111111-1111-1111-1111-111111111111")
				ctx = WithManagedSubscriptionCandidates(ctx, []*auth.SubscriptionAccount{{ID: "teammate", Provider: auth.SubscriptionProviderCodex, State: tc.seatState, Enabled: true, CooldownUntil: &resetAt}})
				rec := httptest.NewRecorder()
				require.NoError(t, svc.ProxyOpenAIChatCompletion(ctx, []byte(body), rec, codexSubHTTPRequest("/v1/chat/completions", body)))
				require.Contains(t, rec.Body.String(), "answer")
				responses = append(responses, rec.Body.String())
			}
			if tc.wantNudge {
				require.Contains(t, responses[1], "your own ChatGPT (Codex) plan is out of usage")
				return
			}
			require.Equal(t, "Bearer pool-seat", bearers[len(bearers)-1])
			require.NotContains(t, strings.Join(responses, ""), "your own ChatGPT (Codex) plan is out of usage", "the turn stays on a subscription")
		})
	}
}

func TestSpentOwnClaudePlanNudgesWhenTheLastLinkedSeatRunsOutMidTurn(t *testing.T) {
	const ownToken = "sk-ant-oat01-own-spent"
	var tokens []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := r.Header.Get("Authorization") + r.Header.Get("X-Api-Key")
		tokens = append(tokens, token)
		if token == "Bearer sk-ant-oat01-teammate" {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("anthropic-ratelimit-unified-status", "rejected")
			w.Header().Set("anthropic-ratelimit-unified-5h-utilization", "1.0")
			w.Header().Set("anthropic-ratelimit-unified-5h-reset", time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"type":"error","error":{"type":"rate_limit_error","message":"usage limit reached"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: message_start\ndata: "+`{"type":"message_start","message":{"id":"synthetic","usage":{"input_tokens":3}}}`+"\n\n")
		_, _ = io.WriteString(w, "event: content_block_start\ndata: "+`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`+"\n\n")
		_, _ = io.WriteString(w, "event: content_block_delta\ndata: "+`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"answer"}}`+"\n\n")
		_, _ = io.WriteString(w, "event: content_block_stop\ndata: "+`{"type":"content_block_stop","index":0}`+"\n\n")
		_, _ = io.WriteString(w, "event: message_delta\ndata: "+`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`+"\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer upstream.Close()
	observer := usage.NewObserver([]byte("salt"), time.Hour, time.Now)
	observer.Record(observer.Key([]byte(ownToken)), usage.Snapshot{Secondary: usage.Window{UsedPercent: 1.0, WindowMinutes: 10080}})
	model := "claude-sonnet-4-6"
	svc := NewService(staticRouter{decision: router.Decision{Provider: providers.ProviderAnthropic, Model: model, Reason: "test"}}, map[string]providers.Client{providers.ProviderAnthropic: anthropic.NewClient("synthetic-paid-key", upstream.URL)}, nil, nil, false, providers.ProviderAnthropic, model, nil).
		WithManagedSubscriptions(&scriptedSubscriptionLeaser{leases: []subscriptions.Lease{{AccountID: "teammate", AccessToken: "sk-ant-oat01-teammate", ProviderAccount: "teammate-provider", Tier: auth.SubscriptionTierShared}}}).
		WithUsageObserver(observer).
		WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAnthropic: {}})
	ctx := WithManagedSubscriptionCandidates(managedSubscriptionContext(auth.SubscriptionProviderClaude), []*auth.SubscriptionAccount{{ID: "teammate", Provider: auth.SubscriptionProviderClaude, Enabled: true, State: auth.SubscriptionAccountStateActive}})
	ctx = context.WithValue(ctx, AnthropicSubscriptionContextKey{}, ownToken)
	body := `{"model":"` + model + `","stream":true,"max_tokens":1024,"messages":[{"role":"user","content":"hello"}]}`
	rec := httptest.NewRecorder()
	require.NoError(t, svc.ProxyMessages(ctx, []byte(body), rec, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))))
	require.Contains(t, rec.Body.String(), "answer")
	require.Equal(t, []string{"Bearer sk-ant-oat01-teammate", "synthetic-paid-key"}, tokens, "the spent seat hands the turn to paid capacity")
	require.Contains(t, rec.Body.String(), ownClaudePlanSpentPhrase)
}

func TestSpentOwnCodexPlanNudgesWhenTheLastLinkedSeatRunsOutMidTurn(t *testing.T) {
	for _, ingress := range []string{"chat", "responses"} {
		t.Run(ingress, func(t *testing.T) {
			turn := 0
			var bearers []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				bearer := r.Header.Get("Authorization")
				if r.URL.Path == "/wham/usage" {
					if bearer == "Bearer "+codexTestToken {
						_, _ = io.WriteString(w, `{"plan_type":"team","rate_limit":{"allowed":false,"limit_reached":true,"primary_window":{"used_percent":100,"reset_at":`+
							strconv.FormatInt(time.Now().Add(3*time.Hour).Unix(), 10)+`}}}`)
						return
					}
					_, _ = io.WriteString(w, `{"rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":10}}}`)
					return
				}
				bearers = append(bearers, bearer)
				if bearer == "Bearer pool-seat" && turn == 1 {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusTooManyRequests)
					_, _ = io.WriteString(w, `{"error":{"type":"usage_limit_reached","message":"usage limit reached","resets_at":`+strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)+`}}`)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":\"answer\"}\n\n")
				_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"synthetic\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":11,\"output_tokens\":7}}}\n\n")
			}))
			defer server.Close()
			client := openai.NewClient("weave-capacity", server.URL)
			client.SetCodexBaseURL(server.URL)
			observer := usage.NewObserver([]byte("test-salt"), time.Hour, time.Now)
			observer.Record(observer.Key([]byte(codexTestToken)), usage.Snapshot{Primary: usage.Window{UsedPercent: 1, WindowMinutes: codexQuotaWindowMinutes, ResetAt: time.Now().Add(3 * time.Hour)}})
			svc := NewService(staticRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: codexCoveredModel, Reason: "test"}}, map[string]providers.Client{providers.ProviderOpenAI: client}, nil, nil, false, providers.ProviderOpenAI, codexCoveredModel, nil).
				WithManagedSubscriptions(&scriptedSubscriptionLeaser{leases: []subscriptions.Lease{{AccountID: "teammate", AccessToken: "pool-seat", ProviderAccount: "teammate-provider", Tier: auth.SubscriptionTierShared}}, repeatLast: true}).
				WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderOpenAI: {}}).
				WithUsageObserver(observer)
			path, body := "/v1/chat/completions", `{"model":"auto","stream":true,"messages":[{"role":"user","content":"synthetic"}],"tools":[{"type":"function","function":{"name":"read_file","parameters":{"type":"object"}}}]}`
			if ingress == "responses" {
				path, body = "/v1/responses", `{"model":"auto","stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"synthetic"}]}],"tools":[{"type":"function","name":"read_file","parameters":{"type":"object"}}]}`
			}
			// The caller's plan is already known spent. The first turn uses the
			// linked seat; the second sees that seat run out mid-turn.
			var turnBodies []string
			for turn = 0; turn < 2; turn++ {
				ctx := context.WithValue(managedSubscriptionContext(auth.SubscriptionProviderCodex), InstallationIDContextKey{}, "11111111-1111-1111-1111-111111111111")
				ctx = WithManagedSubscriptionCandidates(ctx, []*auth.SubscriptionAccount{{ID: "teammate", Provider: auth.SubscriptionProviderCodex, State: auth.SubscriptionAccountStateActive, Enabled: true}})
				rec := httptest.NewRecorder()
				if ingress == "responses" {
					ctx = context.WithValue(ctx, ClientIdentityContextKey{}, ClientIdentity{ClientApp: ClientAppCodex})
					require.NoError(t, svc.ProxyOpenAIResponses(ctx, []byte(body), rec, codexSubHTTPRequest(path, body)))
				} else {
					require.NoError(t, svc.ProxyOpenAIChatCompletion(ctx, []byte(body), rec, codexSubHTTPRequest(path, body)))
				}
				require.Contains(t, rec.Body.String(), "answer")
				turnBodies = append(turnBodies, rec.Body.String())
			}
			require.Equal(t, []string{"Bearer pool-seat", "Bearer pool-seat", "Bearer weave-capacity"}, bearers, "the spent seat hands the second turn to paid capacity")
			require.NotContains(t, turnBodies[0], "your own ChatGPT (Codex) plan is out of usage")
			require.Contains(t, turnBodies[1], "your own ChatGPT (Codex) plan is out of usage")
		})
	}
}

func TestDeferredReconnectNudgeShowsOnAPaidTurnWithoutARoutingMarker(t *testing.T) {
	ctx := withDeferredReconnectNudge(context.Background(), ownClaudePlanSpentNudge)
	unmarked := translate.NewRouterNotice(translate.NoticeModelSelection, "", "claude-sonnet-4-6", "")
	notice := reconnectNoticeForAttempt(ctx, http.Header{}, unmarked)
	require.Equal(t, translate.NoticeSubscriptionReconnect, notice.Kind)
	require.Equal(t, ownClaudePlanSpentNudge, notice.Text)
	onSeat := context.WithValue(ctx, CredentialsContextKey{}, &Credentials{OAuth: true})
	require.Equal(t, unmarked, reconnectNoticeForAttempt(onSeat, http.Header{}, unmarked), "a linked seat keeps the turn on a subscription")
	optedOut := http.Header{routingMarkerHeader: []string{"off"}}
	require.Empty(t, reconnectNoticeForAttempt(ctx, optedOut, unmarked).Text, "an explicit marker opt-out still hides the nudge")
}
