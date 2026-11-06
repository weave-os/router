package proxy

import (
	"context"
	"fmt"
	"github.com/tidwall/gjson"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers/anthropic"
	"weave-os/router/internal/providers/openai"
	"weave-os/router/internal/providers/openaicompat"
	"weave-os/router/internal/proxy/usage"
	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/cluster"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/subscriptions"
)

const (
	stateClaudeModel = "claude-opus-4-8"
	stateCodexModel  = "gpt-5.6-sol"
	statePaidModel   = "deepseek/deepseek-v4-flash"
)

type stateModelRouter struct{}

func (stateModelRouter) Route(_ context.Context, req router.Request) (router.Decision, error) {
	resolver := policy.NewResolver(modelSet([]string{stateClaudeModel, stateCodexModel, statePaidModel}), nil, func(model catalog.Model) string { return model.ID }, policy.ProviderPolicy{})
	eligible := modelSet(resolver.Resolve(req).CandidateModels())
	for _, model := range append(slices.Clone(req.PreferredModels), stateClaudeModel, stateCodexModel, statePaidModel) {
		if _, admitted := eligible[model]; admitted {
			entry, _ := catalog.ByID(model)
			for _, binding := range entry.Providers {
				if _, enabled := req.EnabledProviders[binding.Provider]; enabled {
					return router.Decision{Model: model, Provider: binding.Provider}, nil
				}
			}
		}
	}
	return router.Decision{}, cluster.ErrAllowlistEmptiesPool
}

type stateModelLeaser struct {
	scriptedSubscriptionLeaser
	activeProviders map[subscriptions.Provider]bool
}

func (l *stateModelLeaser) Lease(_ context.Context, owner auth.SubscriptionOwner, provider subscriptions.Provider, _ string) (subscriptions.Lease, bool, error) {
	account := "synthetic-" + string(provider)
	for _, excluded := range owner.ExcludedAccountIDs {
		if excluded == account {
			return subscriptions.Lease{}, true, subscriptions.ErrNoAvailableAccount
		}
	}
	if !l.activeProviders[provider] {
		return subscriptions.Lease{}, true, subscriptions.ErrNoAvailableAccount
	}
	return subscriptions.Lease{AccountID: account, AccessToken: "synthetic-token", ProviderAccount: "synthetic-workspace", OwnerID: "synthetic-owner"}, true, nil
}

