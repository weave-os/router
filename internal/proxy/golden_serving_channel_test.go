package proxy_test

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	anthropicapi "weave-os/router/internal/api/anthropic"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/ingress"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/server/middleware"
)

// Worker ingresses serve /v1/messages through a worker's production admission
// and routing model middleware ahead of the real messages handler. The
// unmanaged worker has no admission; each managed worker's admission store
// answers every request with one outcome. The admitted worker's requests carry
// the serving identity a completed admission attaches, so the turn is served
// without building a release snapshot.
const (
	ingressAdmittedWorker         = "anthropic_messages_admitted_worker"
	ingressUnmanagedWorker        = "anthropic_messages_unmanaged_worker"
	ingressStableWorker           = "anthropic_messages_stable_worker"
	ingressStableWorkerNoProfile  = "anthropic_messages_stable_worker_without_assigned_profile"
	ingressBetaWorkerNoPermission = "anthropic_messages_beta_worker_without_channel_permission"
)

const (
	servingChannelStableURL  = "https://stable.example.test"
	servingChannelBetaURL    = "https://beta.example.test"
	servingChannelProfileKey = "golden-efficient-profile"
	servingChannelReleaseID  = "1111111111111111111111111111111111111111111111111111111111111111"
	servingChannelPolicySHA  = "2222222222222222222222222222222222222222222222222222222222222222"
)

func init() {
	for ingressName, worker := range map[string]*middleware.ServingAdmissionConfig{
		ingressUnmanagedWorker:        nil,
		ingressAdmittedWorker:         nil,
		ingressStableWorker:           servingChannelWorkerConfig(policyregistry.TargetStable, nil),
		ingressStableWorkerNoProfile:  servingChannelWorkerConfig(policyregistry.TargetStable, policyregistry.ErrRoutingModelNotAssigned),
		ingressBetaWorkerNoPermission: servingChannelWorkerConfig(policyregistry.TargetBeta, policyregistry.ErrServingChannelNotPermitted),
	} {
		goldenIngressSurfaces[ingressName] = goldenIngressSurface{path: "/v1/messages", serve: servingChannelWorker(worker)}
	}
}

func servingChannelWorker(worker *middleware.ServingAdmissionConfig) func(*proxy.Service, context.Context, []byte, http.ResponseWriter, *http.Request) error {
	return servingChannelServe(func(svc *proxy.Service) http.Handler { return servingChannelEngine(svc, worker) })
}

func servingChannelServe(build func(*proxy.Service) http.Handler) func(*proxy.Service, context.Context, []byte, http.ResponseWriter, *http.Request) error {
	return func(svc *proxy.Service, _ context.Context, _ []byte, w http.ResponseWriter, r *http.Request) error {
		build(svc).ServeHTTP(w, r)
		return nil
	}
}

func servingChannelEngine(svc *proxy.Service, worker *middleware.ServingAdmissionConfig) *gin.Engine {
	chain := []gin.HandlerFunc{func(c *gin.Context) {
		c.Set("router_installation", &auth.Installation{ID: goldenInstallationID})
		c.Set("router_api_key", &auth.APIKey{ID: "golden-key", InstallationID: goldenInstallationID})
	}}
	if worker != nil {
		chain = append(chain, middleware.WithServingAdmission(worker))
	}
	chain = append(chain,
		middleware.WithClassifierThread(svc, func(router.Strategy) bool { return true }),
		anthropicapi.MessagesHandler(svc, nil, ingress.ErrorVocabulary{Classify: proxy.ClassifyDispatchError, TerminalMessage: proxy.TerminalErrorMessage}))
	engine := gin.New()
	engine.POST("/v1/messages", chain...)
	return engine
}

type servingChannelAdmissionStore struct{ err error }

func (s servingChannelAdmissionStore) Admit(context.Context, string, string, string, policyregistry.AdmissionDecision) (policyregistry.AdmissionScope, policyregistry.SessionReleaseBinding, error) {
	return policyregistry.AdmissionScope{}, policyregistry.SessionReleaseBinding{}, s.err
}

