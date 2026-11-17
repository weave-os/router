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
	WriteStreamError(w http.ResponseWriter)
}

type Formats struct {
	Gemini    Adapter
	Keepalive time.Duration
}

func New(keepalive time.Duration) Formats {
	return Formats{Gemini: Gemini{}, Keepalive: keepalive}
}