func TestSubscriptionStateModelsFundingOrder(t *testing.T) {
	for _, scenario := range []struct {
		name                                                                                                                                                                                    string
		claude, codex, emptyPaid, depleted, liveReject, committedFailure, forced, forcedPin, forceWithRequestSubset, demoteClaude, cooldownClaude, claudeOnlyActive, readmitClaude, rejectCodex bool
		paidSetIncludesClaude                                                                                                                                                                   bool
		utilityTarget, passthroughTarget                                                                                                                                                        bool
		want                                                                                                                                                                                    string
		wantErr                                                                                                                                                                                 bool
	}{
		{name: "Claude included before paid", claude: true, codex: true, want: stateClaudeModel},
		{name: "Claude exhausted uses active Codex", codex: true, want: stateCodexModel},
		{name: "both exhausted use cheap paid set", want: statePaidModel},
		{name: "unavailable Claude skips funding-denied paid target", paidSetIncludesClaude: true, want: statePaidModel},
		{name: "live Claude exhaustion rotates to Codex", claude: true, codex: true, liveReject: true, want: stateCodexModel},
		{name: "live exhaustion uses cheap paid set", claude: true, liveReject: true, want: statePaidModel},
		{name: "empty exhausted set refuses paid", emptyPaid: true, wantErr: true},
		{name: "explicit force cannot buy an active-only model", forced: true, wantErr: true},
		{name: "explicit force still uses included capacity", forced: true, claude: true, want: stateClaudeModel},
		{name: "readmitted force pin survives state request rebuild", forcedPin: true, forceWithRequestSubset: true, claude: true, want: stateClaudeModel},
		{name: "force model overrides request subset", forced: true, forceWithRequestSubset: true, claude: true, want: stateClaudeModel},
		{name: "utility target follows subscription state model sets", utilityTarget: true, claude: true, want: stateClaudeModel},
		{name: "passthrough target follows exhausted funding set", passthroughTarget: true, claude: true, codex: true, want: statePaidModel},
		{name: "session-demoted Claude selects healthy Codex", claude: true, codex: true, demoteClaude: true, want: stateCodexModel},
		{name: "turn-struck Claude rescues failed Codex", claude: true, codex: true, demoteClaude: true, readmitClaude: true, rejectCodex: true, depleted: true, want: stateClaudeModel},
		{name: "session-cooling Claude selects healthy Codex", claude: true, codex: true, cooldownClaude: true, want: stateCodexModel},
		{name: "depleted credits still serve Codex", codex: true, depleted: true, want: stateCodexModel},
		{name: "depleted credits prohibit cheap paid fallback", depleted: true, wantErr: true},
		{name: "included model outside active set rescues failed active model", claude: true, codex: true, liveReject: true, claudeOnlyActive: true, want: stateCodexModel},
		{name: "committed subscription failure never replays", claude: true, committedFailure: true, wantErr: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			claude := &fakeClient{name: providers.ProviderAnthropic, outcomes: []fakeOutcome{{writeBytes: []byte("included Claude answer")}}}
			if scenario.liveReject {
				claude.outcomes = []fakeOutcome{{err: &providers.UpstreamErrorResponse{Status: http.StatusTooManyRequests, Body: []byte(`{"error":{"type":"rate_limit_error"}}`)}}}
			}
			if scenario.committedFailure {
				claude.outcomes = []fakeOutcome{{writeBytes: []byte("partial answer"), err: &providers.UpstreamStatusError{Status: http.StatusBadGateway}}}
			}
			codex := &fakeClient{name: providers.ProviderOpenAI, outcomes: []fakeOutcome{{writeBytes: []byte("included Codex answer")}}}
			if scenario.rejectCodex {
				codex.outcomes = []fakeOutcome{{err: &providers.UpstreamErrorResponse{Status: http.StatusTooManyRequests, Body: []byte(`{"error":{"type":"rate_limit_error"}}`)}}}
			}
			paid := &fakeClient{name: providers.ProviderOpenRouter, outcomes: []fakeOutcome{{writeBytes: []byte("cheap paid answer")}}}
			svc := NewService(stateModelRouter{}, map[string]providers.Client{providers.ProviderAnthropic: claude, providers.ProviderOpenAI: codex, providers.ProviderOpenRouter: paid}, nil, false, nil, nil, false, "", "", nil).
				WithManagedSubscriptions(&stateModelLeaser{activeProviders: map[subscriptions.Provider]bool{subscriptions.ProviderClaude: scenario.claude, subscriptions.ProviderCodex: scenario.codex}})
			ctx := context.WithValue(managedSubscriptionTestContext(), ManagedSubscriptionProvidersContextKey{}, map[auth.SubscriptionProvider]struct{}{auth.SubscriptionProviderClaude: {}, auth.SubscriptionProviderCodex: {}})
			activeModels := []string{stateClaudeModel, stateCodexModel}
			if scenario.claudeOnlyActive {
				activeModels = []string{stateClaudeModel}
			}
			ctx = context.WithValue(ctx, InstallationSubscriptionModelsWhenActiveContextKey{}, activeModels)
			paidModels := []string{statePaidModel}
			if scenario.paidSetIncludesClaude {
				paidModels = append(paidModels, stateClaudeModel)
			}
			if scenario.emptyPaid {
				paidModels = nil
			}
			ctx = context.WithValue(ctx, InstallationSubscriptionModelsWhenInactiveContextKey{}, paidModels)
			ctx = withTurnFailures(ctx)
			if scenario.demoteClaude {
				ctx = context.WithValue(ctx, SessionDemotedModelsContextKey{}, []string{stateClaudeModel})
			}
			if scenario.readmitClaude {
				ctx = context.WithValue(ctx, SessionStrikeReadmitModelsContextKey{}, []string{stateClaudeModel})
			}
			if scenario.cooldownClaude {
				ctx = context.WithValue(ctx, SessionCooldownModelsContextKey{}, map[string]time.Time{stateClaudeModel: time.Now().Add(time.Minute)})
			}
			if scenario.passthroughTarget {
				authService := auth.NewService(nil, nil, nil, nil, nil, nil, time.Now).WithRoutingPolicies(routingPolicyStub{mode: auth.RoutingPolicyPassthrough}, nil)
				var err error
				ctx, err = authService.WithRoutingPolicy(ctx, "state-model-test-installation")
				require.NoError(t, err)
			}
			if scenario.depleted {
				ctx = billing.WithSubscriptionOnly(ctx, billing.SubscriptionOnlyCreditsDepleted)
			}
			req := router.Request{AllowedModels: allowedModelsForRequest(ctx), EnabledProviders: modelSet([]string{providers.ProviderAnthropic, providers.ProviderOpenAI, providers.ProviderOpenRouter})}
			if scenario.forced {
				req.ForceModel = stateClaudeModel
			}
			if scenario.forceWithRequestSubset {
				req.AllowedModels = modelSet([]string{stateCodexModel})
			}
			initialModel := stateClaudeModel
			if scenario.utilityTarget {
				initialModel = statePaidModel
			}
			initialProvider := providers.ProviderAnthropic
			if scenario.passthroughTarget {
				initialModel = statePaidModel
				initialProvider = providers.ProviderOpenRouter
			}
			initialDecision := router.Decision{Model: initialModel, Provider: initialProvider}
			var origin policy.OverrideSource
			if scenario.forcedPin {
				initialDecision.Reason = translate.ReasonUserForceModel
				origin = policy.OverrideSourceRequest
			}
			rec := httptest.NewRecorder()
			buffer := newPreludeBuffer(rec)
			var servedModels []string
			var subscriptionFunding []bool
			var winner router.Decision
			dispatch := failoverInputs{
				w: rec, buf: buffer, subscriptionStateRequest: &req,
				subscriptionStateWinnerProvider: new(string),
				initialDecision:                 initialDecision,
				purpose:                         inference.PurposeAnthropicMessages,
				origin:                          origin,
				buildAlternative: func(target router.Decision) (dispatchAttempt, error) {
					return func(attemptCtx context.Context, decision router.Decision, client providers.Client) error {
						servedModels = append(servedModels, decision.Model)
						subscriptionFunding = append(subscriptionFunding, servedOnSubscription(attemptCtx))
						buffer.Seal()
						return client.Proxy(attemptCtx, decision, providers.PreparedRequest{}, buffer, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
					}, nil
				},
				onAlternative: func(decision router.Decision) { winner = decision },
			}
			_, err := svc.dispatchWithFallback(ctx, dispatch)
			if scenario.wantErr {
				require.Error(t, err)
				require.Zero(t, paid.calls)
			} else {
				require.NoError(t, err)
				require.Equal(t, scenario.want, winner.Model)
				if scenario.want == statePaidModel {
					require.Equal(t, providers.ProviderOpenRouter, *dispatch.subscriptionStateWinnerProvider)
				}
				require.Equal(t, scenario.want, rec.Header().Get(HeaderRouterModel))
				require.Contains(t, rec.Body.String(), "answer")
			}
			for i, model := range servedModels {
				if model == statePaidModel {
					require.False(t, subscriptionFunding[i])
				} else {
					require.True(t, subscriptionFunding[i], "expensive models must never reach paid credentials")
				}
			}
			if scenario.liveReject && !scenario.wantErr {
				require.Equal(t, []string{stateClaudeModel, scenario.want}, servedModels)
				require.Contains(t, turnFailuresFromContext(ctx).models, stateClaudeModel, "the rescued model is struck for the agent turn")
			}
		})
	}
}

