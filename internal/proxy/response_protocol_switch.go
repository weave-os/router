package proxy

import (
	"context"
	"errors"
	"net/http"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/translate"
)

type openAISurface int

const (
	surfaceChat openAISurface = iota
	surfaceResponsesNative
	surfaceResponsesTranslated
)

type modelAttemptBuilder func(router.Decision, translate.EmitOptions, string, context.Context) (dispatch.UpstreamAttempt, error)

type responseProtocolSwitch struct {
	notices             *requestNoticeStream
	writer              *translate.ResponsesWriter
	buffer              *preludeBuffer
	nativeRequest       bool
	translatedResponses bool
	promotedToResponses bool
}

func (s responseProtocolSwitch) passthrough() bool {
	return s.nativeRequest && (s.writer == nil || s.writer.Passthrough())
}

func (s responseProtocolSwitch) surface(provider string, subscription bool) openAISurface {
	if provider != providers.ProviderOpenAI {
		return surfaceChat
	}
	if s.passthrough() {
		return surfaceResponsesNative
	}
	if s.nativeRequest || s.translatedResponses || subscription {
		return surfaceResponsesTranslated
	}
	return surfaceChat
}

func (s *responseProtocolSwitch) fallbackToChat(surface openAISurface) error {
	if committed(s.buffer) {
		return errResponsesPassthroughStuck
	}
	if surface == surfaceResponsesNative {
		if !s.promotedToResponses || s.writer == nil {
			return errResponsesPassthroughStuck
		}
		if err := s.leavePassthrough(); err != nil {
			return err
		}
	}
	s.nativeRequest = false
	s.translatedResponses = false
	return nil
}

func (s responseProtocolSwitch) native(badge bool) {
	if badge {
		s.writer.SetPassthroughBadge()
	} else {
		s.writer.SetPassthrough()
	}
}

func (s responseProtocolSwitch) leavePassthrough() error {
	if s.writer == nil {
		return nil
	}
	if !s.writer.Passthrough() {
		return nil
	}
	if committed(s.buffer) {
		return errResponsesPassthroughStuck
	}
	if s.buffer != nil && !s.buffer.PreludeSent() {
		s.writer.ForgetWithheldHeaders()
	}
	if !s.writer.ClearPassthrough() {
		return errResponsesPassthroughStuck
	}
	return nil
}

func (s responseProtocolSwitch) selectTarget(target router.Decision) error {
	s.notices.selectModel(target.Model)
	if s.writer == nil {
		return nil
	}
	if s.writer.Passthrough() && committed(s.buffer) {
		return errResponsesPassthroughStuck
	}
	if target.Provider != providers.ProviderOpenAI {
		if err := s.leavePassthrough(); err != nil {
			return err
		}
	}
	s.writer.SetRoutedModel(target.Model)
	return nil
}

func (s *responseProtocolSwitch) wrapBuilder(build modelAttemptBuilder) modelAttemptBuilder {
	return func(target router.Decision, opts translate.EmitOptions, marker string, fundingCtx context.Context) (dispatch.UpstreamAttempt, error) {
		if err := s.selectTarget(target); err != nil {
			return nil, err
		}
		attempt, err := build(target, opts, marker, fundingCtx)
		if err != nil {
			return nil, err
		}
		return func(ctx context.Context, binding router.Decision, client dispatch.Upstream) error {
			if err := s.selectTarget(binding); err != nil {
				return err
			}
			return attempt(ctx, binding, client)
		}, nil
	}
}

var errResponsesPassthroughStuck = errors.New("responses passthrough already carries native output; another wire format cannot continue it")

func resetResponsesAttempt(w http.ResponseWriter) {
	if rw, ok := w.(*translate.ResponsesWriter); ok {
		rw.ResetAttempt()
	}
}

func responsesDispatchBodies(body []byte, terminalResponses bool) (nativeBody, conversionBody []byte, err error) {
	nativeBody, err = translate.StripRouterReasoningFromResponsesInput(body)
	if err != nil {
		return nil, nil, err
	}
	if !terminalResponses {
		return nativeBody, body, nil
	}
	nativeBody, err = stripResponsesTerminalArtifacts(nativeBody)
	if err != nil {
		return nil, nil, err
	}
	conversionBody, err = stripResponsesVisualArtifacts(body)
	if err != nil {
		return nil, nil, err
	}
	return nativeBody, conversionBody, nil
}
