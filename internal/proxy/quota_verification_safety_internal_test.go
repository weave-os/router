package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/openai"
	"weave-os/router/internal/subscriptions"
	"weave-os/router/internal/translate"
)

const quotaSafetyResponseSSE = "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"synthetic\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"served answer\"}]}],\"usage\":{\"input_tokens\":12,\"output_tokens\":3}}}\n\n"

func TestQuotaVerificationFailureNeverBuysPaidCapacity(t *testing.T) {
	for _, failure := range []struct {
		name   string
		status int
		body   string
		stall  bool
	}{
		{name: "endpoint 504", status: http.StatusGatewayTimeout},
		{name: "unverifiable quota", status: http.StatusOK, body: `{}`},
		{name: "transport timeout", stall: true},
	} {
		for _, managed := range []bool{false, true} {
			for _, funding := range []struct {
				name   string
				reason billing.SubscriptionOnlyReason
			}{
				{name: "funded"},
				{name: "depleted", reason: billing.SubscriptionOnlyCreditsDepleted},
				{name: "spend capped", reason: billing.SubscriptionOnlySpendCapReached},
				{name: "linked first", reason: billing.SubscriptionOnlyLinkedFirst},
			} {
				credentialName := "direct"
				if managed {
					credentialName = "managed"
				}
				t.Run(failure.name+"/"+credentialName+"/"+funding.name, func(t *testing.T) {
					var preflightCalls, includedCalls, paidCalls atomic.Int32
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
						if req.URL.Path == "/wham/usage" {
							preflightCalls.Add(1)
							if failure.stall {
								<-req.Context().Done()
								return
							}
							w.WriteHeader(failure.status)
							_, _ = io.WriteString(w, failure.body)
							return
						}
						if req.Header.Get("Authorization") == "Bearer synthetic-paid-key" {
							paidCalls.Add(1)
						} else {
							includedCalls.Add(1)
						}
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = io.WriteString(w, quotaSafetyResponseSSE)
					}))
					defer server.Close()
					client := openai.NewClientWithResponseHeaderTimeout("synthetic-paid-key", server.URL, 20*time.Millisecond)
					client.SetCodexBaseURL(server.URL)
					repo := &auxBillingRepo{}
					svc := outOfRosterFundingService(client, translate.ReasonUserForceModel, repo)
					ctx := codexSubscriptionTestCtx()
					body := `{"model":"codex-auto-review","stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"synthetic"}]}]}`
					request := codexSubHTTPRequest("/v1/responses", body)
					if managed {
						ctx = WithManagedSubscriptionCandidates(managedSubscriptionContext(auth.SubscriptionProviderCodex), []*auth.SubscriptionAccount{{
							ID: "synthetic-seat", Provider: auth.SubscriptionProviderCodex, Enabled: true, State: auth.SubscriptionAccountStateActive,
						}})
						ctx = context.WithValue(ctx, InstallationIDContextKey{}, "33333333-3333-3333-3333-333333333333")
						request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
						svc.WithManagedSubscriptions(&scriptedSubscriptionLeaser{leases: []subscriptions.Lease{{
							AccountID: "synthetic-seat", AccessToken: "synthetic-token", ProviderAccount: "synthetic-chatgpt-account",
						}}})
					}
					ctx = context.WithValue(ctx, ClientIdentityContextKey{}, ClientIdentity{ClientApp: ClientAppCodex})
					ctx = WithManagedSubscriptionUsage(outOfRosterBillingCtx(ctx))
					if funding.reason != "" {
						ctx = billing.WithSubscriptionOnly(ctx, funding.reason)
					}
					err := svc.ProxyOpenAIResponses(ctx, []byte(body), httptest.NewRecorder(), request)
					require.Positive(t, preflightCalls.Load())
					require.Zero(t, includedCalls.Load())
					require.Zero(t, paidCalls.Load(), "uncertainty must not buy inference")
					require.Empty(t, repo.snapshot(), "uncertainty must not debit customer credit")
					require.Error(t, err)
					state := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage)
					require.Empty(t, state.UnavailableProviders)
					require.Empty(t, state.QuotaExhaustedAccounts)
					require.Empty(t, state.QuotaExhaustedProviders)
				})
			}
		}
	}
}