// exhaustedPaidLaneRouter reproduces the HMM sidecar's answer when every paid
// arm is ineligible: ErrNoEligibleArm wrapped as policy unavailable.
type exhaustedPaidLaneRouter struct{}

func (exhaustedPaidLaneRouter) Route(ctx context.Context, req router.Request) (router.Decision, error) {
	decision, err := stateModelRouter{}.Route(ctx, req)
	if err != nil {
		return router.Decision{}, fmt.Errorf("hmm_embedding: arm selection: %w: %w", policy.ErrNoEligibleArm, router.ErrStrategyUnavailable)
	}
	return decision, nil
}

func TestSubscriptionStateRateLimitedIncludedLaneKeepsUpstreamStatus(t *testing.T) {
	rateLimit := &providers.UpstreamErrorResponse{
		Status:  http.StatusTooManyRequests,
		Headers: http.Header{"Retry-After": []string{"30"}},
		Body:    []byte(`{"type":"error","error":{"type":"rate_limit_error","message":"rate limited"}}`),
	}
	claude := &fakeClient{name: providers.ProviderAnthropic, outcomes: []fakeOutcome{{err: rateLimit}}}
	svc := NewService(exhaustedPaidLaneRouter{}, map[string]providers.Client{providers.ProviderAnthropic: claude}, nil, false, nil, nil, false, "", "", nil).
		WithManagedSubscriptions(&stateModelLeaser{activeProviders: map[subscriptions.Provider]bool{subscriptions.ProviderClaude: true}})
	ctx := context.WithValue(managedSubscriptionTestContext(), ManagedSubscriptionProvidersContextKey{}, map[auth.SubscriptionProvider]struct{}{auth.SubscriptionProviderClaude: {}})
	ctx = context.WithValue(ctx, InstallationSubscriptionModelsWhenActiveContextKey{}, []string{stateClaudeModel})
	ctx = context.WithValue(ctx, InstallationSubscriptionModelsWhenInactiveContextKey{}, []string{statePaidModel})
	ctx = withTurnFailures(ctx)
	req := router.Request{AllowedModels: allowedModelsForRequest(ctx), EnabledProviders: modelSet([]string{providers.ProviderAnthropic})}
	rec := httptest.NewRecorder()
	buffer := newPreludeBuffer(rec)
	inputs := failoverInputs{
		w: rec, buf: buffer, subscriptionStateRequest: &req,
		subscriptionStateWinnerProvider: new(string),
		initialDecision:                 router.Decision{Model: stateClaudeModel, Provider: providers.ProviderAnthropic},
		purpose:                         inference.PurposeAnthropicMessages,
		buildAlternative: func(router.Decision) (dispatchAttempt, error) {
			return func(attemptCtx context.Context, decision router.Decision, client providers.Client) error {
				buffer.Seal()
				return client.Proxy(attemptCtx, decision, providers.PreparedRequest{}, buffer, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
			}, nil
		},
	}

	_, err := svc.dispatchWithFallback(ctx, inputs)

	require.Error(t, err)
	require.NotErrorIs(t, err, router.ErrStrategyUnavailable, "an exhausted rate-limited pool is not a policy outage")
	class, ok := ClassifyDispatchError(err)
	require.True(t, ok)
	require.Equal(t, http.StatusTooManyRequests, class.Status)
	var upstream *providers.UpstreamErrorResponse
	require.ErrorAs(t, err, &upstream)
	require.Equal(t, "30", upstream.Headers.Get("Retry-After"))
}

func TestSubscriptionStateModelsIntersectGlobalAllowlist(t *testing.T) {
	ctx := conditionalModelsContext([]string{stateClaudeModel, stateCodexModel}, []string{statePaidModel})
	ctx = context.WithValue(ctx, InstallationAllowedModelsContextKey{}, []string{stateCodexModel})
	require.Equal(t, map[string]struct{}{stateCodexModel: {}}, allowedModelsForRequest(ctx))
	svc := &Service{}
	_, paid := svc.subscriptionStateRequest(ctx, router.Request{AllowedModels: allowedModelsForRequest(ctx)}, []string{statePaidModel})
	require.Empty(t, paid.AllowedModels, "a state must never widen the global allowlist")
}

func TestSubscriptionStateModelsHTTPIngress(t *testing.T) {
	for _, surface := range []string{routePathMessages, routePathChatCompletions, routePathResponses} {
		for _, scenario := range []struct {
			name          string
			claude, codex bool
			active        []string
			want          string
		}{
			{name: "Claude", claude: true, codex: true, want: stateClaudeModel},
			{name: "active preference wins first included attempt", claude: true, codex: true, active: []string{stateCodexModel}, want: stateCodexModel},
			{name: "Codex after Claude exhaustion", codex: true, want: stateCodexModel},
			{name: "cheap after both exhausted", want: statePaidModel},
		} {
			t.Run(surface+"/"+scenario.name, func(t *testing.T) {
				var models, credentials []string
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if serveSyntheticCodexQuota(w, r) {
						return
					}
					body, _ := io.ReadAll(r.Body)
					models = append(models, gjson.GetBytes(body, "model").String())
					credentials = append(credentials, r.Header.Get("Authorization"))
					w.Header().Set("Content-Type", "text/event-stream")
					switch {
					case strings.Contains(r.URL.Path, "messages"):
						_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"synthetic\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-opus-4-8\",\"content\":[],\"usage\":{\"input_tokens\":11,\"output_tokens\":0}}}\n\n")
						_, _ = io.WriteString(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"synthetic answer\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":7}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
					case strings.Contains(r.URL.Path, "responses"):
						_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":\"synthetic answer\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"synthetic\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":11,\"output_tokens\":7}}}\n\n")
					default:
						_, _ = io.WriteString(w, "data: {\"id\":\"synthetic\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"synthetic answer\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"synthetic\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":7}}\n\ndata: [DONE]\n\n")
					}
				}))
				defer upstream.Close()
				codexClient := openai.NewClient("expensive-paid-key", upstream.URL)
				codexClient.SetCodexBaseURL(upstream.URL)
				svc := NewService(stateModelRouter{}, map[string]providers.Client{
					providers.ProviderAnthropic:  anthropic.NewClient("expensive-paid-key", upstream.URL),
					providers.ProviderOpenAI:     codexClient,
					providers.ProviderOpenRouter: openaicompat.NewClient("cheap-paid-key", upstream.URL),
				}, nil, false, nil, nil, false, "", "", nil).
					WithManagedSubscriptions(&stateModelLeaser{activeProviders: map[subscriptions.Provider]bool{subscriptions.ProviderClaude: scenario.claude, subscriptions.ProviderCodex: scenario.codex}}).
					WithDeploymentKeyedProviders(modelSet([]string{providers.ProviderAnthropic, providers.ProviderOpenAI, providers.ProviderOpenRouter}))
				ctx := context.WithValue(managedSubscriptionTestContext(), ManagedSubscriptionProvidersContextKey{}, map[auth.SubscriptionProvider]struct{}{auth.SubscriptionProviderClaude: {}, auth.SubscriptionProviderCodex: {}})
				activeModels := scenario.active
				if activeModels == nil {
					activeModels = []string{stateClaudeModel, stateCodexModel}
				}
				ctx = context.WithValue(ctx, InstallationSubscriptionModelsWhenActiveContextKey{}, activeModels)
				ctx = context.WithValue(ctx, InstallationSubscriptionModelsWhenInactiveContextKey{}, []string{statePaidModel})
				body := `{"model":"auto","stream":true,"max_tokens":100,"messages":[{"role":"user","content":"synthetic task"}]}`
				if surface == routePathResponses {
					body = `{"model":"auto","stream":true,"input":[{"role":"user","content":"synthetic task"}]}`
				}
				rec := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodPost, surface, strings.NewReader(body))
				var err error
				switch surface {
				case routePathMessages:
					err = svc.ProxyMessages(ctx, []byte(body), rec, req)
				case routePathResponses:
					err = svc.ProxyOpenAIResponses(ctx, []byte(body), rec, req)
				default:
					err = svc.ProxyOpenAIChatCompletion(ctx, []byte(body), rec, req)
				}
				require.NoError(t, err)
				require.Equal(t, scenario.want, rec.Header().Get(HeaderRouterModel))
				require.Contains(t, rec.Body.String(), "synthetic answer")
				require.Equal(t, []string{scenario.want}, models, "upstream must receive the selected state-set model")
				require.NotContains(t, credentials, "Bearer expensive-paid-key")
				if scenario.want == statePaidModel {
					require.Equal(t, []string{"Bearer cheap-paid-key"}, credentials)
				}
			})
		}
	}
}

