package ingress

import (
	"context"
	"net/http"
	"time"

	"weave-os/router/internal/translate"
)

type Adapter interface {
	Format() translate.Format
	TraceBody(body []byte) []byte
	Parse(ctx context.Context, body []byte) (*translate.RequestEnvelope, []byte, error)
	CheckTarget(provider string) error
}

type StreamErrorWriter interface {
	WriteStreamError(w http.ResponseWriter)
}

type Formats struct {
	Gemini interface {
		Adapter
		StreamErrorWriter
	}
	Anthropic           Anthropic
	OpenAIChat          OpenAIChat
	Responses           Responses
	Keepalive           time.Duration
	AllowedModelsHeader bool
}

func New(keepalive time.Duration) Formats {
	return Formats{Gemini: Gemini{}, Anthropic: Anthropic{}, OpenAIChat: OpenAIChat{}, Responses: Responses{}, Keepalive: keepalive}
}
