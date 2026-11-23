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

var ErrRequestNotJSONObject = translate.ErrNotJSONObject

var ErrGeminiCrossFormatUnsupported = errors.New("gemini cross-format emit not implemented")

type Gemini struct{}

var _ Adapter = Gemini{}

func (Gemini) Format() translate.Format { return translate.FormatGemini }

func (Gemini) Parse(ctx context.Context, body []byte) (*translate.RequestEnvelope, []byte, error) {
	body = stripGeminiEchoes(ctx, body)
	env, err := translate.ParseGemini(body)
	return env, body, err
}

func stripGeminiEchoes(ctx context.Context, body []byte) []byte {
	log := observability.FromContext(ctx)
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

func (Gemini) CheckTarget(provider string) error {
	if provider != providers.ProviderGoogle {
		return fmt.Errorf("%w: decision picked provider %q for Gemini-source request", ErrGeminiCrossFormatUnsupported, provider)
	}
	return nil
}

func (Gemini) WriteStreamError(sink http.ResponseWriter) {
	_, _ = sink.Write([]byte("data: {\"error\":{\"code\":\"UNAVAILABLE\",\"message\":\"upstream stream failed\"}}\n\n"))
	if f, ok := sink.(http.Flusher); ok {
		f.Flush()
	}
}

func (Gemini) TraceBody(body []byte) []byte {
	for _, field := range []string{"model", "stream"} {
		if stripped, err := sjson.DeleteBytes(body, field); err == nil {
			body = stripped
		}
	}
	return body
}