// A classifier asking for a model outside the spent plan's paid set is still
// served from that set rather than passed through on the requested model.
func TestSubscriptionStateModelsClassifierUsesPaidSet(t *testing.T) {
	var models []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveSyntheticCodexQuota(w, r) {
			return
		}
		body, _ := io.ReadAll(r.Body)
		models = append(models, gjson.GetBytes(body, "model").String())
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"synthetic\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"allow\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":1}}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()
	codexClient := openai.NewClient("expensive-paid-key", upstream.URL)
	codexClient.SetCodexBaseURL(upstream.URL)
	svc := NewService(stateModelRouter{}, map[string]providers.Client{
		providers.ProviderAnthropic:  anthropic.NewClient("expensive-paid-key", upstream.URL),
		providers.ProviderOpenAI:     codexClient,
		providers.ProviderOpenRouter: openaicompat.NewClient("cheap-paid-key", upstream.URL),
	}, nil, false, nil, nil, false, "", "", nil).
		WithManagedSubscriptions(&stateModelLeaser{activeProviders: map[subscriptions.Provider]bool{}}).
		WithDeploymentKeyedProviders(modelSet([]string{providers.ProviderAnthropic, providers.ProviderOpenAI, providers.ProviderOpenRouter}))
	ctx := context.WithValue(managedSubscriptionTestContext(), ManagedSubscriptionProvidersContextKey{}, map[auth.SubscriptionProvider]struct{}{auth.SubscriptionProviderClaude: {}, auth.SubscriptionProviderCodex: {}})
	ctx = context.WithValue(ctx, InstallationSubscriptionModelsWhenActiveContextKey{}, []string{stateClaudeModel, stateCodexModel})
	ctx = context.WithValue(ctx, InstallationSubscriptionModelsWhenInactiveContextKey{}, []string{statePaidModel})
	body := `{"model":"` + stateClaudeModel + `","stream":true,"max_tokens":64,"thinking":{"type":"disabled"},"system":"You are a security monitor.","messages":[{"role":"user","content":"grade this action"}]}`
	rec := httptest.NewRecorder()

	require.NoError(t, svc.ProxyMessages(ctx, []byte(body), rec, httptest.NewRequest(http.MethodPost, routePathMessages, strings.NewReader(body))))

	require.Equal(t, statePaidModel, rec.Header().Get(HeaderRouterModel))
	require.Equal(t, []string{statePaidModel}, models)
}

