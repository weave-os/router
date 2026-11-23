package proxy

import (
	"context"
	"net/http"
	"testing"

	"weave-os/router/internal/credentials"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	budgetTestFable     = "claude-fable-5-1"
	budgetTestHaiku     = "claude-haiku-4-5"
	budgetTestUserAgent = "claude-cli/2.1.257 (external, sdk-ts)"
	budgetTestBeta      = "context-1m-2025-08-07"
)

func smallClientBudget() router.ClientBudget {
	return credentials.ResolveClientBudget(ClientIdentity{ClientApp: requestcontext.ClientAppClaudeCode, UserAgent: budgetTestUserAgent}, nil, budgetTestFable, false)
}

func TestResolveClientBudget(t *testing.T) {
	for _, tt := range []struct {
		name      string
		clientApp string
		userAgent string
		model     string
		variant   bool
		beta      string
		window    int
		threshold int
	}{
		{"custom base URL default", requestcontext.ClientAppClaudeCode, budgetTestUserAgent, budgetTestFable, false, "", 200_000, 167_000},
		{"original variant", requestcontext.ClientAppClaudeCode, budgetTestUserAgent, budgetTestFable, true, "", 1_000_000, 967_000},
		{"normalized long context is ambiguous", requestcontext.ClientAppClaudeCode, budgetTestUserAgent, budgetTestFable, false, budgetTestBeta, 0, 0},
		{"comma separated beta", requestcontext.ClientAppClaudeCode, budgetTestUserAgent, testOpus, false, "other, " + budgetTestBeta, 0, 0},
		{"beta substring is not capability", requestcontext.ClientAppClaudeCode, budgetTestUserAgent, budgetTestFable, false, budgetTestBeta + "-other", 200_000, 167_000},
		{"small model does not gain provider support", requestcontext.ClientAppClaudeCode, budgetTestUserAgent, budgetTestHaiku, false, budgetTestBeta, 200_000, 167_000},
		{"dated model", requestcontext.ClientAppClaudeCode, budgetTestUserAgent, budgetTestHaiku + "-20251001", false, "", 200_000, 167_000},
		{"provider qualified model", requestcontext.ClientAppClaudeCode, budgetTestUserAgent, "anthropic/" + budgetTestFable, false, "", 200_000, 167_000},
		{"unverified version", requestcontext.ClientAppClaudeCode, "claude-cli/2.1.258 (external, sdk-ts)", budgetTestFable, true, budgetTestBeta, 0, 0},
		{"missing version", requestcontext.ClientAppClaudeCode, "sdk-ts", budgetTestFable, false, "", 0, 0},
		{"SDK entrypoint is not a harness", "", "sdk-ts", budgetTestFable, false, "", 0, 0},
		{"other harness", requestcontext.ClientAppCodex, budgetTestUserAgent, budgetTestFable, false, "", 0, 0},
		{"unknown model", requestcontext.ClientAppClaudeCode, budgetTestUserAgent, "claude-unknown", false, "", 0, 0},
		{"non Claude model", requestcontext.ClientAppClaudeCode, budgetTestUserAgent, testSol, false, "", 0, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			headers := make(http.Header)
			headers.Set("Anthropic-Beta", tt.beta)
			got := credentials.ResolveClientBudget(ClientIdentity{ClientApp: tt.clientApp, UserAgent: tt.userAgent}, headers, tt.model, tt.variant)
			assert.Equal(t, tt.window, got.DefaultWindow)
			assert.Equal(t, tt.threshold, got.DefaultCompactThreshold)
			assert.Equal(t, tt.variant, got.ModelVariant1M)
			wantEvidence := router.ClientBudgetUnknown
			if tt.window != 0 {
				wantEvidence = router.ClientBudgetHarnessDefault
			}
			if tt.name == "normalized long context is ambiguous" || tt.name == "comma separated beta" {
				wantEvidence = router.ClientBudgetAmbiguousLongContext
			}
			assert.Equal(t, wantEvidence, got.Evidence, "private settings/account caps are never represented as measured thresholds")
		})
	}
}

func TestClientBudgetDoesNotUseRouterAddedBeta(t *testing.T) {
	clientIdentity := ClientIdentity{ClientApp: requestcontext.ClientAppClaudeCode, UserAgent: budgetTestUserAgent}
	headers := make(http.Header)
	budget := credentials.ResolveClientBudget(clientIdentity, headers, testOpus, false)
	env, err := translate.ParseAnthropic([]byte(`{"model":"` + testOpus + `","messages":[{"role":"user","content":"hello"}]}`))
	require.NoError(t, err)
	prepared, err := env.PrepareAnthropic(headers, translate.EmitOptions{TargetModel: testOpus, Capabilities: router.Lookup(testOpus), EnableExtendedContext: true})
	require.NoError(t, err)
	assert.True(t, translate.HasContext1MBeta(prepared.Headers))
	assert.False(t, budget.InboundContext1M)
	assert.Equal(t, 200_000, budget.DefaultWindow)
	assert.False(t, translate.HasContext1MBeta(headers), "upstream preparation must not mutate ingress evidence")
}

func TestClientBudgetRecomputedAcrossSameSessionAndAgents(t *testing.T) {
	clientIdentity := ClientIdentity{SessionID: "shared-session", ClientApp: requestcontext.ClientAppClaudeCode, UserAgent: budgetTestUserAgent}
	var parent = context.Background()
	for _, tt := range []struct {
		model   string
		variant bool
		window  int
	}{
		{budgetTestFable, true, 1_000_000},
		{budgetTestFable, false, 200_000},
		{budgetTestHaiku, false, 200_000},
	} {
		budget := credentials.ResolveClientBudget(clientIdentity, nil, tt.model, tt.variant)
		parent = requestcontext.WithClientBudget(parent, budget)
		svc := &Service{engine: dispatch.NewEngine(nil), strategyServing: hmmPorts()}
		req := svc.withPolicyRequestContext(parent, router.Request{})
		assert.Equal(t, tt.window, req.ClientBudget.DefaultWindow)
	}
	child := requestcontext.WithClientBudget(parent, router.ClientBudget{})
	assert.Zero(t, requestcontext.ClientBudgetFrom(child).DefaultWindow)
	assert.Equal(t, 200_000, requestcontext.ClientBudgetFrom(parent).DefaultWindow)
}
