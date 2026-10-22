package middleware_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"weave-os/router/internal/api/classifier"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/server/middleware"
	"weave-os/router/internal/subscriptions/entitlement"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type classifierEnrollmentTestKey struct{}

type routingModelTestRouter struct{}

func (routingModelTestRouter) Route(context.Context, router.Request) (router.Decision, error) {
	panic("routing model middleware must not invoke inference")
}

func TestRoutingModelSelectsPolicyAndPreservesRequest(t *testing.T) {
	for _, tc := range []struct {
		name            string
		model           string
		initial         router.Strategy
		available       bool
		force           bool
		target          policyregistry.ServingTarget
		plan            entitlement.Plan
		missingSnapshot bool
		missingStrategy bool
		status          int
		want            router.Strategy
	}{
		{name: "default", model: string(router.ModelAutomatic), initial: router.StrategyHMMEmbedding, status: http.StatusOK, want: router.StrategyHMMEmbedding},
		{name: "hmm", model: string(router.ModelHMM), available: true, status: http.StatusOK, want: router.StrategyHMMBeta},
		{name: "hmm unavailable", model: string(router.ModelHMM), status: http.StatusServiceUnavailable},
		{name: "qwen", model: string(router.ModelQwen), target: policyregistry.TargetInternal, available: true, status: http.StatusOK, want: router.StrategyHMMQwen},
		{name: "qwen unavailable", model: string(router.ModelQwen), target: policyregistry.TargetInternal, status: http.StatusServiceUnavailable},
		{name: "qwen requires internal admission", model: string(router.ModelQwen), available: true, status: http.StatusForbidden},
		{name: "qwen cannot replace an atomic classifier thread", model: string(router.ModelQwen), initial: router.StrategyLLMClassifier, available: true, status: http.StatusConflict},
		{name: "default preserves classifier enrollment", model: string(router.ModelAutomatic), initial: router.StrategyLLMClassifier, status: http.StatusOK, want: router.StrategyLLMClassifier},
		{name: "direct model unaffected", model: "gpt-6.1-sol", initial: router.StrategyHMMEmbedding, status: http.StatusOK, want: router.StrategyHMMEmbedding},
		{name: "conflicting direct pin", model: string(router.ModelHMM), available: true, force: true, status: http.StatusConflict},
		{name: "unknown router", model: "weave-missing", status: http.StatusBadRequest},
		{name: "stable cannot select alternate classifier", model: string(router.ModelHMM), initial: router.StrategyHMMEmbedding, available: true, target: policyregistry.TargetStable, status: http.StatusForbidden},
		{name: "stable cannot select qwen", model: string(router.ModelQwen), available: true, target: policyregistry.TargetStable, status: http.StatusForbidden},
		{name: "subscriber profile cannot select qwen", model: string(router.ModelQwen), available: true, target: policyregistry.TargetInternal, plan: entitlement.PlanMax, status: http.StatusForbidden},
		{name: "internal boost profile selects qwen", model: string(router.ModelQwen), available: true, target: policyregistry.TargetInternal, plan: entitlement.PlanBoost, status: http.StatusOK, want: router.StrategyHMMQwen},
		{name: "internal boost profile selects hmm", model: string(router.ModelHMM), initial: router.StrategyHMM, available: true, target: policyregistry.TargetInternal, plan: entitlement.PlanBoost, status: http.StatusOK, want: router.StrategyHMMBeta},
		{name: "stable boost profile cannot select qwen", model: string(router.ModelQwen), available: true, target: policyregistry.TargetStable, plan: entitlement.PlanBoost, status: http.StatusForbidden},
		{name: "internal hmm", model: string(router.ModelHMM), initial: router.StrategyHMMEmbedding, available: true, target: policyregistry.TargetInternal, status: http.StatusOK, want: router.StrategyHMMBeta},
		{name: "hmm missing from admitted release", model: string(router.ModelHMM), available: true, target: policyregistry.TargetInternal, missingStrategy: true, status: http.StatusServiceUnavailable},
		{name: "qwen missing from admitted release", model: string(router.ModelQwen), available: true, target: policyregistry.TargetInternal, missingStrategy: true, status: http.StatusServiceUnavailable},
		{name: "bootstrap readiness cannot admit qwen", model: string(router.ModelQwen), available: true, target: policyregistry.TargetInternal, missingSnapshot: true, status: http.StatusServiceUnavailable},
		{name: "decision_model", model: string(router.ModelDecision), target: policyregistry.TargetInternal, available: true, status: http.StatusOK, want: router.StrategyDecisionModel},
		{name: "decision model does not need an admitted snapshot policy", model: string(router.ModelDecision), target: policyregistry.TargetInternal, available: true, missingStrategy: true, status: http.StatusOK, want: router.StrategyDecisionModel},
		{name: "decision model unavailable without judge credential", model: string(router.ModelDecision), target: policyregistry.TargetInternal, status: http.StatusServiceUnavailable},
		{name: "self-hosted decision model needs no admission", model: string(router.ModelDecision), available: true, status: http.StatusOK, want: router.StrategyDecisionModel},
		{name: "managed decision model requires internal admission", model: string(router.ModelDecision), target: policyregistry.TargetStable, available: true, status: http.StatusForbidden},
		{name: "subscriber profile cannot select decision model", model: string(router.ModelDecision), available: true, target: policyregistry.TargetInternal, plan: entitlement.PlanMax, status: http.StatusForbidden},
		{name: "stable default remains available", model: string(router.ModelAutomatic), initial: router.StrategyHMMEmbedding, target: policyregistry.TargetStable, status: http.StatusOK, want: router.StrategyHMMEmbedding},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			engine := gin.New()
			body := `{"model":"` + tc.model + `","messages":[{"role":"user","content":"hello"}]}`
			engine.POST("/v1/messages", middleware.WithClassifierThread(&proxy.Service{}, func(strategy router.Strategy) bool {
				require.Contains(t, []router.Strategy{router.StrategyHMMBeta, router.StrategyHMMQwen, router.StrategyDecisionModel}, strategy)
				return tc.available
			}), func(c *gin.Context) {
				require.Equal(t, tc.want, router.StrategyFromContext(c.Request.Context()))
				retained, err := io.ReadAll(c.Request.Body)
				require.NoError(t, err)
				require.Equal(t, body, string(retained))
				model, selected := router.RoutingModelFromContext(c.Request.Context())
				require.Equal(t, router.IsRoutingModel(tc.model), selected)
				if selected {
					require.Equal(t, router.RoutingModel(tc.model), model)
				}
				c.Status(http.StatusOK)
			})
			ctx := router.WithStrategy(context.Background(), tc.initial)
			if tc.target != "" {
				identity := requestcontext.ServingIdentity{Target: string(tc.target), ReleaseID: "reviewed-release", BindingID: "reviewed-binding", CredentialIdentity: "test-principal", Plan: string(tc.plan)}
				ctx = requestcontext.WithServingIdentity(ctx, identity)
				if !tc.missingSnapshot {
					snapshot := &policyregistry.Snapshot{Routers: map[router.Strategy]router.Router{router.StrategyHMM: routingModelTestRouter{}}}
					if !tc.missingStrategy {
						snapshot.Routers[router.StrategyHMMBeta] = routingModelTestRouter{}
						snapshot.Routers[router.StrategyHMMQwen] = routingModelTestRouter{}
					}
					ctx = policyregistry.WithServingSnapshot(ctx, snapshot)
				}
			}
			req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body)).WithContext(ctx)
			if tc.force {
				req.Header.Set(proxy.ForceModelHeader, "gpt-6.1-sol")
			}
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, req)
			require.Equal(t, tc.status, recorder.Code, recorder.Body.String())
		})
	}
}