func TestQuotaVerificationRotationAndRealOverloadFunding(t *testing.T) {
	type accountFailure struct {
		quotaStatus     int
		quotaBody       string
		inferenceStatus int
		inferenceBody   string
	}
	unknown := accountFailure{quotaStatus: http.StatusGatewayTimeout}
	healthy := accountFailure{}
	exhausted := accountFailure{quotaBody: `{"rate_limit":{"allowed":false,"limit_reached":true,"primary_window":{"used_percent":100,"reset_at":4102444800}}}`}
	overloaded := accountFailure{inferenceStatus: http.StatusBadGateway, inferenceBody: `{"error":{"type":"service_unavailable_error"}}`}
	for _, scenario := range []struct {
		name          string
		accounts      []accountFailure
		includedCalls int32
		capacity      bool
		includedServe bool
	}{
		{name: "unknown then healthy", accounts: []accountFailure{unknown, healthy}, includedCalls: 1, includedServe: true},
		{name: "unknown then exhausted", accounts: []accountFailure{unknown, exhausted}},
		{name: "unknown then overload", accounts: []accountFailure{unknown, overloaded}, includedCalls: 1, capacity: true},
		{name: "overload then unknown", accounts: []accountFailure{overloaded, unknown}, includedCalls: 1, capacity: true},
		{name: "inference 502", accounts: []accountFailure{overloaded}, includedCalls: 1, capacity: true},
		{name: "inference 503", accounts: []accountFailure{{inferenceStatus: http.StatusServiceUnavailable}}, includedCalls: 1, capacity: true},
		{name: "inference HTML 502", accounts: []accountFailure{{inferenceStatus: http.StatusBadGateway, inferenceBody: "<html>bad gateway</html>"}}, includedCalls: 1, capacity: true},
	} {
		for _, reason := range []billing.SubscriptionOnlyReason{"", billing.SubscriptionOnlyLinkedFirst, billing.SubscriptionOnlyCreditsDepleted, billing.SubscriptionOnlySpendCapReached} {
			t.Run(scenario.name+"/"+string(reason), func(t *testing.T) {
				var includedCalls, paidCalls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					if req.Header.Get("Authorization") == "Bearer synthetic-paid-key" {
						paidCalls.Add(1)
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = io.WriteString(w, quotaSafetyResponseSSE)
						return
					}
					accountIndex, parseErr := strconv.Atoi(strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer synthetic-token-"))
					if parseErr != nil || accountIndex < 0 || accountIndex >= len(scenario.accounts) {
						t.Errorf("unexpected subscription credential")
						w.WriteHeader(http.StatusUnauthorized)
						return
					}
					account := scenario.accounts[accountIndex]
					if req.URL.Path == "/wham/usage" {
						if account.quotaStatus != 0 {
							w.WriteHeader(account.quotaStatus)
							return
						}
						quotaBody := account.quotaBody
						if quotaBody == "" {
							quotaBody = `{"rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":20}}}`
						}
						_, _ = io.WriteString(w, quotaBody)
						return
					}
					includedCalls.Add(1)
					if account.inferenceStatus != 0 {
						w.WriteHeader(account.inferenceStatus)
						_, _ = io.WriteString(w, account.inferenceBody)
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, quotaSafetyResponseSSE)
				}))
				defer server.Close()
				client := openai.NewClient("synthetic-paid-key", server.URL)
				client.SetCodexBaseURL(server.URL)
				repo := &auxBillingRepo{}
				leaser := &healthSubscriptionLeaser{scriptedSubscriptionLeaser: &scriptedSubscriptionLeaser{}}
				accounts := make([]*auth.SubscriptionAccount, 0, len(scenario.accounts))
				for accountIndex := range scenario.accounts {
					accountID := "synthetic-seat-" + strconv.Itoa(accountIndex)
					accounts = append(accounts, &auth.SubscriptionAccount{ID: accountID, Provider: auth.SubscriptionProviderCodex, Enabled: true, State: auth.SubscriptionAccountStateActive})
					leaser.leases = append(leaser.leases, subscriptions.Lease{AccountID: accountID, AccessToken: "synthetic-token-" + strconv.Itoa(accountIndex), ProviderAccount: accountID})
				}
				svc := outOfRosterFundingService(client, translate.ReasonUserForceModel, repo).WithManagedSubscriptions(leaser)
				ctx := WithManagedSubscriptionCandidates(managedSubscriptionContext(auth.SubscriptionProviderCodex), accounts)
				ctx = context.WithValue(ctx, InstallationIDContextKey{}, "33333333-3333-3333-3333-333333333333")
				ctx = context.WithValue(ctx, ClientIdentityContextKey{}, ClientIdentity{ClientApp: ClientAppCodex})
				ctx = outOfRosterBillingCtx(ctx)
				if reason != "" {
					ctx = billing.WithSubscriptionOnly(ctx, reason)
				}
				body := `{"model":"codex-auto-review","stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"synthetic"}]}]}`
				recorder := httptest.NewRecorder()
				err := svc.ProxyOpenAIResponses(ctx, []byte(body), recorder, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body)))
				wantPaid := scenario.capacity && (reason == "" || reason == billing.SubscriptionOnlyLinkedFirst)
				require.Equal(t, scenario.includedCalls, includedCalls.Load())
				if wantPaid || scenario.includedServe {
					require.NoError(t, err)
					require.Contains(t, recorder.Body.String(), "served answer")
					debits := repo.snapshot()
					require.Len(t, debits, 1, "one served turn must settle exactly once")
					if wantPaid {
						require.Equal(t, int32(1), paidCalls.Load())
						require.Negative(t, debits[0].DeltaUsdMicros)
					} else {
						require.Zero(t, paidCalls.Load())
						require.Zero(t, debits[0].DeltaUsdMicros)
					}
				} else {
					require.Error(t, err)
					require.Zero(t, paidCalls.Load())
					require.Empty(t, repo.snapshot())
				}
				state := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage)
				_, capacityMarked := state.UnavailableProviders[subscriptions.ProviderCodex]
				require.Equal(t, scenario.capacity, capacityMarked)
				require.False(t, svc.subscriptionPoolUnavailable(ctx, providers.ProviderAnthropic, overloadRescuerModel), "capacity evidence cannot cross pools")
				for accountIndex, account := range scenario.accounts {
					if account.quotaStatus == http.StatusGatewayTimeout {
						require.NotContains(t, leaser.exhaustedIDs, "synthetic-seat-"+strconv.Itoa(accountIndex))
					}
				}
			})
		}
	}
}
