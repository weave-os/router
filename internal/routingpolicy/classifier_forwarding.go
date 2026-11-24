package routingpolicy

import (
	"net/http"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/translate"
)

func PrepareAnthropicForTurn(env *translate.RequestEnvelope, in http.Header, opts translate.EmitOptions, turnType turntype.TurnType) (providers.PreparedRequest, error) {
	if turnType == turntype.Classifier && env.SourceFormat() == translate.FormatAnthropic && opts.TargetModel == env.Model() {
		return env.PrepareAnthropicVerbatim(in, opts)
	}
	return env.PrepareAnthropic(in, opts)
}

func PrepareOpenAIForTurn(env *translate.RequestEnvelope, in http.Header, opts translate.EmitOptions, turnType turntype.TurnType) (providers.PreparedRequest, error) {
	if turnType == turntype.Classifier {
		if prep, ok := env.PrepareOpenAIVerbatim(opts); ok {
			return prep, nil
		}
	}
	return env.PrepareOpenAI(in, opts)
}

func CodexNativeFastMode(body []byte, opts translate.EmitOptions, turnType turntype.TurnType) ([]byte, error) {
	if turnType == turntype.Classifier {
		return body, nil
	}
	return translate.ApplyOpenAIFastMode(body, opts)
}

func CodexNativeEffort(body []byte, opts translate.EmitOptions, turnType turntype.TurnType) ([]byte, error) {
	if turnType == turntype.Classifier {
		return body, nil
	}
	return translate.ApplyOpenAIResponsesEffort(body, opts)
}

func GeminiOptionsForTurn(opts translate.EmitOptions, turnType turntype.TurnType) translate.EmitOptions {
	if turnType != turntype.Classifier {
		return opts
	}
	opts.ForceReasoningEffort, opts.ForceEffort = "", ""
	opts.AppendAutonomySystem, opts.AppendWorkspaceSystem = false, false
	return opts
}
