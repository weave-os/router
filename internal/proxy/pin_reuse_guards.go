package proxy

import (
	"context"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/candidates"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"
)

type turnPin struct {
	pin   sessionpin.Pin
	found bool
}

func (tp *turnPin) drop() {
	tp.found = false
	tp.pin = sessionpin.Pin{}
}

func dropPinRuledOutLastTurn(ctx context.Context, s *Service, env *translate.RequestEnvelope, req *router.Request, res *turnLoopResult, tp *turnPin, hmmHistory sessionpin.Pin) {
	log := observability.FromContext(ctx)
	if tp.found && !isUserForcedReason(tp.pin.Reason) && automaticallyDisabled(*req, tp.pin.Model) {
		reason, _ := s.globalAutomaticExclusionReason(ctx, tp.pin.Model)
		log.Info("Session pin model is disabled for automatic routing; falling through to scorer",
			"pin_model", tp.pin.Model,
			"pin_provider", tp.pin.Provider,
			"pin_reason", tp.pin.Reason,
			"disable_reason", reason,
			"role", res.PinRole,
		)
		tp.drop()
	}

	if maxedModel := maxedOutServedModel(tp.pin); tp.found && maxedModel != "" {
		log.Info("Session pin maxed out on previous turn; excluding for this turn",
			"pin_model", tp.pin.Model,
			"pin_provider", tp.pin.Provider,
			"maxed_model", maxedModel,
			"last_output_tokens", tp.pin.LastOutputTokens,
		)
		*req = candidates.ExcludeMaxedModel(*req, maxedModel)
		tp.drop()
	}
	if maxedModel := maxedOutServedModel(hmmHistory); maxedModel != "" {
		log.Info("HMM history maxed out on previous turn; excluding for this turn",
			"history_provider", hmmHistory.Provider,
			"maxed_model", maxedModel,
			"last_output_tokens", hmmHistory.LastOutputTokens,
		)
		*req = candidates.ExcludeMaxedModel(*req, maxedModel)
		if tp.found && baseModelOf(tp.pin.Model) == maxedModel {
			tp.drop()
		}
	}

	if tp.found && advisorRejectsModel(env, tp.pin.Model) {
		log.Info("Session pin dropped: the request's advisor tool cannot advise the pinned model",
			"pin_model", tp.pin.Model,
			"advisor_model", env.AdvisorToolModel(),
		)
		tp.drop()
	}
}

func screenPinContextWindow(ctx context.Context, s *Service, env *translate.RequestEnvelope, feats translate.RoutingFeatures, res *turnLoopResult, req *router.Request, tp *turnPin) {
	log := observability.FromContext(ctx)
	if tp.found {
		if _, overCapacity := req.Candidates.ExcludedModels[tp.pin.Model]; overCapacity {
			outputReserveForPin := contextWindowOutputReserve
			if feats.MaxTokens > outputReserveForPin {
				outputReserveForPin = feats.MaxTokens
			}
			pinTokenEstimate := env.ContextOverflowTokenEstimate()
			if modelStripsAnthropicSignatures(tp.pin.Model) {
				pinTokenEstimate -= env.SignatureTokenSavings()
			}
			if projected, ok := res.TokenizerCalibration.ProjectedPrompt(tokenizerFamilyFor(tp.pin.Provider), res.ContextEstimate); ok && projected > pinTokenEstimate {
				pinTokenEstimate = projected
			}
			needed := pinTokenEstimate + outputReserveForPin
			modelCW := gatewayContextWindow(cortexGatewayProvidersForRequest(ctx), tp.pin.Model, tp.pin.Provider)
			if needed > modelCW {
				log.Info("Session pin excluded by context-window pre-filter; falling through to scorer",
					"pin_model", tp.pin.Model,
					"pin_provider", tp.pin.Provider,
					"token_estimate", pinTokenEstimate,
					"needed", needed,
					"model_context_window", modelCW,
				)
				tp.drop()
			} else {
				policyExcluded := s.excludedModelsForRequest(ctx)
				_, policyExcludes := policyExcluded[tp.pin.Model]
				if !policyExcludes && !compatibilityExcludesModel(*req, tp.pin.Model) {
					*req = candidates.ReadmitPinnedModel(*req, tp.pin.Model)
				}
				log.Info("Session pin preserved despite context-window pre-filter exclusion",
					"pin_model", tp.pin.Model,
					"token_estimate", pinTokenEstimate,
					"needed", needed,
					"model_context_window", modelCW,
				)
			}
		}
	}
}

func screenPinForRequest(ctx context.Context, req router.Request, tp *turnPin) {
	log := observability.FromContext(ctx)
	if tp.found && !modelInRequestSubset(ctx, tp.pin.Model) {
		log.Info("Session pin outside request allowed-models subset; falling through to scorer",
			"pin_model", tp.pin.Model,
			"pin_provider", tp.pin.Provider,
		)
		tp.found = false
	}

	if tp.found && req.EnabledProviders != nil {
		if _, ok := req.EnabledProviders[tp.pin.Provider]; !ok {
			log.Info("Session pin provider not in enabled set; falling through to scorer",
				"pin_model", tp.pin.Model,
				"pin_provider", tp.pin.Provider,
			)
			tp.drop()
		}
	}

	if tp.found && req.HasImages && (!catalog.AcceptsImages(tp.pin.Model) ||
		req.ImageCount > 0 && !catalog.AcceptsImageCount(tp.pin.Model, req.ImageCount, req.EnabledProviders)) {
		log.Info("Session pin is text-only for image-bearing turn; falling through to scorer",
			"pin_model", tp.pin.Model,
			"pin_provider", tp.pin.Provider,
		)
		tp.drop()
	}
}
