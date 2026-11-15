package proxy

import (
	"log/slog"
	"net/http"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/translate"
)

type geminiToolModeAttempt struct {
	log     *slog.Logger
	buffer  *preludeBuffer
	env     *translate.RequestEnvelope
	headers http.Header
	opts    translate.EmitOptions
}

// Finalization waits until the retry decision: it may otherwise commit the
// failed attempt's synthetic prelude and make recovery impossible.
func (a geminiToolModeAttempt) dispatch(prepared providers.PreparedRequest,
	send func(providers.PreparedRequest) (func(error) error, error),
) error {
	finalize, upstreamErr := send(prepared)
	if upstreamErr == nil || !prepared.Stats.GeminiValidatedToolMode || committed(a.buffer) || upstreamStatus(upstreamErr) != http.StatusBadRequest {
		return finalize(upstreamErr)
	}
	a.opts.DowngradeGeminiValidatedToAuto = true
	autoRequest, emitErr := a.env.PrepareGemini(a.headers, a.opts)
	if emitErr != nil {
		a.log.Error("Failed to re-translate Gemini request with tool mode AUTO", "err", emitErr)
		return finalize(upstreamErr)
	}
	a.log.Warn("Retrying Gemini request with functionCallingConfig.mode=AUTO after VALIDATED-mode 400",
		"model", a.opts.TargetModel)
	if a.buffer != nil {
		a.buffer.Discard()
	}
	finalize, upstreamErr = send(autoRequest)
	return finalize(upstreamErr)
}
