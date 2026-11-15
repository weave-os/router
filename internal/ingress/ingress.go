// Package ingress is the Protocol & Ingress bounded context: each client wire
// format's request parsing and the format-specific output the orchestrator
// owes the client (terminal error frames, keepalive). Pure format conversion
// stays in internal/translate.
package ingress

import (
	"context"
	"net/http"
	"time"

	"weave-os/router/internal/translate"
)

// Adapter is one client wire format's side of a turn.
type Adapter interface {
	// Format names the client wire format.
	Format() translate.Format
	// TraceBody is the native body as a same-format dispatch forwards it.
	TraceBody(body []byte) []byte
	// Parse turns a native request body into the neutral turn request, also
	// returning the native body without the router's own echoed additions.
	Parse(ctx context.Context, body []byte) (*translate.RequestEnvelope, []byte, error)
	// CheckTarget rejects a routed provider the format cannot be served by.
	CheckTarget(provider string) error
	// WriteStreamError ends a committed stream with the format's error frame.
	WriteStreamError(w http.ResponseWriter)
}

// Formats holds the adapters for the formats the orchestrator serves.
type Formats struct {
	Gemini Adapter
	// Keepalive is the client-silence budget before a ping is injected into
	// a committed stream (ROUTER_SSE_KEEPALIVE_INTERVAL_SECONDS); non-positive
	// disables it.
	Keepalive time.Duration
}

// New returns the adapters for every served format.
func New(keepalive time.Duration) Formats {
	return Formats{Gemini: Gemini{}, Keepalive: keepalive}
}
