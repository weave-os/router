package proxy

import (
	"context"
	"net/http"

	"github.com/tidwall/sjson"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/translate"
)

type classifierRescuePolicy struct {
	enabled       bool
	fallbackModel string
}

type classifierProxyFunc func(router.Decision, providers.PreparedRequest, http.ResponseWriter) error

type classifierRefusalRescue struct {
	armed     bool
	decision  router.Decision
	inHeaders http.Header
	opts      translate.EmitOptions
}

func newClassifierRefusalRescue(ctx context.Context, policy classifierRescuePolicy, turnType turntype.TurnType, stream bool, served router.Decision, in http.Header, opts translate.EmitOptions) classifierRefusalRescue {
	fallbackModel := policy.fallbackModel
	if !policy.enabled || turnType != turntype.Classifier || stream || served.Provider != providers.ProviderAnthropic ||
		fallbackModel == "" || fallbackModel == served.Model ||
		!modelPermittedByAllowlist(ctx, fallbackModel) || !modelInRequestSubset(ctx, fallbackModel) {
		return classifierRefusalRescue{}
	}
	target := served
	target.Model = fallbackModel
	opts.TargetModel = fallbackModel
	opts.Capabilities = router.Lookup(fallbackModel)
	opts.FastMode = false
	return classifierRefusalRescue{armed: true, decision: target, inHeaders: in, opts: opts}
}

func (c classifierRefusalRescue) prepare(served providers.PreparedRequest) (providers.PreparedRequest, bool) {
	body, err := sjson.SetBytes(served.Body, "model", c.decision.Model)
	if err != nil {
		return providers.PreparedRequest{}, false
	}
	if !c.opts.Capabilities.Supports(router.CapServerSideFallback) {
		if body, err = sjson.DeleteBytes(body, "fallbacks"); err != nil {
			return providers.PreparedRequest{}, false
		}
	}
	env, err := translate.ParseAnthropic(body)
	if err != nil {
		return providers.PreparedRequest{}, false
	}
	prep, err := env.PrepareAnthropicVerbatim(c.inHeaders, c.opts)
	if err != nil {
		return providers.PreparedRequest{}, false
	}
	prep.ResponseToolNames = served.ResponseToolNames
	return prep, true
}

func proxyClassifierWithRefusalRescue(ctx context.Context, proxy classifierProxyFunc, served router.Decision, prep providers.PreparedRequest, rescue classifierRefusalRescue, w http.ResponseWriter) (router.Decision, error) {
	if !rescue.armed {
		return served, proxy(served, prep, w)
	}
	first := &bufferedResponse{header: make(http.Header)}
	err := proxy(served, prep, first)
	if err != nil || !detectRefusalSignal(first.body.Bytes()) {
		return served, first.replay(w, err)
	}
	fallbackPrep, ok := rescue.prepare(prep)
	if !ok {
		return served, first.replay(w, nil)
	}
	second := &bufferedResponse{header: make(http.Header)}
	secondErr := proxy(rescue.decision, fallbackPrep, second)
	log := observability.FromContext(ctx)
	category := refusalCategory(first.body.Bytes())
	if secondErr != nil || second.status != http.StatusOK || detectRefusalSignal(second.body.Bytes()) {
		log.Info("classifier refusal not rescued on fallback model",
			"from_model", served.Model, "to_model", rescue.decision.Model, "refusal_category", category, "fallback_err", secondErr)
		return served, first.replay(w, nil)
	}
	log.Info("classifier refusal re-served on fallback model",
		"from_model", served.Model, "to_model", rescue.decision.Model, "refusal_category", category)
	w.Header().Set(HeaderRouterModel, rescue.decision.Model)
	return rescue.decision, second.replay(w, nil)
}
