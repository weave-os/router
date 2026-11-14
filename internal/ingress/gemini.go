package ingress

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/translate"

	"github.com/tidwall/sjson"
)

// ErrGeminiCrossFormatUnsupported is returned when a Gemini-source request
// lands on a non-Google provider. Cross-format emit is deferred until
// traffic asks for it.
var ErrGeminiCrossFormatUnsupported = errors.New("gemini cross-format emit not implemented")

// StripGeminiEchoes removes the router's own additions a client echoes back.
func StripGeminiEchoes(ctx context.Context, body []byte) []byte {
	log := observability.FromContext(ctx)
	// Strip the one-click thumbs footer that prior streamed answers appended as a
	// trailing model text part. Clients echo it back in contents[] on the next
	// turn, so without this it (and its signed rate URLs) accumulates in upstream
	// context (see ProxyMessages for the symmetric Anthropic/OpenAI path).
	// Best-effort: a strip failure returns a nil body, so we log-and-continue
	// with the original bytes rather than aborting the turn over cosmetic
	// cleanup — matching the OpenAI chat path's feedback-footer strip.
	if strippedBody, stripErr := translate.StripFeedbackFooterFromGeminiContents(body); stripErr != nil {
		log.Error("Failed to strip feedback footer from Gemini contents", "err", stripErr)
	} else {
		body = strippedBody
	}
	if strippedBody, stripErr := translate.StripRoutingMarkerFromGeminiContents(body); stripErr != nil {
		log.Error("Failed to strip routing marker from Gemini contents", "err", stripErr)
	} else {
		body = strippedBody
	}
	return body
}

// CheckGeminiTarget rejects a decision a Gemini-source request cannot be served by.
func CheckGeminiTarget(provider string) error {
	// Cross-format from a Gemini envelope is deferred; handler maps to HTTP 501.
	if provider != providers.ProviderGoogle {
		return fmt.Errorf("%w: decision picked provider %q for Gemini-source request", ErrGeminiCrossFormatUnsupported, provider)
	}
	return nil
}

// WriteGeminiStreamError writes a terminal error frame to a committed
// Gemini stream; called only on post-commit transport or truncation failure.
func WriteGeminiStreamError(sink http.ResponseWriter) {
	_, _ = sink.Write([]byte("data: {\"error\":{\"code\":\"UNAVAILABLE\",\"message\":\"upstream stream failed\"}}\n\n"))
	if f, ok := sink.(http.Flusher); ok {
		f.Flush()
	}
}

// GeminiForwardedBody drops the synthetic routing fields the handler injected,
// so the attempt trace compares against what a same-format dispatch forwards.
func GeminiForwardedBody(body []byte) []byte {
	for _, field := range []string{"model", "stream"} {
		if stripped, err := sjson.DeleteBytes(body, field); err == nil {
			body = stripped
		}
	}
	return body
}
