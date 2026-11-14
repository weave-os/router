package proxy

import (
	"log/slog"
	"strings"

	"weave-os/router/internal/routingpolicy"
	"weave-os/router/internal/translate"
)

func advisorExclusions(env *translate.RequestEnvelope, routable map[string]struct{}) func(map[string]struct{}) (map[string]struct{}, []string) {
	return func(excluded map[string]struct{}) (map[string]struct{}, []string) {
		return excludeAdvisorOutrankingModels(env, excluded, routable)
	}
}

func logPrefilter(log *slog.Logger, env *translate.RequestEnvelope, imageCount, outputReserve int, pre routingpolicy.Prefiltered) {
	if len(pre.UnsignedHistory) > 0 {
		log.Info("gemini pre-filter: excluded gemini-3.x for unsigned tool-call history",
			"excluded_models", strings.Join(pre.UnsignedHistory, ","),
		)
	}
	if len(pre.AdvisorOutranking) > 0 {
		log.Info("advisor pre-filter: excluded models the advisor tool cannot advise",
			"advisor_model", env.AdvisorToolModel(),
			"excluded_models", strings.Join(pre.AdvisorOutranking, ","),
		)
	}
	if len(pre.ImageLimited) > 0 {
		log.Info("image limit pre-filter: excluded models whose bindings cap images below the request count",
			"image_count", imageCount,
			"excluded_models", strings.Join(pre.ImageLimited, ","),
		)
	}
	if len(pre.ContextOverflowed) > 0 {
		log.Info("context window pre-filter: excluded over-capacity models",
			"overflow_token_estimate", pre.OverflowEstimate,
			"output_reserve", outputReserve,
			"excluded_count", len(pre.ContextOverflowed)-len(pre.OverflowAdmitted),
			"excluded_models", strings.Join(pre.ContextOverflowed, ","),
			"admitted_for_upstream", strings.Join(pre.OverflowAdmitted, ","),
		)
	}
}