func TestSubscriptionStateModelsPaidFallbackSkipsUnavailableProvider(t *testing.T) {
	claude := &fakeClient{name: providers.ProviderAnthropic, outcomes: []fakeOutcome{{writeBytes: []byte("must not be called")}}}
	paid := &fakeClient{name: providers.ProviderOpenRouter, outcomes: []fakeOutcome{{writeBytes: []byte("cheap paid answer")}}}
	svc := NewService(stateModelRouter{}, map[string]providers.Client{providers.ProviderAnthropic: claude, providers.ProviderOpenRouter: paid}, nil, false, nil, nil, false, "", "", nil).
		WithManagedSubscriptions(&stateModelLeaser{activeProviders: map[subscriptions.Provider]bool{}})
	svc.deploymentKeyedProviders = map[string]struct{}{providers.ProviderOpenRouter: {}}
	ctx := context.WithValue(managedSubscriptionTestContext(), ManagedSubscriptionProvidersContextKey{}, map[auth.SubscriptionProvider]struct{}{auth.SubscriptionProviderClaude: {}, auth.SubscriptionProviderCodex: {}})
	ctx = context.WithValue(ctx, InstallationExcludedProvidersContextKey{}, []string{providers.ProviderAnthropic})
	ctx = context.WithValue(ctx, InstallationSubscriptionModelsWhenActiveContextKey{}, []string{stateClaudeModel, stateCodexModel})
	ctx = context.WithValue(ctx, InstallationSubscriptionModelsWhenInactiveContextKey{}, []string{stateClaudeModel, statePaidModel})
	req := router.Request{
		AllowedModels:    allowedModelsForRequest(ctx),
		EnabledProviders: modelSet([]string{providers.ProviderAnthropic, providers.ProviderOpenAI, providers.ProviderOpenRouter}),
	}
	rec := httptest.NewRecorder()
	buffer := newPreludeBuffer(rec)
	var winner router.Decision
	var servedModels []string
	var paidTargetSelected bool
	_, err := svc.dispatchWithFallback(ctx, failoverInputs{
		w: rec, buf: buffer, subscriptionStateRequest: &req,
		initialDecision: router.Decision{Model: stateClaudeModel, Provider: providers.ProviderAnthropic},
		purpose:         inference.PurposeAnthropicMessages,
		buildAlternative: func(target router.Decision) (dispatchAttempt, error) {
			return func(attemptCtx context.Context, decision router.Decision, client providers.Client) error {
				servedModels = append(servedModels, decision.Model)
				buffer.Seal()
				return client.Proxy(attemptCtx, decision, providers.PreparedRequest{}, buffer, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
			}, nil
		},
		onSubscriptionStateTarget: func(decision router.Decision, _ []catalog.ProviderBinding) { winner = decision },
		onSubscriptionStatePaidTarget: func(decision router.Decision, _ []catalog.ProviderBinding) {
			paidTargetSelected = decision.Model == statePaidModel
		},
	})
	require.NoError(t, err)
	require.Equal(t, statePaidModel, winner.Model)
	require.True(t, paidTargetSelected, "paid-target preparation runs only after subscription candidates fail")
	require.Equal(t, 0, claude.calls)
	require.Equal(t, []string{statePaidModel}, servedModels)
}

func TestSubscriptionStateFailedTargetReachesRecoveryCallback(t *testing.T) {
	paidClient := &fakeClient{name: providers.ProviderOpenRouter}
	svc := NewService(stateModelRouter{}, map[string]providers.Client{providers.ProviderOpenRouter: paidClient}, nil, false, nil, nil, false, "", "", nil)
	ctx := context.WithValue(context.Background(), InstallationSubscriptionModelsWhenActiveContextKey{}, []string{stateClaudeModel})
	ctx = context.WithValue(ctx, InstallationSubscriptionModelsWhenInactiveContextKey{}, []string{statePaidModel})
	request := router.Request{
		AllowedModels:    allowedModelsForRequest(ctx),
		EnabledProviders: modelSet([]string{providers.ProviderAnthropic, providers.ProviderOpenAI, providers.ProviderOpenRouter}),
	}
	var recoveryDecision router.Decision
	var attemptedModel string
	winnerProvider := new(string)
	_, err := svc.dispatchWithFallback(ctx, failoverInputs{
		w:                               httptest.NewRecorder(),
		subscriptionStateRequest:        &request,
		subscriptionStateWinnerProvider: winnerProvider,
		initialDecision:                 router.Decision{Model: stateClaudeModel, Provider: providers.ProviderAnthropic},
		purpose:                         inference.PurposeAnthropicMessages,
		buildAlternative: func(router.Decision) (dispatchAttempt, error) {
			return func(_ context.Context, attemptedTarget router.Decision, _ providers.Client) error {
				attemptedModel = attemptedTarget.Model
				return &providers.UpstreamStatusError{Status: http.StatusBadRequest}
			}, nil
		},
		onSubscriptionStateTarget: func(target router.Decision, _ []catalog.ProviderBinding) {
			recoveryDecision = target
		},
	})
	require.Error(t, err)
	require.Empty(t, *winnerProvider, "a failed target must not be recorded as the provider that served the request")
	require.Equal(t, statePaidModel, recoveryDecision.Model)
	require.Equal(t, statePaidModel, attemptedModel)
}

func TestSubscriptionStateModelsProtectPaidSummaries(t *testing.T) {
	env, err := translate.ParseAnthropic([]byte(sampleConversation))
	require.NoError(t, err)
	fake := &fakeHandoverProvider{respBody: canonicalAnthropicResponse, respStatus: http.StatusOK}
	summarizer := newTestSummarizer(t, fake, "", time.Second)
	ctx := conditionalModelsContext([]string{policy.HandoverSummaryDefaultModel}, []string{statePaidModel})
	_, _, err = summarizer.Summarize(ctx, env, router.Request{})
	require.Error(t, err)
	require.Zero(t, fake.calls)
	ctx = context.WithValue(ctx, InstallationSubscriptionModelsWhenInactiveContextKey{}, []string{policy.HandoverSummaryDefaultModel})
	text, _, err := summarizer.Summarize(ctx, env, router.Request{})
	require.NoError(t, err)
	require.Equal(t, "Refactor in progress: step 1 done, step 2 pending.", text)
	require.Equal(t, 1, fake.calls)
}

func TestSubscriptionStateModelsEmptySelectedStateFailsClosed(t *testing.T) {
	ctx := conditionalModelsContext([]string{stateClaudeModel}, nil)
	ctx = context.WithValue(ctx, subscriptionStateAllowedModelsKey{}, modelSet(nil))
	allowed := allowedModelsForRequest(ctx)
	require.NotNil(t, allowed, "nil would mean unrestricted routing")
	require.Empty(t, allowed)
	require.False(t, modelPermittedByAllowlist(ctx, stateClaudeModel))
}

func TestSubscriptionStatePaidRescueRequiresExhaustedSetTarget(t *testing.T) {
	ctx := conditionalModelsContext([]string{stateClaudeModel, stateCodexModel}, []string{statePaidModel})
	require.True(t, paidFallbackForbidden(ctx))
	require.False(t, paidFallbackForbiddenForModel(ctx, statePaidModel))
	require.True(t, paidFallbackForbiddenForModel(ctx, stateClaudeModel))
	require.Equal(t, []router.Decision{{Model: statePaidModel}}, subscriptionStatePaidRescueDecisions(ctx, []router.Decision{
		{Model: stateClaudeModel}, {Model: statePaidModel}, {Model: stateCodexModel},
	}))

	allowedRescueCtx := subscriptionStatePaidRescueContext(ctx, statePaidModel)
	require.False(t, paidFallbackForbidden(allowedRescueCtx))
	require.True(t, paidFallbackForbidden(subscriptionStatePaidRescueContext(ctx, stateClaudeModel)))
}

func TestSubscriptionStateModelsKeepSafetyExclusions(t *testing.T) {
	ctx := conditionalModelsContext([]string{stateClaudeModel, stateCodexModel}, []string{statePaidModel})
	request := router.Request{
		AllowedModels:                 allowedModelsForRequest(ctx),
		SafetyExcludedModels:          map[string]struct{}{stateClaudeModel: {}},
		UnsignedHistoryExcludedModels: map[string]struct{}{stateCodexModel: {}},
	}
	_, narrowed := (&Service{}).subscriptionStateRequest(ctx, request, []string{stateClaudeModel, stateCodexModel})
	require.Empty(t, narrowed.AllowedModels)
}

func TestSubscriptionStateModelsStrictDirectSubscription(t *testing.T) {
	client := &fakeClient{name: providers.ProviderAnthropic, outcomes: []fakeOutcome{{writeBytes: []byte("included answer")}}}
	svc := NewService(stateModelRouter{}, map[string]providers.Client{providers.ProviderAnthropic: client}, nil, false, nil, nil, false, "", "", nil).
		WithUsageObserver(conditionalModelsObserver(usage.Snapshot{Primary: usage.Window{UsedPercent: 0.1, WindowMinutes: 300}}))
	ctx := WithManagedSubscriptionUsage(context.Background())
	ctx = context.WithValue(ctx, InstallationSubscriptionModelsWhenActiveContextKey{}, []string{stateClaudeModel, stateCodexModel})
	ctx = context.WithValue(ctx, InstallationSubscriptionModelsWhenInactiveContextKey{}, []string{statePaidModel})
	ctx = context.WithValue(ctx, InstallationUsageBypassContextKey{}, UsageBypassConfig{Enabled: true})
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+conditionalModelsSubscriptionToken)
	request := router.Request{RequestedModel: stateClaudeModel, AllowedModels: allowedModelsForRequest(ctx), EnabledProviders: modelSet([]string{providers.ProviderAnthropic, providers.ProviderOpenAI})}
	rec := httptest.NewRecorder()
	buffer := newPreludeBuffer(rec)
	var winner router.Decision
	_, err := svc.dispatchWithFallback(ctx, failoverInputs{
		w: rec, buf: buffer, subscriptionStateRequest: &request, subscriptionStateHeaders: headers,
		initialDecision: router.Decision{Model: stateCodexModel, Provider: providers.ProviderOpenAI},
		purpose:         inference.PurposeAnthropicMessages,
		buildAlternative: func(target router.Decision) (dispatchAttempt, error) {
			return func(attemptCtx context.Context, decision router.Decision, upstream providers.Client) error {
				require.True(t, servedOnSubscription(attemptCtx))
				require.Equal(t, conditionalModelsSubscriptionToken, string(CredentialsFromContext(attemptCtx).APIKey))
				buffer.Seal()
				return upstream.Proxy(attemptCtx, decision, providers.PreparedRequest{}, buffer, httptest.NewRequest(http.MethodPost, routePathMessages, nil))
			}, nil
		},
		onAlternative: func(target router.Decision) { winner = target },
	})
	require.NoError(t, err)
	require.Equal(t, stateClaudeModel, winner.Model)
	require.Equal(t, reasonUsageBypass, winner.Reason)
	require.Equal(t, "included answer", rec.Body.String())
}