func servingChannelWorkerConfig(target policyregistry.ServingTarget, admission error) *middleware.ServingAdmissionConfig {
	decisions, err := policyregistry.NewAdmissionDecisionCache(servingChannelAdmissionStore{err: admission}, 8, time.Minute, func() time.Time { return goldenEpoch })
	if err != nil {
		panic(err)
	}
	return &middleware.ServingAdmissionConfig{
		Decisions:     decisions,
		Identity:      policyregistry.WorkerIdentity{Target: target},
		StableBaseURL: servingChannelStableURL,
		BetaBaseURL:   servingChannelBetaURL,
	}
}

// servingChannelAdmitted stands in for a completed worker admission: the
// identity the admission middleware attaches once the session is bound.
func servingChannelAdmitted(target policyregistry.ServingTarget, profileKey string) func(context.Context) context.Context {
	return func(ctx context.Context) context.Context {
		return requestcontext.WithServingIdentity(ctx, requestcontext.ServingIdentity{
			Target: string(target), ActivationID: "golden-activation", ReleaseID: servingChannelReleaseID, BindingID: "golden-binding",
			ProfileKey: profileKey, BindingGeneration: 1, StateNamespace: "golden-binding", CredentialIdentity: "golden-principal",
			Recipe: requestcontext.ServingRecipe{PolicySHA256: servingChannelPolicySHA},
		})
	}
}

func servingChannelControlArm(controlModel string) func(context.Context) context.Context {
	return func(ctx context.Context) context.Context {
		return context.WithValue(ctx, auth.BlindExperimentContextKey{}, auth.BlindExperimentState{
			Active: true, Arm: auth.BlindExperimentArmPassthrough, AssignmentSource: auth.BlindExperimentAssignmentAutomatic,
			CanonicalSubjectKey: "golden-subject", Mode: auth.BlindExperimentModeSession, ControlModel: controlModel,
		})
	}
}

func servingChannelBody(model string) string {
	return `{"model":"` + model + `","max_tokens":4096,"messages":[{"role":"user","content":"Say hi"}]}`
}

func servingChannelScenario(name, worker string, request goldenRequest) goldenScenario {
	return goldenScenario{name: "serving_channel_" + name, ingress: worker, upstreams: routingPolicyUpstreams(), requests: []goldenRequest{request},
		configure: routingPolicySetup{turns: routingPolicyTurns(routingPolicyHMM(routingPolicyLow, 0.9))}.configure}
}

// goldenServingChannelScenarios pin the routing models a worker admits by
// serving channel and profile, and the blind experiment's control model.
func goldenServingChannelScenarios() []goldenScenario {
	efficient, beta := servingChannelBody(string(router.ModelAutoEfficient)), servingChannelBody(string(router.ModelAutoBeta))
	return []goldenScenario{
		servingChannelScenario("auto_efficient_served_on_managed_worker", ingressAdmittedWorker,
			routingPolicyRequest(efficient, servingChannelAdmitted(policyregistry.TargetStable, servingChannelProfileKey))),
		servingChannelScenario("auto_efficient_refused_without_assigned_profile", ingressStableWorkerNoProfile, routingPolicyRefused(efficient, nil)),
		servingChannelScenario("auto_efficient_refused_on_unmanaged_worker", ingressUnmanagedWorker, routingPolicyRefused(efficient, nil)),
		servingChannelScenario("auto_beta_misdirected_on_stable_worker", ingressStableWorker, routingPolicyRefused(beta, nil)),
		servingChannelScenario("auto_beta_case_variant_misdirected_on_stable_worker", ingressStableWorker,
			routingPolicyRefused(servingChannelBody(strings.ToUpper(string(router.ModelAutoBeta))), nil)),
		servingChannelScenario("auto_beta_refused_without_channel_permission", ingressBetaWorkerNoPermission, routingPolicyRefused(beta, nil)),
		servingChannelScenario("auto_beta_served_as_auto_on_beta_worker", ingressAdmittedWorker,
			routingPolicyRequest(beta, servingChannelAdmitted(policyregistry.TargetBeta, ""))),
		servingChannelScenario("blind_control_arm_serves_control_model_for_auto", ingressUnmanagedWorker,
			routingPolicyRequest(servingChannelBody(string(router.ModelAutomatic)), servingChannelControlArm(routingPolicyLow))),
		servingChannelScenario("blind_control_arm_keeps_explicit_hmm_model", ingressUnmanagedWorker,
			routingPolicyRefused(servingChannelBody(string(router.ModelHMM)), nil, servingChannelControlArm(routingPolicyLow))),
	}
}
