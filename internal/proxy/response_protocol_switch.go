package proxy

import (
	"context"
	"errors"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/translate"
)

type modelAttemptBuilder func(router.Decision, translate.EmitOptions, string, context.Context) (dispatchAttempt, error)

// responseProtocolSwitch owns the client writer's protocol across model changes.
// A synthetic routing prelude can remain visible while a failed provider
// attempt is replaced without committing its output.
type responseProtocolSwitch struct {
	writer *translate.ResponsesWriter
	buffer *preludeBuffer
}

func (s responseProtocolSwitch) native(badge bool) {
	if badge {
		s.writer.SetPassthroughBadge()
	} else {
		s.writer.SetPassthrough()
	}
}

func (s responseProtocolSwitch) translated() error {
	if s.writer == nil {
		return nil
	}
	if !s.writer.Passthrough() {
		return nil
	}
	if s.buffer != nil && !s.buffer.Committed() && !s.buffer.PreludeSent() {
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
	if target.Provider != providers.ProviderOpenAI {
		if err := s.translated(); err != nil {
			return err
		}
	}
	s.writer.SetRoutedModel(target.Model)
	return nil
}

func (s responseProtocolSwitch) wrapBuilder(build modelAttemptBuilder) modelAttemptBuilder {
	return func(target router.Decision, opts translate.EmitOptions, marker string, fundingCtx context.Context) (dispatchAttempt, error) {
		if err := s.selectTarget(target); err != nil {
			return nil, err
		}
		attempt, err := build(target, opts, marker, fundingCtx)
		if err != nil {
			return nil, err
		}
		return func(ctx context.Context, binding router.Decision, client providers.Client) error {
			// Binding failover may change the provider after the model was chosen.
			if err := s.selectTarget(binding); err != nil {
				return err
			}
			return attempt(ctx, binding, client)
		}, nil
	}
}

var errResponsesPassthroughStuck = errors.New("responses passthrough already carries native output; another wire format cannot continue it")
