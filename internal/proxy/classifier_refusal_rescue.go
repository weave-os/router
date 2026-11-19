package proxy

import (
	"context"
	"net/http"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/translate"
)

type classifierRescuePolicy struct {
	enabled     bool
	models      []string
	unavailable func(model string) bool
	rejected    func(model string, err error)
}

func newClassifierRescuePolicy(ctx context.Context, s *Service) classifierRescuePolicy {
	return classifierRescuePolicy{
		enabled: s.ResolveCyberRefusalRetry(ctx),
		models:  append([]string{s.ResolveCyberRefusalFallbackModel(ctx)}, catalog.ClassifierRefusalRescueModels()...),
		unavailable: func(model string) bool {
			return s.subscriptionModels.deniedForCaller(ctx, model, s.clock.Now())
		},
		rejected: func(model string, err error) {
			s.recordSubscriptionModelRejection(ctx, providers.ProviderAnthropic, model, err)
		},
	}
}

type classifierProxyFunc func(router.Decision, providers.PreparedRequest, http.ResponseWriter) error

type classifierRescueTarget struct {
	decision router.Decision
	opts     translate.EmitOptions
}

type classifierRefusalRescue struct {
	targets   []classifierRescueTarget
	rejected  func(model string, err error)
	inHeaders http.Header
	client    *translate.RequestEnvelope
}

func newClassifierRefusalRescue(ctx context.Context, policy classifierRescuePolicy, turnType turntype.TurnType, served router.Decision, client *translate.RequestEnvelope, in http.Header, opts translate.EmitOptions) classifierRefusalRescue {
	if !policy.enabled || turnType != turntype.Classifier || client.Stream() {
		return classifierRefusalRescue{}
	}
	rescue := classifierRefusalRescue{rejected: policy.rejected, inHeaders: in, client: client}
	seen := map[string]bool{served.Model: true}
	for _, model := range policy.models {
		if model == "" || seen[model] || !modelPermittedByAllowlist(ctx, model) || !modelInRequestSubset(ctx, model) ||
			(policy.unavailable != nil && policy.unavailable(model)) {
			continue
		}
		seen[model] = true
		target := classifierRescueTarget{decision: served, opts: opts}
		target.decision.Model = model
		target.opts.TargetModel = model
		target.opts.Capabilities = router.Lookup(model)
		target.opts.FastMode = false
		rescue.targets = append(rescue.targets, target)
	}
	return rescue
}

func (c classifierRefusalRescue) prepare(served providers.PreparedRequest, target classifierRescueTarget) (providers.PreparedRequest, bool) {
	body, err := sjson.SetBytes(served.Body, "model", target.decision.Model)
	if err != nil {
		return providers.PreparedRequest{}, false
	}
	if body, err = c.client.WithClientThinking(body); err != nil {
		return providers.PreparedRequest{}, false
	}
	if !target.opts.Capabilities.Supports(router.CapServerSideFallback) {
		if body, err = sjson.DeleteBytes(body, "fallbacks"); err != nil {
			return providers.PreparedRequest{}, false
		}
	}
	if effort := gjson.GetBytes(body, "output_config.effort").String(); effort != "" {
		if body, err = sjson.SetBytes(body, "output_config.effort", translate.ResolveForceEffort(target.opts.Capabilities, effort)); err != nil {
			return providers.PreparedRequest{}, false
		}
	}
	env, err := translate.ParseAnthropic(body)
	if err != nil {
		return providers.PreparedRequest{}, false
	}
	prep, err := env.PrepareAnthropicVerbatim(c.inHeaders, target.opts)
	if err != nil {
		return providers.PreparedRequest{}, false
	}
	if beta := translate.ServerSideFallbackBeta(translate.ExtendedContextBeta(prep.Headers.Get("anthropic-beta"), target.opts), prep.Body); beta != "" {
		prep.Headers.Set("anthropic-beta", beta)
	}
	prep.ResponseToolNames = served.ResponseToolNames
	return prep, true
}

func proxyClassifierWithRefusalRescue(ctx context.Context, proxy classifierProxyFunc, served router.Decision, prep providers.PreparedRequest, rescue classifierRefusalRescue, w http.ResponseWriter) (router.Decision, error) {
	if len(rescue.targets) == 0 {
		return served, proxy(served, prep, w)
	}
	servedResp := &bufferedResponse{header: make(http.Header)}
	err := proxy(served, prep, servedResp)
	if err != nil || !detectRefusalSignal(servedResp.body.Bytes()) {
		return served, servedResp.replay(w, err)
	}
	log := observability.FromContext(ctx)
	category := refusalCategory(servedResp.body.Bytes())
	for _, target := range rescue.targets {
		fallbackPrep, ok := rescue.prepare(prep, target)
		if !ok {
			break
		}
		fallbackResp := &bufferedResponse{header: make(http.Header)}
		fallbackErr := proxy(target.decision, fallbackPrep, fallbackResp)
		if anthropicSubscriptionModelRejected(fallbackErr) {
			rescue.rejected(target.decision.Model, fallbackErr)
			log.Info("classifier refusal fallback model unavailable on subscription; trying next",
				"from_model", served.Model, "to_model", target.decision.Model, "refusal_category", category)
			continue
		}
		if fallbackErr != nil || fallbackResp.status != http.StatusOK || detectRefusalSignal(fallbackResp.body.Bytes()) {
			log.Info("classifier refusal not rescued on fallback model",
				"from_model", served.Model, "to_model", target.decision.Model, "refusal_category", category, "fallback_err", fallbackErr)
			break
		}
		log.Info("classifier refusal re-served on fallback model",
			"from_model", served.Model, "to_model", target.decision.Model, "refusal_category", category)
		w.Header().Set(HeaderRouterModel, target.decision.Model)
		return target.decision, fallbackResp.replay(w, nil)
	}
	return served, servedResp.replay(w, nil)
}
