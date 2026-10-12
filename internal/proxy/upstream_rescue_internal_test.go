package proxy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/cluster"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/subscriptions"
)

func TestSubscriptionPreferencesDoNotExcludeOrgAllowedSonnet(t *testing.T) {
	sonnet := catalog.ModelIDClaudeSonnet55.String()
	ctx := context.WithValue(context.Background(), InstallationAllowedModelsContextKey{}, []string{sonnet, catalog.ModelGPT6Luna})
	ctx = context.WithValue(ctx, InstallationSubscriptionModelsWhenActiveContextKey{}, []string{catalog.ModelGPT6Luna})
	ctx = context.WithValue(ctx, InstallationSubscriptionModelsWhenInactiveContextKey{}, []string{catalog.ModelGPT6Luna})
	require.Contains(t, allowedModelsForRequest(ctx), sonnet)
	require.NotContains(t, (&Service{}).excludedModelsForRequest(ctx), sonnet)
	ctx = context.WithValue(ctx, InstallationAllowedModelsContextKey{}, []string{catalog.ModelGPT6Luna})
	require.NotContains(t, allowedModelsForRequest(ctx), sonnet)
}

type rescueSessionStore struct{ *rowBackedPinStore }

func (s rescueSessionStore) Get(_ context.Context, _ [sessionpin.SessionKeyLen]byte, role string) (sessionpin.Pin, bool, error) {
	pin, found := s.rows[role]
	return pin, found, nil
}

type rescuePriorityRouter struct{ decisions []router.Decision }

func (r rescuePriorityRouter) Route(_ context.Context, request router.Request) (router.Decision, error) {
	for _, decision := range r.decisions {
		if _, excluded := request.ExcludedModels[decision.Model]; excluded {
			continue
		}
		if _, excluded := request.AutomaticExcludedModels[decision.Model]; excluded {
			continue
		}
		if request.AllowedModels != nil {
			if _, allowed := request.AllowedModels[decision.Model]; !allowed {
				continue
			}
		}
		if request.EnabledProviders != nil {
			if _, enabled := request.EnabledProviders[decision.Provider]; !enabled {
				continue
			}
		}
		return decision, nil
	}
	return router.Decision{}, cluster.ErrAllowlistEmptiesPool
}

type subscriptionFailingClient struct{ failingClient }

func (*subscriptionFailingClient) SupportsSubscriptions() bool { return true }

func TestUpstreamRescueDoesNotReturnToFailedModel(t *testing.T) {
	for _, failure := range []struct {
		name             string
		subscriptionWalk bool
		err              error
	}{
		{"http", false, &providers.UpstreamErrorResponse{Status: http.StatusBadGateway}},
		{"subscription-http", true, &providers.UpstreamErrorResponse{Status: http.StatusBadGateway}},
		{"overloaded", false, &providers.UpstreamErrorResponse{Status: providerOverloadedStatus}},
		{"subscription-overloaded", true, &providers.UpstreamErrorResponse{Status: providerOverloadedStatus}},
		{"transport", false, &url.Error{Op: http.MethodPost, URL: "https://synthetic.invalid/v1/messages", Err: syscall.ECONNRESET}},
		{"subscription-transport", true, &url.Error{Op: http.MethodPost, URL: "https://synthetic.invalid/v1/messages", Err: syscall.ECONNRESET}},
	} {
		for _, openAI := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/chat=%t", failure.name, openAI), func(t *testing.T) {
				store := rescueSessionStore{newRowBackedPinStore()}
				primary := &subscriptionFailingClient{failingClient{err: failure.err}}
				sibling := &servingClient{}
				priority := rescuePriorityRouter{decisions: []router.Decision{rescuableDecision("cluster:test", true), {Model: rescuerModel, Provider: providers.ProviderOpenAI}}}
				svc := NewService(priority, map[string]providers.Client{providers.ProviderAnthropic: primary, providers.ProviderOpenAI: sibling}, nil, false, nil, store, false, "", "", nil).
					WithAvailableModels(modelSet([]string{rescuedPrimaryModel, rescuerModel})).
					WithDeploymentKeyedProviders(modelSet([]string{providers.ProviderAnthropic, providers.ProviderOpenAI})).
					WithRetrySleep(func(context.Context, time.Duration) error { return nil })
				ctx := rescuedFailureCtx()
				if failure.subscriptionWalk {
					svc.WithManagedSubscriptions(&stateModelLeaser{activeProviders: map[subscriptions.Provider]bool{subscriptions.ProviderClaude: true}})
					ctx = context.WithValue(ctx, ManagedSubscriptionProvidersContextKey{}, map[auth.SubscriptionProvider]struct{}{auth.SubscriptionProviderClaude: {}})
					ctx = context.WithValue(ctx, InstallationSubscriptionModelsWhenActiveContextKey{}, []string{rescuedPrimaryModel})
					ctx = context.WithValue(ctx, InstallationSubscriptionModelsWhenInactiveContextKey{}, []string{rescuerModel})
				}
				body := anthropicMessagesBody()
				serve := func() *httptest.ResponseRecorder {
					recorder := httptest.NewRecorder()
					request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(string(body)))
					var err error
					if openAI {
						err = svc.ProxyOpenAIChatCompletion(ctx, body, recorder, request)
					} else {
						err = svc.ProxyMessages(ctx, body, recorder, request)
					}
					require.NoError(t, err)
					return recorder
				}
				first := serve()
				require.Contains(t, first.Body.String(), "Upstream provider anthropic is unavailable; rerouting this request to "+rescuerModel)
				failedAttempts := primary.calls
				require.Positive(t, failedAttempts)
				for range 3 {
					next := serve()
					require.Equal(t, rescuerModel, next.Header().Get(HeaderRouterModel))
					require.Equal(t, failedAttempts, primary.calls, "subsequent turns must not probe the failed model again")
				}
			})
		}
	}
}

