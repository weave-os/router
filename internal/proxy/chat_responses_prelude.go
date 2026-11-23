package proxy

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/translate"
)

type chatResponsesPreludeDeps struct {
	svc                     *Service
	responseWriter          http.ResponseWriter
	contentSink             http.ResponseWriter
	responsesBody           []byte
	openAIResponsesEndpoint bool
	handoverInvoked         bool
	turnType                turntype.TurnType
	decision                router.Decision
	facts                   requestcontext.RequestFacts
	footerEchoed            bool
	bindingCount            int
	marker                  string
	notice                  translate.RouterNotice
	stream                  bool
	log                     *slog.Logger
}

type chatResponsesPrelude struct {
	responsesBody       []byte
	responsesWriter     *translate.ResponsesWriter
	isResponses         bool
	responseSwitch      *responseProtocolSwitch
	responsesNotices    *requestNoticeStream
	responsesPreludeBuf *preludeBuffer
	preludeBuf          *preludeBuffer
	sink                http.ResponseWriter
	makeMarkerSink      func(notice translate.RouterNotice) (http.ResponseWriter, error)
}

func prepareChatResponsesPrelude(ctx context.Context, d chatResponsesPreludeDeps) (chatResponsesPrelude, error) {
	p := chatResponsesPrelude{responsesBody: d.responsesBody}
	initialNativeResponses, responsesPreludeWillEmit := p.selectProtocol(ctx, d)
	if err := p.wrapResponsesNotice(ctx, d, initialNativeResponses); err != nil {
		return chatResponsesPrelude{}, err
	}
	if p.responsesPreludeBuf != nil {
		p.preludeBuf = p.responsesPreludeBuf
		p.sink = d.contentSink
	} else {
		p.preludeBuf = newPreludeBuffer(d.contentSink)
		p.sink = p.preludeBuf
	}
	p.responseSwitch.buffer = p.preludeBuf
	if p.isResponses {
		if err := p.startResponses(ctx, d, initialNativeResponses, responsesPreludeWillEmit); err != nil {
			return chatResponsesPrelude{}, err
		}
	}
	p.makeMarkerSink = p.markerSinkFor(ctx, d)
	return p, nil
}

func (p *chatResponsesPrelude) selectProtocol(ctx context.Context, d chatResponsesPreludeDeps) (initialNativeResponses, responsesPreludeWillEmit bool) {
	promotedToResponses := false
	if len(p.responsesBody) == 0 && !d.handoverInvoked && d.openAIResponsesEndpoint {
		if native, ok := ctx.Value(nativeResponsesBodyContextKey{}).([]byte); ok && len(native) > 0 {
			p.responsesBody = native
			promotedToResponses = true
		}
	}

	p.responsesWriter, p.isResponses = d.responseWriter.(*translate.ResponsesWriter)
	p.responsesNotices = &requestNoticeStream{now: d.svc.clock.Now}
	p.responseSwitch = &responseProtocolSwitch{
		notices:             p.responsesNotices,
		writer:              p.responsesWriter,
		nativeRequest:       len(p.responsesBody) > 0,
		translatedResponses: !p.isResponses && len(p.responsesBody) == 0 && d.openAIResponsesEndpoint,
		promotedToResponses: promotedToResponses,
	}
	initialNativeResponses = p.responseSwitch.nativeRequest && d.decision.Provider == providers.ProviderOpenAI
	if footer := d.svc.feedbackFooter(ctx, d.facts.Client.ClientApp, d.turnType, d.footerEchoed); p.isResponses && footer != "" {
		p.responsesWriter.SetFooterText(footer)
	}
	responsesPreludeWillEmit = d.stream && !initialNativeResponses && (d.bindingCount <= 1 || d.marker != "")
	if initialNativeResponses {
		responsesPreludeWillEmit = d.stream && requestcontext.SupportsResponsesTerminalSurfaces(d.facts.Client.ClientApp) && d.marker != ""
	}
	return initialNativeResponses, responsesPreludeWillEmit
}

func (p *chatResponsesPrelude) wrapResponsesNotice(ctx context.Context, d chatResponsesPreludeDeps, initialNativeResponses bool) error {
	rw, ok := d.contentSink.(*translate.ResponsesWriter)
	if !ok {
		return nil
	}
	var noticeErr error
	rw.WrapInner(func(inner http.ResponseWriter) http.ResponseWriter {
		p.responsesPreludeBuf = newPreludeBuffer(inner)
		preludeNotice := d.notice
		if initialNativeResponses && !requestcontext.SupportsResponsesTerminalSurfaces(d.facts.Client.ClientApp) {
			preludeNotice.Text = ""
		}
		noticeSink, prepareErr := p.responsesNotices.prepare(ctx, p.responsesPreludeBuf, p.responsesPreludeBuf, translate.FormatOpenAIResponses, d.stream, preludeNotice)
		noticeErr = prepareErr
		if prepareErr != nil {
			return p.responsesPreludeBuf
		}
		return noticeSink
	})
	if noticeErr != nil {
		return fmt.Errorf("emit Responses router notice: %w", noticeErr)
	}
	return nil
}

func (p *chatResponsesPrelude) startResponses(ctx context.Context, d chatResponsesPreludeDeps, initialNativeResponses, responsesPreludeWillEmit bool) error {
	if initialNativeResponses {
		p.responseSwitch.native(requestcontext.SupportsResponsesTerminalSurfaces(d.facts.Client.ClientApp) &&
			(d.marker != "" || d.svc.feedbackFooter(ctx, d.facts.Client.ClientApp, d.turnType, d.footerEchoed) != ""))
	}
	if !responsesPreludeWillEmit {
		return nil
	}
	if err := p.responsesWriter.Prelude(true); err != nil {
		d.log.Error("Responses prelude failed", "err", err)
		return nil
	}
	p.responsesWriter.Flush()
	if err := p.responsesPreludeBuf.CommitPrelude(); err != nil {
		return fmt.Errorf("commit Responses prelude: %w", err)
	}
	return nil
}

func (p *chatResponsesPrelude) markerSinkFor(ctx context.Context, d chatResponsesPreludeDeps) func(notice translate.RouterNotice) (http.ResponseWriter, error) {
	sink, preludeBuf, responsesPreludeBuf := p.sink, p.preludeBuf, p.responsesPreludeBuf
	isResponses, responseSwitch, responsesNotices := p.isResponses, p.responseSwitch, p.responsesNotices
	openAIPrelude := &requestNoticeStream{now: d.svc.clock.Now}
	return func(notice translate.RouterNotice) (http.ResponseWriter, error) {
		if isResponses || responseSwitch.passthrough() {
			if isResponses && responsesPreludeBuf != nil && notice.Kind == translate.NoticeSubscriptionReconnect && (!responseSwitch.passthrough() || requestcontext.SupportsResponsesTerminalSurfaces(d.facts.Client.ClientApp)) {
				return sink, responsesNotices.deliver(notice)
			}
			return sink, nil
		}
		noticeSink, noticeErr := openAIPrelude.prepare(ctx, sink, preludeBuf, translate.FormatOpenAI, d.stream, notice)
		return noticeSink, noticeErr
	}
}
