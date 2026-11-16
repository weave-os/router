package proxy

import (
	"context"
	"fmt"
	"net/http"
	randomrouter "weave-os/router/internal/router/random_router"
	"weave-os/router/internal/translate"
)

func ValidateRandomBenchmarkInput(ctx context.Context, body []byte, headers http.Header) error {
	if randomBenchmarkFrom(ctx) == nil || !randomBenchmarkHeaders(headers) {
		return randomrouter.ErrUnavailable
	}
	converted, err := translate.ConvertResponsesToChatCompletionsWithOptions(body, translate.ResponsesConversionOptions{PortableCodex: true})
	if err != nil {
		return err
	}
	env, err := translate.ParseOpenAI(converted.Body)
	if err != nil {
		return err
	}
	if !env.Stream() {
		return randomrouter.ErrUnavailable
	}
	_, beta := env.ExtractBetaCommand()
	_, forced := env.ExtractForceModelCommand()
	_, feedback := env.ExtractRouterFeedbackCommand()
	if beta || forced || feedback || env.ExtractRouterSessionCommand() || env.ExtractRouterModelsCommand() || env.ExtractListModelsCommand() {
		return fmt.Errorf("router commands are unavailable in random benchmark mode: %w", randomrouter.ErrUnavailable)
	}
	return nil
}

func detectCyclicToolCallLoopForRequest(ctx context.Context, env *translate.RequestEnvelope) (looped bool, top translate.ToolCallSig, topCount int, distinctRatio float64, total int) {
	if randomBenchmarkFrom(ctx) != nil {
		return
	}
	return detectCyclicToolCallLoop(env)
}
