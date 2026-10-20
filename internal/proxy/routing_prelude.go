package proxy

import (
	"context"
	"net/http"

	"weave-os/router/internal/translate"
)

// The coordinator outlives binding attempts; the buffer's provider commitment
// remains the retry gate even after a notice becomes visible.
type requestNoticeStream struct {
	delivery   *translate.NoticeDelivery
	lastMarker string
}

func (s *requestNoticeStream) prepare(ctx context.Context, sink http.ResponseWriter, prelude *preludeBuffer, format translate.Format, streaming bool, model, marker string) (http.ResponseWriter, error) {
	if s.delivery == nil {
		capabilities := translate.NoticeCapabilities{Format: format, Streaming: streaming, Presentation: translate.NoticeText, Eager: streaming && (format == translate.FormatOpenAI || format == translate.FormatOpenAIResponses)}
		s.delivery = translate.NewNoticeDelivery(ctx, translate.NewProtocolNoticeSink(sink, capabilities), capabilities)
	}
	retainNotices := prelude == nil || prelude.PreludeSent()
	s.delivery.ResetAttempt(retainNotices)
	if !retainNotices {
		s.lastMarker = ""
	}
	if marker != "" && marker != s.lastMarker {
		notice := translate.NewRouterNotice(translate.NoticeModelSelection, marker, model, "")
		outcome, err := s.delivery.Deliver(notice)
		if err != nil {
			return nil, err
		}
		if outcome == translate.NoticeDelivered {
			s.lastMarker = marker
		}
	}
	return s.delivery, nil
}
