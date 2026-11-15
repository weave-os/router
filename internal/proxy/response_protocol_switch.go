package proxy

import (
	"context"
	"errors"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/translate"
)

// openAISurface names which OpenAI endpoint an attempt POSTs to and in what
// representation; the three cases differ in both emit and response handling.
type openAISurface int

const (
	// surfaceChat is /v1/chat/completions with the client's own format.
	surfaceChat openAISurface = iota
	// surfaceResponsesNative is /v1/responses with a Responses caller's
	// original bytes, streamed back verbatim.
	surfaceResponsesNative
	// surfaceResponsesTranslated is /v1/responses emitted from a
	// chat/completions request, with the response translated back to chat.
	surfaceResponsesTranslated
)

type modelAttemptBuilder func(router.Decision, translate.EmitOptions, string, context.Context) (dispatch.UpstreamAttempt, error)

// responseProtocolSwitch owns the client writer's protocol across model changes.
// A synthetic routing prelude can remain visible while a failed provider
// attempt is replaced without committing its output.
type responseProtocolSwitch struct {
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
			// Binding failover may change the provider after the model was chosen.
			if err := s.selectTarget(binding); err != nil {
				return err
			}
			return attempt(ctx, binding, client)
		}, nil
	}
}

var errResponsesPassthroughStuck = errors.New("responses passthrough already carries native output; another wire format cannot continue it")