type recoveryJSONClient struct{ servingClient }

func (c *recoveryJSONClient) Proxy(ctx context.Context, decision router.Decision, prepared providers.PreparedRequest, writer http.ResponseWriter, request *http.Request) error {
	if strings.Contains(string(prepared.Body), `"stream":true`) {
		return c.servingClient.Proxy(ctx, decision, prepared, writer, request)
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusOK)
	_, err := io.WriteString(writer, `{"id":"synthetic","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"recovered"}]}],"choices":[{"index":0,"message":{"role":"assistant","content":"recovered"},"finish_reason":"stop"}],"usage":{"input_tokens":5,"output_tokens":2,"prompt_tokens":5,"completion_tokens":2}}`)
	return err
}

type nativeRecoveryClient struct{ recoveryJSONClient }

func (c *nativeRecoveryClient) Proxy(ctx context.Context, decision router.Decision, prepared providers.PreparedRequest, writer http.ResponseWriter, request *http.Request) error {
	if decision.Model == rescuedPrimaryModel {
		return &providers.UpstreamErrorResponse{Status: http.StatusBadGateway}
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusOK)
	_, err := io.WriteString(writer, `{"id":"synthetic","type":"message","role":"assistant","content":[{"type":"text","text":"recovered"}],"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":2}}`)
	return err
}

func TestUpstreamRecoveryNoticeBeforeNativeResponseHeaders(t *testing.T) {
	sonnet := catalog.ModelIDClaudeSonnet55.String()
	decision := rescuableDecision("cluster:test", true)
	decision.Metadata.CandidateModels = []string{rescuedPrimaryModel, sonnet}
	decision.Metadata.CandidateProviders = map[string]string{sonnet: providers.ProviderAnthropic}
	svc := NewService(staticRouter{decision: decision}, map[string]providers.Client{providers.ProviderAnthropic: &nativeRecoveryClient{}}, nil, false, nil, nil, false, "", "", nil).
		WithAvailableModels(modelSet([]string{rescuedPrimaryModel, sonnet})).
		WithDeploymentKeyedProviders(modelSet([]string{providers.ProviderAnthropic})).
		WithRetrySleep(func(context.Context, time.Duration) error { return nil })
	body := fmt.Sprintf(`{"model":%q,"max_tokens":1024,"messages":[{"role":"user","content":"synthetic task"}]}`, rescuedPrimaryModel)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, routePathMessages, nil)
	request.Header.Set(routingMarkerHeader, "off")
	err := svc.ProxyMessages(rescuedFailureCtx(), []byte(body), recorder, request)
	require.NoError(t, err)
	require.Contains(t, recorder.Body.String(), "recovered")
	require.Equal(t, "Upstream provider anthropic is unavailable; rerouting this request to "+sonnet, recorder.Result().Header.Get(HeaderRouterNotice))
}

func TestUpstreamRecoveryNoticeAcrossResponseFormats(t *testing.T) {
	for _, surface := range []string{routePathMessages, routePathChatCompletions, routePathResponses} {
		for _, streaming := range []bool{false, true} {
			for _, recoveryFails := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/stream=%t/failed=%t", surface, streaming, recoveryFails), func(t *testing.T) {
					primary := &failingClient{err: &providers.UpstreamErrorResponse{Status: http.StatusBadGateway}}
					var sibling providers.Client = &recoveryJSONClient{}
					if recoveryFails {
						sibling = &failingClient{err: &providers.UpstreamErrorResponse{Status: http.StatusBadGateway}}
					}
					svc := newRescuedFailureTurnService(nil, rescuableDecision("cluster:test", true), primary, sibling, true)
					ctx := context.WithValue(rescuedFailureCtx(), ClientIdentityContextKey{}, ClientIdentity{ClientApp: ClientAppCodex})
					body := fmt.Sprintf(`{"model":"%s","stream":%t,"messages":[{"role":"user","content":"synthetic task"}]}`, rescuedPrimaryModel, streaming)
					if surface == routePathResponses {
						body = fmt.Sprintf(`{"model":"%s","stream":%t,"input":[{"role":"user","content":"synthetic task"}]}`, rescuedPrimaryModel, streaming)
					}
					recorder := httptest.NewRecorder()
					request := httptest.NewRequest(http.MethodPost, surface, strings.NewReader(body))
					var err error
					switch surface {
					case routePathMessages:
						err = svc.ProxyMessages(ctx, []byte(body), recorder, request)
					case routePathResponses:
						err = svc.ProxyOpenAIResponses(ctx, []byte(body), recorder, request)
					default:
						err = svc.ProxyOpenAIChatCompletion(ctx, []byte(body), recorder, request)
					}
					if recoveryFails {
						require.Error(t, err)
						require.NotContains(t, recorder.Body.String(), "Upstream provider")
						require.Empty(t, recorder.Result().Header.Get(HeaderRouterNotice))
					} else {
						require.NoError(t, err)
						if streaming {
							require.Contains(t, recorder.Body.String(), "Upstream provider anthropic is unavailable; rerouting this request to "+rescuerModel)
						} else {
							require.Equal(t, "Upstream provider anthropic is unavailable; rerouting this request to "+rescuerModel, recorder.Result().Header.Get(HeaderRouterNotice))
						}
					}
				})
			}
		}
	}
}

