package proxy

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy/usage"
	"weave-os/router/internal/requestcontext"
)

func TestSelectableSubscriptionProvidersCoverCallerFundedLanes(t *testing.T) {
	claudeLogin := http.Header{"Authorization": []string{"Bearer " + exhaustedSubToken}}
	codexLogin := http.Header{
		"Authorization": []string{"Bearer " + codexTestJWT},
		http.CanonicalHeaderKey(requestcontext.ChatGPTAccountIDHeader): []string{"acct-1"},
	}
	ownKeys := func(provider string) context.Context {
		return context.WithValue(routerKeyedCtx(), credentials.ExternalAPIKeysContextKey{}, []*auth.ExternalAPIKey{
			{Provider: provider, Plaintext: []byte("synthetic-own-key")},
		})
	}
	serviceWithUsageSnapshot := func(token string, snap usage.Snapshot) *Service {
		return &Service{hmmServing: hmmPorts(),
			usageObserver: observerWithSnapshot(token, snap),
			credentials:   credentials.Brokerage{KeyedProviders: map[string]struct{}{}},
		}
	}

	cases := []struct {
		name    string
		svc     *Service
		ctx     context.Context
		headers http.Header
		want    []string
		absent  []string
	}{
		{name: "direct Claude Code login", svc: &Service{hmmServing: hmmPorts()}, ctx: routerKeyedCtx(), headers: claudeLogin, want: []string{providers.ProviderAnthropic}, absent: []string{providers.ProviderOpenAI}},
		{name: "direct Codex login", svc: &Service{hmmServing: hmmPorts()}, ctx: routerKeyedCtx(), headers: codexLogin, want: []string{providers.ProviderOpenAI}, absent: []string{providers.ProviderAnthropic}},
		{name: "own Anthropic API key", svc: &Service{hmmServing: hmmPorts()}, ctx: ownKeys(providers.ProviderAnthropic), headers: http.Header{}, want: []string{providers.ProviderAnthropic}},
		{name: "own OpenAI API key", svc: &Service{hmmServing: hmmPorts()}, ctx: ownKeys(providers.ProviderOpenAI), headers: http.Header{}, want: []string{providers.ProviderOpenAI}},
		{name: "Claude login billing usage overage", svc: serviceWithUsageSnapshot(exhaustedSubToken, usage.Snapshot{OverageInUse: true}), ctx: routerKeyedCtx(), headers: claudeLogin, want: []string{providers.ProviderAnthropic}},
		{name: "Claude login billing overage past a full included window", svc: serviceWithUsageSnapshot(exhaustedSubToken, usage.Snapshot{OverageInUse: true, Secondary: usage.Window{UsedPercent: 1.0, WindowMinutes: 10080}}), ctx: routerKeyedCtx(), headers: claudeLogin, want: []string{providers.ProviderAnthropic}},
		{name: "Codex login billing extra usage", svc: serviceWithUsageSnapshot(codexTestJWT, usage.Snapshot{OverageInUse: true}), ctx: routerKeyedCtx(), headers: codexLogin, want: []string{providers.ProviderOpenAI}},
		{name: "spent Claude login keeps the lane with an own key", svc: serviceWithUsageSnapshot(exhaustedSubToken, exhaustedSnapshot()), ctx: ownKeys(providers.ProviderAnthropic), headers: claudeLogin, want: []string{providers.ProviderAnthropic}},
		{name: "spent Claude login without an own key leaves the lane", svc: serviceWithUsageSnapshot(exhaustedSubToken, exhaustedSnapshot()), ctx: routerKeyedCtx(), headers: claudeLogin, absent: []string{providers.ProviderAnthropic}},
		{name: "no caller funding", svc: &Service{hmmServing: hmmPorts()}, ctx: routerKeyedCtx(), headers: http.Header{}, absent: []string{providers.ProviderAnthropic, providers.ProviderOpenAI}},
		{name: "subscription routing disabled", svc: &Service{hmmServing: hmmPorts()}, ctx: context.WithValue(routerKeyedCtx(), requestcontext.InstallationSubscriptionRoutingDisabledContextKey{}, true), headers: claudeLogin, absent: []string{providers.ProviderAnthropic, providers.ProviderOpenAI}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.svc.selectableSubscriptionProviders(tc.ctx, tc.headers)
			for _, provider := range tc.want {
				assert.Contains(t, got, provider)
			}
			for _, provider := range tc.absent {
				assert.NotContains(t, got, provider)
			}
		})
	}
}
