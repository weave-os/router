package proxy

import (
	"context"
	"net/http"
	"time"

	"weave-os/router/internal/translate"
)

// The coordinator outlives binding attempts; the buffer's provider commitment
// remains the retry gate even after a notice becomes visible.
type requestNoticeStream struct {
	delivery   *translate.NoticeDelivery
	lastNotice translate.RouterNotice
}

func (s *requestNoticeStream) prepare(ctx context.Context, sink http.ResponseWriter, prelude *preludeBuffer, format translate.Format, streaming bool, notice translate.RouterNotice) (http.ResponseWriter, error) {
	if s.delivery == nil {
		capabilities := translate.NoticeCapabilities{Format: format, Streaming: streaming, Presentation: translate.NoticeText, Eager: streaming && (format == translate.FormatOpenAI || format == translate.FormatOpenAIResponses)}
		s.delivery = translate.NewNoticeDelivery(ctx, translate.NewProtocolNoticeSink(sink, capabilities), capabilities)
	}
	if trace := attemptTraceFromContext(ctx); trace != nil && streaming {
		s.delivery.SetBeforeProviderContent(func() (translate.RouterNotice, bool) {
			return slowStartNotice(ctx, trace, slowStarts, time.Now())
		})
	}
	retainNotices := prelude == nil || prelude.PreludeSent()
	s.delivery.ResetAttempt(retainNotices)
	if !retainNotices {
		s.lastNotice = translate.RouterNotice{}
	}
	if notice.Text != "" && (notice.Kind != s.lastNotice.Kind || notice.Text != s.lastNotice.Text) {
		outcome, err := s.delivery.Deliver(notice)
		if err != nil {
			return nil, err
		}
		if outcome == translate.NoticeDelivered {
			s.lastNotice = notice
		}
	}
	return s.delivery, nil
}
