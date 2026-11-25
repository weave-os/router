package proxy

import (
	"context"
	"weave-os/router/internal/conversation"
	"weave-os/router/internal/feedback"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router/handover"
)

func (s *Service) WithRouterFeedbackStore(store feedback.RouterFeedbackStore) *Service {
	if s.lifecycle == nil {
		s.lifecycle = &conversation.Lifecycle{}
	}
	s.lifecycle.SetRouterFeedbackStore(store)
	return s
}

func (s *Service) WithPiHandoffSecret(secret string) *Service {
	if len(secret) >= 32 {
		if s.lifecycle == nil {
			s.lifecycle = &conversation.Lifecycle{}
		}
		s.lifecycle.SetPiHandoffSecret([]byte(secret))
	}
	return s
}

func (s *Service) parseHandoff(ctx context.Context, body []byte) (context.Context, []byte, error) {
	return s.lifecycle.PiHandoff(s.clock.Now).Parse(ctx, body, requestcontext.InstallationIDFromContext(ctx))
}

func (s *Service) WithSummarizer(sz handover.Summarizer) *Service {
	if s.lifecycle == nil {
		s.lifecycle = &conversation.Lifecycle{}
	}
	s.lifecycle.SetSummarizer(sz)
	return s
}

func (s *Service) WithFeedback(links *feedback.Links) *Service {
	if s.lifecycle == nil {
		s.lifecycle = &conversation.Lifecycle{}
	}
	s.lifecycle.SetFeedbackLinks(links)
	return s
}
