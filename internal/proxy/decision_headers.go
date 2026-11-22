package proxy

import (
	"net/http"
	"strconv"
	"time"

	"weave-os/router/internal/ingress"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/sse"
	"weave-os/router/internal/translate"
)

func setDecisionHeaders(h http.Header, decision router.Decision) {
	h.Set(requestcontext.HeaderRouterDecision, decision.Reason)
	h.Set(requestcontext.HeaderRouterProvider, decision.Provider)
	h.Set(requestcontext.HeaderRouterModel, decision.Model)
	h.Set(requestcontext.HeaderRouterContextWindow, strconv.Itoa(contextWindowForRequest(decision.Model, decision.Provider)))
}

func anthropicStreamSink(w http.ResponseWriter, keepaliveInterval time.Duration, footer func() string) (http.ResponseWriter, *streamCostWriter, func()) {
	streamCost := newStreamCostWriter(w)
	var sink http.ResponseWriter = streamCost
	closeKeepalive := func() {}
	if keepaliveInterval > 0 {
		keepalive := sse.NewKeepaliveWriter(sink, ingress.AnthropicPingFrame, keepaliveInterval)
		closeKeepalive = keepalive.Close
		sink = keepalive
	}
	if text := footer(); text != "" {
		sink = translate.NewAnthropicRoutingFooterWriter(sink, text)
	}
	return sink, streamCost, closeKeepalive
}
