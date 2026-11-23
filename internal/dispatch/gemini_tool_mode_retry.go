package dispatch

import (
	"log/slog"
	"net/http"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/translate"
)

type GeminiToolModeAttempt struct {
	Log     *slog.Logger
	Buffer  *PreludeBuffer
	Env     *translate.RequestEnvelope
	Headers http.Header
	Opts    translate.EmitOptions
}

func (a GeminiToolModeAttempt) Dispatch(prepared providers.PreparedRequest,
	send func(providers.PreparedRequest) (func(error) error, error),
) error {
	finalize, upstreamErr := send(prepared)
	if upstreamErr == nil || !prepared.Stats.GeminiValidatedToolMode || preludeCommitted(a.Buffer) || providers.UpstreamStatus(upstreamErr) != http.StatusBadRequest {
		return finalize(upstreamErr)
	}
	a.Opts.DowngradeGeminiValidatedToAuto = true
	autoRequest, emitErr := a.Env.PrepareGemini(a.Headers, a.Opts)
	if emitErr != nil {
		a.Log.Error("Failed to re-translate Gemini request with tool mode AUTO", "err", emitErr)
		return finalize(upstreamErr)
	}
	a.Log.Warn("Retrying Gemini request with functionCallingConfig.mode=AUTO after VALIDATED-mode 400",
		"model", a.Opts.TargetModel)
	if a.Buffer != nil {
		a.Buffer.Discard()
	}
	finalize, upstreamErr = send(autoRequest)
	return finalize(upstreamErr)
}
