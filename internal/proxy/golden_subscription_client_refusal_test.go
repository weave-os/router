package proxy_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/subscriptions"
)

const goldenOutOfExtraUsageBody = `{"type":"error","error":{"type":"invalid_request_error","message":"You're out of extra usage."}}`

func goldenOutOfExtraUsage() goldenStep {
	return goldenStep{err: &providers.UpstreamErrorResponse{
		Status:    http.StatusBadRequest,
		Headers:   http.Header{"Content-Type": {"application/json"}},
		Body:      []byte(goldenOutOfExtraUsageBody),
		BodyBytes: int64(len(goldenOutOfExtraUsageBody)),
		Rejection: providers.UpstreamRejection{Kind: providers.UpstreamRejectionCredential},
	}}
}

func withTwoClaudeSeats(svc *proxy.Service, h *goldenHarness) *proxy.Service {
	return svc.WithManagedSubscriptions(goldenProviderPools{
		subscriptions.ProviderClaude: newGoldenSubscriptionPool(h.clock.Now, "claude-seat-a", "claude-seat-b"),
	})
}

func goldenClaudeSeatsCtx(clientApp, session string) func(context.Context) context.Context {
	return func(ctx context.Context) context.Context {
		models := []string{goldenAnthropicModel}
		ctx = requestcontext.WithClientIdentity(ctx, requestcontext.ClientIdentity{ClientApp: clientApp, SessionID: session})
		ctx = proxy.WithSubscriptionStateModels(ctx, models, models)
		ctx = credentials.WithManagedSubscriptionCandidates(ctx, []*auth.SubscriptionAccount{
			{ID: "claude-seat-a", Provider: auth.SubscriptionProviderClaude, Enabled: true, State: auth.SubscriptionAccountStateActive},
			{ID: "claude-seat-b", Provider: auth.SubscriptionProviderClaude, Enabled: true, State: auth.SubscriptionAccountStateActive},
		})
		return credentials.WithManagedSubscriptionUsage(ctx)
	}
}

// goldenSubscriptionClientRefusalScenarios pin a third-party client turn that
// every Claude seat refuses as out of extra usage: the turn is paid with Weave
// credits, and the refusal is scoped to that client, so the next Claude Code
// turn is still served on a seat.
func goldenSubscriptionClientRefusalScenarios() []goldenScenario {
	return []goldenScenario{{
		name: ingressResponses + "_stream_third_party_out_of_extra_usage_keeps_claude_seats_for_claude_code", ingress: ingressResponses,
		decisions: []router.Decision{goldenAnthropicDecision},
		upstreams: map[string][]goldenStep{providers.ProviderAnthropic: {
			goldenOutOfExtraUsage(), goldenOutOfExtraUsage(), anthropicOK(true, "served on weave credits"), anthropicOK(true, "served on claude seat"),
		}},
		requests: []goldenRequest{
			{body: goldenOpenCodeOpening, headers: map[string]string{"User-Agent": goldenOpenCodeHeadersAgent, "X-OpenCode-Session": "ses_golden_third_party"}, decorateContext: goldenClaudeSeatsCtx(requestcontext.ClientAppOpencode, "golden-third-party-session")},
			{body: goldenOpenCodeOpening, headers: map[string]string{"User-Agent": "claude-cli/2.1.0 (external, cli)"}, decorateContext: goldenClaudeSeatsCtx(requestcontext.ClientAppClaudeCode, "golden-claude-code-session")},
		},
		// What the HMM policy returns once every arm it may rank is excluded.
		noCandidateErr: fmt.Errorf("hmm_embedding: arm selection: %w", policy.ErrNoEligibleArm),
		configure:      withTwoClaudeSeats,
		credentials:    map[string]string{"golden-seat-claude-seat-a": "claude-seat-a", "golden-seat-claude-seat-b": "claude-seat-b"},
	}}
}

// TestThirdPartyOutOfExtraUsageKeepsClaudeSeatsForClaudeCode states the
// invariant the scenario pins: one client's extra-usage refusal never cools the
// shared Claude seats down for every other client.
func TestThirdPartyOutOfExtraUsageKeepsClaudeSeatsForClaudeCode(t *testing.T) {
	t.Parallel()
	sc := goldenSubscriptionClientRefusalScenarios()[0]
	h := newGoldenHarness(t, sc)
	refused := h.serve(t, sc.ingress, sc.requests[0])
	require.Len(t, refused.Attempts, 3, "the third-party turn tries each seat once, then pays")
	requireServedOnCredits(t, refused, "served on weave credits")

	served := h.serve(t, sc.ingress, sc.requests[1])
	require.Equal(t, http.StatusOK, served.Response.Status, "response: %+v", served.Response.Body)
	require.NotEmpty(t, served.Attempts, "the Claude Code turn must still lease a seat")
	last := served.Attempts[len(served.Attempts)-1]
	require.Equal(t, providers.ProviderAnthropic, last.Provider)
	require.Equal(t, "ok", last.Outcome)
}