func TestSubscriptionStateModelsRespectManagedBoostAllowlist(t *testing.T) {
	ctx := boostPlanOwnedContext()
	ctx = context.WithValue(ctx, InstallationAllowedModelsContextKey{}, []string{stateCodexModel})
	ctx = context.WithValue(ctx, InstallationSubscriptionModelsWhenActiveContextKey{}, []string{stateClaudeModel, stateCodexModel})
	ctx = context.WithValue(ctx, InstallationSubscriptionModelsWhenInactiveContextKey{}, []string{statePaidModel})
	require.Equal(t, map[string]struct{}{stateCodexModel: {}}, allowedModelsForRequest(ctx))
}

func TestSubscriptionStateModelsEnabledByFlagWithEmptySets(t *testing.T) {
	ctx := context.WithValue(context.Background(), InstallationSubscriptionStateModelsEnabledContextKey{}, true)

	assert.True(t, subscriptionStateModelsEnabled(ctx))
	assert.Empty(t, installationAllowedModelSet(context.WithValue(ctx, subscriptionStateAllowedModelsKey{}, modelSet(nil))))
}

// A Codex turn whose routed pick is another family can still be served by a
// native Responses attempt on the included Codex plan. That attempt's stream
// must reach the caller intact instead of failing as a cut stream.
func TestSubscriptionStateModelsCodexNativeAttemptAfterCrossFamilyPick(t *testing.T) {
	const nativeStream = "data: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{\"id\":\"resp_synthetic\",\"status\":\"in_progress\",\"output\":[]}}\n\n" +
		"data: {\"type\":\"response.output_item.added\",\"sequence_number\":1,\"output_index\":0,\"item\":{\"id\":\"msg_synthetic\",\"type\":\"message\",\"role\":\"assistant\",\"status\":\"in_progress\",\"content\":[]}}\n\n" +
		"data: {\"type\":\"response.output_text.delta\",\"sequence_number\":2,\"item_id\":\"msg_synthetic\",\"output_index\":0,\"content_index\":0,\"delta\":\"synthetic answer\"}\n\n" +
		"data: {\"type\":\"response.output_item.done\",\"sequence_number\":3,\"output_index\":0,\"item\":{\"id\":\"msg_synthetic\",\"type\":\"message\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[{\"type\":\"output_text\",\"text\":\"synthetic answer\"}]}}\n\n" +
		"data: {\"type\":\"response.completed\",\"sequence_number\":4,\"response\":{\"id\":\"resp_synthetic\",\"status\":\"completed\",\"output\":[{\"id\":\"msg_synthetic\",\"type\":\"message\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[{\"type\":\"output_text\",\"text\":\"synthetic answer\"}]}],\"usage\":{\"input_tokens\":11,\"output_tokens\":7}}}\n\n"
	var models []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveSyntheticCodexQuota(w, r) {
			return
		}
		body, _ := io.ReadAll(r.Body)
		models = append(models, gjson.GetBytes(body, "model").String())
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, nativeStream)
	}))
	defer upstream.Close()
	codexClient := openai.NewClient("expensive-paid-key", upstream.URL)
	codexClient.SetCodexBaseURL(upstream.URL)
	svc := NewService(stateModelRouter{}, map[string]providers.Client{
		providers.ProviderAnthropic:  anthropic.NewClient("expensive-paid-key", upstream.URL),
		providers.ProviderOpenAI:     codexClient,
		providers.ProviderOpenRouter: openaicompat.NewClient("cheap-paid-key", upstream.URL),
	}, nil, false, nil, nil, false, "", "", nil).
		WithManagedSubscriptions(&stateModelLeaser{activeProviders: map[subscriptions.Provider]bool{subscriptions.ProviderCodex: true}}).
		WithDeploymentKeyedProviders(modelSet([]string{providers.ProviderAnthropic, providers.ProviderOpenAI, providers.ProviderOpenRouter}))
	ctx := context.WithValue(managedSubscriptionTestContext(), ManagedSubscriptionProvidersContextKey{}, map[auth.SubscriptionProvider]struct{}{auth.SubscriptionProviderClaude: {}, auth.SubscriptionProviderCodex: {}})
	ctx = context.WithValue(ctx, InstallationSubscriptionModelsWhenActiveContextKey{}, []string{stateClaudeModel, stateCodexModel})
	ctx = context.WithValue(ctx, InstallationSubscriptionModelsWhenInactiveContextKey{}, []string{statePaidModel})
	ctx = context.WithValue(ctx, OpenAISubscriptionContextKey{}, codexTestToken)
	ctx = context.WithValue(ctx, OpenAIAccountIDContextKey{}, codexTestAccountID)
	ctx = context.WithValue(ctx, ClientIdentityContextKey{}, ClientIdentity{ClientApp: ClientAppCodex})

	body := `{"model":"auto","stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"synthetic task"}]}],"tools":[{"type":"custom","name":"exec"}]}`
	rec := httptest.NewRecorder()
	err := svc.ProxyOpenAIResponses(ctx, []byte(body), rec, httptest.NewRequest(http.MethodPost, routePathResponses, strings.NewReader(body)))

	require.NoError(t, err)
	require.Equal(t, []string{stateCodexModel}, models, "the included Codex plan serves the turn")
	out := rec.Body.String()
	assert.Contains(t, out, "synthetic answer")
	assert.Contains(t, out, `"type":"response.completed"`)
	assert.NotContains(t, out, "response.failed")
}