func TestSubscriptionPreferencesAllowUnlistedIncludedSonnet(t *testing.T) {
	sonnet := catalog.ModelIDClaudeSonnet55.String()
	ctx := managedSubscriptionTestContext()
	ctx = context.WithValue(ctx, ManagedSubscriptionProvidersContextKey{}, map[auth.SubscriptionProvider]struct{}{auth.SubscriptionProviderClaude: {}})
	ctx = context.WithValue(ctx, InstallationAllowedModelsContextKey{}, []string{sonnet, catalog.ModelGPT6Luna})
	ctx = context.WithValue(ctx, InstallationSubscriptionModelsWhenActiveContextKey{}, []string{catalog.ModelGPT6Luna})
	ctx = context.WithValue(ctx, InstallationSubscriptionModelsWhenInactiveContextKey{}, []string{catalog.ModelGPT6Luna})
	client := &fakeClient{outcomes: []fakeOutcome{{writeBytes: []byte("included Sonnet answer")}}}
	svc := NewService(nil, map[string]providers.Client{providers.ProviderAnthropic: client}, nil, false, nil, nil, false, "", "", nil).
		WithAvailableModels(modelSet([]string{sonnet, catalog.ModelGPT6Luna})).
		WithManagedSubscriptions(&stateModelLeaser{activeProviders: map[subscriptions.Provider]bool{subscriptions.ProviderClaude: true}})
	request := router.Request{ForceModel: sonnet, AllowedModels: allowedModelsForRequest(ctx), EnabledProviders: modelSet([]string{providers.ProviderAnthropic})}
	recorder := httptest.NewRecorder()
	_, err := svc.dispatchWithFallback(ctx, failoverInputs{
		w: recorder, buf: newPreludeBuffer(recorder), subscriptionStateRequest: &request,
		initialDecision: router.Decision{Model: sonnet, Provider: providers.ProviderAnthropic}, purpose: inference.PurposeAnthropicMessages,
		buildAlternative: func(router.Decision) (dispatchAttempt, error) {
			return func(attemptCtx context.Context, target router.Decision, upstream providers.Client) error {
				require.True(t, servedOnSubscription(attemptCtx))
				return upstream.Proxy(attemptCtx, target, providers.PreparedRequest{}, recorder, nil)
			}, nil
		},
	})
	require.NoError(t, err)
	require.Contains(t, recorder.Body.String(), "included Sonnet answer")
}

func TestUpstreamRescueDefaultPersistsFailureAndNotifiesClient(t *testing.T) {
	for _, openAI := range []bool{false, true} {
		t.Run(map[bool]string{false: "messages", true: "chat"}[openAI], func(t *testing.T) {
			store := &demotionStubPinStore{}
			primary := &failingClient{err: &providers.UpstreamErrorResponse{Status: http.StatusBadGateway, Body: []byte(`{"error":{"type":"api_error","message":"upstream unavailable"}}`)}}
			sibling := &servingClient{}
			// Leave the deployment default intact; a rescue must protect the next turn.
			svc := NewService(staticRouter{decision: rescuableDecision("cluster:test", true)},
				map[string]providers.Client{providers.ProviderAnthropic: primary, providers.ProviderOpenAI: sibling},
				nil, false, nil, store, false, providers.ProviderAnthropic, "", nil).
				WithDeploymentKeyedProviders(modelSet([]string{providers.ProviderAnthropic, providers.ProviderOpenAI})).
				WithRetrySleep(func(context.Context, time.Duration) error { return nil })
			body := anthropicMessagesBody()
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(string(body)))
			var err error
			if openAI {
				err = svc.ProxyOpenAIChatCompletion(rescuedFailureCtx(), body, recorder, request)
			} else {
				err = svc.ProxyMessages(rescuedFailureCtx(), body, recorder, request)
			}
			require.NoError(t, err)
			require.NotEmpty(t, store.demotions)
			for _, demotion := range store.demotions {
				require.Equal(t, rescuedPrimaryModel, demotion.model)
			}
			require.Contains(t, recorder.Body.String(), "Upstream provider anthropic is unavailable; rerouting this request to "+rescuerModel)
		})
	}
}
