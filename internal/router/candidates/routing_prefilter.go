package candidates

import (
	"log/slog"
	"strings"

	"weave-os/router/internal/router"
	"weave-os/router/internal/translate"
)

func AdvisorExclusions(env *translate.RequestEnvelope, routable map[string]struct{}) func(map[string]struct{}) (map[string]struct{}, []string) {
	return func(excluded map[string]struct{}) (map[string]struct{}, []string) {
		return ExcludeAdvisorOutrankingModels(env, excluded, routable)
	}
}

func LogPrefilter(log *slog.Logger, env *translate.RequestEnvelope, imageCount, outputReserve int, prefiltered Prefiltered) {
	if len(prefiltered.UnsignedHistory) > 0 {
		log.Info("gemini pre-filter: excluded gemini-3.x for unsigned tool-call history",
			"excluded_models", strings.Join(prefiltered.UnsignedHistory, ","),
		)
	}
	if len(prefiltered.AdvisorOutranking) > 0 {
		log.Info("advisor pre-filter: excluded models the advisor tool cannot advise",
			"advisor_model", env.AdvisorToolModel(),
			"excluded_models", strings.Join(prefiltered.AdvisorOutranking, ","),
		)
	}
	if len(prefiltered.ImageLimited) > 0 {
		log.Info("image limit pre-filter: excluded models whose bindings cap images below the request count",
			"image_count", imageCount,
			"excluded_models", strings.Join(prefiltered.ImageLimited, ","),
		)
	}
	if len(prefiltered.ContextOverflowed) > 0 {
		log.Info("context window pre-filter: excluded over-capacity models",
			"overflow_token_estimate", prefiltered.OverflowEstimate,
			"output_reserve", outputReserve,
			"excluded_count", len(prefiltered.ContextOverflowed)-len(prefiltered.OverflowAdmitted),
			"excluded_models", strings.Join(prefiltered.ContextOverflowed, ","),
			"admitted_for_upstream", strings.Join(prefiltered.OverflowAdmitted, ","),
		)
	}
}

func PrefilteredCandidates(excluded, allowed map[string]struct{}, prefiltered Prefiltered) router.Candidates {
	return router.Candidates{
		ExcludedModels:                excluded,
		AllowedModels:                 allowed,
		SafetyExcludedModels:          prefiltered.Safety,
		ContextWindowExcludedModels:   prefiltered.ContextWindowOnly,
		ImageLimitExcludedModels:      ModelSet(prefiltered.ImageLimited),
		UnsignedHistoryExcludedModels: ModelSet(prefiltered.UnsignedHistory),
		OverflowAdmittedModels:        ModelSet(prefiltered.OverflowAdmitted),
	}
}