// The same swap must keep a Codex freeform tool call intact: the caller asked
// for a custom tool and must get a custom_tool_call back.
func TestSubscriptionStateModelsCodexCustomToolAfterCrossFamilyPick(t *testing.T) {
	var toolTypes []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveSyntheticCodexQuota(w, r) {
			return
		}
		body, _ := io.ReadAll(r.Body)
		toolType := gjson.GetBytes(body, "tools.0.type").String()
		toolTypes = append(toolTypes, toolType)
		item := `{"id":"ctc_synthetic","type":"custom_tool_call","status":"completed","call_id":"call_synthetic","name":"exec","input":"ls -la"}`
		added := `{"id":"ctc_synthetic","type":"custom_tool_call","status":"in_progress","call_id":"call_synthetic","name":"exec","input":""}`
		delta := `{"type":"response.custom_tool_call_input.delta","sequence_number":2,"item_id":"ctc_synthetic","output_index":0,"delta":"ls -la"}`
		if toolType == "function" {
			item = `{"id":"fc_synthetic","type":"function_call","status":"completed","call_id":"call_synthetic","name":"exec","arguments":"{\"input\":\"ls -la\"}"}`
			added = `{"id":"fc_synthetic","type":"function_call","status":"in_progress","call_id":"call_synthetic","name":"exec","arguments":""}`
			delta = `{"type":"response.function_call_arguments.delta","sequence_number":2,"item_id":"fc_synthetic","output_index":0,"delta":"{\"input\":\"ls -la\"}"}`
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{\"id\":\"resp_synthetic\",\"status\":\"in_progress\",\"output\":[]}}\n\n"+
			"data: {\"type\":\"response.output_item.added\",\"sequence_number\":1,\"output_index\":0,\"item\":"+added+"}\n\n"+
			"data: "+delta+"\n\n"+
			"data: {\"type\":\"response.output_item.done\",\"sequence_number\":3,\"output_index\":0,\"item\":"+item+"}\n\n"+
			"data: {\"type\":\"response.completed\",\"sequence_number\":4,\"response\":{\"id\":\"resp_synthetic\",\"status\":\"completed\",\"output\":["+item+"],\"usage\":{\"input_tokens\":11,\"output_tokens\":7}}}\n\n")
	}))
	defer upstream.Close()
	codexClient := openai.NewClient("expensive-paid-key", upstream.URL)
	codexClient.SetCodexBaseURL(upstream.URL)
	svc := NewService(stateModelRouter{}, map[string]providers.Client{
		providers.ProviderAnthropic:  anthropic.NewClient("expensive-paid-key", upstream.URL),
		providers.ProviderOpenAI:     codexClient,
		providers.ProviderOpenRouter: openaicompat.NewClient("cheap-paid-key", upstream.URL),
	}, nil, false, nil, nil, false, "", "", nil).
		WithManagedSubscriptions(&stateModelLeaser{activeProviders: map[subscriptions.Provider]bool{subscriptions.ProviderCodex: true}}).
		WithDeploymentKeyedProviders(modelSet([]string{providers.ProviderAnthropic, providers.ProviderOpenAI, providers.ProviderOpenRouter}))
	ctx := context.WithValue(managedSubscriptionTestContext(), ManagedSubscriptionProvidersContextKey{}, map[auth.SubscriptionProvider]struct{}{auth.SubscriptionProviderClaude: {}, auth.SubscriptionProviderCodex: {}})
	ctx = context.WithValue(ctx, InstallationSubscriptionModelsWhenActiveContextKey{}, []string{stateClaudeModel, stateCodexModel})
	ctx = context.WithValue(ctx, InstallationSubscriptionModelsWhenInactiveContextKey{}, []string{statePaidModel})
	ctx = context.WithValue(ctx, OpenAISubscriptionContextKey{}, codexTestToken)
	ctx = context.WithValue(ctx, OpenAIAccountIDContextKey{}, codexTestAccountID)
	ctx = context.WithValue(ctx, ClientIdentityContextKey{}, ClientIdentity{ClientApp: ClientAppCodex})

	body := `{"model":"auto","stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"list files"}]}],"tools":[{"type":"custom","name":"exec","description":"run a script"}]}`
	rec := httptest.NewRecorder()
	err := svc.ProxyOpenAIResponses(ctx, []byte(body), rec, httptest.NewRequest(http.MethodPost, routePathResponses, strings.NewReader(body)))

	require.NoError(t, err)
	require.Equal(t, []string{"function"}, toolTypes, "the swapped attempt is emitted onto Responses from the chat projection")
	out := rec.Body.String()
	assert.Contains(t, out, `"type":"custom_tool_call"`)
	assert.Contains(t, out, "ls -la")
	assert.Contains(t, out, `"type":"response.completed"`)
	assert.NotContains(t, out, "response.failed")
}