func TestClassifierThreadsRemainExplicitlyEnrolled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	installation := uuid.New()
	store := &threadEndpointStore{}
	svc := &proxy.Service{}
	require.NoError(t, svc.WithClassifierSessions(proxy.ClassifierSessionConfig{
		Release: "llm-classifier-v1.0.0", ReleaseSHA256: strings.Repeat("a", 64),
		SelectionPolicySHA256: strings.Repeat("b", 64), SigningKey: []byte(strings.Repeat("k", 32)),
		InstallationIDs: []uuid.UUID{installation},
	}, store, unusedThreadClassifier{}))
	ctx := context.WithValue(context.Background(), proxy.InstallationIDContextKey{}, installation.String())
	ctx = context.WithValue(ctx, proxy.APIKeyIDContextKey{}, "test-credential")
	engine := gin.New()
	engine.POST("/v1/router/threads", classifier.StartThreadHandler(svc))
	for _, path := range []string{"/v1/messages", "/v1/responses"} {
		engine.POST(path, middleware.WithClassifierThread(svc, svc.PolicyStrategyAvailable), func(c *gin.Context) {
			strategy := router.StrategyFromContext(c.Request.Context())
			if _, enrolled := c.Request.Context().Value(classifierEnrollmentTestKey{}).(bool); enrolled {
				require.Equal(t, router.StrategyLLMClassifier, strategy)
			} else {
				require.Equal(t, router.StrategyCluster, strategy)
			}
			c.Status(http.StatusOK)
		})
	}
	startRequest := httptest.NewRequest(http.MethodPost, "/v1/router/threads", strings.NewReader(`{"new_chat_id":"`+uuid.NewString()+`"}`)).WithContext(ctx)
	startResponse := httptest.NewRecorder()
	engine.ServeHTTP(startResponse, startRequest)
	require.Equal(t, http.StatusOK, startResponse.Code, startResponse.Body.String())
	var ticket struct {
		Token string `json:"thread_token"`
	}
	require.NoError(t, json.Unmarshal(startResponse.Body.Bytes(), &ticket))
	for _, path := range []string{"/v1/messages", "/v1/responses"} {
		for _, token := range []string{"", "forged", ticket.Token} {
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"`+string(router.ModelAutomatic)+`"}`)).WithContext(ctx)
			if token != "" {
				req.Header.Set(proxy.ClassifierThreadHeader, token)
			}
			if token == ticket.Token {
				req = req.WithContext(context.WithValue(req.Context(), classifierEnrollmentTestKey{}, true))
			}
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, req)
			want := http.StatusOK
			if token == "forged" {
				want = http.StatusConflict
			}
			if token == ticket.Token {
				want = http.StatusOK
			}
			require.Equal(t, want, recorder.Code, recorder.Body.String())
		}
	}
}

func TestRoutingModelReadsGeminiURLModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	body := `{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`
	engine.POST("/v1beta/models/:modelAction", middleware.WithClassifierThread(&proxy.Service{}, func(router.Strategy) bool { return true }), func(c *gin.Context) {
		require.Equal(t, router.StrategyHMMQwen, router.StrategyFromContext(c.Request.Context()))
		model, selected := router.RoutingModelFromContext(c.Request.Context())
		require.True(t, selected)
		require.Equal(t, router.ModelQwen, model)
		c.Status(http.StatusOK)
	})
	identity := requestcontext.ServingIdentity{Target: string(policyregistry.TargetInternal), ReleaseID: "reviewed-release", BindingID: "reviewed-binding", CredentialIdentity: "test-principal"}
	ctx := requestcontext.WithServingIdentity(context.Background(), identity)
	ctx = policyregistry.WithServingSnapshot(ctx, &policyregistry.Snapshot{Routers: map[router.Strategy]router.Router{router.StrategyHMMQwen: routingModelTestRouter{}}})
	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/"+string(router.ModelQwen)+":generateContent", strings.NewReader(body)).WithContext(ctx)
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
}

func TestRoutingModelGeminiURLOverridesBodyAndRejectsForceHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, bodyModel := range []string{string(router.ModelAutomatic), string(router.ModelHMM), "upstream-model-fixture"} {
		for _, forced := range []bool{false, true} {
			t.Run(string(bodyModel)+"/force="+fmt.Sprint(forced), func(t *testing.T) {
				engine := gin.New()
				engine.POST("/v1beta/models/:modelAction", middleware.WithClassifierThread(&proxy.Service{}, func(router.Strategy) bool { return true }), func(c *gin.Context) {
					require.False(t, forced, "a routing selector cannot bypass conflicting force headers through body.model")
					require.Equal(t, router.StrategyHMMQwen, router.StrategyFromContext(c.Request.Context()))
					model, selected := router.RoutingModelFromContext(c.Request.Context())
					require.True(t, selected)
					require.Equal(t, router.ModelQwen, model)
					c.Status(http.StatusOK)
				})
				identity := requestcontext.ServingIdentity{Target: string(policyregistry.TargetInternal), ReleaseID: "reviewed-release", BindingID: "reviewed-binding", CredentialIdentity: "test-principal"}
				ctx := requestcontext.WithServingIdentity(context.Background(), identity)
				ctx = policyregistry.WithServingSnapshot(ctx, &policyregistry.Snapshot{Routers: map[router.Strategy]router.Router{router.StrategyHMMQwen: routingModelTestRouter{}}})
				body := `{"model":"` + string(bodyModel) + `","contents":[{"role":"user","parts":[{"text":"hello"}]}]}`
				request := httptest.NewRequest(http.MethodPost, "/v1beta/models/"+string(router.ModelQwen)+":generateContent", strings.NewReader(body)).WithContext(ctx)
				if forced {
					request.Header.Set(proxy.ForceModelHeader, "gpt-6.1-sol")
				}
				response := httptest.NewRecorder()
				engine.ServeHTTP(response, request)
				if forced {
					require.Equal(t, http.StatusConflict, response.Code, response.Body.String())
					require.Contains(t, response.Body.String(), "routing_model_conflicts_with_force_model")
				} else {
					require.Equal(t, http.StatusOK, response.Code, response.Body.String())
				}
			})
		}
	}
}
